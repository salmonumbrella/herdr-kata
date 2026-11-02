package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/prompt"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"hegel.dev/go/hegel"
)

type fakeKata struct {
	kata.Kata
	jobs      []kata.CronJob
	workflows []kata.CronWorkflow
	ready     map[string]bool
	date      *kata.DateValue
	err       error
	createErr error
	creates   int
	bodies    [][]byte
}

func (k *fakeKata) Jobs() ([]kata.CronJob, error)                 { return k.jobs, k.err }
func (k *fakeKata) Workflows() ([]kata.CronWorkflow, error)       { return k.workflows, k.err }
func (k *fakeKata) Ready() (map[string]bool, error)               { return k.ready, k.err }
func (k *fakeKata) PlanningDates(string) (*kata.DateValue, error) { return k.date, k.err }
func (k *fakeKata) IssueTitle(string) (string, error)             { return "Existing fixture issue", nil }
func (k *fakeKata) CreateIssue(string, string, string) (string, string, error) {
	k.creates++
	if k.createErr != nil {
		return "", "", k.createErr
	}
	return "abcd", "01J00000000000000000000020", k.err
}
func (k *fakeKata) SetDeadline(string, time.Time) error { return k.err }
func (k *fakeKata) Notify(string, string, string) error { return k.err }
func (k *fakeKata) Observe(_ string, b []byte, rev int) (int, bool, error) {
	k.bodies = append(k.bodies, slices.Clone(b))
	return rev + 1, false, k.err
}

type fakeHC struct {
	hc.HC
	core        string
	terms       map[string]hc.Terminal
	runs, kills int
}

func (h *fakeHC) CoreID(context.Context) (string, error) { return h.core, nil }
func (h *fakeHC) Run(_ context.Context, s hc.RunSpec) (hc.Launched, error) {
	h.runs++
	id := fmt.Sprintf("t%d", h.runs)
	h.terms[id] = hc.Terminal{Address: id, Name: s.Name, State: "live", CreatedMs: int64(h.runs), PID: uint32(100 + h.runs), Labels: s.Labels, Argv: append(slices.Clone(s.Prefix), s.Argv...)}
	return hc.Launched{Address: id, Name: s.Name, State: "live"}, nil
}
func (h *fakeHC) Get(_ context.Context, id string) (hc.Terminal, error) {
	v, ok := h.terms[id]
	if !ok {
		return v, hc.ErrNotFound
	}
	return v, nil
}
func (h *fakeHC) List(_ context.Context, all bool, labels map[string]string) ([]string, error) {
	if !all {
		return nil, errors.New("missing --all")
	}
	var out []string
	for id, v := range h.terms {
		if v.Labels["kata.nonce"] == labels["kata.nonce"] {
			out = append(out, id)
		}
	}
	return out, nil
}
func (h *fakeHC) Interrupt(context.Context, string) error { return nil }
func (h *fakeHC) Kill(_ context.Context, id string) error {
	h.kills++
	v := h.terms[id]
	v.State = "finished"
	v.Reason = "killed"
	h.terms[id] = v
	return nil
}
func (h *fakeHC) Remove(_ context.Context, id string) error {
	if h.terms[id].State == "live" {
		return hc.ErrLive
	}
	delete(h.terms, id)
	return nil
}

var at = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

