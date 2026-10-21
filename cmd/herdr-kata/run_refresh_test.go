package main

import (
	"bytes"
	"fmt"
	"log/slog"
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

// The CLI retries a missing cached job after one guarded definitions refresh.
func TestRunRefreshesMissingProjectJobOnce(t *testing.T) {
	for _, scenario := range []string{"new job", "still absent", "changed project", "already cached", "unrelated unsupported", "selected unsupported"} {
		t.Run(scenario, func(t *testing.T) {
			var warnings bytes.Buffer
			oldLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&warnings, nil)))
			defer slog.SetDefault(oldLogger)
			root, configDir := t.TempDir(), t.TempDir()
			t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", configDir)
			bin, calls, jobs := filepath.Join(root, "kata"), filepath.Join(root, "calls"), filepath.Join(root, "jobs")
			const script = `#!/bin/sh
printf '%s\n' "$*" >> "$KATA_TEST_CALLS"
case "$*" in
  *' health') printf '%s\n' '{"version":"test","schema_version":33}' ;;
  *'cron capabilities') printf '{"project_uid":"%s","event_features":["cron_v1"]}\n' "$KATA_TEST_PROJECT_UID" ;;
  *'federation identity') printf '%s\n' '{"instance_uid":"instance","auth":{"actor":"worker"}}' ;;
  *'daemon diagnose '*) printf '%s\n' '{"state":"project_unverifiable","endpoint":"http://hub.example"}' ;;
  *'cron job list') cat "$KATA_TEST_JOBS" ;;
  *'cron workflow list') printf '%s\n' '{"workflows":[]}' ;;
  *) exit 90 ;;
esac
`
			if e := os.WriteFile(bin, []byte(script), 0700); e != nil {
				t.Fatal(e)
			}
			uid := "project-b"
			if scenario == "changed project" {
				uid = "different-project"
			}
			env := map[string]string{"KATA_SERVER": "http://hub.example", "KATA_TEST_CALLS": calls, "KATA_TEST_JOBS": jobs, "KATA_TEST_PROJECT_UID": uid}
			text := fmt.Sprintf(`session="work"
executor="worker"
[kata]
bin=%q
[kata.env]
KATA_SERVER="http://hub.example"
KATA_TEST_CALLS=%q
KATA_TEST_JOBS=%q
KATA_TEST_PROJECT_UID=%q
[profiles.default]
kind="codex"
[[projects]]
name="example-project"
actor="worker"
[[projects]]
name="team-project"
actor="worker"
`, bin, calls, jobs, uid)
			if e := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(text), 0600); e != nil {
				t.Fatal(e)
			}
			body := `{"jobs":[{"uid":"new-job","name":"new job","definition_event_uid":"new-event","definition":{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"work"},"issue":{"kind":"per-run","title":"Work"},"overlap":"forbid","catchup":"skip"}}]}`
			if scenario == "unrelated unsupported" {
				body = strings.TrimSuffix(body, "]}") + `,{"uid":"unrelated-job","name":"unrelated job","definition_event_uid":"unrelated-event","definition":{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"other work"},"issue":{"kind":"per-run","title":"Other"},"overlap":"forbid","catchup":"skip","options":{"agent":"missing"}}}]}`
			}
			if scenario == "selected unsupported" {
				body = strings.Replace(body, `,"overlap":"forbid"`, "", 1)
			}
			if scenario == "still absent" {
				body = `{"jobs":[]}`
			}
			if e := os.WriteFile(jobs, []byte(body), 0600); e != nil {
				t.Fatal(e)
			}
			f, l := state.OSFS{}, state.Layout{Root: root}
			if e := state.InitializeLayout(f, l); e != nil {
				t.Fatal(e)
			}
			pl := l.Project("project-b")
			pin := kata.Installation{Version: 1, Name: "team-project", CreatedAt: time.Now(), Executor: "worker", InstanceUID: "instance", ProjectUID: "project-b", Actor: "worker", Route: kata.Route{Bin: bin, Env: env, Endpoint: "http://hub.example", DiagnoseState: "project_unverifiable"}}
			if e := state.WriteJSON(f, pl.StateFile(), pin); e != nil {
				t.Fatal(e)
			}
			snapshot := defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{}}
			if scenario == "already cached" {
				snapshot.Jobs = []defs.JobEntry{{UID: "new-job", Name: "new job", Owner: defs.Adapter, Supported: true}}
			}
			if e := state.WriteJSON(f, pl.DefsSnapshot(), snapshot); e != nil {
				t.Fatal(e)
			}
			lock, ok, e := state.TryLock(l.WorkerLock())
			if e != nil || !ok {
				t.Fatal(ok, e)
			}
			defer lock.Unlock()
			if e := state.WriteJSON(f, l.ServeInfo(), map[string]int{"worker_pid": 0}); e != nil {
				t.Fatal(e)
			}
			var out, stderr bytes.Buffer
			code := run([]string{"run", "new-job", "--project", "team-project"}, &out, &stderr)
			trace, _ := os.ReadFile(calls)
			listCalls := strings.Count(string(trace), "cron job list")
			for line := range strings.SplitSeq(strings.TrimSpace(string(trace)), "\n") {
				if line != "" && !strings.Contains(line, "--project team-project") {
					t.Fatal("refresh used another project", line)
				}
			}
			if scenario == "new job" || scenario == "already cached" || scenario == "unrelated unsupported" {
				if code != 0 {
					t.Fatal(code, stderr.String())
				}
				entries, e := os.ReadDir(l.RequestsDir())
				if e != nil || len(entries) != 1 {
					t.Fatal(entries, e)
				}
				var req runner.Request
				if e := state.ReadJSON(f, filepath.Join(l.RequestsDir(), entries[0].Name()), 65536, &req); e != nil || req.Project != "project-b" || req.Job != "new-job" {
					t.Fatal(req, e)
				}
			} else {
				if code != 1 {
					t.Fatal(code, stderr.String())
				}
				entries, e := os.ReadDir(l.RequestsDir())
				if e != nil && !os.IsNotExist(e) {
					t.Fatal(e)
				}
				if len(entries) != 0 {
					t.Fatal("refused refresh published a request", entries)
				}
			}
			wantLists := 1
			if scenario == "already cached" || scenario == "changed project" {
				wantLists = 0
			}
			if listCalls != wantLists {
				t.Fatal("definitions were not refreshed exactly once", listCalls, string(trace))
			}
			if strings.Count(string(trace), "cron workflow list") != wantLists {
				t.Fatal("workflow snapshot refresh differed", string(trace))
			}
			if scenario == "changed project" && !strings.Contains(stderr.String(), "project UID differs") {
				t.Fatal("changed route did not fence refresh", stderr.String())
			}
			if scenario == "still absent" && !strings.Contains(stderr.String(), `unknown job "new-job"`) {
				t.Fatal(stderr.String())
			}
			if scenario == "unrelated unsupported" && warnings.Len() != 0 {
				t.Fatal("manual refresh did not restrict warnings to its target", warnings.String())
			}
			if scenario == "selected unsupported" && (!strings.Contains(stderr.String(), "invalid overlap") || !strings.Contains(warnings.String(), "job=new-job")) {
				t.Fatal("selected unsupported job was not explained", stderr.String(), warnings.String())
			}
			if scenario == "already cached" && len(trace) != 0 {
				t.Fatal("cached selection contacted Kata", string(trace))
			}
		})
	}
}
