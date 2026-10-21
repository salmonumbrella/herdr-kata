package main

import (
	"bytes"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"testing"
)

func TestRunWithoutWorker(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	var out, err bytes.Buffer
	if code := run([]string{"run", "job"}, &out, &err); code != 1 || !bytes.Contains(err.Bytes(), []byte("runtime not running")) {
		t.Fatal(code, out.String(), err.String())
	}
}
func TestCLIControlArguments(t *testing.T) {
	for _, args := range [][]string{{"run"}, {"release"}, {"abandon"}, {"run", "one", "two"}, {"release", "run", "--wait"}, {"resume", "run", "--project", "example-project"}} {
		var out, err bytes.Buffer
		if code := run(args, &out, &err); code != 2 {
			t.Fatal(args, code, err.String())
		}
	}
}

func TestRunWaitOutcomes(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	worker := exec.Command(os.Args[0], "-test.run=^TestControlSignalWorker$")
	worker.Env = append(os.Environ(), "HERDR_TEST_SIGNAL_READY="+ready)
	if e := worker.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = worker.Process.Kill(); _ = worker.Wait() })
	for until := time.Now().Add(10 * time.Second); ; {
		if _, e := os.Stat(ready); e == nil {
			break
		}
		if time.Now().After(until) {
			t.Fatal("signal worker did not start")
		}
		time.Sleep(time.Millisecond)
	}
	for _, outcome := range []string{"succeeded", "failed", "refused"} {
		t.Run(outcome, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
			l := state.Layout{Root: root}
			lock, ok, e := state.TryLock(l.WorkerLock())
			if e != nil || !ok {
				t.Fatal(ok, e)
			}
			defer lock.Unlock()
			if e = state.WriteJSON(state.OSFS{}, l.ServeInfo(), map[string]int{"worker_pid": worker.Process.Pid}); e != nil {
				t.Fatal(e)
			}
			var out, stderr bytes.Buffer
			done := make(chan int, 1)
			go func() { done <- run([]string{"run", "job", "--wait"}, &out, &stderr) }()
			var req runner.Request
			for until := time.Now().Add(10 * time.Second); ; {
				entries, _ := os.ReadDir(l.RequestsDir())
				if len(entries) == 1 && state.ReadJSON(state.OSFS{}, filepath.Join(l.RequestsDir(), entries[0].Name()), 4096, &req) == nil {
					break
				}
				if time.Now().After(until) {
					t.Fatal("request not published")
				}
				time.Sleep(time.Millisecond)
			}
			// No journal or response is available yet: --wait must keep polling.
			select {
			case code := <-done:
				t.Fatal("wait returned before result", code)
			case <-time.After(20 * time.Millisecond):
			}
			want := 1
			if outcome == "refused" {
				e = state.WriteJSON(state.OSFS{}, runner.RequestResultPath(l, req.UID), runner.RequestResult{Version: 1, RunUID: req.RunUID, Error: "manual job disabled"})
			} else {
				now := time.Now()
				e = state.WriteJSON(state.OSFS{}, l.RunFile(req.RunUID), runner.Run{Version: 1, UID: req.RunUID, Outcome: outcome, EndedAt: &now})
				if outcome == "succeeded" {
					want = 0
				}
			}
			if e != nil {
				t.Fatal(e)
			}
			select {
			case code := <-done:
				if code != want {
					t.Fatal(code, want, out.String(), stderr.String())
				}
				if !bytes.Contains(out.Bytes(), []byte(req.RunUID)) {
					t.Fatal("allocated run UID missing", out.String())
				}
				if outcome == "refused" && !bytes.Contains(stderr.Bytes(), []byte("manual job disabled")) {
					t.Fatal(stderr.String())
				}
			case <-time.After(10 * time.Second):
				t.Fatal("wait did not return after result")
			}
		})
	}
}

func TestControlSignalWorker(t *testing.T) {
	ready := os.Getenv("HERDR_TEST_SIGNAL_READY")
	if ready == "" {
		return
	}
	signal.Ignore(syscall.SIGUSR1)
	if e := os.WriteFile(ready, nil, 0600); e != nil {
		t.Fatal(e)
	}
	time.Sleep(time.Hour)
}

