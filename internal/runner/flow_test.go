package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
)

func TestEffectiveInput(t *testing.T) {
	for _, tc := range []struct {
		name          string
		override      *string
		options, want string
		refuse        bool
	}{
		{name: "job", options: `{"input":"job"}`, want: "job"},
		{name: "manual", override: new("manual"), options: `{"input":"job"}`, want: "manual"},
		{name: "empty override", override: new(""), options: `{"input":"job"}`, refuse: true},
		{name: "missing", refuse: true},
		{name: "oversized", override: new(strings.Repeat("x", 4097)), refuse: true},
		{name: "nul", override: new("\x00"), refuse: true},
		{name: "invalid utf8", override: new(string([]byte{255})), refuse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdmission(t)
			f.d.ManualInput = tc.override
			f.d.Def.Options = json.RawMessage(tc.options)
			f.d.WFDef.Input = "task description"
			r, e := f.m.prepare(f.d, f.uid, "occurrence", Target{})
			if (e != nil) != tc.refuse {
				t.Fatalf("run=%+v err=%v", r, e)
			}
			if e == nil && r.Input != tc.want {
				t.Fatalf("input %q", r.Input)
			}
		})
	}
	f := newChildAdmission(t)
	f.d.Child.Binding.WFDef.Input = "task description"
	f.start(t)
	r := f.admit(t)
	if r.Input != "Child context" {
		t.Fatalf("child input %q", r.Input)
	}
}

