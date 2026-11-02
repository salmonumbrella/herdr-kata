package main

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/worker"
)

type unavailableKata struct{ kata.Kata }

func (unavailableKata) Jobs() ([]kata.CronJob, error) { return nil, &kata.Error{Code: 7} }
func (unavailableKata) Observe(string, []byte, int) (int, bool, error) {
	return 0, false, &kata.Error{Code: 7}
}

func TestPendingRequestTriggersFastPollOnlyOnce(t *testing.T) {
	for _, kind := range []string{"run", "release"} {
		t.Run(kind, func(t *testing.T) {
			_, l := operatorFixture(t)
			cfg := config.Config{Projects: []config.Project{{Name: "example-project", Actor: "worker"}}}
			w := worker.New(cfg, journal.New(l), nil, map[string]kata.Kata{"example-project": unavailableKata{}}, "/bin/false")
			_ = w.Tick(context.Background(), time.Now().UTC())
			req := journal.Request{ID: "waiting", Kind: kind, JobUID: "01J00000000000000000000010"}
			if kind == "release" {
				r := localRun(t, l, "held")
				req.Run = r.RunUID
			}
			q := journal.Requests{Dir: l.Requests()}
			if err := q.Write(req); err != nil {
				t.Fatal(err)
			}
			calls := 0
			advance := func() { calls++; _ = w.Tick(context.Background(), time.Now().UTC()) }
			for range 5 {
				pollNewRequests(q, w.SeenRequests, advance, io.Discard)
			}
			if calls != 1 {
				t.Fatalf("known pending request caused %d fast ticks", calls)
			}
			if done, _, err := q.Result(req.ID); err != nil || done {
				t.Fatalf("pending request was consumed: %v %v", done, err)
			}
			advance() // The regular 30-second tick still retries pending work.
			pollNewRequests(q, w.SeenRequests, advance, io.Discard)
			if calls != 2 {
				t.Fatalf("fast poll repeated regular retry: %d calls", calls)
			}
		})
	}
}
