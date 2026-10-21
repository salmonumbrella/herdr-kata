package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestEvalRunRejectsIneligibleSnapshotWithoutWait(t *testing.T) {
	for _, tc := range []struct {
		name      string
		owner     defs.Owner
		supported bool
		reasons   []string
		want      string
	}{
		{"native", defs.Native, true, nil, "owned by Kata's native scheduler"},
		{"unsupported", defs.Adapter, false, []string{"checkout missing"}, "checkout missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := state.Layout{Root: t.TempDir()}
			configDir := t.TempDir()
			t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
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
			f := state.OSFS{}
			if err := state.InitializeLayout(f, l); err != nil {
				t.Fatal(err)
			}
			if err := state.WriteJSON(f, l.Project("project").StateFile(), kata.Installation{Version: 1, Name: "example-project", ProjectUID: "project"}); err != nil {
				t.Fatal(err)
			}
			snapshot := defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{{UID: "job", Name: "job", Owner: tc.owner, Supported: tc.supported, Reasons: tc.reasons}}}
			if err := state.WriteJSON(f, l.Project("project").DefsSnapshot(), snapshot); err != nil {
				t.Fatal(err)
			}
			lock, ok, err := state.TryLock(l.WorkerLock())
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			defer lock.Unlock()
			if err := state.WriteJSON(f, l.ServeInfo(), map[string]int{"worker_pid": 0}); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			if code := run([]string{"run", "job"}, &out, &stderr); code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), tc.want) {
				t.Error(code, out.String(), stderr.String())
			}
			entries, err := os.ReadDir(l.RequestsDir())
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Error("refused job queued", entries)
			}
		})
	}
}

func TestEvalUnknownJobWording(t *testing.T) {
	cfg := config.Config{Projects: []config.Project{{Name: "example-project", Actor: "worker"}}}
	_, _, runErr := resolveRunJob(t.Context(), cfg, state.OSFS{}, state.Layout{Root: t.TempDir()}, "missing-job", "")
	var out bytes.Buffer
	runsErr := listRunsProjects(t.Context(), cfg, "", "missing-job", 10, &out, func(config.Config) runsReader { return &runsKata{} })
	for _, err := range []error{runErr, runsErr} {
		if err == nil || err.Error() != `unknown job "missing-job"` {
			t.Error(err)
		}
	}
}

func TestEvalWaitShowsHeldReason(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	now := time.Now()
	const uid = "01K00000000000000000000000"
	r := runner.Run{Version: 1, UID: uid, Phase: runner.Held, Outcome: "failed", Reason: "step_failed", EndedAt: &now}
	if err := state.WriteJSON(state.OSFS{}, l.RunFile(uid), r); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := awaitRunOutcome(l, runner.Request{UID: "request", RunUID: uid}, &out, &stderr); code != 1 || out.String() != uid+": held (step_failed)\n" {
		t.Fatal(code, out.String(), stderr.String())
	}
}

func TestEvalStatusOnlyCurrentControlRefusals(t *testing.T) {
	for _, changed := range []string{"unchanged", "journal advanced", "accepted control", "later refusal"} {
		t.Run(changed, func(t *testing.T) {
			l := state.Layout{Root: t.TempDir()}
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
			const uid = "01K00000000000000000000000"
			r := runner.Run{Version: 1, UID: uid, Phase: runner.Held, Reason: "step_failed"}
			if err := state.WriteJSON(state.OSFS{}, l.RunFile(uid), r); err != nil {
				t.Fatal(err)
			}
			path := runner.RequestResultPath(l, "request")
			result := runner.RequestResult{Version: 1, RunUID: uid, Error: "control request refused: occupants not proven stopped"}
			if err := state.WriteJSON(state.OSFS{}, path, result); err != nil {
				t.Fatal(err)
			}
			before := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			middle := before.Add(time.Minute)
			after := middle.Add(time.Minute)
			runTime, resultTime := before, middle
			if changed == "journal advanced" {
				runTime = after
			}
			if changed == "later refusal" {
				runTime = middle
				resultTime = after
			}
			if err := os.Chtimes(l.RunFile(uid), runTime, runTime); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, resultTime, resultTime); err != nil {
				t.Fatal(err)
			}
			if changed == "accepted control" {
				accepted := runner.RequestResultPath(l, "accepted")
				if err := state.WriteJSON(state.OSFS{}, accepted, runner.RequestResult{Version: 1, RunUID: uid}); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(accepted, after, after); err != nil {
					t.Fatal(err)
				}
			}
			for _, asJSON := range []bool{false, true} {
				var out, stderr bytes.Buffer
				code := statusCommand(l, asJSON, false, &out, &stderr)
				want := changed == "unchanged" || changed == "later refusal"
				if code != 0 || strings.Contains(out.String(), result.Error) != want {
					t.Error(changed, asJSON, code, out.String(), stderr.String())
				}
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("status removed acknowledgement evidence", err)
			}
		})
	}
}