func TestRunInputRequest(t *testing.T) {
	for _, args := range [][]string{{"run", "job", "--input", "hello"}, {"run", "--input=hello", "job"}, {"run", "job", "--input", ""}} {
		t.Run(strings.Join(args, "/"), func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
			l := state.Layout{Root: root}
			lock, ok, e := state.TryLock(l.WorkerLock())
			if e != nil || !ok {
				t.Fatal(e)
			}
			defer lock.Unlock()
			if e = state.WriteJSON(state.OSFS{}, l.ServeInfo(), map[string]int{"worker_pid": 0}); e != nil {
				t.Fatal(e)
			}
			var out, stderr bytes.Buffer
			if code := run(args, &out, &stderr); code != 0 {
				t.Fatal(code, stderr.String())
			}
			entries, e := os.ReadDir(l.RequestsDir())
			if e != nil || len(entries) != 1 {
				t.Fatal(entries, e)
			}
			var req runner.Request
			if e = state.ReadJSON(state.OSFS{}, filepath.Join(l.RequestsDir(), entries[0].Name()), 64<<10, &req); e != nil {
				t.Fatal(e)
			}
			expected := "hello"
			if args[len(args)-1] == "" {
				expected = ""
			}
			if req.Input == nil || *req.Input != expected {
				t.Fatalf("input missing %+v", req)
			}
		})
	}
}

func TestAbandonLocalOfflineQuarantinesWholeRun(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", filepath.Join(root, "no-config"))
	l := state.Layout{Root: root}
	uid := "01K00000000000000000000000"
	store := runner.NewStore(state.OSFS{}, l)
	if err := store.Create(&runner.Run{Version: 1, UID: uid, Phase: runner.Held, Reason: "admission_rejected", Ops: []runner.Op{{ID: "admission", Kind: "observe", Payload: []byte(`{}`)}}}); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(l.RunDir(uid), "steps", "main", "1", "output.log")
	if err := os.MkdirAll(filepath.Dir(artifact), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("run evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		var out, stderr bytes.Buffer
		code := run([]string{"abandon", uid, "--local"}, &out, &stderr)
		if code != 0 || !strings.Contains(out.String(), "quarantined") {
			t.Fatal(code, out.String(), stderr.String())
		}
	}
	if _, err := os.Stat(l.RunDir(uid)); !os.IsNotExist(err) {
		t.Fatal("source still present", err)
	}
	got, err := os.ReadFile(filepath.Join(l.QuarantineRunDir(uid), "steps", "main", "1", "output.log"))
	if err != nil || string(got) != "run evidence" {
		t.Fatal(string(got), err)
	}
	var saved runner.Run
	if err := state.ReadJSON(state.OSFS{}, filepath.Join(l.QuarantineRunDir(uid), "run.json"), 4<<20, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Ops[0].Done || !state.Quarantined(state.OSFS{}, l, uid) {
		t.Fatal("evidence or veto lost")
	}
}
func TestAbandonLocalRefusesEitherRuntimeLock(t *testing.T) {
	for _, which := range []string{"serve", "worker"} {
		t.Run(which, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
			l := state.Layout{Root: root}
			path := l.ServeLock()
			if which == "worker" {
				path = l.WorkerLock()
			}
			lock, ok, err := state.TryLock(path)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			defer lock.Unlock()
			var out, stderr bytes.Buffer
			if code := run([]string{"abandon", "01K00000000000000000000000", "--local"}, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "stop the runtime first") {
				t.Fatal(code, stderr.String())
			}
		})
	}
}

func TestRunProjectIsFrozenInRequest(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	configDir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", configDir)
	if e := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`session="work"
executor="worker"
[kata]
bin="/bin/kata"
[profiles.default]
kind="codex"
[[projects]]
name="example-project"
actor="worker"
[[projects]]
name="team-project"
actor="worker"
`), 0600); e != nil {
		t.Fatal(e)
	}
	l := state.Layout{Root: root}
	f := state.OSFS{}
	if e := state.InitializeLayout(f, l); e != nil {
		t.Fatal(e)
	}
	for _, p := range []struct{ uid, name, job string }{{"project-a", "example-project", "job-a"}, {"project-b", "team-project", "job-b"}} {
		if e := state.WriteJSON(f, l.Project(p.uid).StateFile(), kata.Installation{Version: 1, ProjectUID: p.uid, Name: p.name}); e != nil {
			t.Fatal(e)
		}
		if e := state.WriteJSON(f, l.Project(p.uid).DefsSnapshot(), defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{{UID: p.job, Name: "same-name", Owner: defs.Adapter, Supported: true}}}); e != nil {
			t.Fatal(e)
		}
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
	if code := run([]string{"run", "same-name"}, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "ambiguous") {
		t.Fatal(code, stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if code := run([]string{"run", "--input", "shared input", "same-name", "--project", "team-project"}, &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	if !strings.Contains(out.String(), "project team-project") {
		t.Fatal("run confirmation lacks project", out.String())
	}
	entries, e := os.ReadDir(l.RequestsDir())
	if e != nil || len(entries) != 1 {
		t.Fatal(entries, e)
	}
	var req runner.Request
	if e := state.ReadJSON(f, filepath.Join(l.RequestsDir(), entries[0].Name()), 65536, &req); e != nil || req.Project != "project-b" || req.Job != "job-b" || req.Input == nil || *req.Input != "shared input" {
		t.Fatal(req, e)
	}
}

func TestResumeCLIRequest(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	l := state.Layout{Root: root}
	lock, ok, e := state.TryLock(l.WorkerLock())
	if e != nil || !ok {
		t.Fatal(ok, e)
	}
	defer lock.Unlock()
	if e = state.WriteJSON(state.OSFS{}, l.ServeInfo(), map[string]int{"worker_pid": 0}); e != nil {
		t.Fatal(e)
	}
	uid := "01K00000000000000000000000"
	var out, stderr bytes.Buffer
	responded := acknowledgeNextControl(t, l)
	defer responded()
	if code := run([]string{"resume", uid, "--reset-loops"}, &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	entries, e := os.ReadDir(l.RequestsDir())
	if e != nil || len(entries) != 1 {
		t.Fatal(entries, e)
	}
	var req runner.Request
	if e = state.ReadJSON(state.OSFS{}, filepath.Join(l.RequestsDir(), entries[0].Name()), 4096, &req); e != nil || req.Kind != "resume" || req.RunUID != uid || !req.ResetLoops || !strings.HasPrefix(out.String(), uid+"\n") {
		t.Fatal(req, out.String(), e)
	}
}

func TestRunMissingFlagValue(t *testing.T) {
	for _, tc := range []struct{ flag, message string }{
		{"--input", "--input requires a value"},
		{"-input", "--input requires a value"},
		{"--project", "--project requires a value"},
		{"-project", "--project requires a value"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			var out, stderr bytes.Buffer
			if code := run([]string{"run", "job", tc.flag}, &out, &stderr); code != 2 || !strings.Contains(stderr.String(), tc.message) {
				t.Fatal(code, out.String(), stderr.String())
			}
		})
	}
}

func TestRunSkipsResolutionWithoutLayout(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
	configDir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", configDir)
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("invalid config"), 0600); err != nil {
		t.Fatal(err)
	}
	lock, ok, err := state.TryLock(l.WorkerLock())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer lock.Unlock()
	if err := state.WriteJSON(state.OSFS{}, l.ServeInfo(), map[string]int{"worker_pid": 0}); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"run", "job"}, &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	entries, err := os.ReadDir(l.RequestsDir())
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	var req runner.Request
	if err := state.ReadJSON(state.OSFS{}, filepath.Join(l.RequestsDir(), entries[0].Name()), 64<<10, &req); err != nil || req.Job != "job" || req.Project != "" {
		t.Fatal(req, err)
	}
}

