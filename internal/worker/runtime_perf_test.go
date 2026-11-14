package worker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/clock"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
)

func TestRequestPollingForgetsCompletedRequests(t *testing.T) {
	w, h, k := fixture(t)
	request(t, w, journal.Request{ID: "first", Kind: "run", JobUID: k.jobs[0].UID})
	request(t, w, journal.Request{ID: "waiting", Kind: "run", JobUID: k.jobs[0].UID})
	tick(t, w, at)
	tick(t, w, at.Add(time.Second))
	q := journal.Requests{Dir: w.Store.Layout.Requests()}
	if done, reason, err := q.Result("first"); err != nil || !done || reason != "" || h.runs != 1 {
		t.Fatalf("first request: done=%v reason=%q launches=%d err=%v", done, reason, h.runs, err)
	}
	if done, reason, err := q.Result("waiting"); err != nil || done || reason == "" {
		t.Fatalf("pending request: done=%v reason=%q err=%v", done, reason, err)
	}
	if len(w.SeenRequests) != 1 || !w.SeenRequests["waiting"] {
		t.Fatalf("polling retains completed requests or forgets pending work: %v", w.SeenRequests)
	}
}

func TestSameTickRequestsRespectCapacity(t *testing.T) {
	for _, gate := range []string{"global", "project", "overlap"} {
		t.Run(gate, func(t *testing.T) {
			w, h, k := fixture(t)
			w.Config.Projects[0].MaxActiveRuns = 2
			k.jobs[0].Definition.Overlap = "allow"
			switch gate {
			case "global":
				w.Config.Limits.MaxActiveRuns = 1
			case "project":
				w.Config.Projects[0].MaxActiveRuns = 1
			case "overlap":
				k.jobs[0].Definition.Overlap = "forbid"
			}
			request(t, w, journal.Request{ID: "first", Kind: "run", JobUID: k.jobs[0].UID})
			request(t, w, journal.Request{ID: "waiting", Kind: "run", JobUID: k.jobs[0].UID})
			tick(t, w, at)
			first := active(t, w)
			q := journal.Requests{Dir: w.Store.Layout.Requests()}
			if done, reason, err := q.Result("waiting"); err != nil || done || reason == "" || h.runs != 1 || first.RequestID != "first" {
				t.Fatalf("same-tick gate bypassed: done=%v reason=%q launches=%d first=%q err=%v", done, reason, h.runs, first.RequestID, err)
			}
			finishAgent(t, w, h, first)
			for n := 1; n <= 3; n++ {
				tick(t, w, at.Add(time.Duration(n)*time.Second))
			}
			if done, reason, err := q.Result("waiting"); err != nil || !done || reason != "" || h.runs != 2 {
				t.Fatalf("freed capacity not refreshed: done=%v reason=%q launches=%d err=%v", done, reason, h.runs, err)
			}
		})
	}
}

func BenchmarkTickBlockedRequests(b *testing.B) {
	w, _, k := fixture(b)
	ctx := clock.WithNow(context.Background(), at)
	if err := w.Tick(ctx, at); err != nil {
		b.Fatal(err)
	}
	for n := range 128 {
		r := journal.Run{Version: 1, Key: journal.ManualKey("executor", "example-project", fmt.Sprint(n)), RunUID: fmt.Sprintf("%026d", n), Project: "example-project", JobUID: k.jobs[0].UID, Phase: "reported"}
		if n == 0 {
			r.Phase, r.ReportDone = "held", true
		}
		if err := w.Store.Create(r); err != nil {
			b.Fatal(err)
		}
	}
	q := journal.Requests{Dir: w.Store.Layout.Requests()}
	for n := range 32 {
		if err := q.Write(journal.Request{ID: fmt.Sprintf("waiting-%d", n), Kind: "run", JobUID: k.jobs[0].UID}); err != nil {
			b.Fatal(err)
		}
	}
	if err := w.Tick(ctx, at); err != nil {
		b.Fatal(err)
	}
	pending, err := q.Pending()
	if err != nil || len(pending) != 32 {
		b.Fatalf("blocked requests: %d %v", len(pending), err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := w.Tick(ctx, at); err != nil {
			b.Fatal(err)
		}
	}
}
