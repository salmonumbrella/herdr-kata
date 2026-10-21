package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/agentlimit"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/usage"
)

func workflowExecution(t *testing.T, steps ...defs.Step) *executionFixture {
	t.Helper()
	f := newExecution(t)
	_, err := f.s.Transition(f.uid, func(r *Run) error {
		r.JobDef.Action = defs.Action{Kind: "execute", WorkflowUID: "workflow"}
		r.WFDef = &defs.WorkflowDef{Version: 1, Steps: steps}
		r.Steps = nil
		for _, s := range steps {
			r.Steps = append(r.Steps, StepState{Key: s.Key, Kind: s.Kind, Attempt: 1, Submission: None})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func executeWorkflow(t *testing.T, f *executionFixture) *Run {
	t.Helper()
	if e := f.m.Execute(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	return loaded(t, f.s, f.uid)
}
func TestTopoSerialSkip(t *testing.T) {
	f := workflowExecution(t,
		defs.Step{Key: "dependent", Kind: "command", Command: "echo bad >> order", After: []string{"failed"}},
		defs.Step{Key: "failed", Kind: "command", Command: "echo first >> order; exit 1"},
		defs.Step{Key: "independent", Kind: "command", Command: "echo second >> order"},
		defs.Step{Key: "transitive", Kind: "command", Command: "echo bad >> order", After: []string{"dependent"}},
	)
	r := executeWorkflow(t, f)
	b, e := os.ReadFile(filepath.Join(r.Cwd, "order"))
	if e != nil || string(b) != "first\nsecond\n" || r.Outcome != "failed" || r.Steps[0].Status != "skipped" || r.Steps[3].Status != "skipped" {
		t.Fatalf("run=%+v output=%q err=%v", r, b, e)
	}
}
func TestResumeSkipsSucceeded(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "first", Kind: "command", Command: "echo duplicate >> marker"}, defs.Step{Key: "second", Kind: "command", Command: "echo once >> marker", After: []string{"first"}})
	f.s.Transition(f.uid, func(r *Run) error {
		r.Phase = Running
		r.Steps[0].Status = "succeeded"
		r.Steps[0].Receipt = &Receipt{Version: 1, Run: r.UID, Step: "first", Attempt: 1, StartedAt: time.Now(), EndedAt: time.Now()}
		return nil
	})
	r := executeWorkflow(t, f)
	b, e := os.ReadFile(filepath.Join(r.Cwd, "marker"))
	if e != nil || string(b) != "once\n" || r.Outcome != "succeeded" {
		t.Fatal(r, string(b), e)
	}
}
func TestCommandNonzeroRetries(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "check", Kind: "command", Command: "if [ -f marker ]; then exit 0; fi; touch marker; exit 7", Retries: 1})
	r := executeWorkflow(t, f)
	if r.Outcome != "succeeded" || r.Steps[0].Attempt != 2 || r.Steps[0].Receipt == nil || r.Steps[0].Receipt.ExitCode != 0 {
		t.Fatal(r)
	}
}
func TestCommandNoReceiptUnknown(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "check", Kind: "command", Command: "touch reran", Retries: 3})
	f.s.Transition(f.uid, func(r *Run) error { r.Phase = Running; r.Steps[0].Submission = Sending; return nil })
	r := executeWorkflow(t, f)
	if r.Phase != Held || r.Outcome != "unknown" || r.Steps[0].Attempt != 1 {
		t.Fatal(r)
	}
	if _, e := os.Stat(filepath.Join(r.Cwd, "reran")); !os.IsNotExist(e) {
		t.Fatal("resent command", e)
	}
}
func TestCommandOnlyNoTab(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "check", Kind: "command", Command: "true"})
	r := executeWorkflow(t, f)
	if r.Phase != Settled || r.Outcome != "succeeded" || f.count("tab.create") != 0 || f.count("agent.start") != 0 {
		t.Fatal(r)
	}
}
func TestCommandExitZeroClosesWithTestEvidence(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "check", Kind: "command", Command: "printf verified"})
	f.s.Transition(f.uid, func(r *Run) error { r.IssueOwned = true; r.IssueRef = "project#task"; return nil })
	r := executeWorkflow(t, f)
	_, p := settlePayload(t, r.Ops, "close")
	if p.Close == nil || len(p.Close.Evidence) != 1 || p.Close.Evidence[0] != "test:printf verified" || !strings.Contains(p.Close.Message, "1 command step exited 0") {
		t.Fatal(p)
	}
}
func TestPromptStepsShareAgent(t *testing.T)      { testWorkflowPrompts(t, "succeeded", false) }
func TestWorkflowNeedsHumanHandsOff(t *testing.T) { testWorkflowPrompts(t, "needs-human", false) }
func TestWorkflowPromptRetries(t *testing.T)      { testWorkflowPrompts(t, "failed", true) }
func testWorkflowPrompts(t *testing.T, status string, retry bool) {
	f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first", Retries: 1}, defs.Step{Key: "command", Kind: "command", Command: "true", After: []string{"first"}}, defs.Step{Key: "last", Kind: "prompt", Prompt: "last", After: []string{"command"}})
	f.s.Transition(f.uid, func(r *Run) error { r.IssueOwned = true; r.IssueRef = "project#task"; return nil })
	f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Working, herdrtest.Working, herdrtest.Working}})
	ctx, cancel := context.WithCancel(t.Context())
	defer func() { cancel(); f.m.Wait() }()
	if e := f.m.Execute(ctx, f.uid); e != nil {
		t.Fatal(e)
	}
	n := 2
	if status == "needs-human" {
		n = 1
	}
	if retry {
		n = 3
	}
	for prompt := 0; prompt < n; prompt++ {
		var r *Run
		var s StepState
		for until := time.Now().Add(30 * time.Second); ; {
			r = loaded(t, f.s, f.uid)
			found := false
			for _, step := range r.Steps {
				if step.Kind == "prompt" && step.Submission == Accepted && step.Status == "" {
					s = step
					found = true
					break
				}
			}
			if found && f.count("agent.prompt") == prompt+1 {
				break
			}
			if time.Now().After(until) {
				t.Fatal("no next prompt", r)
			}
			time.Sleep(time.Millisecond)
		}
		resultStatus := status
		if retry && prompt > 0 {
			resultStatus = "succeeded"
		}
		path := filepath.Join(f.s.layout.AttemptDir(f.uid, s.Key, s.Attempt), "result.json")
		if e := WriteResult(f.s.fsys, path, &Result{Version: 1, Run: f.uid, Step: s.Key, Attempt: s.Attempt, Status: resultStatus, Summary: "Verified the workflow prompt step and its focused assertions.", Evidence: []string{"test:prompt-check"}}); e != nil {
			t.Fatal(e)
		}
		a, e := f.m.h.AgentGet(ctx, r.Herdr.AgentName)
		if e != nil {
			t.Fatal(e)
		}
		a.Status = "idle"
		a.InteractiveReady = true
		seq := uint64(prompt + 1)
		a.CompletionSeq = &seq
		f.h.SetAgent(a)
	}
	f.m.Wait()
	r := loaded(t, f.s, f.uid)
	if f.count("agent.start") != 1 || f.count("tab.create") != 1 || f.count("agent.prompt") != n {
		t.Fatal("agent not shared", r)
	}
	if status == "needs-human" {
		if r.Phase != HandedOff || r.Steps[1].Status != "" || f.count("tab.close") != 0 {
			t.Fatal(r)
		}
	} else {
		if r.Outcome != "succeeded" || r.Phase != Settled {
			t.Fatal(r)
		}
		_, p := settlePayload(t, r.Ops, "observe")
		if !strings.Contains(p.Observation.Summary.Message, "workflow prompt") {
			t.Fatal(p)
		}
		for _, op := range r.Ops {
			var p Payload
			json.Unmarshal(op.Payload, &p)
			if p.Close != nil && !strings.Contains(strings.Join(p.Close.Evidence, ","), "test:true") {
				t.Fatal(p)
			}
		}
	}
}