func TestProjectLabelUsesOnlyProjectPin(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	f := state.OSFS{}
	if err := state.WriteJSON(f, l.StateFile(), kata.Installation{Version: 1, ProjectUID: "project-a", Name: "root-project"}); err != nil {
		t.Fatal(err)
	}
	if got := projectLabel(l, "project-a"); got != "project-a" {
		t.Fatalf("label = %q, want project-a", got)
	}
	if err := state.WriteJSON(f, l.Project("project-a").StateFile(), kata.Installation{Version: 1, ProjectUID: "project-a", Name: "example-project"}); err != nil {
		t.Fatal(err)
	}
	if got := projectLabel(l, "project-a"); got != "example-project" {
		t.Fatalf("label = %q, want example-project", got)
	}
}

func TestRunWithProjectBeforeFirstStart(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
	configDir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", configDir)
	text := `session="work"
executor="worker"
[kata]
bin="/missing/kata"
[[projects]]
name="example-project"
actor="worker"
[profiles.default]
kind="codex"
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"run", "known-job", "--project", "example-project"}, &out, &stderr); code != 1 || out.Len() != 0 || stderr.String() != "runtime not running; run `herdr-kata up`\n" {
		t.Fatal(code, out.String(), stderr.String())
	}
	entries, err := os.ReadDir(l.RequestsDir())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("run queued before startup", entries)
	}
}
