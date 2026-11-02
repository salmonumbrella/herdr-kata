package worker

import (
	"errors"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

func TestPerRunJobsDoNotNeedReadiness(t *testing.T) {
	w, h, k := fixture(t)
	k.failures = map[string]error{"ready": errors.New("ready list timed out")}
	k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "cron", Cron: new("0 * * * *")}
	k.jobs[0].CreatedAt = at.Add(-90 * time.Minute)
	k.jobs[0].Definition.Catchup = "latest"
	for _, now := range []time.Time{at.Add(-time.Minute), at.Add(time.Second)} {
		if err := w.Tick(t.Context(), now); err != nil {
			t.Fatalf("per-run tick read readiness: %v", err)
		}
	}
	if h.runs != 1 {
		t.Fatalf("launches = %d, want 1", h.runs)
	}
}

func TestExistingIssueJobStillNeedsReadiness(t *testing.T) {
	w, h, k := fixture(t)
	k.failures = map[string]error{"ready": errors.New("ready list timed out")}
	k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "cron", Cron: new("0 * * * *")}
	k.jobs[0].CreatedAt = at.Add(-90 * time.Minute)
	k.jobs[0].Definition.Catchup = "latest"
	k.jobs[0].Definition.Issue = &kata.IssuePolicy{Kind: "existing", UID: new("01J00000000000000000000099")}
	_ = w.Tick(t.Context(), at.Add(-time.Minute))
	if err := w.Tick(t.Context(), at.Add(time.Second)); err == nil {
		t.Fatal("existing-issue tick ignored the readiness failure")
	}
	if h.runs != 0 {
		t.Fatalf("launches = %d, want 0", h.runs)
	}
}