func TestEvalStatusHidesLegacyControlPaths(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	const uid = "01K00000000000000000000000"
	for i, ref := range []string{uid, "bad"} {
		result := runner.RequestResult{Version: 1, RunUID: ref, Error: fmt.Sprintf("lstat %s: no such file or directory", l.RunFile(ref))}
		if err := state.WriteJSON(state.OSFS{}, runner.RequestResultPath(l, fmt.Sprint(i)), result); err != nil {
			t.Fatal(err)
		}
	}
	for _, asJSON := range []bool{false, true} {
		var out, stderr bytes.Buffer
		if code := statusCommand(l, asJSON, false, &out, &stderr); code != 0 || strings.Contains(out.String(), l.Root) || strings.Contains(out.String(), "lstat ") {
			t.Error(code, out.String(), stderr.String())
		}
	}
}

func TestEvalLocalAbandonAcknowledgesNewCorruption(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	const uid = "01K00000000000000000000000"
	if err := os.MkdirAll(l.RunDir(uid), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.RunFile(uid), []byte("broken journal"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"abandon", uid, "--local"}, &out, &stderr); code != 0 || !strings.Contains(out.String(), "quarantined (local resolution)") {
		t.Error(code, out.String(), stderr.String())
	}
	var ack struct {
		Acknowledged bool `json:"acknowledged"`
	}
	if err := state.ReadJSON(state.OSFS{}, filepath.Join(l.QuarantineRunDir(uid), "acknowledged.json"), 4096, &ack); err != nil || !ack.Acknowledged {
		t.Error("corruption not acknowledged", err)
	}
	entries, err := os.ReadDir(l.QuarantineRunDir(uid))
	if err != nil {
		t.Fatal(err)
	}
	preserved := false
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(l.QuarantineRunDir(uid), entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		preserved = preserved || string(data) == "broken journal"
	}
	if !preserved {
		t.Fatal("corrupt evidence lost")
	}
}

// Fail the first quarantine directory sync after the journal has moved. A retry
// would succeed and create acknowledged.json, hiding this durability failure.
type corruptJournalSyncFault struct {
	state.OSFS
	journal string
	moved   bool
	failed  bool
}

func (f *corruptJournalSyncFault) Rename(from, to string) error {
	if err := f.OSFS.Rename(from, to); err != nil {
		return err
	}
	if from == f.journal {
		f.moved = true
	}
	return nil
}
func (f *corruptJournalSyncFault) SyncDir(path string) error {
	if f.moved && !f.failed {
		f.failed = true
		return errors.New("quarantine directory sync failed")
	}
	return f.OSFS.SyncDir(path)
}
func TestEvalLocalAbandonPreservesQuarantineSyncFailure(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	const uid = "01K00000000000000000000000"
	if err := os.MkdirAll(l.RunDir(uid), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.RunFile(uid), []byte("broken journal"), 0600); err != nil {
		t.Fatal(err)
	}
	fsys := &corruptJournalSyncFault{journal: l.RunFile(uid)}
	var out, stderr bytes.Buffer
	code := abandonWithFS([]string{uid, "--local"}, &out, &stderr, fsys)
	if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "corrupt state") || !strings.Contains(stderr.String(), "quarantine directory sync failed") {
		t.Error(code, out.String(), stderr.String())
	}
	if !fsys.failed || !state.Quarantined(state.OSFS{}, l, uid) {
		t.Fatal("fault did not preserve the quarantined journal")
	}
	if _, err := os.Stat(filepath.Join(l.QuarantineRunDir(uid), "acknowledged.json")); !os.IsNotExist(err) {
		t.Fatal("failed preservation was acknowledged", err)
	}
}
