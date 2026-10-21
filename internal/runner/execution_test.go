package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

type executionFixture struct {
	m   *Manager
	s   *Store
	h   *herdrtest.Server
	uid string
}

func newExecution(t *testing.T) *executionFixture {
	t.Helper()
	h := herdrtest.New(t)
	s := testStore(t)
	uid := "01K00000000000000000000000"
	r := &Run{Version: 1, UID: uid, Phase: Admitted, Cwd: s.layout.RunDir(uid), ExpiresAt: executionDeadline(t), Profile: config.Profile{Kind: "codex"}, JobDef: defs.JobDef{Action: defs.Action{Kind: "execute", Prompt: "Inspect the task"}}, Steps: []StepState{{Key: "main", Kind: "prompt", Attempt: 1, Submission: None}}}
	if e := s.Create(r); e != nil {
		t.Fatal(e)
	}
	m := NewManager(config.Config{}, nil, &herdr.Client{Path: h.Path}, s, nil, nil, RealClock{})
	m.environment = cleanLaunch{}
	return &executionFixture{m, s, h, uid}
}

// Ordinary execution tests wait for runner completion, not a short run timeout.
// Deadline tests override this value explicitly. With -timeout 0 there is no
// outer watchdog, so leave the product deadline out of the experiment too.
func executionDeadline(t *testing.T) time.Time {
	t.Helper()
	if deadline, ok := t.Deadline(); ok {
		return deadline
	}
	return time.Date(9999, time.December, 31, 0, 0, 0, 0, time.UTC)
}