func TestNoteValidation(t *testing.T) {
	for _, note := range []string{strings.Repeat("x", 281), "two\nlines", "\x00", string([]byte{255})} {
		r := &Result{Version: 1, Run: "run", Step: "step", Attempt: 1, Status: "succeeded", Note: note}
		if e := WriteResult(testStore(t).fsys, filepath.Join(t.TempDir(), "result.json"), r); e == nil {
			t.Fatalf("accepted note %q", note)
		}
	}
}
func TestCommandNotesAndEnvironment(t *testing.T) {
	f := workflowExecution(t,
		defs.Step{Key: "first", Kind: "command", Command: `printf '%s' "$HERDR_KATA_INPUT" > "$HERDR_KATA_NOTE_FILE"`},
		defs.Step{Key: "second", Kind: "command", Command: `printf '%s' "$HERDR_KATA_PREVIOUS" > previous`, After: []string{"first"}},
	)
	f.s.Transition(f.uid, func(r *Run) error { r.Input = "literal {{previous}}"; return nil })
	r := executeWorkflow(t, f)
	b, e := os.ReadFile(filepath.Join(r.Cwd, "previous"))
	if e != nil || string(b) != r.Input || r.Steps[0].Note != r.Input || r.Steps[1].Note != "" {
		t.Fatalf("run=%+v previous=%q err=%v", r, b, e)
	}
}
func TestCommandInvalidNoteHolds(t *testing.T) {
	for _, command := range []string{`printf 'two\nlines' > "$HERDR_KATA_NOTE_FILE"`, `head -c 281 /dev/zero | tr '\000' x > "$HERDR_KATA_NOTE_FILE"`, `ln -s /dev/null "$HERDR_KATA_NOTE_FILE"`} {
		f := workflowExecution(t, defs.Step{Key: "first", Kind: "command", Command: command}, defs.Step{Key: "next", Kind: "command", Command: "touch launched", After: []string{"first"}})
		r := executeWorkflow(t, f)
		if r.Phase != Held || r.Steps[1].Submission != None {
			t.Fatalf("invalid note advanced: %+v", r)
		}
	}
}
func TestExpandedPromptBeforeLaunch(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: strings.Repeat("{{input}}", 100)})
	f.s.Transition(f.uid, func(r *Run) error { r.Input = strings.Repeat("x", 4096); return nil })
	f.m.cfg.Limits.MaxActiveRuns = 1
	if e := f.m.RestoreGates(); e != nil {
		t.Fatal(e)
	}
	r := executeWorkflow(t, f)
	if f.m.FreeSlots() != 1 || !f.m.GateHeld(r.Job.UID) {
		t.Fatal("prompt rejection consumed capacity or lost gate", r)
	}
	if r.Phase != Held || f.count("tab.create") != 0 || f.count("agent.start") != 0 || f.count("agent.prompt") != 0 {
		t.Fatal("oversized prompt launched", r)
	}
}
func TestPromptExpansionOnce(t *testing.T) {
	r := &Run{Input: "{{previous}}", WFDef: &defs.WorkflowDef{Steps: []defs.Step{{Key: "a", Kind: "command"}, {Key: "b", Kind: "prompt", Prompt: "{{input}} / {{previous}}"}}}, Steps: []StepState{{Key: "a", Status: "succeeded", Note: "{{input}}"}, {Key: "b", Attempt: 1}}}
	got := BuildPrompt(r, r.Steps[1], "result", "report", true)
	if !strings.Contains(got, "{{previous}} / {{input}}") {
		t.Fatal(got)
	}
}
func TestFreshAgentAttemptBinding(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first"}, defs.Step{Key: "second", Kind: "prompt", Prompt: "second", Options: json.RawMessage(`{"agent_mode":"fresh"}`), After: []string{"first"}})
	f.launch(t)
	f.submit(t)
	f.result(t, "succeeded")
	if f.await(t) == nil {
		t.Fatal("no first result")
	}
	first := loaded(t, f.s, f.uid).Herdr
	if e := f.m.launch(context.Background(), f.uid, 1); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	if r.Herdr.AgentName != first.AgentName || r.Steps[1].Herdr == nil || r.Steps[1].Herdr.AgentName == first.AgentName || len(r.Steps[1].Herdr.AgentName) > 32 || f.count("agent.start") != 2 {
		t.Fatalf("fresh provenance: %+v", r)
	}
	f.m.submitStep(t.Context(), f.uid, 1)
	r = loaded(t, f.s, f.uid)
	s := r.Steps[1]
	path := filepath.Join(f.s.layout.AttemptDir(f.uid, s.Key, s.Attempt), "result.json")
	if e := WriteResult(f.s.fsys, path, &Result{Version: 1, Run: f.uid, Step: s.Key, Attempt: 1, Status: "succeeded", Note: "fresh note"}); e != nil {
		t.Fatal(e)
	}
	res, e := f.m.awaitStepResult(t.Context(), f.uid, 1)
	if e != nil || res == nil || loaded(t, f.s, f.uid).Steps[1].Note != "fresh note" {
		t.Fatal(res, e)
	}
	f.s.Transition(f.uid, func(r *Run) error { r.Phase = Settled; r.Outcome = "succeeded"; return nil })
	if e := f.m.cleanup(t.Context(), f.uid); e != nil || f.count("tab.close") != 2 {
		t.Fatal("cleanup", e, f.count("tab.close"))
	}
}

