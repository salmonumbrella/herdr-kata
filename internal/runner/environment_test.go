package runner

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"go.kenn.io/kit/secretref"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This is the environment-check boundary, independent of the fake socket's
// agent lifecycle. Actual pane environments are exercised with real Herdr.
type cleanLaunch struct{}

func (cleanLaunch) Check(_ context.Context, c *herdr.Client, _ func(context.Context) (string, error), _ map[string]string, _ []string, _, _ string) (herdr.ServerID, error) {
	if c == nil {
		return herdr.ServerID{}, nil
	}
	return herdr.ReadServerID(c.Path)
}

type leakingLaunch struct{}

func (leakingLaunch) Check(context.Context, *herdr.Client, func(context.Context) (string, error), map[string]string, []string, string, string) (herdr.ServerID, error) {
	return herdr.ServerID{}, errors.New("Herdr launch environment leaks KATA_SERVER")
}

func TestUncleanEnvironmentHoldsBeforeAgent(t *testing.T) {
	f := newExecution(t)
	f.m.environment = leakingLaunch{}
	f.launch(t)
	r := loaded(t, f.s, f.uid)
	if r.Phase != Held || r.Reason != "herdr_env_unclean" || r.Steps[0].Attempt != 1 || r.Steps[0].Submission != None || f.count("agent.start") != 0 || f.count("tab.create") != 0 {
		t.Fatalf("launch environment was not checked: %s %s", r.Phase, r.Reason)
	}
}

// Live regression: a manual prompt request made while Herdr is contaminated
// must become an observable run, rather than an automatically retried admission.
func TestManualUncleanEnvironmentRequiresResume(t *testing.T) {
	f := newAdmission(t)
	h := herdrtest.New(t)
	f.m.h = &herdr.Client{Path: h.Path}
	f.m.environment = leakingLaunch{}
	t.Setenv("EXAMPLE_SOURCE", "fixture-secret-only")
	f.m.cfg.Secrets = map[string]secretref.Ref{"review": {Env: "EXAMPLE_SOURCE"}}
	f.d.Def.SecretRefs = map[string]string{"GITHUB_TOKEN": "review"}
	f.d.Manual, f.d.ManualRunUID = true, f.uid
	f.d.WFDef.Steps = []defs.Step{{Key: "main", Kind: "prompt", Prompt: "work"}}
	observations := make(chan kata.Observation, 8)
	f.k.observe = func(_ context.Context, _ string, o kata.Observation) (kata.Run, bool, error) {
		observations <- o
		return kata.Run{}, false, nil
	}
	f.k.comment = func(context.Context, string, string, string) error { return nil }
	f.k.notify = func(context.Context, string, string, string) error { return nil }
	f.k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) { return 8, nil }
	f.k.close = func(context.Context, kata.CloseReq) error { return nil }
	f.k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{UID: "issue", ProjectUID: "project", Status: "open", Author: "operator", Revision: 7}, nil
	}
	k := &resumeKata{admissionKata: f.k, ready: []kata.IssueView{{UID: "issue"}}}
	f.m.k, f.q.k = k, k
	f.start(t)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	result, err := f.m.Admit(ctx, f.d)
	if err != nil || result != sched.Admitted {
		t.Fatalf("manual request did not become a visible run: %v %v", result, err)
	}
	f.m.Wait()
	r := loaded(t, f.s, f.uid)
	if r.Phase != Held || r.Reason != "herdr_env_unclean" || r.IssueUID != "issue" || r.ActiveStep != "main" || r.Steps[0].Attempt != 1 || r.Steps[0].Submission != None || !strings.Contains(r.Steps[0].Reason, "KATA_SERVER") {
		t.Fatalf("missing durable environment hold: %+v", r)
	}
	if err := f.q.wait(ctx, f.uid, r.Ops[len(r.Ops)-1].ID); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"running", "unknown"} {
		select {
		case observation := <-observations:
			if observation.Status != status || status == "unknown" && !strings.Contains(observation.Summary.Message, "herdr_env_unclean") {
				t.Fatal("Kata did not receive the admitted and held run", observation)
			}
		default:
			t.Fatalf("Kata did not receive the %s observation", status)
		}
	}
	req := Request{Version: 1, UID: "environment-resume", Kind: "resume", RunUID: f.uid}
	if err = f.m.Resume(ctx, req); err == nil || !strings.Contains(err.Error(), "KATA_SERVER") {
		t.Fatal("unclean resume did not explain the leak", err)
	}
	if len(loaded(t, f.s, f.uid).ConsumedResumes) != 0 {
		t.Fatal("refusal consumed resume")
	}
	f.m.environment = cleanLaunch{}
	if err = f.m.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if result, err = f.m.Admit(ctx, f.d); err != nil || result != sched.Admitted {
		t.Fatal("held request was not acknowledged", result, err)
	}
	if err = f.m.Execute(ctx, f.uid); err != nil {
		t.Fatal(err)
	}
	f.m.Wait()
	if r = loaded(t, f.s, f.uid); r.Phase != Held || r.Reason != "herdr_env_unclean" {
		t.Fatal("repair automatically released the hold", r.Phase, r.Reason)
	}
	for _, call := range h.Requests() {
		if call.Method == "agent.start" || call.Method == "agent.prompt" {
			t.Fatal("an agent started before explicit resume")
		}
	}
	h.BeforeRequest = func(call herdrtest.Request) {
		if call.Method != "agent.prompt" {
			return
		}
		path := filepath.Join(f.s.layout.AttemptDir(f.uid, "main", 1), "result.json")
		if err := WriteResult(f.s.fsys, path, &Result{Version: 1, Run: f.uid, Step: "main", Attempt: 1, Status: "succeeded", Summary: "Verified the repaired launch environment with the focused runtime checks.", Evidence: []string{"test:fixture environment check"}}); err != nil {
			t.Error(err)
		}
	}
	if err = f.m.Resume(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err = f.m.Execute(ctx, f.uid); err != nil {
		t.Fatal(err)
	}
	f.m.Wait()
	r = loaded(t, f.s, f.uid)
	if r.Outcome != "succeeded" || r.Steps[0].Attempt != 1 {
		t.Fatalf("resume did not continue the unsent step: %s attempt %d", r.Outcome, r.Steps[0].Attempt)
	}
	starts, prompts := 0, 0
	for _, call := range h.Requests() {
		if call.Method == "agent.start" {
			starts++
		}
		if call.Method == "agent.prompt" {
			prompts++
		}
	}
	if starts != 1 || prompts != 1 {
		t.Fatalf("resume starts=%d prompts=%d", starts, prompts)
	}
}