func TestWorkflowUncertainReceiptsNeverRetry(t *testing.T) {
	for _, status := range []string{"", "failed", "succeeded"} {
		for _, flag := range []string{"uncertain", "alive", "interrupted"} {
			t.Run(status+"/"+flag, func(t *testing.T) {
				f := workflowExecution(t, defs.Step{Key: "check", Kind: "command", Command: "touch duplicate", Retries: 0})
				f.s.Transition(f.uid, func(r *Run) error {
					r.Phase = Running
					r.Steps[0].Submission = Sending
					r.Steps[0].Status = status
					r.Steps[0].Receipt = &Receipt{Version: 1, Run: r.UID, Step: "check", Attempt: 1, StartedAt: time.Now(), EndedAt: time.Now(), ExitCode: 7, Uncertain: flag == "uncertain", GroupAlive: flag == "alive", Interrupted: flag == "interrupted"}
					return nil
				})
				r := executeWorkflow(t, f)
				if r.Phase != Held || r.Outcome != "unknown" || r.Steps[0].Attempt != 1 {
					t.Fatal(r)
				}
				if _, e := os.Stat(filepath.Join(r.Cwd, "duplicate")); !os.IsNotExist(e) {
					t.Fatal("retried uncertain command", e)
				}
			})
		}
	}
}
func TestWorkflowCommandRetryRequiresGoneGroup(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "check", Kind: "command", Command: "touch duplicate", Retries: 3})
	f.s.Transition(f.uid, func(r *Run) error {
		r.Phase = Running
		r.Steps[0].Status = "failed"
		r.Steps[0].Receipt = &Receipt{Version: 1, Run: r.UID, Step: "check", Attempt: 1, StartedAt: time.Now(), EndedAt: time.Now(), ExitCode: 7, PGID: syscall.Getpgrp()}
		return nil
	})
	r := executeWorkflow(t, f)
	if r.Phase != Held || r.Outcome != "unknown" || r.Steps[0].Attempt != 1 {
		t.Fatal(r)
	}
}
func TestWorkflowCommandTimeoutUnknown(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "check", Kind: "command", Command: "exec sleep 10", Retries: 3})
	f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = time.Now().Add(time.Second); return nil })
	r := executeWorkflow(t, f)
	if r.Phase != Held || r.Outcome != "unknown" || r.Steps[0].Receipt == nil || !r.Steps[0].Receipt.Interrupted || r.Steps[0].Attempt != 1 {
		t.Fatal(r)
	}
}