func (f *executionFixture) launch(t *testing.T) {
	t.Helper()
	if e := f.m.launch(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
}
func (f *executionFixture) script(s herdrtest.Script) { f.h.Script("hk-"+strings.ToLower(f.uid), s) }
func (f *executionFixture) result(t *testing.T, status string) {
	t.Helper()
	r := loaded(t, f.s, f.uid)
	step := r.Steps[0]
	p := filepath.Join(f.s.layout.AttemptDir(f.uid, step.Key, step.Attempt), "result.json")
	if e := WriteResult(f.s.fsys, p, &Result{Version: 1, Run: f.uid, Step: step.Key, Attempt: step.Attempt, Status: status, Summary: "Inspected the implementation and verified the focused tests pass.", Evidence: []string{"test:go test ./internal/run"}}); e != nil {
		t.Fatal(e)
	}
}
func (f *executionFixture) submit(t *testing.T) {
	t.Helper()
	if e := f.m.submit(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
}
func (f *executionFixture) accepted(t *testing.T) { t.Helper(); f.launch(t); f.submit(t) }
func (f *executionFixture) count(method string) int {
	n := 0
	for _, r := range f.h.Requests() {
		if r.Method == method {
			n++
		}
	}
	return n
}
func (f *executionFixture) await(t *testing.T) *Result {
	t.Helper()
	res, e := f.m.awaitResult(t.Context(), f.uid)
	if e != nil {
		t.Fatal(e)
	}
	return res
}
func TestLaunchPendingThenReady(t *testing.T) {
	f := newExecution(t)
	f.script(herdrtest.Script{Start: herdrtest.PendingReady})
	f.launch(t)
	r := loaded(t, f.s, f.uid)
	if r.Herdr.Session == nil || !r.Herdr.LaunchIntent || r.Phase != Running {
		t.Fatalf("launch: %+v", r)
	}
	for _, req := range f.h.Requests() {
		if req.Method == "agent.start" {
			r := loaded(t, f.s, f.uid)
			if r.Herdr.AgentName == "" || r.Herdr.TerminalID == "" {
				t.Fatal("no launch intent")
			}
		}
	}
}
func TestLaunchPendingThenBlocked(t *testing.T) {
	f := newExecution(t)
	if _, e := f.s.Transition(f.uid, func(r *Run) error {
		r.IssueRef, r.IssueOwned = "example-project#task", true
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	f.script(herdrtest.Script{Start: herdrtest.PendingReady, Get: []herdr.Agent{
		{Status: "idle", InteractiveReady: true, LaunchPending: true},
		{Status: "blocked", LaunchPending: true},
	}})
	f.m.clock = &activityClock{at: time.Now()}
	f.launch(t)
	r := loaded(t, f.s, f.uid)
	if r.Phase != Blocked || r.Steps[0].Submission != None {
		t.Fatal(r)
	}
	f.submit(t)
	assertStartupAttention(t, loaded(t, f.s, f.uid))
	if f.count("agent.prompt") != 0 {
		t.Fatal("submitted during launch block")
	}
}
func TestLaunchReplyLostAdoptsOriginal(t *testing.T) {
	f := newExecution(t)
	f.script(herdrtest.Script{Start: herdrtest.DropStart})
	f.launch(t)
	f.launch(t)
	r := loaded(t, f.s, f.uid)
	if r.Herdr.Session == nil || f.count("agent.start") != 1 {
		t.Fatal(r, f.count("agent.start"))
	}
}
func TestLaunchForeignOccupantHeld(t *testing.T) {
	f := newExecution(t)
	f.launch(t)
	r := loaded(t, f.s, f.uid)
	a, e := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
	if e != nil {
		t.Fatal(e)
	}
	a.TerminalID = "foreign"
	f.h.SetAgent(a)
	f.launch(t)
	r = loaded(t, f.s, f.uid)
	if r.Phase != Held || f.count("agent.start") != 1 {
		t.Fatal(r)
	}
}
func TestSubmitOnlyWhenReady(t *testing.T) {
	f := newExecution(t)
	f.launch(t)
	r := loaded(t, f.s, f.uid)
	a, _ := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
	a.Status = "working"
	a.InteractiveReady = false
	f.h.SetAgent(a)
	f.submit(t)
	if f.count("agent.prompt") != 0 || loaded(t, f.s, f.uid).Steps[0].Submission != None {
		t.Fatal("sent to working agent")
	}
}
func TestAgentBlockedRejectedThenSubmitsAfterUnblock(t *testing.T) {
	f := newExecution(t)
	f.launch(t)
	f.h.Fail("agent.prompt", herdr.Error{Code: "agent_blocked"})
	f.submit(t)
	r := loaded(t, f.s, f.uid)
	if r.Phase != Blocked || r.Steps[0].Submission != Rejected {
		t.Fatal(r)
	}
	f.submit(t)
	r = loaded(t, f.s, f.uid)
	if r.Steps[0].Submission != Accepted || f.count("agent.prompt") != 2 {
		t.Fatal(r)
	}
}
func TestIdleWithResult(t *testing.T) {
	f := newExecution(t)
	f.accepted(t)
	f.result(t, "succeeded")
	if r := f.await(t); r == nil || r.Status != "succeeded" {
		t.Fatal(r)
	}
}
func TestSettlesWithoutCompletionSeq(t *testing.T) {
	f := newExecution(t)
	f.h.OmitCompletionSeq = true
	f.accepted(t)
	f.result(t, "succeeded")
	if r := f.await(t); r == nil || r.Status != "succeeded" {
		t.Fatal(r)
	}
}
func TestIdleNoResultHeld(t *testing.T) {
	f := newExecution(t)
	f.accepted(t)
	f.m.clock = &activityClock{at: time.Now()}
	f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = f.m.clock.Now().Add(time.Minute); return nil })
	if f.await(t) != nil {
		t.Fatal("invented result")
	}
	r := loaded(t, f.s, f.uid)
	if r.Phase != Held || r.Reason != "no_result" {
		t.Fatal(r)
	}
}
func TestResultWhileWorkingWaits(t *testing.T) {
	f := newExecution(t)
	f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Working}})
	f.accepted(t)
	f.result(t, "succeeded")
	done := make(chan *Result, 1)
	go func() { r, _ := f.m.awaitResult(t.Context(), f.uid); done <- r }()
	select {
	case <-done:
		t.Fatal("working result used")
	case <-time.After(30 * time.Millisecond):
	}
	r := loaded(t, f.s, f.uid)
	a, _ := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
	a.Status = "idle"
	seq := uint64(1)
	a.CompletionSeq = &seq
	f.h.SetAgent(a)
	select {
	case res := <-done:
		if res == nil {
			t.Fatal("result lost")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("did not wake")
	}
}
func TestStalledButWorkingNoResend(t *testing.T)        { testReconcile(t, herdrtest.Stalled) }
func TestSocketDropReconcilesSameOccupant(t *testing.T) { testReconcile(t, herdrtest.Drop) }
func testReconcile(t *testing.T, o herdrtest.Outcome) {
	f := newExecution(t)
	f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{o}, Wait: []herdrtest.Outcome{herdrtest.Idle}})
	f.accepted(t)
	f.result(t, "succeeded")
	if f.await(t) == nil {
		t.Fatal("no result")
	}
	f.submit(t)
	if f.count("agent.prompt") != 1 {
		t.Fatal("resent")
	}
}

