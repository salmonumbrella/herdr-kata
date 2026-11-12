package doctor

import (
	"bytes"
	"context"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/control"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/report"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type fakeHC struct {
	hc.HC
	root string
	argv [][]string
}

func (h *fakeHC) Info(context.Context) (hc.ServerInfo, error) {
	return hc.ServerInfo{CoreID: "fixture-core", Version: "0.1.16", UptimeMs: 2000}, nil
}
func (h *fakeHC) Get(context.Context, string) (hc.Terminal, error) {
	return hc.Terminal{State: "live", Labels: map[string]string{"kata.worker": control.Name(h.root)}}, nil
}
func (h *fakeHC) Exec(_ context.Context, argv []string) (string, error) {
	h.argv = append(h.argv, slices.Clone(argv))
	if argv[len(argv)-1] == "whoami" {
		return `{"kata_api_version":1,"actor":"worker","source":"flag"}`, nil
	}
	return `{"kata_api_version":1,"event_features":["cron_v1"],"project_uid":"01J00000000000000000000020"}`, nil
}

type fakeKata struct {
	runs       map[string][]kata.CronRun
	runErr     error
	readyCalls *int
	requested  *[]string
	kata.Kata
	notify    bool
	jobs      []kata.CronJob
	workflows []kata.CronWorkflow
}

func (k fakeKata) Jobs() (kata.JobList, error) {
	if k.jobs != nil {
		return kata.JobList{Jobs: k.jobs}, nil
	}
	if !k.notify {
		return kata.JobList{}, nil
	}
	return kata.JobList{Jobs: []kata.CronJob{{UID: "01J00000000000000000000010", Name: "example-job", Definition: kata.JobDefinition{Version: 1, Kind: "job", Enabled: true, Action: kata.Action{Kind: "notify"}, Issue: &kata.IssuePolicy{Kind: "existing", UID: new("01J00000000000000000000020")}, CheckoutKey: new("example-project")}}}}, nil
}
func (k fakeKata) Workflows() ([]kata.CronWorkflow, error) { return k.workflows, nil }
func fixture(t *testing.T) (config.Config, state.Layout, *fakeHC) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("XDG_RUNTIME_DIR", root)
	c := config.Config{Executor: "executor", HCSession: "fixture", Kata: config.KataCfg{Bin: "/bin/sh", Daemon: "fixture", Env: map[string]string{"KATA_HOME": "/srv/kata-home"}}, Projects: []config.Project{{Name: "example-project", Actor: "worker"}}, Checkouts: map[string]string{"example-project": root}, Profiles: map[string]config.Profile{"default": {Argv: []string{"/bin/sh"}}}, Limits: config.Limits{MaxActiveRuns: 4, DefaultTimeout: 2 * time.Hour, MaxTimeout: 24 * time.Hour}}
	return c, state.Layout{Root: root}, &fakeHC{root: root}
}
func TestDoctorListsUnsupportedWithReasons(t *testing.T) {
	c, l, h := fixture(t)
	var out bytes.Buffer
	if err := Check(context.Background(), c, l, h, map[string]kata.Kata{"example-project": fakeKata{jobs: []kata.CronJob{{UID: "job", Definition: kata.JobDefinition{Enabled: true, Trigger: kata.Trigger{Kind: "manual"}, Action: kata.Action{Kind: "execute"}}}}}}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "checkout not configured") {
		t.Fatal(out.String())
	}
}
func TestDoctorRunsKataInCoreEnv(t *testing.T) {
	c, l, h := fixture(t)
	var out bytes.Buffer
	if err := Check(context.Background(), c, l, h, map[string]kata.Kata{"example-project": fakeKata{}}, &out); err != nil {
		t.Fatal(err)
	}
	if len(h.argv) != 2 {
		t.Fatal(h.argv)
	}
	for _, args := range h.argv {
		for _, want := range []string{"env", "KATA_HOME=/srv/kata-home", "--project", "example-project", "--daemon", "fixture", "--as", "worker"} {
			if !slices.Contains(args, want) {
				t.Fatalf("core exec omitted %s: %q", want, args)
			}
		}
	}
}

