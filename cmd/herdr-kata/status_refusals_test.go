package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestStatusDropsRefusalsForRemovedJobs(t *testing.T) {
	for _, tc := range []struct {
		name, project, job string
		snapshot           bool
		jobs               []defs.JobEntry
		visible            bool
	}{
		{"deleted", "project-a", "job", true, []defs.JobEntry{{UID: "job", DeletedAt: new(time.Time)}}, false},
		{"absent", "project-a", "job", true, nil, false},
		{"current", "project-a", "job", true, []defs.JobEntry{{UID: "job", Name: "paused-job"}}, true},
		{"named current", "project-a", "paused-job", true, []defs.JobEntry{{UID: "job", Name: "paused-job"}}, true},
		{"missing snapshot", "project-a", "job", false, nil, true},
		{"another project", "project-b", "job", true, []defs.JobEntry{{UID: "job"}}, false},
		{"unknown project", "unknown-project", "job", true, nil, true},
		{"empty pin name", "project-a", "job", true, nil, false},
		{"legacy", "", "job", true, nil, true},
		{"unsafe project", "../outside", "job", true, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := state.Layout{Root: t.TempDir()}
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
			f := state.OSFS{}
			if err := state.InitializeLayout(f, l); err != nil {
				t.Fatal(err)
			}
			for _, project := range []struct{ uid, name string }{{"project-a", "example-project"}, {"project-b", "team-project"}} {
				if tc.name == "empty pin name" {
					project.name = ""
				}
				pl := l.Project(project.uid)
				if err := state.WriteJSON(f, pl.StateFile(), kata.Installation{Version: 1, ProjectUID: project.uid, Name: project.name}); err != nil {
					t.Fatal(err)
				}
				if tc.snapshot {
					var jobs []defs.JobEntry
					if project.uid == "project-a" {
						jobs = tc.jobs
					}
					if err := state.WriteJSON(f, pl.DefsSnapshot(), defs.Snapshot{Version: 2, Jobs: jobs}); err != nil {
						t.Fatal(err)
					}
				}
			}
			const uid = "01K00000000000000000000000"
			refusal := "manual run refused: job is paused"
			path := runner.RequestResultPath(l, "manual-request")
			result := map[string]any{"version": 1, "run_uid": uid, "error": refusal}
			if tc.name != "legacy" {
				result["kind"], result["project"], result["job"] = "run", tc.project, tc.job
			} else {
				refusal = `manual run refused for project "example-project" job "job": job is paused`
				result["error"] = refusal
			}
			if err := state.WriteJSON(f, path, result); err != nil {
				t.Fatal(err)
			}
			const controlRefusal = "control request refused: release requires an unfinished run"
			if err := state.WriteJSON(f, runner.RequestResultPath(l, "release-request"), runner.RequestResult{Version: 1, RunUID: "01K00000000000000000000001", Error: controlRefusal}); err != nil {
				t.Fatal(err)
			}
			for _, asJSON := range []bool{false, true} {
				var out, stderr bytes.Buffer
				if code := statusCommand(l, asJSON, false, &out, &stderr); code != 0 {
					t.Fatal(code, stderr.String())
				}
				// JSON escapes quotes, so the UID identifies the manual refusal in both views.
				if strings.Contains(out.String(), uid) != tc.visible || !strings.Contains(out.String(), controlRefusal) {
					t.Errorf("json=%v visible=%v: %s", asJSON, tc.visible, out.String())
				}
			}
			var saved runner.RequestResult
			if err := state.ReadJSON(f, path, 64<<10, &saved); err != nil || saved.Error != refusal {
				t.Fatal("status changed acknowledgement evidence", saved, err)
			}
		})
	}
}