// A missing name alone is insufficient, but the saved pane back at its shell
// proves the occupant stopped. Do not spend the remaining two-hour lifetime.
func TestAgentGoneAtShellHoldsPromptly(t *testing.T) {
	f := newExecution(t)
	f.accepted(t)
	f.h.RemoveAgent("hk-" + strings.ToLower(f.uid))
	start := time.Now()
	f.m.clock = &activityClock{at: start}
	if _, e := f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = start.Add(2 * time.Hour); return nil }); e != nil {
		t.Fatal(e)
	}
	if f.await(t) != nil {
		t.Fatal("invented result")
	}
	r := loaded(t, f.s, f.uid)
	if r.Reason != "agent_lost" || r.Phase != Held || !r.SlotReleased || f.m.clock.Now().Sub(start) > 10*time.Second {
		t.Fatalf("phase=%s reason=%s released=%v elapsed=%s", r.Phase, r.Reason, r.SlotReleased, f.m.clock.Now().Sub(start))
	}
	f.submit(t)
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	if f.count("agent.prompt") != 1 || f.count("agent.start") != 1 {
		t.Fatal("replaced a lost execution")
	}
}

func TestMissingAgentRequiresStoppedProof(t *testing.T) {
	for _, change := range []string{"foreign occupant", "changed terminal", "process read failure"} {
		t.Run(change, func(t *testing.T) {
			f := newExecution(t)
			f.accepted(t)
			r := loaded(t, f.s, f.uid)
			a, e := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
			if e != nil {
				t.Fatal(e)
			}
			f.h.RemoveAgent(a.Name)
			switch change {
			case "foreign occupant":
				a.Name = "foreign"
				f.h.SetAgent(a)
			case "changed terminal":
				pane, e := f.m.h.PaneGet(t.Context(), r.Herdr.PaneID)
				if e != nil {
					t.Fatal(e)
				}
				pane.TerminalID = "replacement"
				f.h.SetPane(pane)
			case "process read failure":
				f.h.BeforeRequest = func(req herdrtest.Request) {
					if req.Method == "pane.process_info" {
						f.h.Fail(req.Method, herdr.Error{Code: "internal"})
					}
				}
			}
			start := time.Now()
			f.m.clock = &activityClock{at: start}
			if _, e := f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = start.Add(time.Minute); return nil }); e != nil {
				t.Fatal(e)
			}
			f.await(t)
			r = loaded(t, f.s, f.uid)
			if r.Phase != Held || r.Reason != "agent_lost" || r.SlotReleased || f.m.clock.Now().Sub(start) < time.Minute {
				t.Fatalf("unproven stop: phase=%s reason=%s released=%v elapsed=%s", r.Phase, r.Reason, r.SlotReleased, f.m.clock.Now().Sub(start))
			}
			if f.count("agent.prompt") != 1 || f.count("agent.start") != 1 {
				t.Fatal("replaced an unproven execution")
			}
		})
	}
}
func TestTimeoutHeldNotKilled(t *testing.T) {
	f := newExecution(t)
	f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Timeout}})
	f.accepted(t)
	// Expire the run only after its occupant is working, without a wall wait.
	f.m.clock = &activityClock{at: time.Now()}
	f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = f.m.clock.Now(); return nil })
	f.await(t)
	r := loaded(t, f.s, f.uid)
	if r.Reason != "timeout" || r.Phase != Held || r.Steps[0].Submission != Accepted || f.count("agent.prompt") != 1 || f.count("tab.close") != 0 {
		t.Fatal(r)
	}
}

