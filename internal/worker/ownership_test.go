package worker

import (
	"errors"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"testing"
	"time"
)

// Excluded jobs must not cause readiness reads, observations, journals or launches.
func TestUnownedJobsFilteredBeforeReadiness(t *testing.T) {
	for _, tt := range []struct {
		name, reason string
		change       func(*kata.CronJob)
	}{
		{"adapter", "unsupported job: adapter: other", func(j *kata.CronJob) { j.Definition.Options = map[string]any{"adapter": "other"} }},
		{"non-string adapter", "unsupported job: adapter: 7", func(j *kata.CronJob) { j.Definition.Options = map[string]any{"adapter": 7} }},
		{"notify", "job action is not execute", func(j *kata.CronJob) { j.Definition.Action.Kind = "notify" }},
		{"disabled", "job is disabled", func(j *kata.CronJob) { j.Definition.Enabled = false }},
		{"deleted", "job is deleted", func(j *kata.CronJob) { j.DeletedAt = new(at) }},
		{"foreign pin", "job is pinned to executor other", func(j *kata.CronJob) { j.Definition.Options = map[string]any{"executor": "other"} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w, h, k := fixture(t)
			j := &k.jobs[0]
			j.Definition.Issue = &kata.IssuePolicy{Kind: "existing", UID: new("issue")}
			tt.change(j)
			k.failures = map[string]error{"ready": errors.New("unexpected readiness read")}
			request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: j.UID})
			tick(t, w, at)
			tick(t, w, at.Add(time.Second))
			done, reason, err := (journal.Requests{Dir: w.Store.Layout.Requests()}).Result("request")
			if err != nil || !done || reason != tt.reason {
				t.Fatalf("request %v %q %v", done, reason, err)
			}
			runs, err := w.Store.All()
			if err != nil || len(runs) != 0 || h.runs != 0 || len(k.bodies) != 0 || len(w.Unsupported) != 0 {
				t.Fatalf("runs %v err %v launches %d bodies %d unsupported %v", runs, err, h.runs, len(k.bodies), w.Unsupported)
			}
		})
	}
}

func TestExecutorPinOwnership(t *testing.T) {
	for _, pin := range []any{"executor", nil, "", 42} {
		w, h, k := fixture(t)
		k.jobs[0].Definition.Options = map[string]any{"executor": pin}
		request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
		tick(t, w, at)
		if pin == "executor" {
			if h.runs != 1 {
				t.Fatalf("launches %d", h.runs)
			}
		} else {
			_, reason, _ := (journal.Requests{Dir: w.Store.Layout.Requests()}).Result("request")
			if reason != "unsupported job: options.executor must be a non-empty string" || h.runs != 0 {
				t.Fatalf("pin %v reason %q launches %d", pin, reason, h.runs)
			}
		}
	}
}
