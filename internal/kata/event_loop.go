package kata

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type EventHooks struct {
	OnDefs, OnIssue, OnInbox func(Event)
	OnReset                  func()
}
type Tailer interface {
	Tail(context.Context, int64) (<-chan TailItem, <-chan error)
}
type Cursor struct {
	AfterID int64 `json:"after_id"`
}

// A new stream's start snapshot waits up to streamStartGrace for the first
// line, then for streamQuiet without a line, but never past streamReplayCap
// from the stream's start or maxHeldLines held lines.
const (
	streamStartGrace = time.Second
	streamQuiet      = 250 * time.Millisecond
	streamReplayCap  = 2 * time.Second
	maxHeldLines     = 1000
)

// Hooks enqueue coalesced reconciliation work. A handled cursor never suppresses
// startup/reset snapshots; it is only a transport optimization.
func RunEvents(ctx context.Context, k Tailer, l state.Layout, h EventHooks) error {
	var cursor Cursor
	if err := state.ReadJSON(state.OSFS{}, l.EventsCursor(), 1024, &cursor); err != nil && !errors.Is(err, fs.ErrNotExist) {
		if _, ok := errors.AsType[*state.CorruptError](err); !ok {
			return err
		}
		slog.Warn("discarding corrupt transport cursor", "error", err)
		cursor = Cursor{}
		if err := os.Remove(l.EventsCursor()); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	var items <-chan TailItem
	var errs <-chan error
	defer func() {
		cancel()
		// Tail closes these channels only after killing and reaping its child.
		// The worker must not exit while that child is still an orphan candidate.
		if items != nil {
			for range items {
			}
		}
		if errs != nil {
			for range errs {
			}
		}
	}()
	waits := 0
	for ctx.Err() == nil {
		started := time.Now()
		healthy := false
		items, errs = k.Tail(ctx, cursor.AfterID)
		// The stream's start snapshot waits for the stream's opening lines:
		// up to a second for the first, then until the stream is quiet for a
		// moment. Kata opens a stream from a stale cursor with a reset, and one
		// from an early cursor by replaying history. Lines held until the
		// snapshot are covered by it: their resets and definition events add
		// no refresh of their own, and their issue events follow it.
		snapshot := func() {
			if h.OnReset != nil {
				h.OnReset()
			}
		}
		handle := func(item TailItem, covered bool) error {
			if item.Reset != nil {
				if !covered {
					snapshot()
				}
				cursor.AfterID = item.Reset.AfterID
				return state.WriteJSON(state.OSFS{}, l.EventsCursor(), cursor)
			}
			e := item.Event
			switch {
			case strings.HasPrefix(e.Type, "cron.job."), strings.HasPrefix(e.Type, "cron.workflow."):
				if h.OnDefs != nil && !covered {
					h.OnDefs(e)
				}
			case strings.HasPrefix(e.Type, "issue."):
				if h.OnIssue != nil {
					h.OnIssue(e)
				}
			}
			switch e.Type {
			case "issue.created", "issue.metadata_updated", "issue.updated", "issue.closed", "issue.link_created", "issue.link_deleted", "issue.scheduled_reached":
				if h.OnInbox != nil {
					h.OnInbox(e)
				}
			}
			if e.EventID > cursor.AfterID {
				cursor.AfterID = e.EventID
				return state.WriteJSON(state.OSFS{}, l.EventsCursor(), cursor)
			}
			return nil
		}
		var held []TailItem
		pending := true
		// flush takes the start snapshot, then handles the lines it covers.
		flush := func() error {
			pending = false
			snapshot()
			for _, item := range held {
				if ctx.Err() != nil {
					return nil
				}
				if err := handle(item, true); err != nil {
					return err
				}
			}
			held = nil
			return nil
		}
		opening := time.NewTimer(streamStartGrace)
		for items != nil {
			select {
			case <-ctx.Done():
				opening.Stop()
				return nil
			case <-opening.C:
				if pending {
					if err := flush(); err != nil {
						return err
					}
				}
			case item, ok := <-items:
				if !ok {
					items = nil
					continue
				}
				if ctx.Err() != nil {
					opening.Stop()
					return nil
				}
				healthy = true
				if pending {
					held = append(held, item)
					if wait := min(streamQuiet, time.Until(started.Add(streamReplayCap))); wait > 0 && len(held) < maxHeldLines {
						opening.Reset(wait)
						continue
					}
					opening.Stop()
					if err := flush(); err != nil {
						return err
					}
					continue
				}
				if err := handle(item, false); err != nil {
					return err
				}
			}
		}
		opening.Stop()
		if pending {
			if err := flush(); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case err := <-errs:
			if err != nil && ctx.Err() == nil {
				slog.Warn("events disconnected", "error", err)
				if h.OnReset != nil {
					h.OnReset()
				}
			}
		}
		// A stalled hub can hold each stream for a minute without events, so
		// only an event or a stream that outlived the cap resets the backoff.
		if healthy || time.Since(started) >= WaitCap {
			waits = 0
		}
		timer := time.NewTimer(WaitDelay(time.Second, waits))
		waits++
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}
