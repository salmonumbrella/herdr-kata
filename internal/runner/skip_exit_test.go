package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

var gateOptions = json.RawMessage(`{"skip_exit":78}`)

func existingIssue(f *executionFixture) {
	f.s.Transition(f.uid, func(r *Run) error {
		r.IssueOwned, r.IssueRef = false, "project#queue"
		return nil
	})
}

// A closed gate skips its dependents, never retries, settles as success and,
// on an existing issue, posts nothing: the prompt never starts an agent.
func TestSkipExitClosesGateQuietly(t *testing.T) {
	f := workflowExecution(t,
		defs.Step{Key: "gate", Kind: "command", Command: "echo gate >> order; echo 'one PR open' > \"$HERDR_KATA_NOTE_FILE\"; exit 78", Retries: 2, Options: gateOptions},
		defs.Step{Key: "work", Kind: "prompt", Prompt: "ship the next one", After: []string{"gate"}},
		defs.Step{Key: "after", Kind: "command", Command: "echo after >> order", After: []string{"work"}},
	)
	existingIssue(f)
	r := executeWorkflow(t, f)
	b, _ := os.ReadFile(filepath.Join(r.Cwd, "order"))
	if r.Phase != Settled || r.Outcome != "succeeded" || string(b) != "gate\n" {
		t.Fatalf("run=%+v order=%q", r, b)
	}
	if g := r.Steps[0]; g.Status != "skipped" || g.Reason != GateClosed || g.Attempt != 1 || g.Note != "one PR open" {
		t.Fatalf("gate=%+v", g)
	}
	for _, s := range r.Steps[1:] {
		if s.Status != "skipped" || s.Reason != "dependency_not_succeeded" {
			t.Fatalf("dependent=%+v", s)
		}
	}
	if f.count("tab.create") != 0 || f.count("agent.start") != 0 || f.count("agent.prompt") != 0 {
		t.Fatal("closed gate started an agent")
	}
	wantSettleKinds(t, r.Ops[len(r.Ops)-1:], "observe")
	for _, op := range r.Ops {
		if op.Kind == "comment" || op.Kind == "meta.set" || op.Kind == "notify" {
			t.Fatalf("closed gate wrote to the issue: %+v", op)
		}
	}
	_, p := settlePayload(t, r.Ops, "observe")
	if p.Observation.Status != "succeeded" || !strings.Contains(p.Observation.Summary.Message, "gate gate closed, later steps skipped") {
		t.Fatal(p.Observation)
	}
}

// An open gate (exit 0) runs its dependents as usual.
func TestSkipExitOpenGateRunsDependents(t *testing.T) {
	f := workflowExecution(t,
		defs.Step{Key: "gate", Kind: "command", Command: "echo gate >> order", Options: gateOptions},
		defs.Step{Key: "work", Kind: "command", Command: "echo work >> order", After: []string{"gate"}},
	)
	existingIssue(f)
	r := executeWorkflow(t, f)
	b, _ := os.ReadFile(filepath.Join(r.Cwd, "order"))
	if r.Outcome != "succeeded" || string(b) != "gate\nwork\n" || r.Steps[0].Status != "succeeded" || r.Steps[1].Status != "succeeded" {
		t.Fatalf("run=%+v order=%q", r, b)
	}
}

// Any other nonzero exit is still an ordinary failure that retries.
func TestSkipExitOtherCodeFails(t *testing.T) {
	f := workflowExecution(t,
		defs.Step{Key: "gate", Kind: "command", Command: "echo gate >> order; exit 1", Retries: 1, Options: gateOptions},
		defs.Step{Key: "work", Kind: "command", Command: "echo work >> order", After: []string{"gate"}},
	)
	existingIssue(f)
	r := executeWorkflow(t, f)
	b, _ := os.ReadFile(filepath.Join(r.Cwd, "order"))
	if r.Outcome != "failed" || string(b) != "gate\ngate\n" || r.Steps[0].Status != "failed" || r.Steps[0].Attempt != 2 {
		t.Fatalf("run=%+v order=%q", r, b)
	}
	if !hasOp(r, "comment") {
		t.Fatal("failure stayed quiet")
	}
}