func TestFreshSwitchRefusesUnprovenPrevious(t *testing.T) {
	for _, change := range []string{"working", "session", "sequence", "no session"} {
		t.Run(change, func(t *testing.T) {
			f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first"}, defs.Step{Key: "next", Kind: "prompt", Prompt: "next", After: []string{"first"}, Options: json.RawMessage(`{"agent_mode":"fresh"}`)})
			reason := "agent_switch_unproven"
			f.accepted(t)
			f.result(t, "succeeded")
			f.await(t)
			r := loaded(t, f.s, f.uid)
			a, _ := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
			switch change {
			case "working":
				a.Status = "working"
			case "session":
				a.Session.Value = "foreign"
			case "sequence":
				a.CompletionSeq = r.Herdr.PreSubmitSeq
			case "no session":
				// A Codex profile with hooks off never reports its session.
				a.Session = nil
				if _, e := f.s.Transition(f.uid, func(r *Run) error { r.Herdr.Session = nil; return nil }); e != nil {
					t.Fatal(e)
				}
				reason = "agent_switch_unproven: agent reported no Herdr session"
			}
			f.h.SetAgent(a)
			if e := f.m.launch(t.Context(), f.uid, 1); e != nil {
				t.Fatal(e)
			}
			r = loaded(t, f.s, f.uid)
			if r.Phase != Held || r.Reason != reason || f.count("agent.start") != 1 {
				t.Fatalf("advanced past unproven: %+v", r)
			}
		})
	}
}
func TestCommandNoteFrozenAcrossRecovery(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "first", Kind: "command", Command: `printf original > "$HERDR_KATA_NOTE_FILE"`}, defs.Step{Key: "next", Kind: "command", Command: `printf '%s' "$HERDR_KATA_PREVIOUS" > recovered`, After: []string{"first"}})
	if e := f.m.runCommandStep(t.Context(), f.uid, 0, f.sMustSteps(t)[0]); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(f.s.layout.AttemptDir(f.uid, "first", 1), "note.txt")
	if e := os.WriteFile(path, []byte("changed"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r := loaded(t, f.s, f.uid)
	b, e := os.ReadFile(filepath.Join(r.Cwd, "recovered"))
	if e != nil || string(b) != "original" {
		t.Fatalf("note changed across restart %q %v", b, e)
	}
}
func (f *executionFixture) sMustSteps(t *testing.T) []defs.Step {
	t.Helper()
	return loaded(t, f.s, f.uid).WFDef.Steps
}

func TestFreshRetryAndReturnToShared(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first"}, defs.Step{Key: "fresh", Kind: "prompt", Prompt: "fresh", After: []string{"first"}, Retries: 1, Options: json.RawMessage(`{"agent_mode":"fresh"}`)}, defs.Step{Key: "last", Kind: "prompt", Prompt: "last", After: []string{"fresh"}})
	f.h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method != "agent.prompt" {
			return
		}
		r, e := f.s.Load(f.uid)
		if e != nil {
			panic(e)
		}
		for _, s := range r.Steps {
			if s.Submission == Sending && s.Status == "" {
				status := "succeeded"
				if s.Key == "fresh" && s.Attempt == 1 {
					status = "failed"
				}
				e = WriteResult(f.s.fsys, filepath.Join(f.s.layout.AttemptDir(f.uid, s.Key, s.Attempt), "result.json"), &Result{Version: 1, Run: f.uid, Step: s.Key, Attempt: s.Attempt, Status: status, Note: s.Key})
				if e != nil {
					panic(e)
				}
				break
			}
		}
	}
	r := executeWorkflow(t, f)
	if r.Outcome != "succeeded" || f.count("agent.start") != 3 || f.count("agent.prompt") != 4 || len(r.PastAttempts) != 1 || r.PastAttempts[0].Herdr == nil || r.PastAttempts[0].Herdr.AgentName == r.Steps[1].Herdr.AgentName || f.count("tab.close") != 3 {
		t.Fatalf("fresh retry/shared return: %+v", r)
	}
}

func TestPromptFileMustBeCompleteRegularAndFrozen(t *testing.T) {
	for _, mode := range []string{"partial", "symlink", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			f := newExecution(t)
			r := loaded(t, f.s, f.uid)
			prompt, _, e := f.m.attemptPaths(r, 0)
			if e != nil {
				t.Fatal(e)
			}
			if e = os.MkdirAll(filepath.Dir(prompt), 0700); e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "partial":
				e = os.WriteFile(prompt, []byte("partial"), 0600)
			case "oversized":
				e = os.WriteFile(prompt, []byte(strings.Repeat("x", defs.MaxPromptBytes+1)), 0600)
			case "symlink":
				e = os.Symlink("/dev/null", prompt)
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = f.m.writePrompt(r, 0); e == nil {
				t.Fatal("unsafe frozen prompt accepted")
			}
		})
	}
}

