package worker

import (
	"context"
	"github.com/salmonumbrella/herdr-kata/internal/clock"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIssueScheduleHasJobCreationFloor(t *testing.T) {
	for _, offset := range []time.Duration{-time.Second, 0} {
		t.Run(offset.String(), func(t *testing.T) {
			w, h, k := fixture(t)
			uid := "01J00000000000000000000020"
			k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "issue-scheduled", IssueUID: &uid}
			k.jobs[0].Definition.Issue = &kata.IssuePolicy{Kind: "existing", UID: &uid}
			k.ready[uid] = true
			date := k.jobs[0].CreatedAt.Add(offset)
			k.date = &kata.DateValue{Value: date.Format(time.RFC3339), Instant: date}
			tick(t, w, at)
			want := 1
			if offset < 0 {
				want = 0
			}
			if h.runs != want {
				t.Fatalf("date relative to creation %s: launches=%d want=%d", offset, h.runs, want)
			}
		})
	}
}

func TestInvalidWorkflowHasOwnHoldReason(t *testing.T) {
	w, _, k := fixture(t)
	workflow(k)
	k.workflows[0].Definition.Steps[0].After = []string{"work"}
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
	tick(t, w, at)
	if got := active(t, w); got.Hold != "invalid_workflow" {
		t.Fatalf("invalid workflow mislabeled: %s", got.Hold)
	}
}

func TestAttemptDirectoryErrorRetriesWithoutInvalidPromptHold(t *testing.T) {
	w, h, k := fixture(t)
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
	// Prepare the journal without advancing into launch.
	tick(t, w, at)
	r := active(t, w)
	// A second workflow step encounters a genuine attempt-directory I/O failure.
	finishAgent(t, w, h, r)
	if err := w.Store.Update(r.Key, func(r *journal.Run) error {
		r.Plan.Steps = append(r.Plan.Steps, defs.Step{Key: "next", Kind: "prompt", Text: "Fixture", After: []string{"main"}, Argv: []string{"/bin/sh"}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(w.Store.Layout.Run(r.Key), "attempts", "next")
	if err := os.WriteFile(dir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := w.Tick(clock.WithNow(context.Background(), at.Add(time.Second)), at.Add(time.Second)); err == nil {
		t.Fatal("I/O error not returned")
	}
	got, _ := w.Store.Load(r.Key)
	if got.Phase == "held" {
		t.Fatalf("transient I/O held run: %s", got.Hold)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	tick(t, w, at.Add(2*time.Second))
	if h.runs != 2 {
		t.Fatalf("repaired path did not retry: %d launches", h.runs)
	}
}