func TestNeedsHumanResultHandsOff(t *testing.T) {
	f := newExecution(t)
	f.accepted(t)
	f.result(t, "needs-human")
	res := f.await(t)
	if e := f.m.settle(t.Context(), f.uid, res); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	if r.Phase != HandedOff || r.Outcome != "unknown" {
		t.Fatal(r)
	}
}
func TestWorkspaceFreshAfterServerRestart(t *testing.T) {
	f := newExecution(t)
	w, e := f.m.ensureWorkspace(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	if e = f.h.Restart(); e != nil {
		t.Fatal(e)
	}
	v, e := f.m.ensureWorkspace(t.Context())
	if e != nil || v.WorkspaceID == w.WorkspaceID || v.ServerID == w.ServerID {
		t.Fatal(w, v, e)
	}
}
func TestCleanupRefusesExtraPane(t *testing.T)      { testCleanup(t, "pane", false) }
func TestCleanupRefusesMissingSession(t *testing.T) { testCleanup(t, "session", false) }
func TestCleanupClosesWithProof(t *testing.T)       { testCleanup(t, "", true) }
func testCleanup(t *testing.T, change string, closed bool) {
	f := newExecution(t)
	f.accepted(t)
	r := loaded(t, f.s, f.uid)
	if change == "pane" {
		tab, _ := f.m.h.TabGet(t.Context(), r.Herdr.TabID)
		tab.PaneCount = 2
		f.h.SetTab(tab)
	}
	if change == "session" {
		f.s.Transition(f.uid, func(r *Run) error { r.Herdr.Session = nil; return nil })
	}
	f.s.Transition(f.uid, func(r *Run) error { r.Phase = Settled; r.Outcome = "succeeded"; return nil })
	if e := f.m.cleanup(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	if (f.count("tab.close") == 1) != closed {
		t.Fatal("cleanup proof ignored")
	}
}
func TestExecuteReturnsWhilePromptWaits(t *testing.T) {
	f := newExecution(t)
	f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Working}})
	ctx, cancel := context.WithCancel(t.Context())
	defer func() { cancel(); f.m.Wait() }()
	waiting := make(chan struct{}, 1)
	f.h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method == "agent.wait" {
			waiting <- struct{}{}
			<-ctx.Done()
		}
	}
	if e := f.m.Execute(ctx, f.uid); e != nil {
		t.Fatal(e)
	}
	// Execute returned while the runner's wait RPC remains blocked.
	select {
	case <-waiting:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if f.count("agent.prompt") != 1 {
		t.Fatal("did not execute")
	}
	f.m.Execute(ctx, f.uid)
	if f.count("agent.prompt") != 1 {
		t.Fatal("duplicate execution")
	}
	for _, req := range f.h.Requests() {
		if req.Method == "tab.create" {
			var p struct{ Env map[string]string }
			json.Unmarshal(req.Params, &p)
			if !filepath.IsAbs(p.Env["HERDR_KATA_REPORT"]) || p.Env["HERDR_KATA_RUN"] != f.uid {
				t.Fatal(p)
			}
		}
	}
	r := loaded(t, f.s, f.uid)
	b, e := os.ReadFile(filepath.Join(f.s.layout.AttemptDir(f.uid, "main", 1), "prompt.md"))
	if e != nil || !strings.Contains(string(b), r.Herdr.ReportBin) {
		t.Fatal("prompt", e)
	}
}