type coldProbe struct{ t *testing.T }

func (p coldProbe) Check(ctx context.Context, _ *herdr.Client, _ func(context.Context) (string, error), _ map[string]string, _ []string, _, _ string) (herdr.ServerID, error) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) < 15*time.Second {
		p.t.Error("resume did not allow a cold environment probe its full budget")
	}
	return herdr.ServerID{}, context.DeadlineExceeded
}
func TestColdResumeProbeTimeoutKeepsRequest(t *testing.T) {
	f := heldResumeFixture(t)
	f.s.Transition(f.uid, func(r *Run) error { r.Reason = "herdr_env_unclean"; return nil })
	f.m.environment = coldProbe{t}
	uid := "01K00000000000000000000001"
	publishResume(t, f, Request{Version: 1, UID: uid, Kind: "resume", RunUID: f.uid})
	if e := f.m.HandleRequests(t.Context(), nil); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(f.s.layout.RequestsDir(), uid+".json")); e != nil {
		t.Fatal("cold probe timeout discarded resume", e)
	}
	if r := loaded(t, f.s, f.uid); r.Resume != nil || len(r.ConsumedResumes) != 0 {
		t.Fatal("probe timeout consumed resume")
	}
}

type replaceAfterProof struct{ h *herdrtest.Server }

func (p replaceAfterProof) Check(context.Context, *herdr.Client, func(context.Context) (string, error), map[string]string, []string, string, string) (herdr.ServerID, error) {
	id, e := herdr.ReadServerID(p.h.Path)
	if e != nil {
		return herdr.ServerID{}, e
	}
	return id, p.h.Restart()
}
func TestReplacementBetweenProbeAndWorkspaceCannotLaunch(t *testing.T) {
	f := newExecution(t)
	f.m.environment = replaceAfterProof{f.h}
	f.launch(t)
	r := loaded(t, f.s, f.uid)
	if r.Phase != Held || r.Reason != "herdr_env_unclean" || f.count("agent.start") != 0 || f.count("agent.prompt") != 0 {
		t.Fatal("unprobed replacement launched", r.Phase, r.Reason)
	}
}

type currentProbeHelper struct{ t *testing.T }

func (p currentProbeHelper) Check(_ context.Context, c *herdr.Client, _ func(context.Context) (string, error), _ map[string]string, _ []string, bin, _ string) (herdr.ServerID, error) {
	current, e := os.Executable()
	if e != nil {
		return herdr.ServerID{}, e
	}
	if bin != current {
		p.t.Error("probe invoked retained report executable")
	}
	return herdr.ReadServerID(c.Path)
}
func TestProbeUsesWorkerWhileReportBinaryIsRetained(t *testing.T) {
	f := newExecution(t)
	f.m.environment = currentProbeHelper{t}
	f.s.Transition(f.uid, func(r *Run) error { r.Herdr.ReportBin = "/srv/example/previous-build"; return nil })
	f.launch(t)
	for _, req := range f.h.Requests() {
		if req.Method == "tab.create" {
			var p struct {
				Env map[string]string `json:"env"`
			}
			if e := json.Unmarshal(req.Params, &p); e != nil {
				t.Fatal(e)
			}
			if p.Env["HERDR_KATA_REPORT"] != "/srv/example/previous-build" {
				t.Fatal("recovery changed retained report binary")
			}
		}
	}
}

