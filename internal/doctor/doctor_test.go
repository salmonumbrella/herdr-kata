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
	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
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
	kata.Kata
	notify bool
}

func (k fakeKata) Jobs() ([]kata.CronJob, error) {
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
	c := config.Config{Executor: "executor", HCSession: "fixture", Kata: config.KataCfg{Bin: "/bin/sh", Daemon: "fixture", Env: map[string]string{"KATA_HOME": "/srv/kata-home"}}, Projects: []config.Project{{Name: "example-project", Actor: "worker"}}, Checkouts: map[string]string{"example-project": root}, Profiles: map[string]config.Profile{"default": {Argv: []string{"/bin/sh"}}}, Limits: config.Limits{DefaultTimeout: 2 * time.Hour, MaxTimeout: 24 * time.Hour}}
	return c, state.Layout{Root: root}, &fakeHC{root: root}
}
func TestDoctorListsUnsupportedWithReasons(t *testing.T) {
	c, l, h := fixture(t)
	var out bytes.Buffer
	if err := Check(context.Background(), c, l, h, map[string]kata.Kata{"example-project": fakeKata{notify: true}}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "notify_unsupported") {
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