func TestRetryOnlyOnReportedFailed(t *testing.T) {
	for _, status := range []string{"succeeded", "failed", "needs-human"} {
		t.Run(status, func(t *testing.T) {
			f := newExecution(t)
			f.accepted(t)
			f.result(t, status)
			res := f.await(t)
			f.m.settle(t.Context(), f.uid, res)
			f.m.Execute(t.Context(), f.uid)
			f.m.Wait()
			r := loaded(t, f.s, f.uid)
			if r.Steps[0].Attempt != 1 || f.count("agent.prompt") != 1 || r.Steps[0].Status != status {
				t.Fatal("job without retries re-prompted", r)
			}
		})
	}
}
func TestCompletionSequenceMustAdvanceWhenPresent(t *testing.T) {
	f := newExecution(t)
	f.accepted(t)
	f.result(t, "succeeded")
	r := loaded(t, f.s, f.uid)
	a, _ := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
	a.CompletionSeq = r.Herdr.PreSubmitSeq
	f.h.SetAgent(a)
	f.m.clock = &activityClock{at: time.Now()}
	f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = f.m.clock.Now().Add(10 * time.Second); return nil })
	f.h.Fail("agent.wait", herdr.Error{Code: "timeout"})
	if f.await(t) != nil || loaded(t, f.s, f.uid).Reason != "timeout" {
		t.Fatal("stale completion counted")
	}
}
func TestCleanupRefusesChangedProvenance(t *testing.T) {
	for _, change := range []string{"terminal", "pane-tab", "workspace", "session", "agent-name", "server", "working"} {
		t.Run(change, func(t *testing.T) {
			f := newExecution(t)
			f.accepted(t)
			r := loaded(t, f.s, f.uid)
			a, _ := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
			p, _ := f.m.h.PaneGet(t.Context(), r.Herdr.PaneID)
			switch change {
			case "terminal":
				p.TerminalID = "replacement"
				f.h.SetPane(p)
			case "pane-tab":
				p.TabID = "replacement"
				f.h.SetPane(p)
			case "workspace":
				p.WorkspaceID = "replacement"
				f.h.SetPane(p)
			case "session":
				a.Session.Value = "replacement"
				f.h.SetAgent(a)
			case "agent-name":
				a.Name = "replacement"
				f.h.SetAgent(a)
			case "server":
				if e := f.h.Restart(); e != nil {
					t.Fatal(e)
				}
			case "working":
				a.Status = "working"
				f.h.SetAgent(a)
			}
			f.s.Transition(f.uid, func(r *Run) error { r.Phase = Settled; r.Outcome = "succeeded"; return nil })
			f.m.cleanup(t.Context(), f.uid)
			if f.count("tab.close") != 0 {
				t.Fatal("closed foreign/live tab")
			}
		})
	}
}
func TestBlockedAwaitRestoresAttention(t *testing.T) {
	f := newExecution(t)
	f.s.Transition(f.uid, func(r *Run) error { r.IssueOwned = true; r.IssueRef = "project#issue"; return nil })
	f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Blocked}, Wait: []herdrtest.Outcome{herdrtest.Idle}})
	f.accepted(t)
	f.result(t, "succeeded")
	if f.await(t) == nil {
		t.Fatal("missing result")
	}
	r := loaded(t, f.s, f.uid)
	var values []string
	for _, op := range r.Ops {
		var p Payload
		json.Unmarshal(op.Payload, &p)
		if p.Name == "work.attention" {
			values = append(values, p.Value)
		}
	}
	if strings.Join(values, ",") != "needs-human,ok" {
		t.Fatal(values)
	}
}
func TestLaunchNameTakenAdoptsMatchingIntent(t *testing.T) {
	f := newExecution(t)
	f.launch(t)
	r := loaded(t, f.s, f.uid)
	f.s.Transition(f.uid, func(r *Run) error { r.Herdr.Session = nil; return nil })
	f.h.Script(r.Herdr.AgentName, herdrtest.Script{Start: herdrtest.NameTaken})
	f.launch(t)
	if loaded(t, f.s, f.uid).Herdr.Session == nil || f.count("agent.start") != 1 {
		t.Fatal("failed intent adoption")
	}
}

