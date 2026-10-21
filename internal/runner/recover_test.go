package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// Recovery tests keep the execution deadline separate from socket/setup latency.
func newRecovery(t *testing.T) *executionFixture {
	t.Helper()
	f := newExecution(t)
	if _, e := f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = time.Now().Add(time.Minute); return nil }); e != nil {
		t.Fatal(e)
	}
	return f
}

func TestRecoverResolvedAttemptAdvances(t *testing.T) {
	f := newRecovery(t)
	f.accepted(t)
	f.result(t, "succeeded")
	f.await(t)
	// The copied result is authoritative even if its artifact disappears.
	r := loaded(t, f.s, f.uid)
	os.Remove(filepath.Join(f.s.layout.AttemptDir(f.uid, "main", 1), "result.json"))
	if r.Steps[0].Result == nil {
		t.Fatal("result not journaled")
	}
	if err := f.m.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.m.Wait()
	r = loaded(t, f.s, f.uid)
	if r.Phase != Settled || r.Outcome != "succeeded" || f.count("agent.prompt") != 1 {
		t.Fatal(r)
	}
}
func TestRecoverHandedOffNotResumed(t *testing.T) {
	f := newRecovery(t)
	f.s.Transition(f.uid, func(r *Run) error { r.Phase = HandedOff; return nil })
	if err := f.m.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.m.Wait()
	if f.count("agent.start") != 0 || f.count("agent.prompt") != 0 {
		t.Fatal("handoff resumed")
	}
}
func TestRecoverSubmissionStates(t *testing.T) {
	for _, sub := range []Submission{None, Rejected, Sending, Accepted} {
		t.Run(string(sub), func(t *testing.T) {
			f := newRecovery(t)
			f.h.OmitCompletionSeq = true
			f.launch(t)
			f.s.Transition(f.uid, func(r *Run) error { r.Steps[0].Submission = sub; return nil })
			f.result(t, "succeeded")
			if err := f.m.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.m.Wait()
			want := 0
			if sub == None || sub == Rejected {
				want = 1
			}
			if f.count("agent.prompt") != want || f.count("agent.start") != 1 {
				t.Fatal("resubmission", sub, f.h.Requests())
			}
		})
	}
}
func TestRecoverCommandReceipts(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "receipt"}[valid], func(t *testing.T) {
			f := newRecovery(t)
			f.s.Transition(f.uid, func(r *Run) error {
				r.ExpiresAt = time.Now().Add(time.Minute)
				r.Phase = Running
				r.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "build", Kind: "command", Command: "touch duplicate"}, {Key: "later", Kind: "command", Command: "printf once >> marker", After: []string{"build"}}}}
				r.Steps = []StepState{{Key: "build", Kind: "command", Attempt: 1, Submission: Sending}, {Key: "later", Kind: "command", Attempt: 1, Submission: None}}
				return nil
			})
			if valid {
				now := time.Now()
				receipt := Receipt{Version: 1, Run: f.uid, Step: "build", Attempt: 1, StartedAt: now, EndedAt: now, ExitCode: 0, PGID: 99999999}
				if e := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.AttemptDir(f.uid, "build", 1), "receipt.json"), receipt); e != nil {
					t.Fatal(e)
				}
			}
			if e := f.m.Recover(t.Context()); e != nil {
				t.Fatal(e)
			}
			f.m.Wait()
			r := loaded(t, f.s, f.uid)
			if valid {
				if r.Steps[0].Status != "succeeded" || r.Steps[0].Receipt == nil || r.Outcome != "succeeded" || r.Steps[1].Status != "succeeded" {
					t.Fatal(r)
				}
			} else if r.Phase != Held || r.Reason != "command_no_receipt" {
				t.Fatal(r)
			}
			if f.count("agent.start") != 0 {
				t.Fatal("command launched agent")
			}
		})
	}
}
func TestRecoverCorruptPreparedTwice(t *testing.T) {
	f := newAdmission(t)
	target, e := f.m.pin()
	if e != nil {
		t.Fatal(e)
	}
	r, e := f.m.prepare(f.d, f.uid, "occurrence", target)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.Create(r); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(f.s.layout.RunFile(f.uid), []byte("broken"), 0600); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if e = f.m.Recover(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	if !state.Quarantined(f.s.fsys, f.s.layout, f.uid) {
		t.Fatal("not quarantined")
	}
	if _, e = f.s.Load(f.uid); !missing(e) {
		t.Fatal("new journal", e)
	}
}

func TestRecoverUnsubmittedMissingSessionHeld(t *testing.T) {
	f := newRecovery(t)
	f.h.OmitSession = true
	f.launch(t)
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	if r := loaded(t, f.s, f.uid); r.Phase != Held || f.count("agent.prompt") != 0 {
		t.Fatal("resubmitted without session proof", r)
	}
}
func TestRecoverHeldWithoutReportQueuesSettlement(t *testing.T) {
	f := newRecovery(t)
	f.m.hold(f.uid, "timeout")
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	if r.EndedAt == nil || len(r.Ops) == 0 {
		t.Fatal("held run missing report", r)
	}
}
func TestRecoveryAndQueueQuarantineCorruptState(t *testing.T) {
	f := newAdmission(t)
	r := &Run{Version: 1, UID: f.uid, Phase: Prepared}
	f.s.Create(r)
	os.WriteFile(f.s.layout.RunFile(f.uid), []byte("broken"), 0600)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- f.q.Run(ctx) }()
	if e := f.m.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	if e := f.m.RestoreGates(); e != nil {
		t.Fatal(e)
	}
	cancel()
	if e := <-done; e != nil && !errors.Is(e, context.Canceled) {
		t.Fatal("quarantine stopped queue", e)
	}
}

