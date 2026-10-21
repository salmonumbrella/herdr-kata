package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// P2/M2: an editor opened without a selected row still uses the pinned UID.
func TestTUIServiceSavesJobRunParentInPinnedProject(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
	cfg := config.Config{Executor: "board", Kata: config.Kata{Bin: "/fake", Project: "example-project", Actor: "worker"}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}
	pinLayout := l.Project("project")
	if err := state.InitializeLayout(state.OSFS{}, l); err != nil {
		t.Fatal(err)
	}
	pin := kata.Installation{Version: 1, Name: "example-project", CreatedAt: time.Now(), Executor: cfg.Executor, InstanceUID: "instance", ProjectUID: "project", Actor: "worker", Route: kata.Route{Bin: "/fake", Env: map[string]string{}, Endpoint: "local", DiagnoseState: "ready"}}
	if e := state.WriteJSON(state.OSFS{}, pinLayout.StateFile(), pin); e != nil {
		t.Fatal(e)
	}
	parent := "01K00000000000000000000001"
	writes := 0
	var saved struct {
		Definition struct {
			Options struct {
				RunParent string `json:"run_parent"`
			} `json:"options"`
		} `json:"definition"`
	}
	k := kata.New(cfg.Kata, l.Root, func(_ context.Context, args, _ []string, _ string, in []byte) ([]byte, []byte, int, error) {
		command := strings.Join(args, " ")
		var out string
		switch {
		case strings.HasSuffix(command, " health"):
			out = `{"version":"test","schema_version":33}`
		case strings.Contains(command, "cron capabilities"):
			out = `{"project_uid":"project","event_features":["cron_v1"]}`
		case strings.Contains(command, "federation identity"):
			out = `{"instance_uid":"instance","auth":{"actor":"worker"}}`
		case strings.Contains(command, "daemon diagnose"):
			out = `{"state":"ready","endpoint":"local","project_uid":"project"}`
		case strings.Contains(command, "show -- "+parent):
			out = `{"issue":{"uid":"` + parent + `","project_uid":"project","status":"open"}}`
		case strings.Contains(command, "cron job create"):
			writes++
			if e := json.Unmarshal(in, &saved); e != nil {
				t.Fatal(e)
			}
			out = `{"job":{"uid":"saved"}}`
		default:
			t.Fatalf("unexpected Kata call: %s", command)
		}
		return []byte(out), nil, 0, nil
	}, nil)
	svc := &tuiService{CLI: k, cfg: cfg, ctx: t.Context(), projectUID: "project"}
	job := kata.Job{UID: "01K00000000000000000000002", Name: "Run", Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Check"},"issue":{"kind":"per-run","title":"Run child"},"options":{"run_parent":"` + parent + `"},"overlap":"forbid","catchup":"latest"}`)}
	if _, err := svc.SaveJob(t.Context(), job, ""); err != nil {
		t.Fatal(err)
	}
	if writes != 1 || saved.Definition.Options.RunParent != parent {
		t.Fatal("valid same-project parent was not saved", writes, saved)
	}
}