func TestNoResultPollsOnlyLocalFile(t *testing.T) {
	f := newExecution(t)
	f.accepted(t)
	f.m.clock = &activityClock{at: time.Now()}
	f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = f.m.clock.Now().Add(time.Minute); return nil })
	before := f.count("agent.get")
	f.await(t)
	if reads := f.count("agent.get") - before; reads > 2 {
		t.Fatalf("idle file grace polled Herdr %d times", reads)
	}
}
func TestAdmitReturnsWhileExecutionWaits(t *testing.T) {
	f := newAdmission(t)
	h := herdrtest.New(t)
	f.m.h = &herdr.Client{Path: h.Path}
	f.d.Def.Action = defs.Action{Kind: "execute", Prompt: "Inspect"}
	f.d.Workflow = nil
	f.d.WFDef = nil
	h.Script("hk-"+strings.ToLower(f.uid), herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Working}})
	f.start(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer func() { cancel(); f.m.Wait() }()
	done := make(chan error, 1)
	go func() {
		decision, e := f.m.Admit(ctx, f.d)
		if decision != sched.Admitted && e == nil {
			e = errors.New("not admitted")
		}
		done <- e
	}()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("admit blocked on prompt")
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		r := loaded(t, f.s, f.uid)
		if r.Steps[0].Submission == Accepted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("prompt never accepted")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLaunchRefusesReusedTerminalBeforeStart(t *testing.T) {
	f := newExecution(t)
	ws, e := f.m.ensureWorkspace(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	tab, pane, e := f.m.h.TabCreate(t.Context(), ws.WorkspaceID, "task", "", nil)
	if e != nil {
		t.Fatal(e)
	}
	f.s.Transition(f.uid, func(r *Run) error {
		r.Herdr = HerdrRef{ServerID: ws.ServerID, WorkspaceID: ws.WorkspaceID, TabID: tab.TabID, PaneID: pane.PaneID, TerminalID: "old-terminal"}
		return nil
	})
	f.launch(t)
	if f.count("agent.start") != 0 || loaded(t, f.s, f.uid).Phase != Held {
		t.Fatal("launched in replaced terminal")
	}
}
func TestRunPaneMetadataDuringExecution(t *testing.T) {
	f := newExecution(t)
	f.launch(t)
	r := loaded(t, f.s, f.uid)
	pane, e := f.m.h.PaneGet(t.Context(), r.Herdr.PaneID)
	if e != nil || pane.Tokens["run"] != f.uid || pane.Tokens["phase"] != "running" {
		t.Fatal(pane, e)
	}
}

func TestLaunchReadinessPollIsBounded(t *testing.T) {
	f := newExecution(t)
	f.m.clock = &activityClock{at: time.Now()}
	f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = f.m.clock.Now().Add(time.Hour); return nil })
	f.script(herdrtest.Script{Start: herdrtest.PendingReady, Get: []herdr.Agent{{Status: "unknown"}}})
	f.launch(t)
	r := loaded(t, f.s, f.uid)
	if r.Phase != Held || r.Reason != "launch_timeout" || f.count("agent.get") > 122 {
		t.Fatal("unbounded startup", r, f.count("agent.get"))
	}
}
func TestLaunchRetriesPaneBusyWithinBound(t *testing.T) {
	f := newExecution(t)
	f.h.Fail("agent.start", herdr.Error{Code: "agent_pane_busy"}, herdr.Error{Code: "agent_pane_busy"})
	f.launch(t)
	if f.count("agent.start") != 3 || loaded(t, f.s, f.uid).Phase != Running {
		t.Fatal("busy pane was not retried")
	}
}
func TestHeldIdleReleasesSlotKeepsGate(t *testing.T) {
	f := newRecovery(t)
	f.m.cfg.Limits.MaxActiveRuns = 1
	f.m.reservations[f.uid] = "job"
	f.accepted(t)
	f.m.clock = &activityClock{at: time.Now()}
	f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = f.m.clock.Now().Add(time.Minute); return nil })
	f.await(t)
	if f.m.FreeSlots() != 1 || !f.m.GateHeld("job") {
		t.Fatal("idle unresolved capacity/gate")
	}
}