func TestStatusDropsRefusalsForResolvedLostRuns(t *testing.T) {
	for _, tc := range []struct {
		name, resolution string
		visible          bool
	}{
		{"abandoned", "completed report", false},
		{"later refusal after abandonment", "completed report", true},
		{"pending abandonment", "pending report", true},
		{"inspected abandonment", "inspected report", true},
		{"another run", "mismatched report", true},
		{"local resolution", "acknowledged quarantine", false},
		{"later refusal after local resolution", "acknowledged quarantine", true},
		{"unresolved quarantine", "unacknowledged quarantine", true},
		{"unknown run", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := state.Layout{Root: t.TempDir()}
			t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
			const uid = "01K00000000000000000000000"
			f := state.OSFS{}
			refusal := "lstat " + l.RunFile(uid) + ": no such file or directory"
			path := runner.RequestResultPath(l, "release-request")
			if err := state.WriteJSON(f, path, runner.RequestResult{Version: 1, RunUID: uid, Error: refusal}); err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(tc.resolution, "report") {
				report := runner.Run{Version: 1, UID: uid, Source: "abandon-report", Phase: runner.Settled, Outcome: "unknown", Reason: "local state lost", SlotReleased: true, IssueUID: "issue",
					Ops: []runner.Op{{ID: "abandon:comment", Kind: "comment", Done: true}}}
				switch tc.resolution {
				case "pending report":
					report.Ops[0].Done = false
				case "inspected report":
					report.Inspect = "loss report needs inspection"
				case "mismatched report":
					report.UID = "01K00000000000000000000001"
				}
				reports := state.Layout{Root: filepath.Join(l.Root, "abandoned", "reports")}
				if err := state.WriteJSON(f, reports.RunFile(uid), report); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasSuffix(tc.resolution, "quarantine") {
				if err := state.WriteJSON(f, filepath.Join(l.QuarantineRunDir(uid), "acknowledged.json"), struct {
					Acknowledged bool `json:"acknowledged"`
				}{tc.resolution == "acknowledged quarantine"}); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(tc.name, "later refusal") {
				older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
				resolutionPath := filepath.Join(l.QuarantineRunDir(uid), "acknowledged.json")
				if tc.resolution == "completed report" {
					resolutionPath = (state.Layout{Root: filepath.Join(l.Root, "abandoned", "reports")}).RunFile(uid)
				}
				if err := os.Chtimes(resolutionPath, older, older); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 { // Repeated reads must use durable resolution, with no process-local cache.
				for _, asJSON := range []bool{false, true} {
					var out, stderr bytes.Buffer
					if code := statusCommand(l, asJSON, false, &out, &stderr); code != 0 {
						t.Fatal(code, stderr.String())
					}
					if strings.Contains(out.String(), "local run state unavailable") != tc.visible {
						t.Errorf("json=%v visible=%v: %s", asJSON, tc.visible, out.String())
					}
				}
			}
			var saved runner.RequestResult
			if err := state.ReadJSON(f, path, 64<<10, &saved); err != nil || saved.Error != refusal {
				t.Fatal("status changed acknowledgement evidence", saved, err)
			}
		})
	}
}

func TestStatusKeepsRefusalsWhenResolutionEvidenceIsUnreadable(t *testing.T) {
	for _, artifact := range []string{"acknowledgement", "abandon report"} {
		for _, contents := range []string{"broken JSON", `{"acknowledged":true,"unexpected":true}`} {
			t.Run(artifact+"/"+contents, func(t *testing.T) {
				l := state.Layout{Root: t.TempDir()}
				t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
				t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
				const uid = "01K00000000000000000000000"
				result := runner.RequestResult{Version: 1, RunUID: uid, Error: "local run state unavailable; inspect herdr-kata doctor"}
				resultPath := runner.RequestResultPath(l, "release-request")
				if err := state.WriteJSON(state.OSFS{}, resultPath, result); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(l.QuarantineRunDir(uid), "acknowledged.json")
				if artifact == "abandon report" {
					path = (state.Layout{Root: filepath.Join(l.Root, "abandoned", "reports")}).RunFile(uid)
				}
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
				for _, asJSON := range []bool{false, true} {
					var out, stderr bytes.Buffer
					if code := statusCommand(l, asJSON, false, &out, &stderr); code != 0 {
						t.Errorf("json=%v: status failed: %d %s", asJSON, code, stderr.String())
					}
					if !strings.Contains(out.String(), uid) || !strings.Contains(out.String(), result.Error) {
						t.Errorf("json=%v: status hid the refusal: %s", asJSON, out.String())
					}
				}
				var saved runner.RequestResult
				if err := state.ReadJSON(state.OSFS{}, resultPath, 64<<10, &saved); err != nil || saved != result {
					t.Fatal("status changed acknowledgement evidence", saved, err)
				}
				if saved, err := os.ReadFile(path); err != nil || string(saved) != contents {
					t.Fatal("status changed damaged evidence", string(saved), err)
				}
			})
		}
	}
}

func TestStatusShowsUnreadableRequestWithoutRunIdentity(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	result := runner.RequestResult{Version: 1, Error: "request 01K00000000000000000000001: permission denied"}
	if err := state.WriteJSON(state.OSFS{}, runner.RequestResultPath(l, "01K00000000000000000000001"), result); err != nil {
		t.Fatal(err)
	}
	rows, err := currentControlResults(l)
	if err != nil || len(rows) != 1 || rows[0].Error != "request 01K00000000000000000000001: permission denied" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}
