package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestRunControlsNameFrozenProject(t *testing.T) {
	for _, kind := range []string{"resume", "release", "abandon"} {
		t.Run(kind, func(t *testing.T) {
			l := state.Layout{Root: t.TempDir()}
			t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
			f := state.OSFS{}
			if err := state.InitializeLayout(f, l); err != nil {
				t.Fatal(err)
			}
			if err := state.WriteJSON(f, l.Project("project-b").StateFile(), kata.Installation{Version: 1, Name: "team-project", ProjectUID: "project-b"}); err != nil {
				t.Fatal(err)
			}
			uid := "01K00000000000000000000000"
			r := runner.Run{Version: 1, UID: uid, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-b"}}, Phase: runner.Held, Reason: "admission_rejected"}
			if err := state.WriteJSON(f, l.RunFile(uid), r); err != nil {
				t.Fatal(err)
			}
			args := []string{kind, uid}
			if kind == "abandon" {
				args = append(args, "--local")
			} else {
				lock, ok, err := state.TryLock(l.WorkerLock())
				if err != nil || !ok {
					t.Fatal(ok, err)
				}
				defer lock.Unlock()
				if err := state.WriteJSON(f, l.ServeInfo(), map[string]int{"worker_pid": 0}); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "abandon" {
				defer acknowledgeNextControl(t, l)()
			}
			var out, stderr bytes.Buffer
			if code := run(args, &out, &stderr); code != 0 {
				t.Fatal(code, stderr.String())
			}
			if !strings.Contains(out.String(), "project team-project") {
				t.Fatalf("confirmation lacks frozen project: %q", out.String())
			}
			if kind == "abandon" {
				if err := state.ReadJSON(f, filepath.Join(l.QuarantineRunDir(uid), "run.json"), 4<<20, &r); err != nil {
					t.Fatal("missing quarantined journal", err)
				}
			} else {
				files, err := state.OSFS{}.ReadDir(l.RequestsDir())
				if err != nil || len(files) != 1 {
					t.Fatal(files, err)
				}
				var req runner.Request
				if err := state.ReadJSON(f, filepath.Join(l.RequestsDir(), files[0].Name()), 64<<10, &req); err != nil || req.RunUID != uid || req.Kind != kind {
					t.Fatal(req, err)
				}
			}
		})
	}
}