func TestDeletedLocalStateNoRelaunch(t *testing.T) {
	f := newAdmission(t)
	h := herdrtest.New(t)
	f.m.h = &herdr.Client{Path: h.Path}
	r := &Run{Version: 1, UID: f.uid, Phase: Prepared}
	f.s.Create(r)
	os.RemoveAll(f.s.layout.RunDir(f.uid))
	f.k.run = func(context.Context, string) (kata.Run, error) {
		return kata.Run{UID: f.uid, Status: "running", ExecutorLabel: f.cfg.Executor}, nil
	}
	var creates int
	f.k.create = func(context.Context, kata.CreateReq) (kata.IssueView, error) { creates++; return kata.IssueView{}, nil }
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	result, e := f.m.Admit(t.Context(), f.d)
	if e != nil || result != sched.Dropped {
		t.Fatal(result, e)
	}
	if creates != 0 || len(h.Requests()) != 0 {
		t.Fatal("lost state relaunched")
	}
	if _, e = f.s.Load(f.uid); !missing(e) {
		t.Fatal("journal recreated", e)
	}
}
func TestRecoverPluginCheckoutRemoved(t *testing.T) {
	if os.Getenv("HK_STAGED_REPORT_TEST") == "1" {
		return
	}
	f := newRecovery(t)
	plugin := t.TempDir()
	source := filepath.Join(plugin, "herdr-kata")
	staged := filepath.Join(f.s.layout.BinDir(), "herdr-kata-retained")
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(exe)
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(source, data, 0700)
	os.MkdirAll(f.s.layout.BinDir(), 0700)
	os.WriteFile(staged, data, 0700)
	f.s.Transition(f.uid, func(r *Run) error { r.Herdr.ReportBin = staged; r.ExpiresAt = time.Now().Add(time.Minute); return nil })
	os.RemoveAll(plugin)
	f.h.OmitCompletionSeq = true
	f.result(t, "succeeded")
	if e = f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r := loaded(t, f.s, f.uid)
	if r.Herdr.ReportBin != staged || r.Phase != Settled {
		t.Fatal(r)
	}
	for _, req := range f.h.Requests() {
		if req.Method == "tab.create" {
			var p struct{ Env map[string]string }
			json.Unmarshal(req.Params, &p)
			if p.Env["HERDR_KATA_REPORT"] != staged {
				t.Fatal(p)
			}
		}
	}
	cmd := exec.Command(staged, "-test.run=^TestRecoverPluginCheckoutRemoved$")
	cmd.Env = append(os.Environ(), "HK_STAGED_REPORT_TEST=1")
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatal(e, string(out))
	}
}

