package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/clock"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/attempt"
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
	jobs          []kata.CronJob
	workflows     []kata.CronWorkflow
	ready         map[string]bool
	date          *kata.DateValue
	err           error
	failures      map[string]error
	createErr     error
	creates       int
	bodies        [][]byte
	notifications []notification
}

type notification struct{ ref, to, message string }

func (k *fakeKata) Capabilities() (kata.Caps, error) { return kata.Caps{}, nil }

func (k *fakeKata) Jobs() (kata.JobList, error) {
	return kata.JobList{Jobs: k.jobs}, errors.Join(k.err, k.failures["jobs"])
}
func (k *fakeKata) Workflows() ([]kata.CronWorkflow, error) {
	return k.workflows, errors.Join(k.err, k.failures["workflows"])
}
func (k *fakeKata) Ready() (map[string]bool, error) {
	return k.ready, errors.Join(k.err, k.failures["ready"])
}
func (k *fakeKata) PlanningDates(string) (*kata.DateValue, error) { return k.date, k.err }
func (k *fakeKata) IssueTitle(string) (string, error)             { return "Existing fixture issue", nil }
func (k *fakeKata) CreateIssue(string, string, string, string) (string, string, error) {
	k.creates++
	if k.createErr != nil {
		return "", "", k.createErr
	}
	return "abcd", "01J00000000000000000000020", k.err
}
func (k *fakeKata) SetDeadline(string, time.Time) error { return k.err }
func (k *fakeKata) CreateAlertCarrier(title, body, key string) (string, error) {
	ref, _, err := k.CreateIssue(title, body, key, "")
	return ref, err
}
func (k *fakeKata) Notify(ref, to, message string) error {
	k.notifications = append(k.notifications, notification{ref, to, message})
	return k.err
}
func (k *fakeKata) Observe(_ string, b []byte, rev int) (int, bool, error) {
	k.bodies = append(k.bodies, slices.Clone(b))
	return rev + 1, false, k.err
}

type fakeHC struct {
	specs []hc.RunSpec
	hc.HC
	core                    string
	terms                   map[string]hc.Terminal
	runs, kills, interrupts int
	sends                   int
}

