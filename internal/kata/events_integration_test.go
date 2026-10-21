//go:build kata

package kata

import (
	"context"
	"testing"
	"time"
)

func TestIntegTailSeesJobCreatedAndResumes(t *testing.T) {
	c := newKata(t)
	ctx, cancel := context.WithCancel(t.Context())
	items, errs := c.Tail(ctx, 0)
	fixtureJob(t, c)
	var cursor int64
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for cursor == 0 {
		select {
		case item := <-items:
			if item.Reset != nil {
				continue
			}
			e := item.Event
			if e.Type == "cron.job.created" {
				cursor = e.EventID
			}
		case err := <-errs:
			t.Fatalf("tail exited: %v", err)
		case <-timer.C:
			t.Fatal("no job event")
		}
	}
	cancel()
	for range errs {
	}
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	items, errs = c.Tail(ctx, cursor)
	i := fixtureIssue(t, c)
	timer.Reset(10 * time.Second)
	for {
		select {
		case item := <-items:
			if item.Reset != nil {
				continue
			}
			e := item.Event
			if e.EventID <= cursor {
				t.Fatal("cursor replay", e)
			}
			if e.Type == "issue.created" && e.IssueUID == i.UID {
				cancel()
				for range errs {
				}
				return
			}
		case err := <-errs:
			t.Fatalf("resumed tail exited: %v", err)
		case <-timer.C:
			t.Fatal("no resumed event")
		}
	}
}