func TestRecoverPreparedReservationsSurviveLowerCapacity(t *testing.T) {
	f := newAdmission(t)
	defer f.m.Wait()
	f.m.cfg.Limits.MaxActiveRuns = 1
	target, e := f.m.pin()
	if e != nil {
		t.Fatal(e)
	}
	var ids []string
	for i := range 2 {
		d := f.d
		d.Occurrence = d.Occurrence.Add(time.Duration(i) * time.Second)
		key := ident.OccurrenceKey(d.Occurrence)
		uid := ident.RunUID(d.Occurrence, ident.ScheduledSource(d.Job.UID, key))
		r, e := f.m.prepare(d, uid, key, target)
		if e != nil {
			t.Fatal(e)
		}
		if e = f.s.Create(r); e != nil {
			t.Fatal(e)
		}
		ids = append(ids, uid)
	}
	f.start(t)
	if e = f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	for _, uid := range ids {
		r := loaded(t, f.s, uid)
		if r.Phase == Prepared {
			t.Fatal("durable reservation was refused after capacity changed", uid)
		}
	}
}
func TestRecoverResolvedWorkflowPreservesFailure(t *testing.T) {
	f := newRecovery(t)
	now := time.Now()
	f.s.Transition(f.uid, func(r *Run) error {
		r.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "build", Kind: "command", Command: "false"}, {Key: "review", Kind: "prompt", Prompt: "Review"}}}
		r.Steps = []StepState{{Key: "build", Kind: "command", Attempt: 1, Status: "failed", Receipt: &Receipt{Version: 1, Run: f.uid, Step: "build", Attempt: 1, ExitCode: 1, StartedAt: now, EndedAt: now, PGID: 99999999}}, {Key: "review", Kind: "prompt", Attempt: 1, Status: "succeeded", Result: &ResultRef{Version: 1, Run: f.uid, Step: "review", Attempt: 1, Status: "succeeded", Summary: "Reviewed independent work and verified all focused checks successfully."}}}
		return nil
	})
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	if r := loaded(t, f.s, f.uid); r.Outcome != "failed" {
		t.Fatal("independent success hid failure", r.Outcome)
	}
}
func TestRecoverGoneResolvedAttemptAdvancesWorkflow(t *testing.T) {
	f := newRecovery(t)
	f.accepted(t)
	f.result(t, "succeeded")
	r := loaded(t, f.s, f.uid)
	f.m.h.TabClose(t.Context(), r.Herdr.TabID)
	f.s.Transition(f.uid, func(r *Run) error {
		r.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "main", Kind: "prompt", Prompt: "First"}, {Key: "later", Kind: "command", Command: "printf once >> marker", After: []string{"main"}}}}
		r.ExpiresAt = time.Now().Add(30 * time.Second)
		r.Steps = append(r.Steps, StepState{Key: "later", Kind: "command", Attempt: 1, Submission: None})
		return nil
	})
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r = loaded(t, f.s, f.uid)
	if r.Steps[0].Result == nil || r.Steps[1].Status != "succeeded" || r.Outcome != "succeeded" {
		t.Fatal("resolved attempt never advanced", r)
	}
}
func TestRecoverResolvedFailedCommandPreservesRetry(t *testing.T) {
	f := newRecovery(t)
	now := time.Now()
	f.s.Transition(f.uid, func(r *Run) error {
		r.ExpiresAt = time.Now().Add(time.Minute)
		r.Phase = Running
		r.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "check", Kind: "command", Command: "false", Retries: 1}}}
		r.Steps = []StepState{{Key: "check", Kind: "command", Attempt: 1, Submission: Sending}}
		return nil
	})
	receipt := Receipt{Version: 1, Run: f.uid, Step: "check", Attempt: 1, ExitCode: 1, StartedAt: now, EndedAt: now, PGID: 99999999}
	if e := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.AttemptDir(f.uid, "check", 1), "receipt.json"), receipt); e != nil {
		t.Fatal(e)
	}
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r := loaded(t, f.s, f.uid)
	if r.Steps[0].Attempt != 2 || r.Outcome != "failed" || r.Steps[0].Receipt == nil {
		t.Fatal("settled before permitted retry", r)
	}
}
func TestRecoverCleanupFailureRetainsTab(t *testing.T) {
	f := newRecovery(t)
	f.accepted(t)
	f.result(t, "succeeded")
	res := f.await(t)
	if e := f.m.settle(t.Context(), f.uid, res); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	f.h.Fail("tab.close", herdr.Error{Code: "temporarily_unavailable"})
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal("cleanup failure stopped recovery", e)
	}
	if _, e := f.m.h.TabGet(t.Context(), r.Herdr.TabID); e != nil {
		t.Fatal("tab not retained", e)
	}
}

func TestRecoverNotifyWithoutSteps(t *testing.T) {
	f := newRecovery(t)
	f.s.Transition(f.uid, func(r *Run) error {
		r.Steps = nil
		r.JobDef.Action = defs.Action{Kind: "notify", Message: "Reminder"}
		r.JobDef.Issue = &defs.IssuePolicy{Kind: "existing", UID: "project#task"}
		r.IssueRef = "project#task"
		return nil
	})
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r := loaded(t, f.s, f.uid)
	if r.Phase != Settled || r.Outcome != "succeeded" || r.Inspect != "" || len(r.Ops) != 3 {
		t.Fatal(r)
	}
}