func fixture(t *testing.T) (*Worker, *fakeHC, *fakeKata) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("XDG_RUNTIME_DIR", root)
	cfg := config.Config{Executor: "executor", HCSession: "fixture", HCBin: "/bin/sh", Kata: config.KataCfg{Bin: "/bin/sh", Daemon: "fixture"}, Projects: []config.Project{{Name: "example-project", Actor: "worker", Commands: true, MaxActiveRuns: 1}}, Checkouts: map[string]string{"example-project": root}, Profiles: map[string]config.Profile{"default": {Argv: []string{"/bin/sh"}}}, Limits: config.Limits{DefaultTimeout: 2 * time.Hour, MaxTimeout: 24 * time.Hour, MaxActiveRuns: 4}}
	job := kata.CronJob{UID: "01J00000000000000000000010", DefinitionEventUID: "01J00000000000000000000011", CreatedAt: at.Add(-time.Hour), Name: "example-job", Definition: kata.JobDefinition{Version: 1, Kind: "job", Enabled: true, Trigger: kata.Trigger{Kind: "manual"}, Action: kata.Action{Kind: "execute", Prompt: new("Task {{input}}")}, Issue: &kata.IssuePolicy{Kind: "per-run", Title: new("Example task")}, CheckoutKey: new("example-project"), Catchup: "all", Overlap: "forbid"}}
	k := &fakeKata{jobs: []kata.CronJob{job}, ready: make(map[string]bool)}
	h := &fakeHC{core: "core-one", terms: make(map[string]hc.Terminal)}
	return New(cfg, journal.New(state.Layout{Root: filepath.Join(root, "state")}), h, map[string]kata.Kata{"example-project": k}, "/usr/local/bin/herdr-kata"), h, k
}
func request(t *testing.T, w *Worker, r journal.Request) {
	t.Helper()
	if err := (journal.Requests{Dir: w.Store.Layout.Requests()}).Write(r); err != nil {
		t.Fatal(err)
	}
}
func tick(t *testing.T, w *Worker, now time.Time) {
	t.Helper()
	if err := w.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
}
func active(t *testing.T, w *Worker) journal.Run {
	t.Helper()
	runs, err := w.Store.Active()
	if err != nil || len(runs) != 1 {
		t.Fatalf("active %v %v", runs, err)
	}
	return runs[0]
}
func finishAgent(t *testing.T, w *Worker, h *fakeHC, r journal.Run) {
	t.Helper()
	a := r.Attempts[len(r.Attempts)-1]
	path := filepath.Join(w.Store.Layout.Attempt(r.Key, a.Step, a.N), "result.json")
	if err := prompt.WriteResult(path, prompt.Result{RunUID: r.RunUID, Step: a.Step, Attempt: a.N, Status: "succeeded", Summary: "Complete", Note: "Done"}); err != nil {
		t.Fatal(err)
	}
	term := h.terms[a.Address]
	term.State = "finished"
	term.Reason = "exited"
	h.terms[a.Address] = term
}
func TestPromptJobEndToEnd(t *testing.T) {
	w, h, k := fixture(t)
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
	tick(t, w, at)
	r := active(t, w)
	if h.runs != 1 || len(r.AdmissionBody) == 0 {
		t.Fatal(r, h.runs)
	}
	finishAgent(t, w, h, r)
	tick(t, w, at.Add(time.Second))
	for n := 2; n < 12; n++ {
		tick(t, w, at.Add(time.Duration(n)*time.Second))
	}
	got, err := w.Store.Load(r.Key)
	if err != nil || got.Phase != "reported" || got.Outcome != "succeeded" || h.runs != 1 || len(k.bodies) != 2 {
		t.Fatal(got, err, h.runs, len(k.bodies))
	}
}
func workflow(k *fakeKata) {
	k.jobs[0].Definition.Action = kata.Action{Kind: "execute", WorkflowUID: new("01J00000000000000000000012")}
	k.workflows = []kata.CronWorkflow{{UID: "01J00000000000000000000012", DefinitionEventUID: "01J00000000000000000000013", Name: "example-workflow", Definition: kata.WorkflowDefinition{Version: 1, Steps: []kata.WorkflowStep{{Key: "gate", Kind: "command", Command: new("exit 7"), Options: map[string]any{"skip_exit": json.Number("7")}}, {Key: "work", Kind: "prompt", Prompt: new("Task"), After: []string{"gate"}}}}}}
}
func TestWorkflowWithGatedCommand(t *testing.T) {
	w, h, k := fixture(t)
	workflow(k)
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
	tick(t, w, at)
	r := active(t, w)
	a := r.Attempts[0]
	receipt := journal.Receipt{ExitCode: 7, StartedAt: a.LaunchedAt, EndedAt: a.LaunchedAt.Add(time.Second)}
	if err := state.WriteJSON(filepath.Join(w.Store.Layout.Attempt(r.Key, a.Step, a.N), "receipt.json"), receipt); err != nil {
		t.Fatal(err)
	}
	term := h.terms[a.Address]
	term.State = "finished"
	term.Reason = "exited"
	h.terms[a.Address] = term
	tick(t, w, at.Add(2*time.Second))
	got, _ := w.Store.Load(r.Key)
	if got.Phase != "reported" || got.Outcome != "succeeded" || h.runs != 1 {
		t.Fatal(got, h.runs)
	}
}
func TestCommandsDisabledUnsupported(t *testing.T) {
	w, h, k := fixture(t)
	workflow(k)
	w.Config.Projects[0].Commands = false
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
	_ = w.Tick(context.Background(), at)
	if h.runs != 0 || len(w.Unsupported[k.jobs[0].UID]) == 0 {
		t.Fatal(h.runs, w.Unsupported)
	}
}
func TestOverlapForbid(t *testing.T) {
	w, h, k := fixture(t)
	k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "interval", IntervalSeconds: new(int64(1))}
	tick(t, w, at)
	tick(t, w, at.Add(time.Second))
	tick(t, w, at.Add(2*time.Second))
	if h.runs != 1 {
		t.Fatal(h.runs)
	}
}
func TestCapacityLimits(t *testing.T) {
	w, h, k := fixture(t)
	w.Config.Limits.MaxActiveRuns = 2
	k.jobs[0].Definition.Overlap = "allow"
	w.Config.Projects = append(w.Config.Projects, config.Project{Name: "other-project", Actor: "worker", MaxActiveRuns: 1})
	w.Config.Checkouts["other-project"] = w.Config.Checkouts["example-project"]
	job := k.jobs[0]
	job.UID = "01J00000000000000000000030"
	job.Definition.CheckoutKey = new("other-project")
	other := &fakeKata{jobs: []kata.CronJob{job}, ready: map[string]bool{}}
	w.Clients["other-project"] = other
	for n := 0; n < 2; n++ {
		request(t, w, journal.Request{ID: fmt.Sprintf("a%d", n), Kind: "run", Project: "example-project", JobUID: k.jobs[0].UID})
		request(t, w, journal.Request{ID: fmt.Sprintf("b%d", n), Kind: "run", Project: "other-project", JobUID: job.UID})
	}
	tick(t, w, at)
	if h.runs != 2 {
		t.Fatal(h.runs)
	}
	runs, _ := w.Store.Active()
	counts := map[string]int{}
	for _, r := range runs {
		counts[r.Project]++
	}
	if counts["example-project"] != 1 || counts["other-project"] != 1 {
		t.Fatal(counts)
	}
}
func TestProjectOutageIsolated(t *testing.T) {
	w, h, k := fixture(t)
	k.err = &kata.Error{Code: 7}
	otherJob := k.jobs[0]
	otherJob.UID = "01J00000000000000000000030"
	other := &fakeKata{jobs: []kata.CronJob{otherJob}, ready: map[string]bool{}}
	w.Config.Projects = append(w.Config.Projects, config.Project{Name: "other-project", Actor: "worker", MaxActiveRuns: 1})
	w.Clients["other-project"] = other
	request(t, w, journal.Request{ID: "request", Kind: "run", Project: "other-project", JobUID: otherJob.UID})
	err := w.Tick(context.Background(), at)
	if err == nil || h.runs != 1 {
		t.Fatal(err, h.runs)
	}
}
func TestCrashAfterJournalBeforeCheckpoint(t *testing.T) {
	w, h, k := fixture(t)
	job := k.jobs[0]
	job.Definition.Trigger = kata.Trigger{Kind: "interval", IntervalSeconds: new(int64(1))}
	k.jobs[0] = job
	tick(t, w, at)
	plan, reasons := defs.Freeze(job, nil, w.Config, "example-project", exec.LookPath)
	if len(reasons) != 0 {
		t.Fatal(reasons)
	}
	occ := at.Add(time.Second).Format(time.RFC3339)
	r := journal.Run{Version: 1, Key: journal.RunKey("executor", "example-project", job.UID, occ), RunUID: "01J00000000000000000000001", Project: "example-project", Actor: "worker", Executor: "executor", JobUID: job.UID, Occurrence: occ, Phase: "prepared", Plan: plan, StartedAt: new(at.Add(time.Second)), ExpiresAt: at.Add(time.Hour)}
	if err := state.WriteJSON(w.Store.Layout.Job(job.UID), sched.Checkpoint{Version: 1, JobUID: job.UID, EventUID: job.DefinitionEventUID, Through: at.Add(time.Second), Pending: []sched.Occurrence{{Key: occ, At: at.Add(time.Second)}}}); err != nil {
		t.Fatal(err)
	}
	if err := w.Store.Create(r); err != nil {
		t.Fatal(err)
	}
	tick(t, w, at.Add(2*time.Second))
	got := active(t, w)
	if got.RunUID != r.RunUID || h.runs != 1 {
		t.Fatal(got, h.runs)
	}
}
func TestManualRun(t *testing.T) {
	w, _, k := fixture(t)
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID, Input: "manual context"})
	tick(t, w, at)
	r := active(t, w)
	pending, err := (journal.Requests{Dir: w.Store.Layout.Requests()}).Pending()
	if err != nil || len(pending) != 0 || r.Plan.Input != "manual context" || r.RequestID != "request" {
		t.Fatal(r, pending, err)
	}
}
func TestRelease(t *testing.T) {
	w, _, k := fixture(t)
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
	tick(t, w, at)
	r := active(t, w)
	_ = w.Store.Update(r.Key, func(r *journal.Run) error { r.Phase = "held"; r.Hold = "no_result"; r.Outcome = "unknown"; return nil })
	tick(t, w, at.Add(time.Second))
	request(t, w, journal.Request{ID: "release", Kind: "release", Run: r.RunUID})
	tick(t, w, at.Add(2*time.Second))
	got, _ := w.Store.Load(r.Key)
	if got.Phase != "released" || got.Outcome != "unknown" {
		t.Fatal(got)
	}
}
func TestRestartRecoversWithoutRelaunch(t *testing.T) {
	w, h, k := fixture(t)
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
	tick(t, w, at)
	r := active(t, w)
	_ = w.Store.Update(r.Key, func(r *journal.Run) error {
		a := &r.Attempts[0]
		a.Address = ""
		a.PID = 0
		a.CreatedMs = 0
		a.Status = "launching"
		return nil
	})
	restarted := New(w.Config, w.Store, h, w.Clients, w.Bin)
	tick(t, restarted, at.Add(time.Second))
	got := active(t, restarted)
	if h.runs != 1 || got.Attempts[0].Address == "" {
		t.Fatal(got, h.runs)
	}
}
func TestCoreRestartHoldsAgentLost(t *testing.T) {
	w, h, k := fixture(t)
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
	tick(t, w, at)
	r := active(t, w)
	h.core = "core-two"
	h.terms = map[string]hc.Terminal{}
	tick(t, w, at.Add(time.Second))
	got, _ := w.Store.Load(r.Key)
	if got.Phase != "held" || got.Hold != "agent_lost" || h.runs != 1 || h.kills != 0 {
		t.Fatal(got, h.runs, h.kills)
	}
}
func TestIssueScheduledRequiresReady(t *testing.T) {
	w, h, k := fixture(t)
	uid := "01J00000000000000000000020"
	k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "issue-scheduled", IssueUID: &uid}
	k.jobs[0].Definition.Issue = &kata.IssuePolicy{Kind: "existing", UID: &uid}
	k.date = &kata.DateValue{Value: "2026-11-02T08:00:00Z", Instant: at.Add(-time.Hour)}
	tick(t, w, at)
	if h.runs != 0 {
		t.Fatal(h.runs)
	}
	k.date = nil
	k.ready[uid] = true
	tick(t, w, at.Add(time.Second))
	if h.runs != 0 {
		t.Fatal("removed date fired")
	}
	k.date = &kata.DateValue{Value: "2026-11-02T08:01:00Z", Instant: at.Add(-59 * time.Minute)}
	tick(t, w, at.Add(2*time.Second))
	if h.runs != 1 || k.creates != 0 {
		t.Fatal(h.runs, k.creates)
	}
}