func TestDoctorFiltersUnownedAndShowsForeignPin(t *testing.T) {
	c, l, h := fixture(t)
	jobs := []kata.CronJob{
		{UID: "foreign", Definition: kata.JobDefinition{Enabled: true, Action: kata.Action{Kind: "execute"}, Options: map[string]any{"executor": "other"}}},
		{UID: "disabled", Definition: kata.JobDefinition{Enabled: false, Action: kata.Action{Kind: "execute"}}},
		{UID: "notify", Definition: kata.JobDefinition{Enabled: true, Action: kata.Action{Kind: "notify"}}},
		{UID: "adapter", Definition: kata.JobDefinition{Enabled: true, Action: kata.Action{Kind: "execute"}, Options: map[string]any{"adapter": "other"}}},
	}
	var out bytes.Buffer
	if err := Check(t.Context(), c, l, h, map[string]kata.Kata{"example-project": fakeKata{jobs: jobs}}, &out); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{"foreign", "disabled", "notify", "adapter"} {
		if strings.Contains(out.String(), "unsupported "+uid+":") {
			t.Fatal(out.String())
		}
	}
	if !strings.Contains(out.String(), "pinned elsewhere foreign: options.executor=other") {
		t.Fatal(out.String())
	}
}

func (k fakeKata) Runs(uid string, limit int) ([]kata.CronRun, error) {
	if limit != 100 {
		panic("wrong page limit")
	}
	if k.requested != nil {
		*k.requested = append(*k.requested, uid)
	}
	return k.runs[uid], k.runErr
}
func (k fakeKata) Ready() (map[string]bool, error) {
	if k.readyCalls != nil {
		*k.readyCalls++
	}
	return nil, nil
}

func TestDoctorForeignPinIndependentOfAdapter(t *testing.T) {
	for _, tt := range []struct {
		name             string
		enabled, deleted bool
		action           string
		want             bool
	}{
		{"other adapter", true, false, "execute", true},
		{"disabled", false, false, "execute", false},
		{"deleted", true, true, "execute", false},
		{"notify", true, false, "notify", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, l, h := fixture(t)
			calls := 0
			requested := []string{}
			j := kata.CronJob{UID: "foreign", Definition: kata.JobDefinition{Enabled: tt.enabled, Action: kata.Action{Kind: tt.action}, Issue: &kata.IssuePolicy{Kind: "existing", UID: new("issue")}, Options: map[string]any{"adapter": "other", "executor": "other-worker"}}}
			if tt.deleted {
				j.DeletedAt = new(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			}
			var out bytes.Buffer
			err := Check(t.Context(), c, l, h, map[string]kata.Kata{"example-project": fakeKata{jobs: []kata.CronJob{j}, readyCalls: &calls, requested: &requested}}, &out)
			if err != nil || strings.Contains(out.String(), "pinned elsewhere foreign: options.executor=other-worker") != tt.want || calls != 0 || len(requested) != 0 || strings.Contains(out.String(), "unsupported foreign:") {
				t.Fatalf("output %s; ready=%d run lists=%v error=%v", out.String(), calls, requested, err)
			}
		})
	}
}

type dropRefusal struct{ kata.Kata }

func (dropRefusal) Observe(string, []byte, int) (int, bool, error) {
	return 0, false, &kata.Error{Code: 3, Message: "invalid historical event"}
}
func TestDoctorDroppedRefusal(t *testing.T) {
	c, l, h := fixture(t)
	o := report.Outbox{Layout: l, Dir: l.Dropped()}
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	p := defs.Plan{JobUID: "job", JobEventUID: "event"}
	cp := sched.Checkpoint{Version: 1, JobUID: "job", EventUID: "event", Through: now}
	if err := o.Drop(p, "example-project", "worker", "executor", "catchup_missed", "detail", "oldest", cp, now); err != nil {
		t.Fatal(err)
	}
	_ = o.Deliver(map[string]kata.Kata{"example-project": dropRefusal{}}, now)
	var out bytes.Buffer
	if err := Check(t.Context(), c, l, h, map[string]kata.Kata{"example-project": fakeKata{}}, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "dropped reports: pending=0 refused=1") || !strings.Contains(text, "invalid historical event") || strings.Contains(text, "delete ") {
		t.Fatal(text)
	}
}