func TestRecoverResolvedUncertainCommandHeld(t *testing.T) {
	for _, status := range []string{"failed", "succeeded"} {
		t.Run(status, func(t *testing.T) {
			f := workflowExecution(t, defs.Step{Key: "check", Kind: "command", Command: "touch duplicate"})
			now := time.Now()
			f.s.Transition(f.uid, func(r *Run) error {
				r.Steps[0].Status = status
				r.Steps[0].Submission = Accepted
				r.Steps[0].Receipt = &Receipt{Version: 1, Run: f.uid, Step: "check", Attempt: 1, StartedAt: now, EndedAt: now, Uncertain: true}
				return nil
			})
			if e := f.m.Recover(t.Context()); e != nil {
				t.Fatal(e)
			}
			f.m.Wait()
			r := loaded(t, f.s, f.uid)
			if r.Phase != Held || r.Outcome != "unknown" {
				t.Fatal(r)
			}
			if _, e := os.Stat(filepath.Join(r.Cwd, "duplicate")); !os.IsNotExist(e) {
				t.Fatal("command resubmitted", e)
			}
		})
	}
}

func TestRecoverMixedWorkflowSharesPromptAgent(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "main", Kind: "prompt", Prompt: "First"}, defs.Step{Key: "check", Kind: "command", Command: "printf once >> marker", After: []string{"main"}}, defs.Step{Key: "last", Kind: "prompt", Prompt: "Last", After: []string{"check"}})
	f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = time.Now().Add(time.Minute); return nil })
	f.h.OmitCompletionSeq = true
	f.accepted(t)
	f.result(t, "succeeded")
	path := filepath.Join(f.s.layout.AttemptDir(f.uid, "last", 1), "result.json")
	if e := WriteResult(f.s.fsys, path, &Result{Version: 1, Run: f.uid, Step: "last", Attempt: 1, Status: "succeeded", Summary: "Verified the last prompt and all workflow checks passed."}); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if e := f.m.Recover(t.Context()); e != nil {
			t.Fatal(e)
		}
		f.m.Wait()
	}
	r := loaded(t, f.s, f.uid)
	b, e := os.ReadFile(filepath.Join(r.Cwd, "marker"))
	if e != nil || string(b) != "once" || r.Outcome != "succeeded" || f.count("agent.start") != 1 || f.count("agent.prompt") != 2 {
		t.Fatal(r, string(b), e, f.h.Requests())
	}
}

func TestRecoverBlockedPromptKeepsNotificationEpisode(t *testing.T) {
	f := newRecovery(t)
	f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = time.Now().Add(time.Minute); r.IssueRef = "project#task"; return nil })
	f.script(herdrtest.Script{Start: herdrtest.PendingBlocked})
	f.launch(t)
	before := loaded(t, f.s, f.uid)
	ctx, cancel := context.WithCancel(t.Context())
	defer func() { cancel(); f.m.Wait() }()
	if e := f.m.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	for until := time.Now().Add(10 * time.Second); f.count("agent.wait") == 0; {
		if time.Now().After(until) {
			t.Fatal("recovery did not wait on blocked agent")
		}
		time.Sleep(time.Millisecond)
	}
	r := loaded(t, f.s, f.uid)
	if r.Phase != Blocked || len(r.Ops) != len(before.Ops) {
		t.Fatal("restart created another blocked notification", before.Ops, r.Ops)
	}
}

func TestRecoveryRetryDoesNotReconcileActiveCommand(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "main", Kind: "command", Command: "cat hold.fifo"})
	r := loaded(t, f.s, f.uid)
	if e := syscall.Mkfifo(filepath.Join(r.Cwd, "hold.fifo"), 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer func() { cancel(); f.m.Wait() }()
	if e := f.m.Execute(ctx, f.uid); e != nil {
		t.Fatal(e)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		r = loaded(t, f.s, f.uid)
		if r.Steps[0].Submission == Sending || r.Steps[0].Submission == Accepted {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("command did not start")
		case <-tick.C:
		}
	}
	if e := f.m.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	r = loaded(t, f.s, f.uid)
	if r.Phase == Held || r.Steps[0].Reason == "command_no_receipt" {
		t.Fatal("recovery retry interfered with active executor", r.Phase, r.Reason, r.Steps[0].Reason)
	}
}

func TestRecoveryChecksRouteOnceForRetainedJournals(t *testing.T) {
	f := newRecovery(t)
	ended := time.Now()
	guard := &testGuard{}
	f.m.guard = guard
	if _, e := f.s.Transition(f.uid, func(r *Run) error { r.Phase = Settled; r.Outcome = "unknown"; r.EndedAt = &ended; return nil }); e != nil {
		t.Fatal(e)
	}
	for _, uid := range []string{"retained-a", "retained-b", "retained-c"} {
		if e := f.s.Create(&Run{Version: 1, UID: uid, Phase: Settled, Outcome: "unknown", EndedAt: &ended}); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	if guard.calls != 1 {
		t.Fatal("recovery route probes grew with retained history", guard.calls)
	}
}
