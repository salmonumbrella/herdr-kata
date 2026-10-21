package kata

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// The transport cursor and refresh hooks must follow wire order, even when
// a slow startup snapshot lets the tail buffer both kinds of line.
func TestEventsPreserveResetOrder(t *testing.T) {
	for _, tc := range []struct {
		name, wire string
		wantHooks  []string
		wantCursor int64
	}{
		// A leading reset is the stream's start snapshot.
		{"reset then event", "{\"reset_required\":true,\"reset_after_id\":8}\n{\"event_id\":9,\"type\":\"issue.updated\"}\n", []string{"snapshot", "event"}, 9},
		// So is a reset among the stream's opening lines.
		{"event then reset", "{\"event_id\":10,\"type\":\"issue.updated\"}\n{\"reset_required\":true,\"reset_after_id\":3}\n", []string{"snapshot", "event"}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := state.Layout{Root: t.TempDir()}
			if err := state.WriteJSON(state.OSFS{}, l.EventsCursor(), Cursor{AfterID: 7}); err != nil {
				t.Fatal(err)
			}
			cli, reaped := tailCLI(t, tc.wire, false, "")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var mu sync.Mutex
			var hooks []string
			record := func(name string) {
				mu.Lock()
				defer mu.Unlock()
				hooks = append(hooks, name)
			}
			// The hooks and cursor follow the whole buffered stream before
			// the reconnect backoff; stop once the cursor reaches its end.
			go func() {
				tick := time.NewTicker(time.Millisecond)
				defer tick.Stop()
				for ; ctx.Err() == nil; <-tick.C {
					var cursor Cursor
					mu.Lock()
					n := len(hooks)
					mu.Unlock()
					if n == len(tc.wantHooks) && state.ReadJSON(state.OSFS{}, l.EventsCursor(), 1024, &cursor) == nil && cursor.AfterID == tc.wantCursor {
						cancel()
						return
					}
				}
			}()
			done := make(chan error, 1)
			go func() {
				done <- RunEvents(ctx, cli, l, EventHooks{
					OnReset: func() {
						if len(hooks) == 0 {
							<-reaped
							record("snapshot")
						} else {
							record("reset")
						}
					},
					OnIssue: func(Event) { record("event") },
				})
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("ordered stream did not finish")
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(hooks, tc.wantHooks) {
				t.Errorf("hooks = %v, want %v", hooks, tc.wantHooks)
			}
			var cursor Cursor
			if err := state.ReadJSON(state.OSFS{}, l.EventsCursor(), 1024, &cursor); err != nil || cursor.AfterID != tc.wantCursor {
				t.Fatalf("cursor = %+v, want %d: %v", cursor, tc.wantCursor, err)
			}
		})
	}
}

func TestEventsCancellationSkipsBufferedHooks(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	if err := state.WriteJSON(state.OSFS{}, l.EventsCursor(), Cursor{AfterID: 7}); err != nil {
		t.Fatal(err)
	}
	cli, reaped := tailCLI(t, "{\"event_id\":9,\"type\":\"issue.updated\"}\n", false, "")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := RunEvents(ctx, cli, l, EventHooks{
		OnReset: func() { <-reaped; cancel() },
		OnIssue: func(Event) { t.Error("handled buffered event after cancellation") },
		OnInbox: func(Event) { t.Error("handled buffered inbox event after cancellation") },
	}); err != nil {
		t.Fatal(err)
	}
	var cursor Cursor
	if err := state.ReadJSON(state.OSFS{}, l.EventsCursor(), 1024, &cursor); err != nil || cursor.AfterID != 7 {
		t.Fatalf("canceled stream advanced cursor: %+v, %v", cursor, err)
	}
}

