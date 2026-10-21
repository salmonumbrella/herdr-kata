package doctor

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestDoctorOmitsRemovedLane(t *testing.T) {
	c, k, h, l := doctorFixture(t)
	lane := definitionLane(t, l, "removed-project", "removed-project")
	if err := state.WriteJSON(state.OSFS{}, lane.DefsSnapshot(), defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{{UID: "old-job", Definition: json.RawMessage(`{"enabled":true}`), Reasons: []string{"unknown checkout"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(state.OSFS{}, l.RunFile("old-run"), runner.Run{Version: 1, UID: "old-run", Phase: runner.Held, Reason: "admission_rejected", Target: runner.Target{Identity: kata.Identity{ProjectUID: "removed-project"}}}); err != nil {
		t.Fatal(err)
	}
	for _, check := range Check(t.Context(), c, k, h, l).Checks {
		if strings.Contains(check.Name, "removed-project") || check.Name == "local run old-run" {
			t.Fatalf("removed lane in doctor: %+v", check)
		}
	}
}

func definitionLane(t *testing.T, root state.Layout, uid, name string) state.Layout {
	t.Helper()
	fsys := state.OSFS{}
	if err := state.WriteJSON(fsys, root.StateFile(), map[string]int{"version": 1}); err != nil {
		t.Fatal(err)
	}
	l := root.Project(uid)
	if err := state.WriteJSON(fsys, l.StateFile(), kata.Installation{Version: 1, Name: name, ProjectUID: uid, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestDoctorUnsupportedDefinitionsOfflineAcrossLanes(t *testing.T) {
	c, _, h, l := doctorFixture(t)
	c.Projects = []config.Project{{Name: "example-project", Actor: "actor"}, {Name: "team-project", Actor: "actor"}}
	k := kata.New(c.Kata, l.Root, func(context.Context, []string, []string, string, []byte) ([]byte, []byte, int, error) {
		return []byte(`{"error":{"code":"transport","message":"offline"}}`), nil, 7, nil
	}, nil)
	lanes := []state.Layout{
		definitionLane(t, l, "project-a", "example-project"),
		definitionLane(t, l, "project-b", "team-project"),
	}
	snap := defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{
		{UID: "unsupported-job", Name: "Needs checkout", Definition: json.RawMessage(`{"enabled":true}`), Reasons: []string{"unknown checkout key", "profile excluded"}},
		{UID: "healthy-job", Name: "Healthy", Definition: json.RawMessage(`{"enabled":true}`), Supported: true},
		{UID: "disabled-job", Definition: json.RawMessage(`{"enabled":false}`), Reasons: []string{"disabled"}},
		{UID: "default-disabled-job", Definition: json.RawMessage(`{}`), Reasons: []string{"disabled"}},
		{UID: "deleted-job", Definition: json.RawMessage(`{"enabled":true}`), DeletedAt: ptrTime(), Reasons: []string{"deleted"}},
	}}
	for _, lane := range lanes {
		if err := state.WriteJSON(state.OSFS{}, lane.DefsSnapshot(), snap); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.Stat(lanes[0].DefsSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	report := Check(t.Context(), c, k, h, l)
	after, err := os.Stat(lanes[0].DefsSnapshot())
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("doctor rewrote definition snapshot", err)
	}
	count := 0
	for _, check := range report.Checks {
		if !strings.HasPrefix(check.Name, "unsupported definition ") {
			continue
		}
		count++
		if check.Passed {
			t.Error("unsupported definition passed", check)
		}
		for _, want := range []string{"Needs checkout", "unsupported-job", "unknown checkout key", "profile excluded", "herdr-kata status --json", "cron job show unsupported-job"} {
			if !strings.Contains(check.Detail, want) {
				t.Errorf("missing %q: %+v", want, check)
			}
		}
	}
	for _, project := range []string{"example-project", "team-project"} {
		found := false
		for _, check := range report.Checks {
			found = found || check.Name == "unsupported definition "+project+" unsupported-job" && strings.Contains(check.Detail, "--project "+project)
		}
		if !found {
			t.Error("missing project-specific definition diagnostic", project, report)
		}
	}
	if count != 2 {
		t.Fatal("expected only enabled unsupported jobs", report)
	}
	snap.Jobs[0].Supported, snap.Jobs[0].Reasons = true, nil
	for _, lane := range lanes {
		if err := state.WriteJSON(state.OSFS{}, lane.DefsSnapshot(), snap); err != nil {
			t.Fatal(err)
		}
	}
	for _, check := range Check(t.Context(), c, k, h, l).Checks {
		if strings.HasPrefix(check.Name, "unsupported definition ") {
			t.Error("repaired definition still reported", check)
		}
	}
}

func TestDoctorDefinitionSnapshotLegacyAndUnreadable(t *testing.T) {
	for _, tc := range []struct {
		name string
		data any
		want string
	}{
		{"legacy", []defs.JobEntry{{UID: "job", Reasons: []string{"unsupported"}}}, "legacy snapshot has no enabled state"},
		{"unknown version", map[string]int{"version": 99}, "unsupported definitions snapshot version 99"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, k, h, l := doctorFixture(t)
			c.Kata.Project = "example-project"
			lane := definitionLane(t, l, "project", "example-project")
			if err := state.WriteJSON(state.OSFS{}, lane.DefsSnapshot(), tc.data); err != nil {
				t.Fatal(err)
			}
			for _, check := range Check(t.Context(), c, k, h, l).Checks {
				if check.Name == "definitions example-project" && !check.Passed && strings.Contains(check.Detail, tc.want) {
					return
				}
			}
			t.Fatal("doctor hid unusable snapshot", tc.name)
		})
	}
}

func TestDoctorDefinitionSnapshotMissingAndStale(t *testing.T) {
	for _, missing := range []bool{true, false} {
		name := "stale"
		if missing {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			c, k, h, l := doctorFixture(t)
			c.Kata.Project = "example-project"
			lane := definitionLane(t, l, "project", "example-project")
			if !missing {
				if err := state.WriteJSON(state.OSFS{}, lane.DefsSnapshot(), defs.Snapshot{Version: 2}); err != nil {
					t.Fatal(err)
				}
				old := time.Now().Add(-time.Hour)
				if err := os.Chtimes(lane.DefsSnapshot(), old, old); err != nil {
					t.Fatal(err)
				}
			}
			for _, check := range Check(t.Context(), c, k, h, l).Checks {
				if check.Name == "definitions example-project" && !check.Passed && strings.Contains(check.Detail, name) && strings.Contains(check.Detail, "herdr-kata up") && strings.Contains(check.Detail, lane.DefsSnapshot()) {
					return
				}
			}
			t.Fatal("doctor did not mark snapshot", name)
		})
	}
}

func TestDoctorReportsNullDefinitionReason(t *testing.T) {
	c, k, h, l := doctorFixture(t)
	c.Kata.Project = "example-project"
	lane := definitionLane(t, l, "project", "example-project")
	snapshot := defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{{
		UID: "invalid-job", Name: "Invalid definition", Definition: json.RawMessage(`null`),
		Reasons: []string{"definition must be an object"},
	}}}
	if err := state.WriteJSON(state.OSFS{}, lane.DefsSnapshot(), snapshot); err != nil {
		t.Fatal(err)
	}
	for _, check := range Check(t.Context(), c, k, h, l).Checks {
		if check.Name != "unsupported definition example-project invalid-job" {
			continue
		}
		if check.Passed {
			t.Fatal("invalid definition passed", check)
		}
		for _, want := range []string{"Invalid definition", "invalid-job", "enabled state unavailable", "definition must be an object"} {
			if !strings.Contains(check.Detail, want) {
				t.Errorf("missing %q in invalid definition diagnostic: %+v", want, check)
			}
		}
		return
	}
	t.Fatal("doctor hid the null definition's cached support reason")
}

// Spec §23.5: doctor repeats the volume warning for enabled jobs as a passing
// check, so a busy job never fails doctor.
func TestDoctorReportsIssueVolumeWarning(t *testing.T) {
	c, _, h, l := doctorFixture(t)
	k := kata.New(c.Kata, l.Root, func(context.Context, []string, []string, string, []byte) ([]byte, []byte, int, error) {
		return []byte(`{"error":{"code":"transport","message":"offline"}}`), nil, 7, nil
	}, nil)
	c.Kata.Project = "example-project"
	lane := definitionLane(t, l, "project-a", "example-project")
	warning := "creates about 48 issues a day; consider an existing issue, a run parent, or a native recurring job"
	snap := defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{
		{UID: "busy-job", Name: "Busy", Definition: json.RawMessage(`{"enabled":true}`), Supported: true, Warnings: []string{"ignored options key: extra", warning}},
		{UID: "quiet-job", Name: "Quiet", Definition: json.RawMessage(`{"enabled":true}`), Supported: true, Warnings: []string{"ignored options key: extra"}},
		{UID: "disabled-job", Definition: json.RawMessage(`{"enabled":false}`), Supported: true, Warnings: []string{warning}},
		{UID: "deleted-job", Definition: json.RawMessage(`{"enabled":true}`), DeletedAt: ptrTime(), Supported: true, Warnings: []string{warning}},
	}}
	if err := state.WriteJSON(state.OSFS{}, lane.DefsSnapshot(), snap); err != nil {
		t.Fatal(err)
	}
	var got []CheckResult
	for _, check := range Check(t.Context(), c, k, h, l).Checks {
		if strings.HasPrefix(check.Name, "issue volume ") {
			got = append(got, check)
		}
	}
	want := CheckResult{Name: "issue volume example-project busy-job", Passed: true, Detail: `warning: project example-project; job "Busy" (busy-job) ` + warning}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}