func TestCleanupFailurePreservesSucceededRun(t *testing.T) {
	f := newRecovery(t)
	f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Working}})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if e := f.m.Execute(ctx, f.uid); e != nil {
		t.Fatal(e)
	}
	for until := time.Now().Add(5 * time.Second); loaded(t, f.s, f.uid).Steps[0].Submission != Accepted; {
		if time.Now().After(until) {
			t.Fatal("no prompt")
		}
		time.Sleep(time.Millisecond)
	}
	f.result(t, "succeeded")
	r := loaded(t, f.s, f.uid)
	f.h.Fail("tab.close", herdr.Error{Code: "temporarily_unavailable"})
	a, _ := f.m.h.AgentGet(ctx, r.Herdr.AgentName)
	a.Status = "idle"
	seq := uint64(1)
	a.CompletionSeq = &seq
	f.h.SetAgent(a)
	f.m.Wait()
	r = loaded(t, f.s, f.uid)
	if r.Phase != Settled || r.Outcome != "succeeded" {
		t.Fatal("cleanup changed execution", r)
	}
}

func TestRepeatedWaitErrorsRespectDeadline(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		t.Run(fmt.Sprint(accepted), func(t *testing.T) {
			f := newExecution(t)
			if accepted {
				f.launch(t)
				r := loaded(t, f.s, f.uid)
				a, _ := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
				a.Status = "working"
				a.InteractiveReady = false
				f.h.SetAgent(a)
			} else {
				f.script(herdrtest.Script{Get: []herdr.Agent{{Status: "working"}}})
			}
			f.m.clock = &activityClock{at: time.Now()}
			f.s.Transition(f.uid, func(r *Run) error {
				r.ExpiresAt = f.m.clock.Now().Add(time.Minute)
				if accepted {
					r.Steps[0].Submission = Accepted
				}
				return nil
			})
			for range 5 {
				f.h.Fail("agent.wait", herdr.Error{Code: "agent_prompt_stalled"})
			}
			done := make(chan error, 1)
			go func() {
				if accepted {
					_, e := f.m.awaitResult(t.Context(), f.uid)
					done <- e
				} else {
					done <- f.m.execute(t.Context(), f.uid)
				}
			}()
			select {
			case e := <-done:
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("wait errors spun without advancing deadline")
			}
			if f.count("agent.wait") > 4 || loaded(t, f.s, f.uid).Reason != "timeout" {
				t.Fatal("unbounded retries")
			}
		})
	}
}

func TestHeldExitedPaneReleasesSlotKeepsGate(t *testing.T) {
	f := newExecution(t)
	f.m.cfg.Limits.MaxActiveRuns = 1
	f.m.reservations[f.uid] = "job"
	f.accepted(t)
	r := loaded(t, f.s, f.uid)
	if e := f.m.h.TabClose(t.Context(), r.Herdr.TabID); e != nil {
		t.Fatal(e)
	}
	f.m.clock = &activityClock{at: time.Now()}
	f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = f.m.clock.Now().Add(10 * time.Second); return nil })
	f.await(t)
	if f.m.FreeSlots() != 1 || !f.m.GateHeld("job") || loaded(t, f.s, f.uid).Reason != "agent_lost" {
		t.Fatal("exited pane retained live capacity")
	}
}

type callbackClock struct {
	*activityClock
	first func()
}

func (c *callbackClock) NewTimer(d time.Duration) Timer {
	if c.first != nil {
		fn := c.first
		c.first = nil
		fn()
	}
	return c.activityClock.NewTimer(d)
}
func TestPaneExitsDuringResultPoll(t *testing.T) {
	for _, report := range []bool{false, true} {
		t.Run(fmt.Sprint(report), func(t *testing.T) {
			f := newExecution(t)
			f.m.cfg.Limits.MaxActiveRuns = 1
			f.m.reservations[f.uid] = "job"
			f.accepted(t)
			r := loaded(t, f.s, f.uid)
			a, e := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
			if e != nil {
				t.Fatal(e)
			}
			f.h.RemoveAgent(r.Herdr.AgentName)
			// The name disappears before the foreground process is proven gone.
			// Only the scheduled pane exit below supplies stopped proof.
			a.Name = "foreign"
			f.h.SetAgent(a)
			f.m.clock = &callbackClock{activityClock: &activityClock{at: time.Now()}, first: func() {
				if report {
					f.result(t, "succeeded")
				}
				if e := f.m.h.TabClose(t.Context(), r.Herdr.TabID); e != nil {
					t.Fatal(e)
				}
			}}
			// The result and exit arrive before expiry, inside the 30-second
			// missing-artifact proof interval. They must trigger a fresh proof.
			if _, e := f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = f.m.clock.Now().Add(20 * time.Second); return nil }); e != nil {
				t.Fatal(e)
			}
			res := f.await(t)
			if report {
				if res == nil || res.Status != "succeeded" {
					t.Fatal("late exit result ignored")
				}
			} else if f.m.FreeSlots() != 1 || !f.m.GateHeld("job") {
				t.Fatal("late exit capacity retained")
			}
		})
	}
}