type canceledLaunch struct{ cancel context.CancelFunc }

func (p canceledLaunch) Check(ctx context.Context, _ *herdr.Client, _ func(context.Context) (string, error), _ map[string]string, _ []string, _, _ string) (herdr.ServerID, error) {
	p.cancel()
	return herdr.ServerID{}, ctx.Err()
}

func TestCanceledEnvironmentProbeKeepsRunRunnable(t *testing.T) {
	f := newExecution(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.m.environment = canceledLaunch{cancel}
	if err := f.m.launch(ctx, f.uid); !errors.Is(err, context.Canceled) {
		t.Fatal("probe converted worker shutdown into an environment hold", err)
	}
	r := loaded(t, f.s, f.uid)
	if r.Phase != Admitted || r.Reason != "" || r.Steps[0].Submission != None || f.count("agent.start") != 0 {
		t.Fatal("shutdown changed the unsent run", r.Phase, r.Reason)
	}
}

func TestResumeEnvironmentHoldWithNeverStartedTab(t *testing.T) {
	for _, condition := range []string{"shell", "foreign-agent", "foreground-process", "terminal-replaced", "server-replaced"} {
		t.Run(condition, func(t *testing.T) {
			f := heldResumeFixture(t)
			h := herdrtest.New(t)
			f.m.h = &herdr.Client{Path: h.Path}
			ws, err := f.m.ensureWorkspace(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			tab, pane, err := f.m.h.TabCreate(t.Context(), ws.WorkspaceID, "Fixture", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			// Crash boundary: the tab identity is durable, but no agent launch
			// intent has been written. Recovery's cold probe then fails.
			_, err = f.s.Transition(f.uid, func(r *Run) error {
				r.Phase, r.Reason, r.Outcome = Admitted, "", ""
				r.EndedAt = nil
				r.WFDef.Steps = []defs.Step{{Key: "main", Kind: "prompt", Prompt: "Inspect"}}
				r.Steps = []StepState{{Key: "main", Kind: "prompt", Attempt: 1, Submission: None, Herdr: &HerdrRef{ServerID: ws.ServerID, WorkspaceID: ws.WorkspaceID, TabID: tab.TabID, PaneID: pane.PaneID, TerminalID: pane.TerminalID, HasSubmitted: new(false)}}}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			f.m.environment = leakingLaunch{}
			if err = f.m.launch(t.Context(), f.uid); err != nil {
				t.Fatal(err)
			}
			if r := loaded(t, f.s, f.uid); r.Phase != Held || r.Reason != "herdr_env_unclean" {
				t.Fatal(r.Phase, r.Reason)
			}
			switch condition {
			case "foreign-agent":
				h.SetAgent(herdr.Agent{Name: "foreign", PaneID: pane.PaneID, TerminalID: pane.TerminalID, TabID: tab.TabID, WorkspaceID: ws.WorkspaceID, Status: "working"})
			case "foreground-process":
				h.BeforeRequest = func(call herdrtest.Request) {
					if call.Method == "pane.process_info" {
						h.SetAgent(herdr.Agent{Name: "foreign", PaneID: pane.PaneID, Status: "working"})
					}
				}
			case "terminal-replaced":
				if _, err = f.s.Transition(f.uid, func(r *Run) error { AgentReference(r).TerminalID = "previous-terminal"; return nil }); err != nil {
					t.Fatal(err)
				}
			case "server-replaced":
				if err = h.Restart(); err != nil {
					t.Fatal(err)
				}
			}
			f.m.environment = cleanLaunch{}
			f.start(t)
			err = f.m.Resume(t.Context(), Request{UID: "resume-tab", RunUID: f.uid})
			r := loaded(t, f.s, f.uid)
			if condition != "shell" {
				if !errors.Is(err, errControlRefused) || r.Phase != Held || len(r.ConsumedResumes) != 0 {
					t.Fatal("unsafe tab authorized resume", err, r.Phase)
				}
				return
			}
			if err != nil {
				t.Fatal("unstarted shell tab could not resume", err)
			}
			if err = f.m.launch(t.Context(), f.uid); err != nil {
				t.Fatal(err)
			}
			r = loaded(t, f.s, f.uid)
			if r.Phase != Running || r.Steps[0].Attempt != 1 || len(r.PastAttempts) != 0 || AgentReference(r).PaneID != pane.PaneID {
				t.Fatal("resume retired the unsent attempt", r.Phase, r.Steps[0].Attempt)
			}
			starts := 0
			for _, call := range h.Requests() {
				if call.Method == "agent.start" {
					starts++
				}
			}
			if starts != 1 {
				t.Fatalf("agent starts = %d, want 1", starts)
			}
		})
	}
}