func TestDoctorFailsQuarantinedEntriesWithoutCheckingTheirProjects(t *testing.T) {
	c, l, h := fixture(t)
	c.Quarantined = []config.Quarantine{{Entry: 2, Name: "broken-project", Reason: "duplicate project name"}}
	var out bytes.Buffer
	err := Check(t.Context(), c, l, h, map[string]kata.Kata{"example-project": fakeKata{}, "broken-project": nil}, &out)
	want := "FAIL config: quarantined project entry 2 (broken-project): duplicate project name\n"
	if err == nil || strings.Count(out.String(), want) != 1 {
		t.Fatalf("doctor error=%v output=%q", err, out.String())
	}
	if len(h.argv) != 2 {
		t.Fatalf("project checks: %q", h.argv)
	}
	for _, argv := range h.argv {
		i := slices.Index(argv, "--project")
		if i < 0 || i+1 >= len(argv) || argv[i+1] != "example-project" {
			t.Fatalf("checked quarantined project: %q", argv)
		}
	}
}

// Compare the observable diagnostics, so wrong precedence or broad flag matching fails.
func TestModelWarnings(t *testing.T) {
	for _, tt := range []struct {
		name  string
		model map[string]any
		argv  []string
		flag  string
		want  bool
	}{
		{name: "absent", flag: "--model", want: true},
		{name: "empty", model: map[string]any{"model": ""}, flag: "--model", want: true},
		{name: "job model", model: map[string]any{"model": "small"}, flag: "--model"},
		{name: "malformed", model: map[string]any{"model": false}, flag: "--model"},
		{name: "null", model: map[string]any{"model": nil}, flag: "--model"},
		{name: "flagless"},
		{name: "fixed separate", flag: "--model", argv: []string{"--model", "small"}},
		{name: "fixed equals", flag: "--model", argv: []string{"--model=small"}},
		{name: "dangling", flag: "--model", argv: []string{"--model"}, want: true},
		{name: "empty equals", flag: "--model", argv: []string{"--model="}, want: true},
		{name: "empty separate", flag: "--model", argv: []string{"--model", ""}, want: true},
		{name: "similar prefix", flag: "--model", argv: []string{"--model-other=small"}, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, l, h := fixture(t)
			c.Profiles["default"] = config.Profile{Argv: append([]string{"/bin/sh"}, tt.argv...), ModelFlag: tt.flag}
			job := kata.CronJob{UID: "job", Definition: kata.JobDefinition{Enabled: true, Trigger: kata.Trigger{Kind: "manual"}, Action: kata.Action{Kind: "execute", Prompt: new("work")}, Options: tt.model}}
			var out bytes.Buffer
			if err := Check(t.Context(), c, l, h, map[string]kata.Kata{"example-project": fakeKata{jobs: []kata.CronJob{job}}}, &out); err != nil {
				t.Fatal(err)
			}
			var got []string
			for line := range strings.SplitSeq(out.String(), "\n") {
				if strings.HasPrefix(line, "warning: job ") {
					got = append(got, line)
				}
			}
			var want []string
			if tt.want {
				want = []string{"warning: job job step main: profile default has no explicit model in job options or profile argv; set options.model"}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("warnings %q want %q; output %s", got, want, out.String())
			}
		})
	}
}