func (h *fakeHC) CoreID(context.Context) (string, error) { return h.core, nil }
func (h *fakeHC) Run(_ context.Context, s hc.RunSpec) (hc.Launched, error) {
	h.runs++
	h.specs = append(h.specs, s)
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
		var out []string
		for id, v := range h.terms {
			if labels["herdr-kata.agent"] != "" && v.Labels["herdr-kata.agent"] == labels["herdr-kata.agent"] {
				out = append(out, id)
			}
		}
		return out, nil
	}
	var out []string
	for id, v := range h.terms {
		if v.Labels["kata.nonce"] == labels["kata.nonce"] {
			out = append(out, id)
		}
	}
	return out, nil
}
func (h *fakeHC) Send(_ context.Context, id, text, status string) (uint64, error) {
	h.sends++
	if id != "t1" || !strings.HasPrefix(text, "Read ") {
		return 0, errors.New("wrong wake")
	}
	return 4, nil
}
func (h *fakeHC) Read(_ context.Context, id string, lines int) (string, bool, error) {
	if _, ok := h.terms[id]; !ok || lines != 200 {
		return "", false, fmt.Errorf("wrong screen request %s %d", id, lines)
	}
	return "Review hooks before starting", true, nil
}
func (h *fakeHC) Interrupt(context.Context, string) error { h.interrupts++; return nil }
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
	if err := w.Tick(clock.WithNow(context.Background(), now), now); err != nil {
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

func TestStepEnvironmentNamesProject(t *testing.T) {
	w, h, k := fixture(t)
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
	tick(t, w, at)
	r := active(t, w)
	if h.runs != 1 {
		t.Fatal(h.runs)
	}
	term := h.terms["t1"]
	if !slices.Contains(term.Argv, "KATA_PROJECT="+r.Project) || !slices.Contains(term.Argv, "KATA_CRON_RUN="+r.RunUID) {
		t.Fatalf("argv %q", term.Argv)
	}
}
func TestAgentSummaryReachesKataAfterCleanup(t *testing.T) {
	for _, status := range []string{"succeeded", "failed", "needs-human"} {
		t.Run(status, func(t *testing.T) {
			w, h, k := fixture(t)
			request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
			tick(t, w, at)
			r := active(t, w)
			a := r.Attempts[0]
			text := "Scratch dependency inspection completed"
			path := filepath.Join(w.Store.Layout.Attempt(r.Key, a.Step, a.N), "result.json")
			if err := prompt.WriteResult(path, prompt.Result{RunUID: r.RunUID, Step: a.Step, Attempt: a.N, Status: status, Summary: text}); err != nil {
				t.Fatal(err)
			}
			term := h.terms[a.Address]
			term.State = "finished"
			term.Reason = "exited"
			h.terms[a.Address] = term
			// Settlement is durable before the worker restarts and publishes.
			if _, err := attempt.Settle(context.Background(), h, w.Store, r.Key, 0, at.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := prompt.WriteResult(path, prompt.Result{RunUID: r.RunUID, Step: a.Step, Attempt: a.N, Status: status, Summary: "Changed after settlement"}); err != nil {
				t.Fatal(err)
			}
			w = New(w.Config, w.Store, h, w.Clients, w.Bin)
			for n := 1; n <= 3; n++ {
				tick(t, w, at.Add(time.Duration(n)*time.Second))
			}
			var obs kata.ObserveCronRunBody
			if err := json.Unmarshal(k.bodies[len(k.bodies)-1], &obs); err != nil {
				t.Fatal(err)
			}
			if obs.Summary.Message == nil || !strings.Contains(*obs.Summary.Message, text) {
				t.Fatalf("agent summary discarded: %+v", obs.Summary)
			}
		})
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
	if got.Phase != "reported" || got.Outcome != "succeeded" || h.runs != 1 || len(k.notifications) != 0 {
		t.Fatal(got, h.runs)
	}
}

func TestFailedCommandAlertsActor(t *testing.T) {
	w, h, k := fixture(t)
	workflow(k)
	k.workflows[0].Definition.Steps = []kata.WorkflowStep{{Key: "main", Kind: "command", Command: new("exit 1")}}
	request(t, w, journal.Request{ID: "failed-command", Kind: "run", JobUID: k.jobs[0].UID})
	tick(t, w, at)
	r := active(t, w)
	a := r.Attempts[0]
	receipt := journal.Receipt{ExitCode: 1, StartedAt: a.LaunchedAt, EndedAt: a.LaunchedAt.Add(time.Second)}
	if err := state.WriteJSON(filepath.Join(w.Store.Layout.Attempt(r.Key, a.Step, a.N), "receipt.json"), receipt); err != nil {
		t.Fatal(err)
	}
	term := h.terms[a.Address]
	term.State, term.Reason, term.ExitCode = "finished", "exited", new(1)
	h.terms[a.Address] = term
	tick(t, w, at.Add(2*time.Second))
	// A restarted worker must leave the reported run and its attention alone.
	w = New(w.Config, journal.New(w.Store.Layout), h, w.Clients, w.Bin)
	tick(t, w, at.Add(3*time.Second))
	tick(t, w, at.Add(4*time.Second))
	got, err := w.Store.Load(r.Key)
	if err != nil || got.Phase != "reported" || got.Outcome != "failed" || h.runs != 1 || len(h.terms) != 0 || len(k.bodies) != 2 {
		t.Fatalf("failed command settlement: %+v %v runs=%d terminals=%d observations=%d", got, err, h.runs, len(h.terms), len(k.bodies))
	}
	want := notification{"abcd", "worker", "run failed: reason: failed; " + r.RunUID}
	if !slices.Equal(k.notifications, []notification{want}) {
		t.Fatalf("failure attention: %+v", k.notifications)
	}
	var obs kata.ObserveCronRunBody
	if err := json.Unmarshal(k.bodies[1], &obs); err != nil || obs.Status != "failed" || obs.Summary.Message == nil || *obs.Summary.Message != "reason: failed" {
		t.Fatalf("final observation: %+v %v", obs, err)
	}
}
func TestCommandsDisabledUnsupported(t *testing.T) {
	w, h, k := fixture(t)
	workflow(k)
	w.Config.Projects[0].Commands = false
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
	_ = w.Tick(clock.WithNow(context.Background(), at), at)
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
	for n := range 2 {
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
	err := w.Tick(clock.WithNow(context.Background(), at), at)
	if err == nil || h.runs != 1 {
		t.Fatal(err, h.runs)
	}
}

func TestBadFilesDoNotBlockAnotherProject(t *testing.T) {
	w, h, k := fixture(t)
	tick(t, w, at)
	bad := journal.ManualKey("executor", "example-project", "broken")
	if err := state.WriteFile(filepath.Join(w.Store.Layout.Run(bad), "run.json"), []byte("{")); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteFile(filepath.Join(w.Store.Layout.Requests(), "broken.json"), []byte("{")); err != nil {
		t.Fatal(err)
	}
	otherJob := k.jobs[0]
	otherJob.UID = "01J00000000000000000000030"
	other := &fakeKata{jobs: []kata.CronJob{otherJob}, ready: map[string]bool{}}
	w.Config.Projects = append(w.Config.Projects, config.Project{Name: "other-project", Actor: "worker", MaxActiveRuns: 1})
	w.Clients["other-project"] = other
	request(t, w, journal.Request{ID: "healthy", Kind: "run", Project: "other-project", JobUID: otherJob.UID})
	tick(t, w, at.Add(time.Second))
	if h.runs != 1 || active(t, w).Project != "other-project" {
		t.Fatal("bad files blocked another project")
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

func TestRunRequestRejectedOnFirstTick(t *testing.T) {
	for _, kind := range []string{"unknown", "disabled", "deleted", "unsupported", "not-ready"} {
		t.Run(kind, func(t *testing.T) {
			w, _, k := fixture(t)
			uid := k.jobs[0].UID
			switch kind {
			case "unknown":
				uid = "01J00000000000000000000099"
			case "disabled":
				k.jobs[0].Definition.Enabled = false
			case "deleted":
				k.jobs[0].DeletedAt = new(at)
			case "unsupported":
				k.jobs[0].Definition.Options = map[string]any{"adapter": "other"}
			case "not-ready":
				k.jobs[0].Definition.Issue = &kata.IssuePolicy{Kind: "existing", UID: new("01J00000000000000000000020")}
			}
			request(t, w, journal.Request{ID: "rejected", Kind: "run", JobUID: uid})
			_ = w.Tick(clock.WithNow(context.Background(), at), at)
			pending, err := (journal.Requests{Dir: w.Store.Layout.Requests()}).Pending()
			if err != nil || len(pending) != 0 {
				t.Fatalf("request still pending: %v %v", pending, err)
			}
			b, err := os.ReadFile(filepath.Join(w.Store.Layout.Requests(), "rejected.rejected"))
			if err != nil || !bytes.Contains(b, []byte("Reason")) {
				t.Fatalf("missing rejection reason: %s %v", b, err)
			}
		})
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
	if err := w.prepare(context.Background(), k, w.Config.Projects[0], p, key, "manual:redelivery", req.ID, at); err != nil {
		t.Fatal(err)
	}
	k.err = &kata.Error{Code: 7}
	_ = w.Tick(clock.WithNow(context.Background(), at.Add(time.Second)), at.Add(time.Second))
	pending, err := (journal.Requests{Dir: w.Store.Layout.Requests()}).Pending()
	if err != nil || len(pending) != 0 {
		t.Fatalf("journal exists but request still pending: %v %v", pending, err)
	}
}

func TestAdmissionRefusalObservedSameTick(t *testing.T) {
	w, h, k := fixture(t)
	k.createErr = &kata.Error{Code: 3}
	request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
	_ = w.Tick(clock.WithNow(context.Background(), at), at)
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

func TestWakeTargetCapacityAcrossProjects(t *testing.T) {
	w, _, _ := fixture(t)
	r := journal.Run{Version: 1, Key: journal.ManualKey("executor", "other-project", "request"), RunUID: "01J00000000000000000000001", Project: "other-project", Phase: "held", Plan: defs.Plan{Agent: "worker"}}
	if err := w.Store.Create(r); err != nil {
		t.Fatal(err)
	}
	can, _, err := w.capacityState(w.Config.Projects[0], "another-job", "forbid", "worker")
	if err != nil || can {
		t.Fatalf("capacity: %v %v", can, err)
	}
	if err := w.Store.Update(r.Key, func(saved *journal.Run) error {
		saved.Project = "example-project"
		saved.JobUID = "another-job"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	can, occupied, err := w.capacityState(w.Config.Projects[0], "another-job", "forbid", "worker")
	if err != nil || can || !occupied {
		t.Fatalf("duplicate gate: %v %v %v", can, occupied, err)
	}
}

func TestWakePausedOrUnreadableDoesNotSend(t *testing.T) {
	for _, phase := range []string{"prepared", "running"} {
		for _, gate := range []string{"paused", "unreadable"} {
			t.Run(phase+"/"+gate, func(t *testing.T) {
				w, h, k := fleetFixture(t)
				h.terms["t1"] = hc.Terminal{Address: "t1", State: "live", Status: "idle", Reported: true, CreatedMs: 1, PID: 1, ForegroundPID: 1, Labels: map[string]string{"herdr-kata.agent": "worker"}}
				r := journal.Run{Version: 1, Key: journal.ManualKey("executor", "example-project", "wake"), RunUID: "01J00000000000000000000001", Project: "example-project", Phase: phase, ExpiresAt: at.Add(time.Hour), Plan: defs.Plan{Agent: "worker", Steps: []defs.Step{{Key: "main", Kind: "prompt"}}}, Attempts: []journal.Attempt{{Step: "main", N: 1, Status: "launching"}}}
				if err := w.Store.Create(r); err != nil {
					t.Fatal(err)
				}
				if gate == "paused" {
					k.pause = []byte(`{"reason":"paused"}`)
				} else {
					k.failures = map[string]error{"jobs": errors.New("unavailable")}
				}
				_ = w.Tick(clock.WithNow(context.Background(), at), at)
				saved, err := w.Store.Load(r.Key)
				if err != nil || h.sends != 0 || saved.Phase != phase || saved.Attempts[0].Address != "" {
					t.Fatalf("run %+v sends %d err %v", saved, h.sends, err)
				}
			})
		}
	}
}