func TestResultArrivesDuringExitProof(t *testing.T) {
	f := newExecution(t)
	f.accepted(t)
	r := loaded(t, f.s, f.uid)
	f.h.RemoveAgent(r.Herdr.AgentName)
	f.m.clock = &activityClock{at: time.Now()}
	if _, e := f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = f.m.clock.Now().Add(time.Minute); return nil }); e != nil {
		t.Fatal(e)
	}
	// ReadResult has seen a missing file. The agent's final artifact arrives
	// before the socket reply supplies pane-exit proof.
	f.h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method != "pane.get" {
			return
		}
		f.result(t, "succeeded")
		if e := f.m.h.TabClose(t.Context(), r.Herdr.TabID); e != nil {
			t.Error(e)
		}
	}
	res := f.await(t)
	if res == nil || res.Status != "succeeded" || loaded(t, f.s, f.uid).Phase == Held {
		t.Fatal("exit proof lost a valid result")
	}
	if f.count("agent.prompt") != 1 {
		t.Fatal("resent after exit")
	}
}

func TestIdleNotInteractiveReadyDoesNotSpin(t *testing.T) {
	f := newExecution(t)
	f.m.clock = &activityClock{at: time.Now()}
	f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = f.m.clock.Now().Add(time.Minute); return nil })
	f.script(herdrtest.Script{Get: []herdr.Agent{{Status: "idle", InteractiveReady: false}}})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	e := f.m.execute(ctx, f.uid)
	if e != nil || loaded(t, f.s, f.uid).Reason != "timeout" || f.count("agent.prompt") != 0 || f.count("agent.wait") != 0 || f.count("agent.get") > 4 {
		t.Fatal("unready idle agent caused rapid waits", e, f.count("agent.wait"), f.count("agent.get"))
	}
}

func TestLaunchMetadataFailureDoesNotPreventPrompt(t *testing.T) {
	f := newExecution(t)
	f.h.Fail("pane.report_metadata", herdr.Error{Code: "unknown_method", Message: "metadata unavailable"})
	f.accepted(t)
	f.result(t, "succeeded")
	res := f.await(t)
	if e := f.m.settle(t.Context(), f.uid, res); e != nil {
		t.Fatal(e)
	}
	if e := f.m.cleanup(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	if r.Phase != Settled || r.Outcome != "succeeded" || f.count("agent.start") != 1 || f.count("agent.prompt") != 1 || f.count("tab.close") != 1 {
		t.Fatal("metadata failure changed task outcome", r)
	}
}

type boundaryClock struct {
	before, at time.Time
	reads      int
}

func (c *boundaryClock) Now() time.Time {
	c.reads++
	if c.reads == 1 {
		return c.before
	}
	return c.at
}
func (c *boundaryClock) NewTimer(d time.Duration) Timer { return RealClock{}.NewTimer(d) }
func TestDeadlineCrossesDuringLostResultRead(t *testing.T) {
	f := newExecution(t)
	f.m.cfg.Limits.MaxActiveRuns = 1
	f.m.reservations[f.uid] = "job"
	f.accepted(t)
	r := loaded(t, f.s, f.uid)
	if e := f.m.h.TabClose(t.Context(), r.Herdr.TabID); e != nil {
		t.Fatal(e)
	}
	f.m.clock = &boundaryClock{before: r.ExpiresAt.Add(-time.Nanosecond), at: r.ExpiresAt}
	_, _, e := f.m.pollResult(t.Context(), r, true)
	if e != nil || f.m.FreeSlots() != 1 || !f.m.GateHeld("job") {
		t.Fatal("deadline boundary lost stopped proof", e)
	}
}
