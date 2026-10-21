package main

import (
	"context"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func kataPin(t *testing.T, root string, cfg config.Config) kata.Installation {
	t.Helper()
	pin := kata.Installation{Version: 1, Name: cfg.Kata.Project, CreatedAt: time.Now().UTC(), ProjectUID: "project-b", InstanceUID: "instance", Actor: cfg.Kata.Actor, Executor: cfg.Executor, Route: kata.Route{Bin: cfg.Kata.Bin, Daemon: cfg.Kata.Daemon, Env: cfg.Kata.Env, Endpoint: "local", DiagnoseState: "ready"}}
	if err := state.WriteJSON(state.OSFS{}, (state.Layout{Root: root}).StateFile(), pin); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(state.OSFS{}, filepath.Join(root, "route.json"), map[string]any{"project": pin.Name, "project_uid": pin.ProjectUID, "instance_uid": pin.InstanceUID, "actor": pin.Actor, "allowed": true, "checked_at": time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	return pin
}

func TestKataSelectsCurrentRecordedLane(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	cfg := config.Config{Executor: "worker", Kata: config.Kata{Bin: "/bin/kata", Project: "example-project", Actor: "worker", Env: map[string]string{"KATA_HOME": filepath.Join(root, "kata-home")}}}
	layout := state.Layout{Root: root}
	kataPin(t, layout.Project("project-b").Root, cfg)
	old := layout.Project("historical")
	pin := kataPin(t, old.Root, cfg)
	pin.ProjectUID = "historical"
	if err := state.WriteJSON(state.OSFS{}, old.StateFile(), pin); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(state.OSFS{}, filepath.Join(old.Root, "route.json"), kata.RouteStatus{Project: pin.Name, ProjectUID: pin.ProjectUID, InstanceUID: pin.InstanceUID, Actor: pin.Actor, CheckedAt: time.Now().UTC(), Allowed: false, Error: "waiting for project"}); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(state.OSFS{}, layout.StateFile(), map[string]int{"version": 1}); err != nil {
		t.Fatal(err)
	}
	cli := kata.New(cfg.Kata, root, func(context.Context, []string, []string, string, []byte) ([]byte, []byte, int, error) {
		t.Error("unexpected Kata subprocess")
		return nil, nil, 1, fmt.Errorf("unexpected Kata subprocess")
	}, nil)

	svc := tuiService{CLI: cli, cfg: cfg, ctx: t.Context()}
	if cmd, err := svc.KataCommand(""); err != nil || cmd == nil {
		t.Fatalf("current lane not selected: %v", err)
	}
}

func TestKataRecordedRouteFence(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"healthy", nil},
		{"missing time", func(r map[string]any) { delete(r, "checked_at") }},
		{"stale", func(r map[string]any) { r["checked_at"] = time.Now().Add(-61 * time.Second) }},
		{"future", func(r map[string]any) { r["checked_at"] = time.Now().Add(time.Minute) }},
		{"refused", func(r map[string]any) { r["allowed"] = false; r["error"] = "access_lost" }},
		{"project", func(r map[string]any) { r["project_uid"] = "other-project" }},
		{"name", func(r map[string]any) { r["project"] = "other-project" }},
		{"instance", func(r map[string]any) { r["instance_uid"] = "other-instance" }},
		{"actor", func(r map[string]any) { r["actor"] = "other-worker" }},
		{"missing", func(r map[string]any) { clear(r) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
			cfg := config.Config{Executor: "worker", Kata: config.Kata{Bin: "/bin/kata", Project: "example-project", Actor: "worker", Env: map[string]string{"KATA_HOME": filepath.Join(root, "kata-home")}}}
			lane := (state.Layout{Root: root}).Project("project-b")
			kataPin(t, lane.Root, cfg)
			if err := state.WriteJSON(state.OSFS{}, (state.Layout{Root: root}).StateFile(), map[string]int{"version": 1}); err != nil {
				t.Fatal(err)
			}
			var recorded map[string]any
			path := filepath.Join(lane.Root, "route.json")
			if err := state.ReadJSON(state.OSFS{}, path, 65536, &recorded); err != nil {
				t.Fatal(err)
			}
			if tc.edit != nil {
				tc.edit(recorded)
				var err error
				if len(recorded) == 0 {
					err = os.Remove(path)
				} else {
					err = state.WriteJSON(state.OSFS{}, path, recorded)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			probes := 0
			cli := kata.New(cfg.Kata, root, func(context.Context, []string, []string, string, []byte) ([]byte, []byte, int, error) {
				probes++
				return nil, nil, 1, fmt.Errorf("unexpected Kata subprocess")
			}, nil)

			svc := tuiService{CLI: cli, cfg: cfg, projectUID: "project-b", ctx: t.Context()}
			cmd, err := svc.KataCommand("")
			if tc.edit == nil {
				if err != nil || cmd == nil {
					t.Fatalf("healthy Kata command: %v", err)
				}
			} else if err == nil || cmd != nil {
				t.Fatalf("unsafe Kata command: %v", err)
			}
			if probes != 0 {
				t.Fatalf("made %d Kata probes", probes)
			}
		})
	}
}

func TestEmbeddedKataCommandUsesPinnedLaneAndDisplay(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	t.Setenv("KATA_TEAM_TOKEN", "lane-credential")
	t.Setenv("KATA_OTHER_TOKEN", "other-credential")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("KATA_COLOR_MODE", "light")
	cfg := config.Config{Executor: "worker", Kata: config.Kata{Bin: "/bin/kata", Project: "example-project", Actor: "worker", Daemon: "team-hub", Env: map[string]string{"KATA_HOME": filepath.Join(root, "kata-home")}, CredentialEnv: "KATA_TEAM_TOKEN", CredentialNames: []string{"KATA_TEAM_TOKEN", "KATA_OTHER_TOKEN"}}}
	kataPin(t, (state.Layout{Root: root}).Project("project-b").Root, cfg)
	if err := state.InitializeLayout(state.OSFS{}, state.Layout{Root: root}); err != nil {
		t.Fatal(err)
	}
	cli := kata.New(cfg.Kata, root, func(context.Context, []string, []string, string, []byte) ([]byte, []byte, int, error) {
		t.Fatal("unexpected Kata probe")
		return nil, nil, 1, nil
	}, nil)
	svc := &tuiService{CLI: cli, cfg: cfg, projectUID: "project-b", ctx: t.Context()}
	cmd, err := svc.KataCommand("abc4")
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != "/bin/kata" || strings.Join(cmd.Args, " ") != "/bin/kata --project example-project --as worker --teammate herdr-kata --daemon team-hub tui abc4" || cmd.Dir != root {
		t.Fatalf("wrong command: %+v", cmd)
	}
	env := map[string]string{}
	for _, pair := range cmd.Env {
		key, value, _ := strings.Cut(pair, "=")
		env[key] = value
	}
	for key, want := range map[string]string{"KATA_HOME": filepath.Join(root, "kata-home"), "KATA_TEAM_TOKEN": "lane-credential", "TERM": "xterm-256color", "COLORTERM": "truecolor", "KATA_COLOR_MODE": "light"} {
		if env[key] != want {
			t.Fatalf("%s=%q, want %q", key, env[key], want)
		}
	}
	if _, ok := env["KATA_OTHER_TOKEN"]; ok {
		t.Fatal("unrelated lane credential reached child")
	}
}

func TestKataRecordedRouteAllowsSlowCheckMargin(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	cfg := config.Config{Executor: "worker", Kata: config.Kata{Bin: "/bin/kata", Project: "example-project", Actor: "worker"}}
	l := state.Layout{Root: root}
	pin := kataPin(t, l.Project("project-b").Root, cfg)
	if err := state.WriteJSON(state.OSFS{}, l.StateFile(), map[string]int{"version": 1}); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(state.OSFS{}, l.Project("project-b").RouteFile(), kata.RouteStatus{Project: pin.Name, ProjectUID: pin.ProjectUID, InstanceUID: pin.InstanceUID, Actor: pin.Actor, Allowed: true, CheckedAt: time.Now().Add(-45 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := kata.RecordedRoute(cfg, pin.ProjectUID); err != nil {
		t.Fatal("slow route check blocked Kata tab", err)
	}
}