func TestRecoverExitedSharedAgentContinues(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first"}, defs.Step{Key: "next", Kind: "prompt", Prompt: "next", After: []string{"first"}})
	f.accepted(t)
	f.result(t, "succeeded")
	old := loaded(t, f.s, f.uid).Herdr
	if e := f.m.h.TabClose(t.Context(), old.TabID); e != nil {
		t.Fatal(e)
	}
	reportNextPrompt(f)
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r := loaded(t, f.s, f.uid)
	if r.Outcome != "succeeded" || f.count("agent.start") != 2 || len(r.PastAttempts) != 1 || r.PastAttempts[0].Herdr.TabID != old.TabID {
		t.Fatalf("shared exit recovery: %+v", r)
	}
}
func TestFreshExitedFailureRetries(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first", Retries: 1, Options: json.RawMessage(`{"agent_mode":"fresh"}`)})
	f.launch(t)
	f.submit(t)
	f.result(t, "failed")
	old := *AgentReference(loaded(t, f.s, f.uid))
	if e := f.m.h.TabClose(t.Context(), old.TabID); e != nil {
		t.Fatal(e)
	}
	if res, e := f.m.awaitStepResult(t.Context(), f.uid, 0); e != nil || res == nil {
		t.Fatal(res, e)
	}
	reportNextPrompt(f)
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r := loaded(t, f.s, f.uid)
	if r.Outcome != "succeeded" || r.Steps[0].Attempt != 2 || f.count("agent.start") != 2 || len(r.PastAttempts) != 1 || r.PastAttempts[0].Herdr.AgentName != old.AgentName {
		t.Fatalf("fresh exit retry: %+v", r)
	}
}
func reportNextPrompt(f *executionFixture) {
	f.h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method != "agent.prompt" {
			return
		}
		r, e := f.s.Load(f.uid)
		if e != nil {
			panic(e)
		}
		for _, s := range r.Steps {
			if s.Submission == Sending && s.Status == "" {
				e = WriteResult(f.s.fsys, filepath.Join(f.s.layout.AttemptDir(f.uid, s.Key, s.Attempt), "result.json"), &Result{Version: 1, Run: f.uid, Step: s.Key, Attempt: s.Attempt, Status: "succeeded"})
				if e != nil {
					panic(e)
				}
				break
			}
		}
	}
}

func TestResolvedExitedSharedAgentContinues(t *testing.T) {
	for _, recover := range []bool{false, true} {
		t.Run(map[bool]string{false: "execution", true: "recovery"}[recover], func(t *testing.T) {
			f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first"}, defs.Step{Key: "next", Kind: "prompt", Prompt: "next", After: []string{"first"}})
			f.accepted(t)
			f.result(t, "succeeded")
			f.await(t)
			old := loaded(t, f.s, f.uid).Herdr
			if e := f.m.h.TabClose(t.Context(), old.TabID); e != nil {
				t.Fatal(e)
			}
			reportNextPrompt(f)
			var r *Run
			if recover {
				if e := f.m.Recover(t.Context()); e != nil {
					t.Fatal(e)
				}
				f.m.Wait()
				r = loaded(t, f.s, f.uid)
			} else {
				r = executeWorkflow(t, f)
			}
			if r.Outcome != "succeeded" || f.count("agent.start") != 2 || len(r.PastAttempts) != 1 || r.PastAttempts[0].Key != "first" || r.PastAttempts[0].Herdr.TabID != old.TabID {
				t.Fatalf("resolved shared exit: %+v", r)
			}
		})
	}
}

