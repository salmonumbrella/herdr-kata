package katabridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/lockfile"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Comment struct {
	UID       string    `json:"uid"`
	IssueUID  string    `json:"issue_uid"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}
type HistoricalComment struct {
	Body     string `json:"body"`
	Author   string `json:"author"`
	Teammate string `json:"teammate,omitempty"`
}
type CommentTransport interface {
	SendComment(context.Context, string, string, string) error
	ReadComments(context.Context, string) ([]HistoricalComment, error)
}
type CommentOutbox struct {
	Dir      string
	Scope    Scope
	Teammate string
}
type commentFile struct {
	Version   int       `json:"version"`
	Scope     Scope     `json:"scope"`
	Teammate  string    `json:"teammate,omitempty"`
	Item      Comment   `json:"item"`
	Delivered bool      `json:"delivered"`
	Attempts  int       `json:"attempts"`
	NextRetry time.Time `json:"next_retry"`
	Error     string    `json:"error,omitempty"`
}

func CommentMarker(uid string) string { return "[herdr-kata result " + uid + "]" }
func (b CommentOutbox) path(uid string) string {
	sum := sha256.Sum256([]byte(uid))
	return filepath.Join(b.Dir, "comments", hex.EncodeToString(sum[:])+".json")
}
func (b CommentOutbox) Queue(item Comment) error {
	if len(item.UID) < 1 || len(item.UID) > 128 || strings.ContainsAny(item.UID, "\n\r[]") || item.CreatedAt.IsZero() || len(item.Body) > 65536 {
		return errors.New("invalid bounded historical comment")
	}
	if _, err := katacli.NormalizeUID(item.IssueUID); err != nil {
		return err
	}
	path := b.path(item.UID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	lock, err := lockfile.Acquire(path + ".lock")
	if err != nil {
		return err
	}
	defer lock.Release()
	old := commentFile{Version: 1, Scope: b.Scope, Teammate: b.Teammate, Item: item}
	if err := statefs.ReadJSON(path, 98304, &old); err == nil {
		if old.Version != 1 || old.Scope != b.Scope || old.Teammate != b.Teammate || old.Item.UID != item.UID || old.Item.IssueUID != item.IssueUID || old.Item.Body != item.Body || !old.Item.CreatedAt.Equal(item.CreatedAt) {
			return errors.New("historical comment identity changed")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return statefs.WriteJSON(path, old, 98304)
}
func (b CommentOutbox) Flush(ctx context.Context, t CommentTransport, now time.Time) error {
	paths, err := filepath.Glob(filepath.Join(b.Dir, "comments", "*.json"))
	if err != nil {
		return err
	}
	var problems []error
	attempts := 0
	for _, path := range paths {
		if attempts >= 100 || ctx.Err() != nil {
			break
		}
		lock, err := lockfile.Acquire(path + ".lock")
		if err != nil {
			problems = append(problems, err)
			continue
		}
		func() {
			defer lock.Release()
			var f commentFile
			if err := statefs.ReadJSON(path, 98304, &f); err != nil {
				problems = append(problems, err)
				return
			}
			if f.Delivered || now.Before(f.NextRetry) {
				return
			}
			if f.Version != 1 || f.Scope != b.Scope || f.Teammate != b.Teammate || path != b.path(f.Item.UID) {
				problems = append(problems, errors.New("saved historical comment routing changed"))
				return
			}
			body := f.Item.Body + "\n" + CommentMarker(f.Item.UID)
			key := "herdr-kata:result:" + f.Item.UID
			attempts++
			var sendErr error
			reconciled := false
			if !now.Before(f.Item.CreatedAt.Add(7 * 24 * time.Hour)) {
				var comments []HistoricalComment
				comments, sendErr = t.ReadComments(ctx, f.Item.IssueUID)
				if sendErr == nil {
					count := 0
					exact := false
					for _, c := range comments {
						if strings.Contains(c.Body, CommentMarker(f.Item.UID)) {
							count++
							exact = c.Body == body && c.Author == b.Scope.Actor && c.Teammate == b.Teammate
						}
					}
					if count > 1 || count == 1 && !exact {
						sendErr = errors.New("ambiguous historical comment marker; needs human inspection")
					} else if count == 1 {
						reconciled = true
					}
				}
			}
			if sendErr == nil && !reconciled {
				sendErr = t.SendComment(ctx, f.Item.IssueUID, body, key)
			}
			if sendErr == nil {
				f.Delivered = true
				f.Error = ""
				f.NextRetry = time.Time{}
			} else {
				if f.Attempts < 1000000 {
					f.Attempts++
				}
				f.Error = "historical comment delivery failed; inspect marker and selected target"
				f.NextRetry = now.Add(RetryDelay(f.Attempts))
				problems = append(problems, sendErr)
			}
			if err := statefs.WriteJSON(path, f, 98304); err != nil {
				problems = append(problems, err)
			}
		}()
	}
	return errors.Join(problems...)
}

type CLIComments struct{ Client katacli.Client }

func (t CLIComments) SendComment(ctx context.Context, issue, body, key string) error {
	return t.Client.Comment(ctx, issue, body, key, new(any))
}
func (t CLIComments) ReadComments(ctx context.Context, issue string) ([]HistoricalComment, error) {
	var out struct {
		Issue struct {
			UID string `json:"uid"`
		} `json:"issue"`
		Comments []HistoricalComment `json:"comments"`
	}
	if err := t.Client.Comments(ctx, issue, &out); err != nil {
		return nil, err
	}
	if out.Issue.UID != issue {
		return nil, fmt.Errorf("historical comment issue identity mismatch")
	}
	return out.Comments, nil
}
func (b CommentOutbox) Status() (pending, failed int, err error) {
	paths, e := filepath.Glob(filepath.Join(b.Dir, "comments", "*.json"))
	if e != nil {
		return 0, 0, e
	}
	for _, path := range paths {
		var f commentFile
		if e := statefs.ReadJSON(path, 98304, &f); e != nil {
			return pending, failed, e
		}
		if f.Scope != b.Scope || f.Teammate != b.Teammate {
			continue
		}
		if !f.Delivered {
			pending++
		}
		if f.Error != "" {
			failed++
		}
	}
	return
}