// Without skip_exit, exit 78 keeps its old meaning.
func TestSkipExitUndeclaredCodeFails(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "gate", Kind: "command", Command: "exit 78"})
	r := executeWorkflow(t, f)
	if r.Outcome != "failed" || r.Steps[0].Status != "failed" {
		t.Fatal(r)
	}
}

// A closed gate does not hide an independent failure.
func TestSkipExitIndependentFailureStillFails(t *testing.T) {
	f := workflowExecution(t,
		defs.Step{Key: "gate", Kind: "command", Command: "exit 78", Options: gateOptions},
		defs.Step{Key: "work", Kind: "command", Command: "true", After: []string{"gate"}},
		defs.Step{Key: "other", Kind: "command", Command: "exit 3"},
	)
	existingIssue(f)
	r := executeWorkflow(t, f)
	if r.Outcome != "failed" || !hasOp(r, "comment") {
		t.Fatal(r)
	}
}

// A per-run issue closes with the gate command as evidence.
func TestSkipExitPerRunCloses(t *testing.T) {
	f := workflowExecution(t,
		defs.Step{Key: "gate", Kind: "command", Command: "exit 78", Options: gateOptions},
		defs.Step{Key: "work", Kind: "prompt", Prompt: "work", After: []string{"gate"}},
	)
	f.s.Transition(f.uid, func(r *Run) error { r.IssueOwned = true; r.IssueRef = "project#task"; return nil })
	r := executeWorkflow(t, f)
	_, p := settlePayload(t, r.Ops, "close")
	if p.Close == nil || p.Close.Reason != "done" || len(p.Close.Evidence) != 1 || p.Close.Evidence[0] != "test:exit 78" || !strings.Contains(p.Close.Message, "gate gate closed") {
		t.Fatal(p)
	}
}

// Recovery maps a saved gate receipt the same way as live completion.
func TestRecoverGateReceiptSkips(t *testing.T) {
	f := newRecovery(t)
	now := time.Now()
	f.s.Transition(f.uid, func(r *Run) error {
		r.ExpiresAt = time.Now().Add(time.Minute)
		r.Phase = Running
		r.IssueOwned, r.IssueRef = false, "project#queue"
		r.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{
			{Key: "check", Kind: "command", Command: "exit 78", Retries: 1, Options: gateOptions},
			{Key: "work", Kind: "command", Command: "touch ran", After: []string{"check"}},
		}}
		r.Steps = []StepState{{Key: "check", Kind: "command", Attempt: 1, Submission: Sending}, {Key: "work", Kind: "command", Attempt: 1, Submission: None}}
		return nil
	})
	receipt := Receipt{Version: 1, Run: f.uid, Step: "check", Attempt: 1, ExitCode: 78, StartedAt: now, EndedAt: now, PGID: 99999999}
	if e := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.AttemptDir(f.uid, "check", 1), "receipt.json"), receipt); e != nil {
		t.Fatal(e)
	}
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r := loaded(t, f.s, f.uid)
	if r.Outcome != "succeeded" || r.Steps[0].Status != "skipped" || r.Steps[0].Reason != GateClosed || r.Steps[0].Attempt != 1 || r.Steps[1].Status != "skipped" {
		t.Fatal(r)
	}
	if _, e := os.Stat(filepath.Join(r.Cwd, "ran")); !os.IsNotExist(e) {
		t.Fatal("dependent ran after closed gate", e)
	}
}

func hasOp(r *Run, kind string) bool {
	for _, op := range r.Ops {
		if op.Kind == kind {
			return true
		}
	}
	return false
}

// A gate receipt from another attempt is not proof the gate closed.
func TestSettleGateRequiresBoundReceipt(t *testing.T) {
	r := commandFixture()
	r.IssueOwned = false
	r.WFDef.Steps[0].Options = gateOptions
	r.Steps[0].Status, r.Steps[0].Reason = "skipped", GateClosed
	r.Steps[0].Receipt.ExitCode = 78
	r.Steps[1].Status, r.Steps[1].Reason, r.Steps[1].Receipt = "skipped", "dependency_not_succeeded", nil
	wantSettleKinds(t, SettleOps(r, nil, settleTime), "observe")
	r.Steps[0].Receipt.Attempt = 2
	ops := SettleOps(r, nil, settleTime)
	if _, p := settlePayload(t, ops, "observe"); p.Observation.Status == "succeeded" {
		t.Fatal("stale gate receipt settled as success", p.Observation)
	}
}
