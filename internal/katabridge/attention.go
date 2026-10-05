package katabridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/lockfile"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"
)

// Attention is installation-local retry intent for ordinary replaceable notify.
// Sent means only an ordinary successful call or matching inbox observation;
// it is neither an authoritative receipt nor a lossless notification promise.
type Attention struct {
	Key       string `json:"key"`
	Source    string `json:"source"`
	Issue     string `json:"issue"`
	Recipient string `json:"recipient"`
	Message   string `json:"message"`
	RecordRun bool   `json:"record_run,omitempty"`
}

// AttentionSettlement is unfinished installation-local history work. It says
// nothing about native ownership or whether a handler has cleared a request.
type AttentionSettlement struct {
	Item        Attention `json:"item"`
	CompletedAt time.Time `json:"completed_at"`
}
type attentionFile struct {
	Version   int                   `json:"version"`
	Scope     Scope                 `json:"scope"`
	Teammate  string                `json:"teammate,omitempty"`
	Item      Attention             `json:"item"`
	Pending   bool                  `json:"pending"`
	Cancelled bool                  `json:"cancelled,omitempty"`
	Attempt   int                   `json:"attempt"`
	NextRetry time.Time             `json:"next_retry"`
	Error     string                `json:"error,omitempty"`
	Unsettled []AttentionSettlement `json:"unsettled,omitempty"`
}
type AttentionOutbox struct {
	Dir      string
	Scope    Scope
	Teammate string
}
type AttentionTransport interface {
	Inbox(context.Context, string, string) ([]Request, error)
	Notify(context.Context, string, string, string) error
}

func (b AttentionOutbox) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(b.Dir, "attention", hex.EncodeToString(sum[:])+".json")
}
func (b AttentionOutbox) edit(key string, fn func(*attentionFile) error) error {
	if key == "" {
		return errors.New("attention key required")
	}
	path := b.path(key)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	lock, err := lockfile.Acquire(path + ".lock")
	if err != nil {
		return err
	}
	defer lock.Release()
	f := attentionFile{Version: 1, Scope: b.Scope, Teammate: b.Teammate}
	if err := statefs.ReadJSON(path, 98304, &f); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if f.Version != 1 || f.Scope != b.Scope || f.Teammate != b.Teammate || f.Item.Key != "" && f.Item.Key != key {
		return errors.New("saved attention routing changed")
	}
	before := f
	before.Unsettled = slices.Clone(f.Unsettled)
	if err := fn(&f); err != nil {
		return err
	}
	if reflect.DeepEqual(before, f) {
		return nil
	}
	return statefs.WriteJSON(path, f, 98304)
}

// RetainError records ambiguous reads without retiring existing pending intent.
func (b AttentionOutbox) RetainError(key, message string) error {
	if _, err := os.Stat(b.path(key)); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return b.edit(key, func(f *attentionFile) error {
		if f.Pending {
			f.Error = message
		}
		return nil
	})
}
func (b AttentionOutbox) Queue(item Attention) error {
	if item.Source == "" || item.Issue == "" || item.Recipient == "" || item.Message == "" || len(item.Message) > 1024 {
		return errors.New("bounded ordinary attention source/recipient/message required")
	}
	return b.edit(item.Key, func(f *attentionFile) error {
		if f.Item == item && !f.Cancelled {
			return nil
		}
		unfinished := f.Unsettled
		*f = attentionFile{Version: 1, Scope: b.Scope, Teammate: b.Teammate, Item: item, Pending: true, Unsettled: unfinished}
		return nil
	})
}
func (b AttentionOutbox) CancelPending(key string) error {
	if _, err := os.Stat(b.path(key)); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return b.edit(key, func(f *attentionFile) error {
		if f.Pending {
			f.Cancelled = true
		}
		f.Pending = false
		f.Error = ""
		return nil
	})
}
func (b AttentionOutbox) FlushOne(ctx context.Context, key string, t AttentionTransport) error {
	if _, err := os.Stat(b.path(key)); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	var deliveryErr error
	err := b.edit(key, func(f *attentionFile) error {
		if !f.Pending {
			return nil
		}
		if time.Now().Before(f.NextRetry) {
			if f.Error != "" {
				deliveryErr = errors.New(f.Error)
			}
			return nil
		}
		requests, err := t.Inbox(ctx, f.Item.Recipient, f.Item.Issue)
		if err == nil {
			for _, r := range requests {
				if r.Ref == f.Item.Issue {
					if f.Attempt > 0 && r.Message == f.Item.Message && r.From == b.Scope.Actor && r.Teammate == b.Teammate {
						retireAttention(f)
					} else {
						f.Error = "ordinary attention slot occupied; waiting for its handler"
					}
					return nil
				}
			}
			err = t.Notify(ctx, f.Item.Issue, f.Item.Recipient, f.Item.Message)
		}
		if err == nil {
			retireAttention(f)
		} else {
			if f.Attempt < 1000000 {
				f.Attempt++
			}
			f.NextRetry = time.Now().Add(RetryDelay(f.Attempt))
			f.Error = "ordinary notification delivery failed"
			deliveryErr = err
		}
		return nil
	})
	return errors.Join(err, deliveryErr)
}