func TestWorkflowBlockedLaunchNamesCurrentStep(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "check", Kind: "command", Command: "true"}, defs.Step{Key: "review", Kind: "prompt", Prompt: "Review"})
	f.s.Transition(f.uid, func(r *Run) error { r.IssueOwned = true; r.IssueRef = "project#task"; return nil })
	f.script(herdrtest.Script{Start: herdrtest.PendingBlocked})
	// An unstarted queue exposes the wake after the blocked operation is saved.
	f.m.q = NewQueue(nil, f.s, nil, RealClock{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); f.m.executePrompt(ctx, f.uid, 1) }()
	defer func() { cancel(); <-done }()
	select {
	case <-f.m.q.wake:
		r := loaded(t, f.s, f.uid)
		if len(r.Ops) == 0 || !strings.HasPrefix(r.Ops[0].ID, "blocked:review:1:true:") {
			t.Fatal("wrong blocked step", r.Ops)
		}
	case <-done:
		t.Fatal("prompt returned before saving its blocked operation")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestCommandInvalidReceiptTimesHoldWithoutRerun(t *testing.T) {
	for _, path := range []string{"step", "workflow"} {
		for _, mode := range []string{"zero", "reversed"} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				f := workflowExecution(t, defs.Step{Key: "check", Kind: "command", Command: "touch duplicate", Retries: 1})
				_, err := f.s.Transition(f.uid, func(r *Run) error {
					r.Phase = Running
					r.Steps[0].Status = "failed"
					receipt := &Receipt{Version: 1, Run: r.UID, Step: "check", Attempt: 1, ExitCode: 7, PGID: 1 << 30}
					if mode == "reversed" {
						receipt.StartedAt = time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC)
						receipt.EndedAt = receipt.StartedAt.Add(-time.Second)
					}
					r.Steps[0].Receipt = receipt
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				var r *Run
				if path == "step" {
					if err := f.m.runCommandStep(t.Context(), f.uid, 0, loaded(t, f.s, f.uid).WFDef.Steps[0]); err != nil {
						t.Fatal(err)
					}
					r = loaded(t, f.s, f.uid)
				} else {
					r = executeWorkflow(t, f)
				}
				if r.Phase != Held || r.Reason != "command_outcome_unknown" || !r.SlotReleased || r.Steps[0].Attempt != 1 {
					t.Fatalf("phase=%s reason=%s released=%t step=%+v", r.Phase, r.Reason, r.SlotReleased, r.Steps[0])
				}
				if _, err := os.Stat(filepath.Join(r.Cwd, "duplicate")); !os.IsNotExist(err) {
					t.Fatalf("command ran again: %v", err)
				}
			})
		}
	}
}

