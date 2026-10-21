package kata

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestPinMismatchDetails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Installation)
		want   string
	}{
		{"executor", func(s *Installation) { s.Executor = "old-worker" }, `executor (pinned="old-worker", current="worker")`},
		{"actor", func(s *Installation) { s.Actor = "old-actor" }, `actor (pinned="old-actor", current="worker")`},
		{"instance", func(s *Installation) { s.InstanceUID = "old-instance" }, `instance UID (pinned="old-instance", current="instance")`},
		{"project", func(s *Installation) { s.ProjectUID = "old-project" }, `project UID (pinned="old-project", current="project")`},
		{"binary", func(s *Installation) { s.Route.Bin = "/opt/old/kata" }, `kata.bin (pinned="/opt/old/kata", current="/opt/bin/kata")`},
		{"daemon", func(s *Installation) { s.Route.Daemon = "old-hub" }, `daemon (pinned="old-hub", current="example-hub")`},
		{"environment changed", func(s *Installation) { s.Route.Env["KATA_HOME"] = "PINNED_PRIVATE_VALUE" }, `kata.env.KATA_HOME (values withheld)`},
		{"environment added", func(s *Installation) { delete(s.Route.Env, "KATA_HOME") }, `kata.env.KATA_HOME (values withheld)`},
		{"environment removed", func(s *Installation) { s.Route.Env["KATA_REMOVED"] = "PINNED_PRIVATE_VALUE" }, `kata.env.KATA_REMOVED (values withheld)`},
		{"environment empty value removed", func(s *Installation) { s.Route.Env["KATA_EMPTY"] = "" }, `kata.env.KATA_EMPTY (values withheld)`},
		{"endpoint", func(s *Installation) { s.Route.Endpoint = "https://hub.example/PINNED_PRIVATE_VALUE" }, `daemon endpoint (values withheld)`},
		{"diagnosis", func(s *Installation) { s.Route.DiagnoseState = "project_unverifiable" }, `daemon diagnosis (pinned="project_unverifiable", current="ready")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route := Route{Bin: "/opt/bin/kata", Daemon: "example-hub", Env: map[string]string{"KATA_HOME": "CURRENT_PRIVATE_VALUE"}, Endpoint: "https://hub.example/CURRENT_PRIVATE_VALUE", DiagnoseState: "ready"}
			i := Identity{InstanceUID: "instance", ProjectUID: "project", Actor: "worker"}
			s := Installation{Version: 1, CreatedAt: time.Now(), Executor: "worker", Actor: "worker", InstanceUID: "instance", ProjectUID: "project", Route: route}
			s.Route.Env = map[string]string{"KATA_HOME": "CURRENT_PRIVATE_VALUE"}
			if err := ComparePin(s, i, route, "worker"); err != nil {
				t.Fatal("matching pin refused", err)
			}
			tc.change(&s)
			err := ComparePin(s, i, route, "worker")
			if err == nil {
				t.Fatal("changed pin permitted")
			}
			for _, want := range []string{tc.want, "restore the differing fields to their pinned settings", "herdr-kata doctor --json", "resolve existing work against its original target", "separate state directory and unique executor", "docs/operations/runtime.md#4-resolve-a-route-mismatch-deliberately"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("missing %q in %s", want, err)
				}
			}
			if strings.Contains(err.Error(), "PRIVATE_VALUE") {
				t.Fatal("diagnostic exposed an environment value")
			}
		})
	}
}

func TestPinMismatchEnvironmentOrderAndPresence(t *testing.T) {
	i := Identity{InstanceUID: "instance", ProjectUID: "project", Actor: "worker"}
	s := Installation{Version: 1, CreatedAt: time.Now(), Executor: "worker", Actor: "worker", InstanceUID: "instance", ProjectUID: "project"}
	r := Route{Env: map[string]string{}}
	if err := ComparePin(s, i, r, "worker"); err == nil || !strings.Contains(err.Error(), "kata.env (pinned present=false, current present=true)") {
		t.Fatal("nil/empty environment fence changed or unexplained", err)
	}
	s.Route.Env = map[string]string{"KATA_Z": "private-z", "KATA_A": "private-a"}
	err := ComparePin(s, i, r, "worker")
	if err == nil || !strings.Contains(err.Error(), "kata.env.KATA_A (values withheld); kata.env.KATA_Z (values withheld)") {
		t.Fatal("changed environment keys missing or unordered", err)
	}
}

func TestPinMismatchPreservesPinAndRecoversAfterRestore(t *testing.T) {
	f, cfg, l := fixture(t)
	g := NewRouteGuard(cfg, f, state.OSFS{}, l)
	if err := g.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(l.StateFile())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Executor = "changed-worker"
	changed := NewRouteGuard(cfg, f, state.OSFS{}, l)
	if err := changed.Check(t.Context()); err == nil || changed.Allowed() || !strings.Contains(err.Error(), `executor (pinned="test", current="changed-worker")`) {
		t.Fatal("mismatch did not fence execution with field detail", err)
	}
	var status map[string]any
	if err := state.ReadJSON(state.OSFS{}, filepath.Join(l.Root, "route.json"), 64<<10, &status); err != nil {
		t.Fatal(err)
	}
	detail, _ := status["error"].(string)
	if status["allowed"] != false || !strings.Contains(detail, "executor (pinned=") {
		t.Fatal("route status lost mismatch detail", status, err)
	}
	after, err := os.ReadFile(l.StateFile())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("mismatch rewrote installation pin", err)
	}
	if err := g.Check(t.Context()); err != nil || !g.Allowed() {
		t.Fatal("restored configuration stayed fenced", err)
	}
}
