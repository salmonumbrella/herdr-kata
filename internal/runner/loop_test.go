package runner

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
)

func loopWorkflow(t *testing.T) *executionFixture {
	f := workflowExecution(t, defs.Step{Key: "make", Kind: "command", Command: "true"}, defs.Step{Key: "check", Kind: "command", Command: "false", After: []string{"make"}, Options: json.RawMessage(`{"on_fail":{"goto":"make","max_loops":3}}`)})
	if _, e := f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = time.Now().Add(30 * time.Second); return nil }); e != nil {
		t.Fatal(e)
	}
	return f
}
func saveVerdict(t *testing.T, f *executionFixture, note string) {
	t.Helper()
	r := loaded(t, f.s, f.uid)
	for i, s := range r.Steps {
		if e := f.m.runCommandStep(t.Context(), f.uid, i, r.WFDef.Steps[i]); e != nil {
			t.Fatal(e)
		}
		_ = s
	}
	if _, e := f.s.Transition(f.uid, func(r *Run) error { r.Steps[1].Note = note; return nil }); e != nil {
		t.Fatal(e)
	}
}
func TestLoopAtomicDecisionAndComparison(t *testing.T) {
	for _, note := range []string{"", "same diagnosis"} {
		t.Run(note, func(t *testing.T) {
			f := loopWorkflow(t)
			saveVerdict(t, f, note)
			if e := f.m.takeLoop(t.Context(), f.uid, 1); e != nil {
				t.Fatal(e)
			}
			r := loaded(t, f.s, f.uid)
			if r.Loop.Used != 1 || r.Steps[0].Attempt != 2 || r.Steps[1].Attempt != 2 || r.Steps[0].Submission != None {
				t.Fatal(r)
			}
			if e := f.m.takeLoop(t.Context(), f.uid, 1); e != nil {
				t.Fatal(e)
			}
			if loaded(t, f.s, f.uid).Loop.Used != 1 {
				t.Fatal("debit repeated")
			}
			// Re-open the store to exercise persisted comparison and attempt identity.
			f.s = NewStore(f.s.fsys, f.s.layout)
			f.m.store = f.s
			saveVerdict(t, f, note)
			if e := f.m.takeLoop(t.Context(), f.uid, 1); e != nil {
				t.Fatal(e)
			}
			r = loaded(t, f.s, f.uid)
			if note == "" {
				if r.Loop.Used != 2 || r.Reason == "loop_stuck" {
					t.Fatal(r)
				}
			} else if r.Phase != Held || r.Reason != "loop_stuck" || r.Loop.Used != 1 {
				t.Fatal(r)
			}
		})
	}
}
func TestSavedVerdictHoldsPending(t *testing.T) {
	f := loopWorkflow(t)
	saveVerdict(t, f, "fix it")
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r := loaded(t, f.s, f.uid)
	hasFinding := false
	for _, op := range r.Ops {
		p, _ := decodePayload(op)
		if p.Chain != nil {
			hasFinding = true
		}
	}
	if !hasFinding {
		t.Fatal("recovered verdict lost its finding")
	}
	if r.Phase != Held || r.Reason != "loop_pending" || r.Loop.Used != 0 || r.Steps[0].Attempt != 1 {
		t.Fatal(r)
	}
}

func TestLoopExhaustedWithoutNotes(t *testing.T) {
	f := loopWorkflow(t)
	for range 4 {
		saveVerdict(t, f, "")
		if e := f.m.takeLoop(t.Context(), f.uid, 1); e != nil {
			t.Fatal(e)
		}
	}
	r := loaded(t, f.s, f.uid)
	if r.Phase != Held || r.Reason != "loop_exhausted" || r.Loop.Used != 3 || r.Steps[1].Attempt != 4 || r.Loop.Comparable {
		t.Fatal(r)
	}
}