// Kata answers a stream from a stale or empty cursor with a reset before any
// event. That reset and the stream's start snapshot are one refresh, not two;
// a stream with nothing to say still gets its start snapshot.
func TestStreamStartAndLeadingResetRefreshOnce(t *testing.T) {
	for _, tc := range []struct {
		name, wire string
		want       []string
	}{
		{"leading reset", "{\"reset_required\":true,\"reset_after_id\":8}\n", []string{"snapshot"}},
		{"quiet stream", "", []string{"snapshot"}},
		{"leading event", "{\"event_id\":9,\"type\":\"issue.updated\"}\n", []string{"snapshot", "event"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l := state.Layout{Root: t.TempDir()}
				if err := state.WriteJSON(state.OSFS{}, l.EventsCursor(), Cursor{AfterID: 7}); err != nil {
					t.Fatal(err)
				}
				cli, _ := tailCLI(t, tc.wire, true, "")
				ctx, cancel := context.WithCancel(t.Context())
				var got []string
				done := make(chan error, 1)
				go func() {
					done <- RunEvents(ctx, cli, l, EventHooks{
						OnReset: func() { got = append(got, "snapshot") },
						OnIssue: func(Event) { got = append(got, "event") },
					})
				}()
				// The stream stays open well past the start grace.
				time.Sleep(10 * time.Second)
				synctest.Wait()
				cancel()
				<-done
				if !slices.Equal(got, tc.want) {
					t.Fatalf("hooks %v, want %v", got, tc.want)
				}
			})
		})
	}
}

// timedTail streams each line after its delay from the stream's start, then
// holds the stream open.
func timedTail(t *testing.T, lines ...timedLine) *CLI {
	t.Helper()
	c := fakeCLI(t, "", nil)
	c.starter = func(ctx context.Context, _, _ []string, _ string) (Proc, error) {
		outR, outW := io.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer outW.Close()
			start := time.Now()
			for _, l := range lines {
				timer := time.NewTimer(time.Until(start.Add(l.after)))
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				if _, err := io.WriteString(outW, l.line+"\n"); err != nil {
					return
				}
			}
			<-ctx.Done()
		}()
		return Proc{Stdout: outR, Stderr: strings.NewReader(""), Wait: func() error { <-done; return nil }}, nil
	}
	return c
}

type timedLine struct {
	after time.Duration
	line  string
}

// Kata 0.19.0 replays a project's history to a stream from an early cursor:
// project.created, cron.workflow.created, cron.job.created. The stream's
// start refresh lists after that replay, so the replayed definition events
// add no listing of their own. A definition change after the replay still
// refreshes, and a replayed issue event still arrives after the snapshot.
func TestStartRefreshCoversReplayedDefinitions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := state.Layout{Root: t.TempDir()}
		if err := state.WriteJSON(state.OSFS{}, l.EventsCursor(), Cursor{AfterID: 7}); err != nil {
			t.Fatal(err)
		}
		cli := timedTail(t,
			timedLine{10 * time.Millisecond, `{"event_id":8,"type":"project.created"}`},
			timedLine{20 * time.Millisecond, `{"event_id":9,"type":"cron.workflow.created"}`},
			timedLine{30 * time.Millisecond, `{"event_id":10,"type":"issue.updated"}`},
			timedLine{40 * time.Millisecond, `{"event_id":11,"type":"cron.job.created"}`},
			timedLine{time.Minute, `{"event_id":12,"type":"cron.job.updated"}`},
		)
		ctx, cancel := context.WithCancel(t.Context())
		var got []string
		done := make(chan error, 1)
		go func() {
			done <- RunEvents(ctx, cli, l, EventHooks{
				OnReset: func() { got = append(got, "snapshot") },
				OnDefs:  func(e Event) { got = append(got, fmt.Sprintf("defs %d", e.EventID)) },
				OnIssue: func(e Event) { got = append(got, fmt.Sprintf("issue %d", e.EventID)) },
			})
		}()
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		cancel()
		<-done
		if want := []string{"snapshot", "issue 10", "defs 12"}; !slices.Equal(got, want) {
			t.Fatalf("hooks %v, want %v", got, want)
		}
		var cursor Cursor
		if err := state.ReadJSON(state.OSFS{}, l.EventsCursor(), 1024, &cursor); err != nil || cursor.AfterID != 12 {
			t.Fatalf("cursor %+v: %v", cursor, err)
		}
	})
}