func TestModelWorkflowWarnings(t *testing.T) {
	for _, tt := range []struct {
		name             string
		job, step        map[string]any
		enabled, deleted bool
		workflow         bool
		want             []string
	}{
		{name: "project and step profiles", enabled: true, workflow: true, want: []string{"warning: job job step project: profile project has no explicit model in job options or profile argv; set options.model", "warning: job job step custom: profile custom has no explicit model in job options or profile argv; set options.model"}},
		{name: "job agent", enabled: true, workflow: true, job: map[string]any{"agent": "custom"}, want: []string{"warning: job job step project: profile custom has no explicit model in job options or profile argv; set options.model", "warning: job job step custom: profile custom has no explicit model in job options or profile argv; set options.model"}},
		{name: "step model", enabled: true, workflow: true, step: map[string]any{"model": "small"}},
		{name: "job model", enabled: true, workflow: true, job: map[string]any{"model": "small"}},
		{name: "invalid job overridden", enabled: true, workflow: true, job: map[string]any{"model": false}, step: map[string]any{"model": "small"}},
		{name: "invalid step", enabled: true, workflow: true, step: map[string]any{"model": false}},
		{name: "foreign", enabled: true, workflow: true, job: map[string]any{"executor": "other"}},
		{name: "disabled", workflow: true},
		{name: "deleted", enabled: true, deleted: true, workflow: true},
		{name: "missing workflow", enabled: true},
		{name: "missing profile", enabled: true, workflow: true, job: map[string]any{"agent": "missing"}, step: map[string]any{"agent": "missing"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, l, h := fixture(t)
			c.Projects[0].DefaultAgent = "project"
			for _, name := range []string{"project", "custom"} {
				c.Profiles[name] = config.Profile{Argv: []string{"/bin/sh"}, ModelFlag: "--model"}
			}
			job := kata.CronJob{UID: "job", Definition: kata.JobDefinition{Enabled: tt.enabled, Trigger: kata.Trigger{Kind: "manual"}, Action: kata.Action{Kind: "execute", WorkflowUID: new("wf")}, Options: tt.job}}
			if tt.deleted {
				job.DeletedAt = new(time.Now())
			}
			custom := map[string]any{"agent": "custom"}
			maps.Copy(custom, tt.step)
			wf := kata.CronWorkflow{UID: "wf", Definition: kata.WorkflowDefinition{Options: map[string]any{"model": false}, Steps: []kata.WorkflowStep{
				{Key: "project", Kind: "prompt", Prompt: new("x"), Options: tt.step},
				{Key: "custom", Kind: "prompt", Prompt: new("y"), Options: custom},
				{Key: "command", Kind: "command", Command: new("true"), Options: map[string]any{"model": false}},
			}}}
			k := fakeKata{jobs: []kata.CronJob{job}}
			if tt.workflow {
				k.workflows = []kata.CronWorkflow{wf}
			}
			var out bytes.Buffer
			if err := Check(t.Context(), c, l, h, map[string]kata.Kata{"example-project": k}, &out); err != nil {
				t.Fatal(err)
			}
			var got []string
			for line := range strings.SplitSeq(out.String(), "\n") {
				if strings.HasPrefix(line, "warning: job ") {
					got = append(got, line)
				}
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("warnings %q want %q; output %s", got, tt.want, out.String())
			}
		})
	}
}

type fleetHC struct{ *fakeHC }

func (h fleetHC) Exec(ctx context.Context, argv []string) (string, error) {
	if argv[len(argv)-1] == "capabilities" {
		return `{"event_features":["cron_v1"],"execution":{"mode":"none"},"controls":["project_pause","run_request"],"features":["cron_runtimes_v1"]}`, nil
	}
	return h.fakeHC.Exec(ctx, argv)
}
func TestDoctorFleetCapabilities(t *testing.T) {
	c, l, h := fixture(t)
	reads := 0
	k := fakeKata{readyCalls: &reads, jobs: []kata.CronJob{{UID: "job", Definition: kata.JobDefinition{Enabled: true, Action: kata.Action{Kind: "execute"}, Issue: &kata.IssuePolicy{Kind: "existing", UID: new("issue")}}}}}
	var out bytes.Buffer
	if err := Check(context.Background(), c, l, fleetHC{h}, map[string]kata.Kata{"example-project": k}, &out); err != nil {
		t.Fatal(err)
	}
	if reads != 0 || !strings.Contains(out.String(), "excluded job: replica execution is not enabled") || !strings.Contains(out.String(), "execution=none() pause=true requests=true runtimes=true summary_v2=false") {
		t.Fatal(reads, out.String())
	}
}

type sessionCapsHC struct {
	*fakeHC
	raw string
}

func (h sessionCapsHC) Exec(ctx context.Context, argv []string) (string, error) {
	if argv[len(argv)-1] == "capabilities" {
		return h.raw, nil
	}
	return h.fakeHC.Exec(ctx, argv)
}
func TestDoctorRunSessionsCapability(t *testing.T) {
	for _, tt := range []struct{ name, raw, want string }{
		{"old", `{"event_features":["cron_v1","cron_run_sessions_v1"]}`, "sessions=false"},
		{"both", `{"cli_features":["cron_run_sessions_v1"],"event_features":["cron_v1","cron_run_sessions_v1"]}`, "sessions=true"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, l, h := fixture(t)
			var out bytes.Buffer
			if err := Check(context.Background(), c, l, sessionCapsHC{h, tt.raw}, map[string]kata.Kata{"example-project": fakeKata{}}, &out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "project example-project capabilities:") || !strings.Contains(out.String(), tt.want) {
				t.Fatal(out.String())
			}
		})
	}
}
