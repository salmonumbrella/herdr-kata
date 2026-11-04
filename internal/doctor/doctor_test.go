package doctor

import (
	"bytes"
	"context"
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
	notify bool
	jobs   []kata.CronJob
}

func (k fakeKata) Jobs() ([]kata.CronJob, error) {
	if k.jobs != nil {
		return k.jobs, nil
	}
	if !k.notify {
		return nil, nil
	}
	return []kata.CronJob{{UID: "01J00000000000000000000010", Name: "example-job", Definition: kata.JobDefinition{Version: 1, Kind: "job", Enabled: true, Action: kata.Action{Kind: "notify"}, Issue: &kata.IssuePolicy{Kind: "existing", UID: new("01J00000000000000000000020")}, CheckoutKey: new("example-project")}}}, nil
}
func (k fakeKata) Workflows() ([]kata.CronWorkflow, error) { return nil, nil }
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
