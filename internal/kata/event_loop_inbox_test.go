package kata

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// inboxTail streams live events: they follow the stream's start snapshot.
type inboxTail struct{ started, processed chan struct{} }

func (k inboxTail) Tail(ctx context.Context, _ int64) (<-chan TailItem, <-chan error) {
	items := make(chan TailItem)
	err := make(chan error)
	go func() {
		defer close(err)
		defer close(items)
		select {
		case <-k.started:
		case <-ctx.Done():
			return
		}
		for _, v := range kataEventList() {
			select {
			case items <- TailItem{Event: v}:
			case <-ctx.Done():
				return
			}
		}
		// Receiving a replay proves the previous event's hooks and cursor
		// write finished, while keeping this live tail open until cancellation.
		select {
		case items <- TailItem{Event: Event{EventID: 7, Type: "cron.run.observed"}}:
			close(k.processed)
		case <-ctx.Done():
			return
		}
		<-ctx.Done()
	}()
	return items, err
}
func kataEventList() []Event {
	return []Event{{EventID: 1, Type: "issue.created"}, {EventID: 2, Type: "issue.metadata_updated"}, {EventID: 3, Type: "issue.updated"}, {EventID: 4, Type: "issue.closed"}, {EventID: 5, Type: "issue.scheduled_reached"}, {EventID: 6, Type: "cron.run.observed"}, {EventID: 7, Type: "issue.commented"}}
}
func TestInboxEventsPokeBeforeCursor(t *testing.T) {
	var calls []int64
	ctx, cancel := context.WithCancel(t.Context())
	l := state.Layout{Root: t.TempDir()}
	tail := inboxTail{started: make(chan struct{}), processed: make(chan struct{})}
	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		runErr = RunEvents(ctx, tail, l, EventHooks{OnReset: func() { close(tail.started) }, OnInbox: func(e Event) {
			var c Cursor
			if err := state.ReadJSON(state.OSFS{}, l.EventsCursor(), 1024, &c); err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("cursor before inbox hook: %v", err)
			}
			if c.AfterID >= e.EventID {
				t.Errorf("cursor %d advanced before inbox hook for event %d", c.AfterID, e.EventID)
			}
			calls = append(calls, e.EventID)
		}})
	}()
	defer func() {
		cancel()
		<-done
		if runErr != nil {
			t.Error(runErr)
		}
	}()
	select {
	case <-tail.processed:
	case <-done:
		t.Fatalf("event stream returned before completion: %v", runErr)
	case <-t.Context().Done():
		t.Fatal("event stream did not finish:", t.Context().Err())
	}
	var c Cursor
	if err := state.ReadJSON(state.OSFS{}, l.EventsCursor(), 1024, &c); err != nil || c.AfterID != 7 {
		t.Fatalf("final cursor: %+v, %v", c, err)
	}
	if !slices.Equal(calls, []int64{1, 2, 3, 4, 5}) {
		t.Fatalf("inbox pokes: %v", calls)
	}
}
