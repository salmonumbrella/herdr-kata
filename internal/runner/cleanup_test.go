package runner

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
)

// A resumed attempt can succeed while its original pre-prompt tab has no
// session identity. Success never supplies the missing ownership proof.
func TestResumedSuccessRetainsOriginalTabWithoutSession(t *testing.T) {
	f := newRecovery(t)
	f.launch(t)
	old := loaded(t, f.s, f.uid).Herdr
	f.h.RemoveAgent(old.AgentName)
	old.Session = nil
	_, err := f.s.Transition(f.uid, func(r *Run) error {
		r.Herdr = old
		r.PastAttempts = []StepState{{Key: "main", Kind: "prompt", Attempt: 1, Submission: None, Herdr: &old}}
		r.Steps = []StepState{{Key: "main", Kind: "prompt", Attempt: 2, Submission: None, LoopRerun: true}}
		r.Resume = &ResumeState{UID: "resume", Step: "main", Attempt: 2, Generation: 1}
		r.ConsumedResumes = []string{"resume"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.launch(t)
	f.submit(t)
	f.result(t, "succeeded")
	if err := f.m.finish(t.Context(), f.uid, f.await(t)); err != nil {
		t.Fatal(err)
	}
	r := loaded(t, f.s, f.uid)
	if r.Outcome != "succeeded" || f.count("agent.prompt") != 1 {
		t.Fatal("resumed execution did not succeed once", r.Outcome)
	}
	if _, err := f.m.h.TabGet(t.Context(), old.TabID); err != nil {
		t.Fatal("closed original tab without session proof", err)
	}
	if _, err := f.m.h.TabGet(t.Context(), AgentReference(r).TabID); !herdrCode(err, "tab_not_found") {
		t.Fatal("retained the proven successful attempt's tab", err)
	}
}

func TestCleanupUsesEachSavedOccupant(t *testing.T) {
	for _, outcome := range []string{"succeeded", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			f := workflowExecution(t,
				defs.Step{Key: "shared", Kind: "prompt", Prompt: "shared"},
				defs.Step{Key: "fresh", Kind: "prompt", Prompt: "fresh", After: []string{"shared"}, Options: json.RawMessage(`{"agent_mode":"fresh"}`)},
			)
			f.h.BeforeRequest = func(req herdrtest.Request) {
				if req.Method != "agent.prompt" {
					return
				}
				r, err := f.s.Load(f.uid)
				if err != nil {
					panic(err)
				}
				for _, s := range r.Steps {
					if s.Submission != Sending || s.Status != "" {
						continue
					}
					status := "succeeded"
					if s.Key == "fresh" {
						status = outcome
					}
					if err := WriteResult(f.s.fsys, filepath.Join(f.s.layout.AttemptDir(f.uid, s.Key, s.Attempt), "result.json"), &Result{Version: 1, Run: f.uid, Step: s.Key, Attempt: s.Attempt, Status: status}); err != nil {
						panic(err)
					}
				}
			}
			r := executeWorkflow(t, f)
			if r.Outcome != outcome || r.Steps[1].Herdr == nil {
				t.Fatalf("outcome=%s steps=%+v", r.Outcome, r.Steps)
			}
			refs := []HerdrRef{r.Herdr, *r.Steps[1].Herdr}
			if refs[0].PaneID == refs[1].PaneID {
				t.Fatal("fixture did not create distinct occupants")
			}
			for _, ref := range refs {
				if outcome == "succeeded" {
					if _, err := f.m.h.TabGet(t.Context(), ref.TabID); !herdrCode(err, "tab_not_found") {
						t.Fatalf("saved tab %s retained: %v", ref.TabID, err)
					}
				} else {
					pane, err := f.m.h.PaneGet(t.Context(), ref.PaneID)
					if err != nil || pane.Tokens["phase"] != "failed" || pane.Tokens["run"] != f.uid {
						t.Fatalf("saved pane metadata = %+v, err=%v", pane, err)
					}
				}
			}
		})
	}
}
