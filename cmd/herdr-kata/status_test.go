package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/daemon"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/inbox"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

var updateStatusGolden = flag.Bool("update-status-golden", false, "update status JSON fixtures")

func TestStatusTwoProjectsJSON(t *testing.T) {
	for _, refused := range []bool{false, true} {
		name := "two-projects"
		if refused {
			name = "two-projects-refused"
		}
		t.Run(name, func(t *testing.T) {
			l := state.Layout{Root: t.TempDir()}
			t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
			f := state.OSFS{}
			if err := state.InitializeLayout(f, l); err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			for _, p := range []struct{ uid, name string }{{"project-a", "example-project"}, {"project-b", "team-project"}} {
				pl := l.Project(p.uid)
				files := map[string]any{
					pl.StateFile():                       kata.Installation{Version: 1, ProjectUID: p.uid, Name: p.name, CreatedAt: at},
					pl.DefsSnapshot():                    defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{{UID: "same-job", Name: "Check", Supported: true, Owner: defs.Adapter, CreatedAt: at, Trigger: &defs.Trigger{Kind: "interval", IntervalSeconds: 60}, Definition: json.RawMessage(`{"enabled":true}`)}}},
					pl.JobCheckpoint("same-job"):         map[string]any{"version": 1, "job_uid": "same-job", "handled_through": "2026-01-01T00:00:00Z"},
					filepath.Join(pl.Root, "inbox.json"): inbox.Snapshot{Version: 1, LastSuccessfulScan: at},
					filepath.Join(pl.Root, "route.json"): daemon.RouteStatus{Project: p.name, Allowed: true},
				}
				if refused && p.uid == "project-b" {
					files[filepath.Join(pl.Root, "route.json")] = daemon.RouteStatus{Project: p.name, Allowed: false, AccessLost: true, Error: "actor lost access to project team-project"}
				}
				for path, value := range files {
					if err := state.WriteJSON(f, path, value); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, p := range []struct{ uid, run, phase string }{{"project-a", "run-a", "running"}, {"project-b", "run-b", "held"}} {
				row := map[string]any{"UID": p.run, "Version": 1, "Phase": p.phase, "Job": map[string]any{"uid": "same-job", "name": "Check"}, "Target": map[string]any{"Identity": map[string]any{"project_uid": p.uid}}}
				if err := state.WriteJSON(f, l.RunFile(p.run), row); err != nil {
					t.Fatal(err)
				}
			}
			var out, stderr bytes.Buffer
			if code := run([]string{"status", "--json"}, &out, &stderr); code != 0 {
				t.Fatal(code, stderr.String())
			}
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, out.Bytes(), "", "  "); err != nil {
				t.Fatal(err)
			}
			got := pretty.Bytes()
			path := filepath.Join("testdata", "status-"+name+".golden.json")
			if *updateStatusGolden {
				if err := os.MkdirAll("testdata", 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("status JSON changed (-want +got):\nwant %s\ngot %s", want, got)
			}
		})
	}
}

func TestStatusUsesSavedQualifiedIssueReference(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	r := map[string]any{"Version": 1, "UID": "run-a", "IssueUID": "issue-uid", "IssueRef": "issue-uid", "issue_refs": map[string]string{"issue-uid": "example-project#abcd"}, "Phase": "settled"}
	if err := state.WriteJSON(state.OSFS{}, l.RunFile("run-a"), r); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"status"}, &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("example-project#abcd")) || bytes.Contains(out.Bytes(), []byte("issue-uid")) {
		t.Fatal(out.String())
	}
}