func retireAttention(f *attentionFile) {
	if f.Item.RecordRun {
		f.Unsettled = append(f.Unsettled, AttentionSettlement{Item: f.Item, CompletedAt: time.Now()})
	}
	f.Pending = false
	f.Error = ""
}

func (b AttentionOutbox) Unsettled() ([]AttentionSettlement, error) {
	paths, err := filepath.Glob(filepath.Join(b.Dir, "attention", "*.json"))
	if err != nil {
		return nil, err
	}
	var out []AttentionSettlement
	for _, path := range paths {
		var f attentionFile
		if err := statefs.ReadJSON(path, 98304, &f); err != nil {
			return nil, err
		}
		if f.Version != 1 || path != b.path(f.Item.Key) {
			return nil, errors.New("unsupported local attention or item key mismatch")
		}
		if f.Scope != b.Scope || f.Teammate != b.Teammate {
			continue
		}
		for _, completion := range f.Unsettled {
			if completion.Item.Key != f.Item.Key || !completion.Item.RecordRun || completion.CompletedAt.IsZero() {
				return nil, errors.New("unfinished attention settlement identity mismatch")
			}
			out = append(out, completion)
		}
	}
	return out, nil
}

func (b AttentionOutbox) FinishSettlement(completion AttentionSettlement) error {
	return b.edit(completion.Item.Key, func(f *attentionFile) error {
		for i, saved := range f.Unsettled {
			if saved.Item.Source == completion.Item.Source {
				if saved != completion {
					return errors.New("unfinished attention settlement changed")
				}
				f.Unsettled = append(f.Unsettled[:i], f.Unsettled[i+1:]...)
				return nil
			}
		}
		return nil
	})
}

type CLIAttention struct{ Client katacli.Client }

func (c CLIAttention) Inbox(ctx context.Context, recipient, issue string) ([]Request, error) {
	inbox, err := ReadInbox(ctx, c.Client, recipient)
	if err != nil || len(inbox.Requests) == 0 {
		return inbox.Requests, err
	}
	// Ordinary inbox entries carry short refs; local intent retains full UIDs.
	// Resolve only this target, never each entry or an assumed UID suffix.
	var target struct {
		Issue struct {
			UID     string `json:"uid"`
			ShortID string `json:"short_id"`
		} `json:"issue"`
	}
	if err := c.Client.Show(ctx, issue, &target); err != nil {
		return nil, err
	}
	if target.Issue.UID != issue || target.Issue.ShortID == "" {
		return nil, errors.New("ordinary attention issue identity mismatch")
	}
	for i := range inbox.Requests {
		if inbox.Requests[i].Ref == target.Issue.ShortID {
			inbox.Requests[i].Ref = issue
		}
	}
	return inbox.Requests, nil
}
func (c CLIAttention) Notify(ctx context.Context, issue, recipient, message string) error {
	return c.Client.Notify(ctx, issue, recipient, message, false, new(any))
}
func (b AttentionOutbox) Status() (pending, failed int, err error) {
	paths, e := filepath.Glob(filepath.Join(b.Dir, "attention", "*.json"))
	if e != nil {
		return 0, 0, e
	}
	for _, path := range paths {
		var f attentionFile
		if e := statefs.ReadJSON(path, 98304, &f); e != nil {
			return pending, failed, e
		}
		if f.Scope != b.Scope || f.Teammate != b.Teammate {
			continue
		}
		if f.Pending {
			pending++
		}
		pending += len(f.Unsettled)
		if f.Error != "" {
			failed++
		}
	}
	return
}

func (b AttentionOutbox) IsPending(key string) (bool, error) {
	var f attentionFile
	err := statefs.ReadJSON(b.path(key), 98304, &f)
	if err != nil {
		return false, err
	}
	if f.Version != 1 || f.Scope != b.Scope || f.Teammate != b.Teammate || f.Item.Key != key {
		return false, errors.New("saved attention routing changed")
	}
	return f.Pending, nil
}
func (b AttentionOutbox) PendingItems() ([]Attention, error) {
	paths, err := filepath.Glob(filepath.Join(b.Dir, "attention", "*.json"))
	if err != nil {
		return nil, err
	}
	var items []Attention
	for _, path := range paths {
		var f attentionFile
		if err := statefs.ReadJSON(path, 98304, &f); err != nil {
			return nil, err
		}
		if f.Version != 1 || path != b.path(f.Item.Key) {
			return nil, errors.New("unsupported local attention or item key mismatch")
		}
		if f.Scope == b.Scope && f.Teammate == b.Teammate && f.Pending {
			items = append(items, f.Item)
		}
	}
	return items, nil
}