func TestExpiredIntentNotRelaunched(t *testing.T) {
	w, h, k := fixture(t)
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
	tick(t, w, at)
	r := active(t, w)
	_ = w.Store.Update(r.Key, func(r *journal.Run) error { r.Attempts[0].Address = ""; r.Attempts[0].Status = "launching"; return nil })
	h.terms = make(map[string]hc.Terminal)
	tick(t, w, r.ExpiresAt.Add(time.Second))
	got, _ := w.Store.Load(r.Key)
	if h.runs != 1 || got.Hold != "timeout" {
		t.Fatalf("expired intent relaunched: runs %d hold %s", h.runs, got.Hold)
	}
}
func TestRequestAcknowledgedWhenDefinitionUnavailable(t *testing.T) {
	w, _, k := fixture(t)
	tick(t, w, at)
	p, reasons := defs.Freeze(k.jobs[0], nil, w.Config, "example-project", exec.LookPath)
	if len(reasons) != 0 {
		t.Fatal(reasons)
	}
	req := journal.Request{ID: "redelivery", Kind: "run", JobUID: k.jobs[0].UID}
	request(t, w, req)
	key := journal.ManualKey(w.Config.Executor, "example-project", req.ID)
	if err := w.prepare(k, w.Config.Projects[0], p, key, "manual:redelivery", req.ID, at); err != nil {
		t.Fatal(err)
	}
	k.err = &kata.Error{Code: 7}
	_ = w.Tick(context.Background(), at.Add(time.Second))
	pending, err := (journal.Requests{Dir: w.Store.Layout.Requests()}).Pending()
	if err != nil || len(pending) != 0 {
		t.Fatalf("journal exists but request still pending: %v %v", pending, err)
	}
}

func TestAdmissionRefusalObservedSameTick(t *testing.T) {
	w, h, k := fixture(t)
	k.createErr = &kata.Error{Code: 3}
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
	_ = w.Tick(context.Background(), at)
	if h.runs != 0 || len(k.bodies) != 1 {
		t.Fatalf("refusal not observed: launches %d observations %d", h.runs, len(k.bodies))
	}
}
func TestManualInputPreserved(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		input := hegel.Draw(ht, hegel.Text())
		w, _, k := fixture(t)
		k.jobs[0].Definition.Options = map[string]any{"input": "default context"}
		request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID, Input: input, InputSet: true})
		tick(t, w, at)
		r := active(t, w)
		if r.Plan.Input != input {
			ht.Fatalf("manual input %q became %q", input, r.Plan.Input)
		}
	})
}

func TestEmptyManualInputOverridesConfigured(t *testing.T) {
	w, _, k := fixture(t)
	k.jobs[0].Definition.Options = map[string]any{"input": "default context"}
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID, InputSet: true})
	tick(t, w, at)
	if got := active(t, w).Plan.Input; got != "" {
		t.Fatalf("explicit empty input became %q", got)
	}
}
