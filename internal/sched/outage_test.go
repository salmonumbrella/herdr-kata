package sched

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
)

func TestRouteOutageBacklogUsesCatchupPolicy(t *testing.T) {
	for _, policy := range []string{"latest", "skip", "all"} {
		t.Run(policy, func(t *testing.T) {
			s, k, a, c := setup(t)
			d, _ := defs.DecodeJob(k.job.Definition)
			d.Catchup = policy
			d.GraceSeconds = 0
			k.job.Definition, _ = json.Marshal(d)
			if err := s.load(t.Context()); err != nil {
				t.Fatal(err)
			}
			k.showErr = errors.New("route unavailable")
			for range 3 {
				c.advance(time.Minute)
				tick(t, s)
			}
			if len(a.got) != 0 {
				t.Fatal("admitted through outage")
			}
			k.showErr = nil
			c.advance(5 * time.Second)
			a.result = Dropped // Free capacity so the whole selected backlog dispatches.
			tick(t, s)
			want := map[string]int{"latest": 1, "skip": 0, "all": 3}[policy]
			if len(a.got) != want {
				t.Fatalf("%s replayed %d occurrences, want %d", policy, len(a.got), want)
			}
			if policy == "latest" && !a.got[0].Occurrence.Equal(c.Now().Add(-5*time.Second)) {
				t.Fatalf("replayed stale occurrence: %v", a.got[0].Occurrence)
			}
			if len(pending(t, s)) != 0 {
				t.Fatal("backlog not checkpointed", pending(t, s))
			}
		})
	}
}