func TestCommandReceiptProofForRetryAndLoop(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "foreign", "wrong-step", "wrong-version", "stale", "zero-attempt", "zero-start", "zero-end", "reversed", "uncertain", "alive", "interrupted"} {
		for _, path := range []string{"retry", "loop"} {
			t.Run(mode+"/"+path, func(t *testing.T) {
				var f *executionFixture
				index := 0
				if path == "loop" {
					f = loopWorkflow(t)
					index = 1
				} else {
					f = workflowExecution(t, defs.Step{Key: "check", Kind: "command", Command: "exit 7", Retries: 1})
				}
				_, err := f.s.Transition(f.uid, func(r *Run) error {
					r.Phase = Running
					s := &r.Steps[index]
					s.Status = "failed"
					now := time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC)
					s.Receipt = &Receipt{Version: 1, Run: r.UID, Step: s.Key, Attempt: 1, ExitCode: 7, PGID: 1 << 30, StartedAt: now, EndedAt: now}
					switch mode {
					case "missing":
						s.Receipt = nil
					case "foreign":
						s.Receipt.Run = "other"
					case "wrong-step":
						s.Receipt.Step = "other"
					case "wrong-version":
						s.Receipt.Version = 2
					case "stale":
						s.Receipt.Attempt = 2
					case "zero-attempt":
						s.Attempt = 0
						s.Receipt.Attempt = 0
					case "zero-start":
						s.Receipt.StartedAt = time.Time{}
					case "zero-end":
						s.Receipt.EndedAt = time.Time{}
					case "reversed":
						s.Receipt.EndedAt = now.Add(-time.Second)
					case "uncertain":
						s.Receipt.Uncertain = true
					case "alive":
						s.Receipt.GroupAlive = true
					case "interrupted":
						s.Receipt.Interrupted = true
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				before := loaded(t, f.s, f.uid)
				if got := certainReceipt(f.uid, before.Steps[index]); got != (mode == "valid") {
					t.Fatalf("certain receipt=%t", got)
				}
				var r *Run
				if path == "loop" {
					if err := f.m.takeLoop(t.Context(), f.uid, index); err != nil {
						t.Fatal(err)
					}
					r = loaded(t, f.s, f.uid)
				} else {
					r = executeWorkflow(t, f)
				}
				if mode == "valid" {
					if r.Steps[index].Attempt != 2 {
						t.Fatalf("did not advance proven attempt: %+v", r.Steps[index])
					}
				} else if r.Phase != Held || r.Steps[index].Attempt != before.Steps[index].Attempt {
					t.Fatalf("advanced unproven attempt: phase=%s step=%+v", r.Phase, r.Steps[index])
				}
			})
		}
	}
}

func TestNextAttemptCarriesOnlyAttemptInputs(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	old := StepState{
		Usage: &usage.Usage{Found: true}, LoopRerun: true,
		FindingContext: &FindingContext{HeadUID: "old-head"}, Note: "old note",
		Herdr: &HerdrRef{PaneID: "old-pane"}, Key: "make", Kind: "prompt", Attempt: 2, Uncharged: 1,
		Limit: &agentlimit.Limit{Kind: "agent_limit"}, Submission: Accepted, SubmittedAt: &now,
		SawActivity: true, Status: "failed", Reason: "old failure", Receipt: &Receipt{ExitCode: 7}, Result: &ResultRef{Status: "failed"},
	}
	want := StepState{Key: "make", Kind: "prompt", Attempt: 3, Uncharged: 4, Submission: None}
	if got := nextAttempt(old, 4, false); !reflect.DeepEqual(got, want) {
		t.Fatalf("next attempt = %+v, want %+v", got, want)
	}
	want.Uncharged, want.LoopRerun = 1, true
	if got := nextAttempt(old, old.Uncharged, old.LoopRerun); !reflect.DeepEqual(got, want) {
		t.Fatalf("retry carry = %+v, want %+v", got, want)
	}
}