func TestHeldTimeoutBetweenFreshAttemptsReleasesSlot(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first", Retries: 1, Options: json.RawMessage(`{"agent_mode":"fresh"}`)})
	f.m.cfg.Limits.MaxActiveRuns = 1
	if e := f.m.RestoreGates(); e != nil {
		t.Fatal(e)
	}
	f.accepted(t)
	f.result(t, "failed")
	f.await(t)
	_, e := f.s.Transition(f.uid, func(r *Run) error {
		old := r.Steps[0]
		r.PastAttempts = append(r.PastAttempts, old)
		r.Steps[0] = StepState{Key: old.Key, Kind: old.Kind, Attempt: 2, Submission: None}
		r.Phase = Held
		r.Outcome = "unknown"
		r.Reason = "timeout"
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.m.ReconcileHeld(t.Context()); e != nil {
		t.Fatal(e)
	}
	if f.m.FreeSlots() != 1 || !f.m.GateHeld(loaded(t, f.s, f.uid).Job.UID) {
		t.Fatal("timeout retained stopped capacity")
	}
}
func TestFreshTransitionKeepsReportBinaryAcrossUpgrade(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first", Options: json.RawMessage(`{"agent_mode":"fresh"}`)}, defs.Step{Key: "next", Kind: "prompt", Prompt: "next", After: []string{"first"}, Options: json.RawMessage(`{"agent_mode":"fresh"}`)})
	f.accepted(t)
	f.result(t, "succeeded")
	f.await(t)
	oldReport := "/state/bin/retained-report"
	_, e := f.s.Transition(f.uid, func(r *Run) error { r.Herdr.ReportBin = ""; r.Steps[0].Herdr.ReportBin = oldReport; return nil })
	if e != nil {
		t.Fatal(e)
	}
	reportNextPrompt(f)
	r := executeWorkflow(t, f)
	prompt, e := os.ReadFile(filepath.Join(f.s.layout.AttemptDir(f.uid, "next", 1), "prompt.md"))
	if e != nil || r.Outcome != "succeeded" || r.Herdr.ReportBin != oldReport || r.Steps[1].Herdr.ReportBin != oldReport || !strings.Contains(string(prompt), oldReport) {
		t.Fatalf("upgrade changed report binary: outcome=%s ref=%+v err=%v", r.Outcome, r.Steps[1].Herdr, e)
	}
}
func TestSharedExitedFailureRetries(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first", Retries: 1})
	f.accepted(t)
	f.result(t, "failed")
	f.await(t)
	old := loaded(t, f.s, f.uid).Herdr
	if e := f.m.h.TabClose(t.Context(), old.TabID); e != nil {
		t.Fatal(e)
	}
	reportNextPrompt(f)
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r := loaded(t, f.s, f.uid)
	if r.Outcome != "succeeded" || r.Steps[0].Attempt != 2 || f.count("agent.start") != 2 || len(r.PastAttempts) != 1 || r.PastAttempts[0].Herdr.TabID != old.TabID {
		t.Fatalf("shared retry: outcome=%s reason=%s attempt=%d history=%+v", r.Outcome, r.Reason, r.Steps[0].Attempt, r.PastAttempts)
	}
}

func TestReturnToExitedSharedAgent(t *testing.T) {
	for _, recover := range []bool{false, true} {
		t.Run(map[bool]string{false: "execution", true: "recovery"}[recover], func(t *testing.T) {
			f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first"}, defs.Step{Key: "fresh", Kind: "prompt", Prompt: "fresh", After: []string{"first"}, Options: json.RawMessage(`{"agent_mode":"fresh"}`)}, defs.Step{Key: "last", Kind: "prompt", Prompt: "last", After: []string{"fresh"}})
			f.accepted(t)
			f.result(t, "succeeded")
			f.await(t)
			old := loaded(t, f.s, f.uid).Herdr
			if e := f.m.launch(t.Context(), f.uid, 1); e != nil {
				t.Fatal(e)
			}
			if e := f.m.submitStep(t.Context(), f.uid, 1); e != nil {
				t.Fatal(e)
			}
			if e := f.m.h.TabClose(t.Context(), old.TabID); e != nil {
				t.Fatal(e)
			}
			if e := WriteResult(f.s.fsys, filepath.Join(f.s.layout.AttemptDir(f.uid, "fresh", 1), "result.json"), &Result{Version: 1, Run: f.uid, Step: "fresh", Attempt: 1, Status: "succeeded"}); e != nil {
				t.Fatal(e)
			}
			if res, e := f.m.awaitStepResult(t.Context(), f.uid, 1); e != nil || res == nil {
				t.Fatal(res, e)
			}
			reportNextPrompt(f)
			var r *Run
			if recover {
				if e := f.m.Recover(t.Context()); e != nil {
					t.Fatal(e)
				}
				f.m.Wait()
				r = loaded(t, f.s, f.uid)
			} else {
				r = executeWorkflow(t, f)
			}
			if r.Outcome != "succeeded" || f.count("agent.start") != 3 || r.Herdr.TabID == old.TabID || len(r.PastAttempts) != 1 || r.PastAttempts[0].Key != "first" || r.PastAttempts[0].Herdr.TabID != old.TabID {
				t.Fatalf("return to shared: outcome=%s reason=%s history=%+v", r.Outcome, r.Reason, r.PastAttempts)
			}
		})
	}
}
func TestLargeFailuresRetainBoundedProvenance(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first", Retries: 100, Options: json.RawMessage(`{"agent_mode":"fresh"}`)})
	for n := 1; n <= 100; n++ {
		res := &Result{Version: 1, Run: f.uid, Step: "first", Attempt: n, Status: "failed", Summary: strings.Repeat("x", 60<<10)}
		if e := WriteResult(f.s.fsys, filepath.Join(f.s.layout.AttemptDir(f.uid, "first", n), "result.json"), res); e != nil {
			t.Fatal(e)
		}
		_, e := f.s.Transition(f.uid, func(r *Run) error {
			r.ActiveStep = "first"
			r.Steps[0].Herdr = &HerdrRef{AgentName: fmt.Sprintf("agent-%d", n), PaneID: fmt.Sprintf("pane-%d", n)}
			r.Steps[0].Result = refFromResult(res)
			r.Steps[0].Status = "failed"
			retainAgent(r, 0)
			r.Steps[0].Herdr = nil
			r.Steps[0].Result = nil
			r.Steps[0].Status = ""
			r.Steps[0].Attempt++
			return nil
		})
		if e != nil {
			t.Fatalf("supported retry %d exceeded journal bound: %v", n, e)
		}
	}
	r := loaded(t, f.s, f.uid)
	if len(r.PastAttempts) != 100 {
		t.Fatal(len(r.PastAttempts))
	}
	for _, s := range r.PastAttempts {
		if s.Result != nil {
			t.Fatal("full report retained in journal")
		}
	}
	for _, n := range []int{1, 100} {
		res, e := ReadResult(f.s.fsys, filepath.Join(f.s.layout.AttemptDir(f.uid, "first", n), "result.json"), f.uid, "first", n)
		if e != nil || len(res.Summary) != 60<<10 {
			t.Fatal("report artifact lost", n, e)
		}
	}
}

func TestNewOccupantAcceptsDelayedSession(t *testing.T) {
	for _, mode := range []string{"fresh", "fresh retry", "recreated shared"} {
		t.Run(mode, func(t *testing.T) {
			options := json.RawMessage(`{"agent_mode":"fresh"}`)
			if mode == "recreated shared" {
				options = nil
			}
			f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first"}, defs.Step{Key: "next", Kind: "prompt", Prompt: "next", After: []string{"first"}, Options: options, Retries: 1})
			f.accepted(t)
			f.result(t, "succeeded")
			f.await(t)
			if mode == "recreated shared" {
				if e := f.m.h.TabClose(t.Context(), loaded(t, f.s, f.uid).Herdr.TabID); e != nil {
					t.Fatal(e)
				}
			}
			if e := f.m.launch(t.Context(), f.uid, 1); e != nil {
				t.Fatal(e)
			}
			if mode == "fresh retry" {
				if e := f.m.submitStep(t.Context(), f.uid, 1); e != nil {
					t.Fatal(e)
				}
				if e := WriteResult(f.s.fsys, filepath.Join(f.s.layout.AttemptDir(f.uid, "next", 1), "result.json"), &Result{Version: 1, Run: f.uid, Step: "next", Attempt: 1, Status: "failed"}); e != nil {
					t.Fatal(e)
				}
				if res, e := f.m.awaitStepResult(t.Context(), f.uid, 1); e != nil || res == nil {
					t.Fatal(res, e)
				}
				if _, e := f.s.Transition(f.uid, func(r *Run) error {
					retainAgent(r, 1)
					r.Steps[1] = StepState{Key: "next", Kind: "prompt", Attempt: 2, Submission: None}
					return nil
				}); e != nil {
					t.Fatal(e)
				}
				if e := f.m.launch(t.Context(), f.uid, 1); e != nil {
					t.Fatal(e)
				}
			}
			r := loaded(t, f.s, f.uid)
			ref := AgentReference(r)
			a, e := f.m.h.AgentGet(t.Context(), ref.AgentName)
			if e != nil {
				t.Fatal(e)
			}
			a.Session = nil
			f.h.SetAgent(a)
			if _, e = f.s.Transition(f.uid, func(r *Run) error { AgentReference(r).Session = nil; return nil }); e != nil {
				t.Fatal(e)
			}
			session := &herdr.AgentSession{Source: "hook", Agent: "codex", Kind: "id", Value: "first-turn-session"}
			f.h.Script(ref.AgentName, herdrtest.Script{SessionAfterPrompt: session})
			if e = f.m.submitStep(t.Context(), f.uid, 1); e != nil {
				t.Fatal(e)
			}
			r = loaded(t, f.s, f.uid)
			if r.Steps[1].Submission != Accepted || AgentReference(r).Session == nil || *AgentReference(r).Session != *session {
				t.Fatalf("new occupant refused: phase=%s reason=%s submission=%s", r.Phase, r.Reason, r.Steps[1].Submission)
			}
		})
	}
}

func TestSharedAgentExitAcrossCommandStep(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first"}, defs.Step{Key: "middle", Kind: "command", Command: "true", After: []string{"first"}}, defs.Step{Key: "next", Kind: "prompt", Prompt: "next", After: []string{"middle"}})
	f.accepted(t)
	f.result(t, "succeeded")
	if f.await(t) == nil {
		t.Fatal("missing first result")
	}
	old := loaded(t, f.s, f.uid).Herdr
	if e := f.m.runCommandStep(t.Context(), f.uid, 1, loaded(t, f.s, f.uid).WFDef.Steps[1]); e != nil {
		t.Fatal(e)
	}
	if e := f.m.h.TabClose(t.Context(), old.TabID); e != nil {
		t.Fatal(e)
	}
	reportNextPrompt(f)
	r := executeWorkflow(t, f)
	if r.Outcome != "succeeded" || f.count("agent.start") != 2 || len(r.PastAttempts) != 1 || r.PastAttempts[0].Key != "first" || r.PastAttempts[0].Herdr.TabID != old.TabID {
		t.Fatal("shared exit did not recover across command", r)
	}
}

func TestSharedAgentSelectionSkipsUnexecutedPrompts(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first"}, defs.Step{Key: "failed", Kind: "command", Command: "false"}, defs.Step{Key: "skip", Kind: "prompt", Prompt: "skip", After: []string{"failed"}}, defs.Step{Key: "independent", Kind: "command", Command: "true"}, defs.Step{Key: "last", Kind: "prompt", Prompt: "last"})
	f.accepted(t)
	f.result(t, "succeeded")
	if f.await(t) == nil {
		t.Fatal("missing first report")
	}
	reportNextPrompt(f)
	r := executeWorkflow(t, f)
	if r.Phase != Settled || r.Outcome != "failed" || r.Steps[2].Status != "skipped" || r.Steps[4].Status != "succeeded" || f.count("agent.start") != 1 || f.count("agent.prompt") != 2 {
		t.Fatal("skipped step selected as occupant", r)
	}
}

func TestInvalidPromptNeverStartsAgent(t *testing.T) {
	for _, mode := range []string{"partial", "attempt-file"} {
		t.Run(mode, func(t *testing.T) {
			f := newExecution(t)
			r := loaded(t, f.s, f.uid)
			prompt, _, err := f.m.attemptPaths(r, 0)
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Dir(prompt)
			if mode == "partial" {
				if err = os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				err = os.WriteFile(prompt, []byte("partial"), 0600)
			} else {
				if err = os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
					t.Fatal(err)
				}
				err = os.WriteFile(dir, []byte("not a directory"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.m.executePrompt(t.Context(), f.uid, 0); err != nil {
				t.Fatal(err)
			}
			r = loaded(t, f.s, f.uid)
			if r.Phase != Held || r.Reason != "invalid_prompt" || !r.SlotReleased || f.count("agent.start") != 0 || f.count("agent.prompt") != 0 {
				t.Fatalf("invalid prompt started an agent or retained capacity: phase=%s reason=%s released=%t starts=%d", r.Phase, r.Reason, r.SlotReleased, f.count("agent.start"))
			}
		})
	}
}
