package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestLocalChecksSurfaceUnfinishedLossReports(t *testing.T) {
	for _, kind := range []string{"pending-observation", "pending-report", "fatal-report", "complete", "quarantined", "auto-quarantined"} {
		t.Run(kind, func(t *testing.T) {
			l := state.Layout{Root: t.TempDir()}
			const uid = "01K00000000000000000000000"
			target := runner.Target{Identity: kata.Identity{ProjectUID: "project"}}
			if err := state.WriteJSON(state.OSFS{}, l.Project("project").StateFile(), kata.Installation{Version: 1, Name: "example-project", ProjectUID: "project"}); err != nil {
				t.Fatal(err)
			}
			pending := struct {
				Version     int
				Target      runner.Target
				Observation kata.Observation
			}{1, target, kata.Observation{IssueUID: "issue"}}
			if err := state.WriteJSON(state.OSFS{}, filepath.Join(l.Root, "abandoned", uid+".json"), pending); err != nil {
				t.Fatal(err)
			}
			reportLayout := state.Layout{Root: filepath.Join(l.Root, "abandoned", "reports")}
			path := reportLayout.RunFile(uid)
			if kind != "pending-observation" {
				op := runner.Op{ID: "abandon:comment", Kind: "comment", NextAt: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}
				r := runner.Run{Version: 1, UID: uid, Source: "abandon-report", Target: target, Phase: runner.Settled, IssueUID: "issue", Ops: []runner.Op{op}}
				if kind == "fatal-report" {
					r.Inspect = "kata validation: comment rejected"
					r.Ops[0].Inspect = r.Inspect
				}
				if kind == "complete" {
					r.Ops[0].Done = true
				}
				if err := state.WriteJSON(state.OSFS{}, path, r); err != nil {
					t.Fatal(err)
				}
				if kind == "auto-quarantined" {
					if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
						t.Fatal(err)
					}
					if _, err := runner.NewStore(state.OSFS{}, reportLayout).Load(uid); err == nil {
						t.Fatal("corruption hidden")
					}
				}
				if kind == "quarantined" {
					if err := state.QuarantineRunDirectory(state.OSFS{}, reportLayout, uid); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, _ := os.ReadFile(path)
			checks, err := LocalChecks(state.OSFS{}, l)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "complete" || kind == "quarantined" {
				if len(checks) != 0 {
					t.Fatal("finished report still unhealthy", checks)
				}
				return
			}
			if len(checks) != 1 || checks[0].Passed || !strings.Contains(checks[0].Detail, uid) {
				t.Fatal("loss report invisible", checks)
			}
			detail := checks[0].Detail
			if kind == "fatal-report" || kind == "auto-quarantined" {
				if kind == "fatal-report" && !strings.Contains(detail, "comment rejected") || !strings.Contains(detail, "herdr-kata stop") || !strings.Contains(detail, reportLayout.RunDir(uid)) || !strings.Contains(detail, reportLayout.QuarantineRunDir(uid)) || !strings.Contains(detail, "resolve the issue separately") {
					t.Fatal("inspection lacks remedy", detail)
				}
				if kind == "auto-quarantined" && !strings.Contains(detail, "automatically quarantined") {
					t.Fatal("automatic quarantine not explained", detail)
				}
			} else if !strings.Contains(detail, `herdr-kata abandon `+uid+` --project "example-project"`) {
				t.Fatal("pending report lacks retry", detail)
			}
			if kind == "pending-report" && !strings.Contains(detail, "2030-01-02T03:04:05Z") {
				t.Fatal("retry time missing", detail)
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(before) {
				t.Fatal("doctor changed reporting journal")
			}
		})
	}
}
