package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

const elsewhereRefusal = "another installation (elsewhere) owns this lane: lease generation 2"

// stoodDownRuntime fakes a running worker whose lane "project" stood down
// after another installation took it over at generation 2.
func stoodDownRuntime(t *testing.T) state.Layout {
	t.Helper()
	l := state.Layout{Root: t.TempDir()}
	t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	lock, ok, err := state.TryLock(l.WorkerLock())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	t.Cleanup(func() { _ = lock.Unlock() })
	started := time.Now().UTC().Add(-time.Minute)
	if err := state.WriteJSON(state.OSFS{}, l.ServeInfo(), map[string]any{"worker_pid": 0, "started_at": started}); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(state.OSFS{}, filepath.Join(l.Project("project").Root, "lease.json"), map[string]any{"held": false, "owner": "elsewhere", "generation": 2, "heartbeat": started, "error": "another installation (elsewhere) took this lane over at lease generation 2", "checked_at": started.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	return l
}

// The stood-down installation refuses run at once, with up's wording, and
// run --wait exits non-zero well within 2 s instead of waiting it out.
func TestRunRefusedWhenLaneLeaseHeldElsewhere(t *testing.T) {
	l := stoodDownRuntime(t)
	for _, args := range [][]string{{"run", "job", "--project", "project"}, {"run", "job", "--project", "project", "--wait"}} {
		var out, stderr bytes.Buffer
		begun := time.Now()
		code := run(args, &out, &stderr)
		if elapsed := time.Since(begun); code == 0 || elapsed > 2*time.Second {
			t.Fatalf("%v: code %d after %s", args, code, elapsed)
		}
		if strings.TrimSpace(stderr.String()) != elsewhereRefusal || strings.Contains(out.String(), "run requested") {
			t.Fatalf("%v: stdout %q stderr %q", args, out.String(), stderr.String())
		}
	}
	if entries, _ := os.ReadDir(l.RequestsDir()); len(entries) != 0 {
		t.Fatal("refused run was queued", len(entries))
	}
}

// Resume and release of a run on the stood-down lane are refused before a
// request is written; the TUI's u action gets the same refusal (R shares
// run's request path).
func TestStoodDownLaneRefusesControls(t *testing.T) {
	l := stoodDownRuntime(t)
	const uid = "01K00000000000000000000009"
	if err := state.WriteJSON(state.OSFS{}, l.RunFile(uid), runner.Run{Version: 1, UID: uid, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project"}}}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"resume", uid}, {"release", uid}} {
		var out, stderr bytes.Buffer
		if code := run(args, &out, &stderr); code != 1 || strings.TrimSpace(stderr.String()) != elsewhereRefusal {
			t.Fatalf("%v: code %d stdout %q stderr %q", args, code, out.String(), stderr.String())
		}
	}
	s := &tuiService{projectUID: "project"}
	if err := s.Resume(uid); err == nil || err.Error() != elsewhereRefusal {
		t.Fatalf("TUI resume: %v", err)
	}
	if entries, _ := os.ReadDir(l.RequestsDir()); len(entries) != 0 {
		t.Fatal("refused controls were queued", len(entries))
	}
}

// A run queued before the takeover is settled as refused by the worker; its
// waiter prints the refusal and returns promptly.
func TestStoodDownLaneRefusesQueuedRunWaiter(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	l := state.Layout{Root: root}
	lock, ok, err := state.TryLock(l.WorkerLock())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer lock.Unlock()
	if err := state.WriteJSON(state.OSFS{}, l.ServeInfo(), map[string]int{"worker_pid": 0}); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run([]string{"run", "job", "--wait"}, &out, &stderr) }()
	var req runner.Request
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for until := time.Now().Add(10 * time.Second); ; <-tick.C {
		entries, _ := os.ReadDir(l.RequestsDir())
		if len(entries) == 1 && state.ReadJSON(state.OSFS{}, filepath.Join(l.RequestsDir(), entries[0].Name()), 4096, &req) == nil {
			break
		}
		if time.Now().After(until) {
			t.Fatal("request not published")
		}
	}
	// With no result and no journal the waiter keeps waiting, as a queued
	// run's would.
	select {
	case code := <-done:
		t.Fatal("waiter returned before a result", code, stderr.String())
	default:
	}
	settled := time.Now()
	if err := state.WriteJSON(state.OSFS{}, runner.RequestResultPath(l, req.UID), runner.RequestResult{Version: 1, Kind: "run", RunUID: req.RunUID, Error: elsewhereRefusal, Reason: runner.ReasonLeaseHeldElsewhere}); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 1 || strings.TrimSpace(stderr.String()) != elsewhereRefusal {
			t.Fatal(code, out.String(), stderr.String())
		}
		if elapsed := time.Since(settled); elapsed > time.Second {
			t.Fatalf("waiter returned %s after the refusal", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("waiter did not return after the refusal")
	}
}

// Abandon reads the lane lease itself, since it writes to Kata without the
// runtime: a live lease of another installation refuses it before any write.
func TestAbandonRefusedWhenLaneLeaseHeldElsewhere(t *testing.T) {
	root := t.TempDir()
	l := state.Layout{Root: filepath.Join(root, "state")}
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", root)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
	const uid = "01K00000000000000000000000"
	bin := filepath.Join(root, "kata")
	heartbeat := time.Now().UTC().Format(time.RFC3339)
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
 *" health"*) printf '%%s\n' '{"version":"0.19.0","schema_version":33}' ;;
 *"cron capabilities"*) printf '%%s\n' '{"project_uid":"project","event_features":["cron_v1"]}' ;;
 *"federation identity"*) printf '%%s\n' '{"instance_uid":"instance","auth":{"actor":"worker"}}' ;;
 *"daemon diagnose"*) printf '%%s\n' '{"state":"ready","endpoint":"local","project_uid":"project"}' ;;
 *"list --status all --label herdr-kata:lease"*) printf '%%s\n' '{"issues":[{"uid":"lease","status":"closed","revision":4,"metadata":{"herdr_kata.lease":{"version":1,"owner":"elsewhere","generation":2,"heartbeat":%q}}}]}' ;;
 *) printf '%%s\n' "$*" >> "$0.unexpected"; exit 9 ;;
esac
`, heartbeat)
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfgText := fmt.Sprintf("session=\"work\"\nexecutor=\"installation\"\n[kata]\nbin=%q\n[[projects]]\nname=\"example-project\"\nactor=\"worker\"\n[profiles.default]\nkind=\"codex\"\n", bin)
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(cfgText), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(root, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	view, err := cfg.SelectProject("example-project")
	if err != nil {
		t.Fatal(err)
	}
	k := kata.New(view.Kata, l.Root, nil, nil)
	if err := state.InitializeLayout(state.OSFS{}, l); err != nil {
		t.Fatal(err)
	}
	if err := kata.NewRouteGuard(view, k, state.OSFS{}, l.Project("project")).Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"abandon", uid, "--project", "example-project"}, &out, &stderr); code != 1 || strings.TrimSpace(stderr.String()) != elsewhereRefusal {
		t.Fatal(code, out.String(), stderr.String())
	}
	if unexpected, err := os.ReadFile(bin + ".unexpected"); err == nil {
		t.Fatalf("abandon called Kata after the refusal: %s", unexpected)
	}
}
