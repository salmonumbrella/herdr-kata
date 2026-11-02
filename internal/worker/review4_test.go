package worker

import (
	"slices"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

func TestReenableResumesCatchupPolicy(t *testing.T) {
	for _, tt := range []struct {
		policy string
		want   []string
	}{
		{"all", []string{"2026-11-02T09:10:00Z", "2026-11-02T10:10:00Z", "2026-11-02T11:10:00Z"}},
		{"latest", []string{"2026-11-02T11:10:00Z"}},
		{"skip", nil},
	} {
		t.Run(tt.policy, func(t *testing.T) {
			w, h, k := fixture(t)
			w.Config.Projects[0].MaxActiveRuns = 4
			k.jobs[0].CreatedAt = at.Add(-50 * time.Minute)
			k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "interval", IntervalSeconds: new(int64(3600))}
			k.jobs[0].Definition.Catchup = tt.policy
			k.jobs[0].Definition.Overlap = "allow"
			tick(t, w, at)
			k.jobs[0].Definition.Enabled = false
			k.jobs[0].DefinitionEventUID = "paused"
			tick(t, w, at.Add(time.Hour))
			tick(t, w, at.Add(2*time.Hour))
			k.jobs[0].Definition.Enabled = true
			k.jobs[0].DefinitionEventUID = "resumed"
			tick(t, w, at.Add(3*time.Hour))
			tick(t, w, at.Add(3*time.Hour+time.Second))
			runs, err := w.Store.All()
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, r := range runs {
				got = append(got, r.Occurrence)
				if r.Plan.JobEventUID != "resumed" {
					t.Fatalf("old definition prepared: %s", r.Plan.JobEventUID)
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, tt.want) || h.runs != len(tt.want) {
				t.Fatalf("reenabled %s: occurrences=%v launches=%d want=%v", tt.policy, got, h.runs, tt.want)
			}
		})
	}
}

func TestPromptEditKeepsCommittedProgressAndDedup(t *testing.T) {
	w, h, k := fixture(t)
	w.Config.Projects[0].MaxActiveRuns = 4
	k.jobs[0].CreatedAt = at.Add(-50 * time.Minute)
	k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "interval", IntervalSeconds: new(int64(3600))}
	k.jobs[0].Definition.Catchup = "all"
	k.jobs[0].Definition.Overlap = "allow"
	tick(t, w, at)
	tick(t, w, at.Add(10*time.Minute))
	first := active(t, w)
	oldText := first.Plan.Steps[0].Text
	finishAgent(t, w, h, first)
	tick(t, w, at.Add(10*time.Minute+time.Second))
	k.jobs[0].Definition.Action.Prompt = new("Edited prompt")
	k.jobs[0].DefinitionEventUID = "prompt-edited"
	tick(t, w, at.Add(3*time.Hour))
	tick(t, w, at.Add(3*time.Hour+time.Second))
	runs, err := w.Store.All()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range runs {
		got = append(got, r.Occurrence)
		if r.Key == first.Key {
			if r.RunUID != first.RunUID || r.Plan.Steps[0].Text != oldText {
				t.Fatal("committed run identity or prompt changed")
			}
		} else if r.Plan.JobEventUID != "prompt-edited" || r.Plan.Steps[0].Text != "Edited prompt" {
			t.Fatal("new occurrence did not freeze edited prompt")
		}
	}
	slices.Sort(got)
	want := []string{"2026-11-02T09:10:00Z", "2026-11-02T10:10:00Z", "2026-11-02T11:10:00Z"}
	if !slices.Equal(got, want) || h.runs != 3 {
		t.Fatalf("prompt edit reset or duplicated progress: occurrences=%v launches=%d", got, h.runs)
	}
}
