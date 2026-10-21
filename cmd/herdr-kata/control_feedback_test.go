package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestControlsReportWorkerRefusal(t *testing.T) {
	for _, tc := range []struct{ kind, refusal, want string }{
		{"resume", "control request refused: occupants not proven stopped", "control request refused: occupants not proven stopped"},
		{"release", "control request refused: occupants not proven stopped", "control request refused: occupants not proven stopped"},
		{"resume", "missing journal", "local run state unavailable; inspect herdr-kata doctor"},
		{"release", "missing journal", "local run state unavailable; inspect herdr-kata doctor"},
	} {
		t.Run(tc.kind+"/"+tc.refusal, func(t *testing.T) {
			l := state.Layout{Root: t.TempDir()}
			t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
			lock, ok, err := state.TryLock(l.WorkerLock())
			if err != nil || !ok {
				t.Fatal(err)
			}
			defer lock.Unlock()
			if err := state.WriteJSON(state.OSFS{}, l.ServeInfo(), map[string]int{"worker_pid": 0}); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			done := make(chan int, 1)
			go func() { done <- run([]string{tc.kind, "01K00000000000000000000000"}, &out, &stderr) }()
			var req runner.Request
			deadline := time.Now().Add(3 * time.Second)
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				entries, _ := os.ReadDir(l.RequestsDir())
				if len(entries) == 1 && state.ReadJSON(state.OSFS{}, filepath.Join(l.RequestsDir(), entries[0].Name()), 4096, &req) == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("no control request")
				}
				<-ticker.C
			}
			select {
			case code := <-done:
				t.Fatalf("control returned before worker acceptance: %d", code)
			default:
			}
			refusal := tc.refusal
			if refusal == "missing journal" {
				_, err := os.Lstat(l.RunFile(req.RunUID))
				if !os.IsNotExist(err) {
					t.Fatal("expected missing local run", err)
				}
				refusal = err.Error()
			}
			if err := state.WriteJSON(state.OSFS{}, runner.RequestResultPath(l, req.UID), runner.RequestResult{Version: 1, RunUID: req.RunUID, Error: refusal}); err != nil {
				t.Fatal(err)
			}
			select {
			case code := <-done:
				if code != 1 || stderr.String() != tc.want+"\n" {
					t.Fatal(code, stderr.String())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("refusal never reached CLI")
			}
			out.Reset()
			stderr.Reset()
			if code := run([]string{"status"}, &out, &stderr); code != 0 || !strings.Contains(out.String(), tc.want) {
				t.Fatal("status hid refusal", code, out.String(), stderr.String())
			}
		})
	}
}

func TestUpHelpOffersDeliberateTakeover(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := run([]string{"up", "--help"}, &out, &stderr); code != 0 || !strings.Contains(stderr.String(), "takeover") {
		t.Fatal(code, stderr.String())
	}
}

// The request writer talks to a worker through durable files. This responder
// acknowledges that boundary without pretending the CLI owns run transitions.
func acknowledgeNextControl(t *testing.T, l state.Layout) func() {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			entries, err := os.ReadDir(l.RequestsDir())
			if err == nil && len(entries) == 1 {
				var req runner.Request
				if err := state.ReadJSON(state.OSFS{}, filepath.Join(l.RequestsDir(), entries[0].Name()), 64<<10, &req); err == nil {
					done <- state.WriteJSON(state.OSFS{}, runner.RequestResultPath(l, req.UID), runner.RequestResult{Version: 1, RunUID: req.RunUID})
					return
				}
			}
			if time.Now().After(deadline) {
				done <- fmt.Errorf("worker saw no durable request")
				return
			}
			<-ticker.C
		}
	}()
	return func() {
		t.Helper()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

// An unavailable binary keeps this CLI-only check from starting a supervisor.
func TestUpWarnsUnavailableCredential(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", root)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(root, "sessions", "work", "herdr.sock"))
	t.Setenv("KATA_STARTUP_TEST_TOKEN", "")
	contents := `session = "work"
executor = "work-runtime"
[kata]
bin = "/missing/kata"
[[projects]]
name = "example-project"
actor = "worker"
credential_env = "KATA_STARTUP_TEST_TOKEN"
[profiles.default]
kind = "codex"
`
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := run([]string{"up"}, &out, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "warning: project example-project: credential_env KATA_STARTUP_TEST_TOKEN") || !strings.Contains(stderr.String(), "credential_file") {
		t.Fatal("up hid the missing credential and startup-hook remedy", code, stderr.String())
	}
}

// Both waiters must trust the addressed run and summarize local-path evidence.
func TestRunWaitChecksWorkerResult(t *testing.T) {
	for _, tc := range []struct{ name, uid, message, want string }{
		{"path-bearing refusal", "run", "PATH", "local run state unavailable; inspect herdr-kata doctor\n"},
		{"mismatched refusal", "other", "refused", "control response differs from request\n"},
		{"mismatched acceptance", "other", "", "control response differs from request\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := state.Layout{Root: t.TempDir()}
			message := tc.message
			if message == "PATH" {
				message = "lstat " + l.RunFile("run") + ": missing"
			}
			if err := state.WriteJSON(state.OSFS{}, runner.RequestResultPath(l, "request"), runner.RequestResult{Version: 1, RunUID: tc.uid, Error: message}); err != nil {
				t.Fatal(err)
			}
			ended := time.Now()
			if err := state.WriteJSON(state.OSFS{}, l.RunFile("run"), runner.Run{Version: 1, UID: "run", Phase: runner.Settled, Outcome: "succeeded", EndedAt: &ended}); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			if code := awaitRunOutcome(l, runner.Request{UID: "request", RunUID: "run"}, &out, &stderr); code != 1 || stderr.String() != tc.want || out.Len() != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
			}
		})
	}
}
