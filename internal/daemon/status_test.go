package daemon

import (
	"encoding/json"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
)

func TestStatusOmitsRemovedLaneAndItsCachedWork(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	statusTestLane(t, l)
	if err := state.WriteJSON(state.OSFS{}, l.Project("project").DefsSnapshot(), defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{{UID: "old-job"}}}); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(state.OSFS{}, l.RunFile("run"), runner.Run{Version: 1, UID: "run", Target: runner.Target{Identity: kata.Identity{ProjectUID: "project"}}, Phase: runner.Settled}); err != nil {
		t.Fatal(err)
	}
	s, err := ReadStatus(l, config.Config{Projects: []config.Project{{Name: "other-project", Actor: "worker"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Projects) != 1 || s.Projects[0].Project != "other-project" || len(s.Definitions) != 0 || len(s.Runs) != 0 {
		t.Fatalf("removed lane still visible: %+v", s)
	}
}

func TestStatusKeepsLegacyLaneWithoutSavedName(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	statusTestLane(t, l)
	if err := state.WriteJSON(state.OSFS{}, l.Project("project").StateFile(), kata.Installation{Version: 1, ProjectUID: "project"}); err != nil {
		t.Fatal(err)
	}
	s, err := ReadStatus(l, config.Config{Projects: []config.Project{{Name: "example-project", Actor: "worker"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, lane := range s.Projects {
		if lane.ProjectUID == "project" {
			return
		}
	}
	t.Fatal("legacy lane retired before its name was resolved", s.Projects)
}

func TestStatusDerivesSigningBlockFromJournal(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	if err := state.InitializeLayout(state.OSFS{}, l); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(state.OSFS{}, l.Project("project").StateFile(), kata.Installation{Version: 1, ProjectUID: "project", Name: "example-project"}); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(state.OSFS{}, l.Project("project").RouteFile(), map[string]any{"allowed": true, "project": "example-project"}); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(state.OSFS{}, l.RunFile("run"), map[string]any{"UID": "run", "Target": runner.Target{Identity: kata.Identity{ProjectUID: "project"}}, "Phase": "prepared", "Ops": []map[string]any{{"ID": "create", "signing_refused": true}}}); err != nil {
		t.Fatal(err)
	}
	s, err := ReadStatus(l)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s)
	if !strings.Contains(string(b), `"signing_blocked":true`) || !strings.Contains(s.Route.Error, "--project example-project") {
		t.Fatal("durable refusal absent from route status", string(b))
	}
}

func TestStatusReadsProjectLanes(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	f := state.OSFS{}
	if e := state.InitializeLayout(f, l); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"project-a", "project-b"} {
		pl := l.Project(name)
		if e := state.WriteJSON(f, pl.StateFile(), kata.Installation{Version: 1, ProjectUID: name, Name: name, CreatedAt: time.Now()}); e != nil {
			t.Fatal(e)
		}
		if e := state.WriteJSON(f, pl.DefsSnapshot(), defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{{UID: "job-" + name, Name: "job"}}}); e != nil {
			t.Fatal(e)
		}
		if e := state.WriteJSON(f, filepath.Join(pl.Root, "route.json"), RouteStatus{Project: name, Allowed: true}); e != nil {
			t.Fatal(e)
		}
	}
	if e := state.WriteJSON(f, l.RunFile("run"), runner.Run{Version: 1, UID: "run", Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-b"}}, Phase: runner.Held}); e != nil {
		t.Fatal(e)
	}
	s, e := ReadStatus(l)
	if e != nil || len(s.Projects) != 2 || len(s.Definitions) != 2 || len(s.Runs) != 1 || s.Runs[0]["project"] != "project-b" {
		t.Fatal(s, e)
	}
}

func TestStatusDuringFirstProjectPin(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	f := state.OSFS{}
	if e := state.InitializeLayout(f, l); e != nil {
		t.Fatal(e)
	}
	// A route check or an interrupted first pin can create the lane directory
	// before the atomic state-file rename. It is not a pinned project yet.
	if e := state.WriteJSON(f, filepath.Join(l.Project("project-a").Root, "route.json"), RouteStatus{Allowed: false}); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadStatus(l); e != nil {
		t.Fatal("status raced first pin", e)
	}
}

func statusTestLane(t *testing.T, l state.Layout) state.Layout {
	t.Helper()
	if err := state.InitializeLayout(state.OSFS{}, l); err != nil {
		t.Fatal(err)
	}
	pl := l.Project("project")
	if err := state.WriteJSON(state.OSFS{}, pl.StateFile(), kata.Installation{Version: 1, Name: "example-project", ProjectUID: "project"}); err != nil {
		t.Fatal(err)
	}
	return pl
}

func TestStatusSigningBlockStaysInItsLane(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	f := state.OSFS{}
	if err := state.InitializeLayout(f, l); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{"project-a", "project-b"} {
		if err := state.WriteJSON(f, l.Project(uid).StateFile(), kata.Installation{Version: 1, ProjectUID: uid, Name: uid}); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.WriteJSON(f, l.RunFile("run"), runner.Run{Version: 1, UID: "run", Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-b"}}, Ops: []runner.Op{{SigningRefused: true}}}); err != nil {
		t.Fatal(err)
	}
	s, err := ReadStatus(l)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Projects) != 2 || s.Projects[0].Route != nil || s.Projects[1].Route == nil || !s.Projects[1].Route.SigningBlocked || s.Projects[1].Route.Error != "signing_blocked: root owner: kata federation root-key repin --project project-b" {
		t.Fatalf("signing refusal crossed lane boundary: %+v", s.Projects)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var aggregate map[string]json.RawMessage
	if err := json.Unmarshal(raw, &aggregate); err != nil {
		t.Fatal(err)
	}
	if _, ok := aggregate["route"]; ok {
		t.Fatalf("multi-lane status has an aggregate route: %s", raw)
	}
}

func TestStatusWithoutLanesOmitsAggregateRoute(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	if err := state.InitializeLayout(state.OSFS{}, l); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(state.OSFS{}, l.RunFile("run"), runner.Run{Version: 1, UID: "run", Target: runner.Target{Identity: kata.Identity{ProjectUID: "project"}}, Ops: []runner.Op{{SigningRefused: true}}}); err != nil {
		t.Fatal(err)
	}
	s, err := ReadStatus(l)
	if err != nil {
		t.Fatal(err)
	}
	if s.Route != nil || len(s.Runs) != 1 || s.Runs[0]["waiting_for_project"] != true {
		t.Fatalf("unpinned run route: %+v", s)
	}
}

func TestStatusStartupFailureOverridesSigningBlock(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	statusTestLane(t, l)
	if err := state.WriteJSON(state.OSFS{}, l.RunFile("run"), runner.Run{Version: 1, UID: "run", Target: runner.Target{Identity: kata.Identity{ProjectUID: "project"}}, Ops: []runner.Op{{SigningRefused: true}}}); err != nil {
		t.Fatal(err)
	}
	if err := saveStartupErrors(l, map[string]string{"example-project": "credential unavailable"}); err != nil {
		t.Fatal(err)
	}
	s, err := ReadStatus(l)
	if err != nil {
		t.Fatal(err)
	}
	if s.Route == nil || s.Route.Allowed || s.Route.SigningBlocked || s.Route.Error != "credential unavailable" || len(s.Projects) != 1 || s.Projects[0].Route != s.Route {
		t.Fatalf("startup failure lost precedence: %+v", s)
	}
}

func TestStatusSigningOverlayDecoratesRunsIndependentOfOrder(t *testing.T) {
	for _, signingUID := range []string{"a-run", "z-run"} {
		t.Run(signingUID, func(t *testing.T) {
			l := state.Layout{Root: t.TempDir()}
			pl := statusTestLane(t, l)
			if err := state.WriteJSON(state.OSFS{}, pl.RouteFile(), RouteStatus{Project: "example-project", Error: "waiting for project example-project; run doctor for route details"}); err != nil {
				t.Fatal(err)
			}
			for _, uid := range []string{"a-run", "m-run", "z-run"} {
				run := runner.Run{Version: 1, UID: uid, Reason: "saved run reason", Target: runner.Target{Identity: kata.Identity{ProjectUID: "project"}}}
				if uid == signingUID {
					run.Ops = []runner.Op{{SigningRefused: true}}
				}
				if err := state.WriteJSON(state.OSFS{}, l.RunFile(uid), run); err != nil {
					t.Fatal(err)
				}
			}
			s, err := ReadStatus(l, config.Config{Projects: []config.Project{{Name: "example-project"}}})
			if err != nil {
				t.Fatal(err)
			}
			if len(s.Projects) != 1 || len(s.Projects[0].Runs) != 3 || s.Route == nil || !s.Route.SigningBlocked || s.Route.Allowed {
				t.Fatalf("signing route or configured lane changed: %+v", s)
			}
			for _, rows := range [][]map[string]any{s.Runs, s.Projects[0].Runs} {
				for _, row := range rows {
					if row["waiting_for_project"] == true || row["Reason"] != "saved run reason" || row["project"] != "example-project" {
						t.Fatalf("run %v: waiting=%v reason=%v project=%v", row["UID"], row["waiting_for_project"], row["Reason"], row["project"])
					}
				}
			}
		})
	}
}

func TestStatusDecoratesWaitingRunsBeforeStartupOverride(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	pl := statusTestLane(t, l)
	if err := state.WriteJSON(state.OSFS{}, pl.RouteFile(), RouteStatus{Project: "example-project", Error: "waiting for project example-project; run doctor for route details"}); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(state.OSFS{}, l.RunFile("run"), runner.Run{Version: 1, UID: "run", Target: runner.Target{Identity: kata.Identity{ProjectUID: "project"}}}); err != nil {
		t.Fatal(err)
	}
	if err := saveStartupErrors(l, map[string]string{"example-project": "credential unavailable"}); err != nil {
		t.Fatal(err)
	}
	s, err := ReadStatus(l)
	if err != nil {
		t.Fatal(err)
	}
	if s.Route == nil || s.Route.Error != "credential unavailable" || len(s.Runs) != 1 || s.Runs[0]["waiting_for_project"] != true || s.Runs[0]["Reason"] != "waiting for project example-project; run doctor for route details" {
		t.Fatalf("startup override changed waiting run decoration: %+v", s)
	}
}

func TestStatusIgnoresRuntimeFilesUnderLane(t *testing.T) {
	for _, path := range []string{"runs/stray/run.json", "serve.json", "request-results/stray.json"} {
		t.Run(path, func(t *testing.T) {
			l := state.Layout{Root: t.TempDir()}
			pl := statusTestLane(t, l)
			// Runtime files belong at the root. A stray lane file must not
			// contribute a run, serve record, result, or decode failure.
			if err := state.WriteJSON(state.OSFS{}, filepath.Join(pl.Root, path), map[string]any{"UID": "stray", "Error": "stray", "pid": "invalid"}); err != nil {
				t.Fatal(err)
			}
			s, err := ReadStatus(l)
			if err != nil {
				t.Fatal(err)
			}
			if len(s.Runs) != 0 || len(s.Projects) != 1 || len(s.Projects[0].Runs) != 0 || s.Projects[0].Serve != nil || len(s.Projects[0].ControlResults) != 0 {
				t.Fatalf("lane read runtime files: %+v", s)
			}
		})
	}
}

func TestStatusReadsSnapshotsWithCorruptControlResult(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	statusTestLane(t, l)
	if err := state.WriteJSON(state.OSFS{}, l.RunFile("run"), runner.Run{Version: 1, UID: "run", Reason: "saved run reason", Target: runner.Target{Identity: kata.Identity{ProjectUID: "project"}}}); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(state.OSFS{}, runner.RequestResultPath(l, "request"), map[string]any{"Version": "invalid"}); err != nil {
		t.Fatal(err)
	}
	s, err := ReadStatus(l)
	if err != nil {
		t.Fatal("control response prevented reading lane snapshots", err)
	}
	if len(s.Projects) != 1 || s.Projects[0].Project != "example-project" || len(s.Projects[0].Runs) != 1 || len(s.Runs) != 1 || s.Runs[0]["Reason"] != "saved run reason" || s.Runs[0]["project"] != "example-project" {
		t.Fatalf("lane snapshots changed: %+v", s)
	}
}

func TestStatusNextFiresUseLanePinAndDefinitionWatermarks(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	pl := statusTestLane(t, l)
	f := state.OSFS{}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := state.WriteJSON(f, pl.StateFile(), kata.Installation{Version: 1, ProjectUID: "project", Name: "example-project", CreatedAt: at.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(f, pl.DefsSnapshot(), defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{
		{UID: "older", Owner: defs.Adapter, Supported: true, Trigger: &defs.Trigger{Kind: "interval", IntervalSeconds: 60}, CreatedAt: at},
		{UID: "newer", Owner: defs.Adapter, Supported: true, Trigger: &defs.Trigger{Kind: "interval", IntervalSeconds: 60}, CreatedAt: at.Add(4 * time.Minute)},
	}}); err != nil {
		t.Fatal(err)
	}
	s, err := ReadStatus(l)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Projects) != 1 || len(s.NextFires) != 2 || !s.NextFires["older"].Equal(at.Add(3*time.Minute)) || !s.NextFires["newer"].Equal(at.Add(5*time.Minute)) || !s.Projects[0].NextFires["older"].Equal(at.Add(3*time.Minute)) {
		t.Fatalf("next-fire watermarks: %+v", s)
	}
}

func TestStatusOmitsNextFireForAuthorGatedJob(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	pl := statusTestLane(t, l)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := state.WriteJSON(state.OSFS{}, pl.DefsSnapshot(), defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{
		{UID: "gated", Owner: defs.Adapter, Supported: true, Trigger: &defs.Trigger{Kind: "interval", IntervalSeconds: 60}, CreatedAt: at, Warnings: []string{"automatic scheduling disabled: original author does not match lane actor"}},
		{UID: "allowed", Owner: defs.Adapter, Supported: true, Trigger: &defs.Trigger{Kind: "interval", IntervalSeconds: 60}, CreatedAt: at},
	}}); err != nil {
		t.Fatal(err)
	}
	s, err := ReadStatus(l)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.NextFires["gated"]; ok {
		t.Fatal("author-gated job promises a fire", s.NextFires)
	}
	if len(s.NextFires) != 1 || s.NextFires["allowed"].IsZero() {
		t.Fatal(s.NextFires)
	}
}
