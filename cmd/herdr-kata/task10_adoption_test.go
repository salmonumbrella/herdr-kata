package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/adoption"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

func adoptionSource(t *testing.T) string {
	t.Helper()
	source := t.TempDir()
	old, err := store.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.PutJob(t.Context(), store.Job{ID: "nightly", Name: "Nightly", Prompt: "bermuda issue list; bermuda forum read; bermuda memory search", CWD: "/old/local/checkout", Enabled: true, Schedule: store.ScheduleManual, Kind: "claude", Timeout: time.Minute, Model: "sonnet"}); err != nil {
		t.Fatal(err)
	}
	if err := old.PutRun(t.Context(), store.Run{ID: "old-run", JobID: "nightly", Outcome: "running", TabID: "private-tab", AgentName: "old-agent", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "flows"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "flows", "inspect.yml"), []byte("about: Inspect\nsteps:\n - id: inspect\n   run: git status\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return source
}
func sourceBytes(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	if err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(dir, path)
			out[rel] = string(raw)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// Catches automatic activation, opening the source with the migrating writer,
// lost retained import identity, and adoption of obsolete runtime handles.
func TestRealTask10ReadonlyInactiveAdoption(t *testing.T) {
	c, s := realNativeProduct(t)
	source := adoptionSource(t)
	before := sourceBytes(t, source)
	argv := []string{"import", "--source", source, "--source-id", "example-installation", "--checkout-key", "primary"}
	output, err := captureStdout(t, func() error { return nativeCmd(argv) })
	if err != nil {
		realNativeFailure(t, err)
	}
	if !strings.Contains(output, "obsolete collaboration") {
		t.Fatalf("missing review warning: %s", output)
	}
	jobs, err := s.Jobs(t.Context())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	if jobs[0].Enabled || jobs[0].CWD != c.Target.Workspace || bytes.Contains(jobs[0].NativeDefinition, []byte("/old/local/checkout")) {
		t.Fatalf("unsafe adoption: %+v", jobs[0])
	}
	uid, event := jobs[0].ID, jobs[0].NativeEventUID
	if _, err := captureStdout(t, func() error { return nativeCmd(argv) }); err != nil {
		realNativeFailure(t, err)
	}
	jobs, err = s.Jobs(t.Context())
	if err != nil || len(jobs) != 1 || jobs[0].ID != uid || jobs[0].NativeEventUID != event {
		t.Fatalf("repeat changed identity/revision: %+v %v", jobs, err)
	}
	workflows, err := c.Definitions(t.Context(), "workflow", false)
	if err != nil || len(workflows) != 1 {
		t.Fatalf("workflow=%+v %v", workflows, err)
	}
	if bytes.Contains(workflows[0].Definition, []byte(source)) {
		t.Fatal("native workflow retains source path")
	}
	runs, err := s.Runs(context.Background(), "", 100)
	if err != nil || len(runs) != 0 {
		t.Fatalf("local runtime imported: %+v %v", runs, err)
	}
	var history struct{ Runs []any }
	if err := c.Runs(t.Context(), "", "", &history); err != nil || len(history.Runs) != 0 {
		t.Fatalf("history imported: %+v %v", history, err)
	}
	after := sourceBytes(t, source)
	if len(after) != len(before) {
		t.Fatalf("source entries changed: %v", after)
	}
	for path, raw := range before {
		if after[path] != raw {
			t.Fatalf("source changed: %s", path)
		}
	}
}

func TestTask10ImportRequiresExplicitSourceIdentity(t *testing.T) {
	t.Setenv("HERDR_KATA_HOME", t.TempDir())
	err := nativeCmd([]string{"import", "--source", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "source-id") {
		t.Fatalf("implicit import accepted/wrong diagnostic: %v", err)
	}
}

func TestTask10ImportRefusesSourceAliasOfInstallation(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlink availability varies on Windows")
	}
	destination := t.TempDir()
	t.Setenv("HERDR_KATA_HOME", destination)
	alias := filepath.Join(t.TempDir(), "source")
	if err := os.Symlink(destination, alias); err != nil {
		t.Fatal(err)
	}
	err := nativeCmd([]string{"import", "--source", alias, "--source-id", "example-installation", "--checkout-key", "primary"})
	if err == nil || !strings.Contains(err.Error(), "separate offline snapshot") {
		t.Fatalf("source alias entered destination writer: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "herdr-kata.db")); !os.IsNotExist(err) {
		t.Fatalf("source alias created store: %v", err)
	}
}

func TestRealTask10InterruptedImportAndChangedSourceConflict(t *testing.T) {
	c, s := realNativeProduct(t)
	source := adoptionSource(t)
	plan, err := adoption.Build(t.Context(), source, "example-installation", "primary", "", s.Native)
	if err != nil {
		t.Fatal(err)
	}
	// This is the state after losing the importer immediately after its first
	// accepted ordinary create. There is no imported local receipt or journal.
	first, err := c.Save(t.Context(), plan.Drafts[0])
	if err != nil {
		realNativeFailure(t, err)
	}
	argv := []string{"import", "--source", source, "--source-id", "example-installation", "--checkout-key", "primary"}
	if _, err := captureStdout(t, func() error { return nativeCmd(argv) }); err != nil {
		realNativeFailure(t, err)
	}
	current, err := c.Definition(t.Context(), "workflow", first.UID)
	if err != nil || current.DefinitionEventUID != first.DefinitionEventUID {
		t.Fatalf("partial import rewrote accepted workflow: %+v %v", current, err)
	}
	jobs, err := s.Jobs(t.Context())
	if err != nil || len(jobs) != 1 || jobs[0].Enabled {
		t.Fatalf("interrupted import result: %+v %v", jobs, err)
	}
	// Explicit activation is local operator intent; retry must not undo it.
	if err := s.SetEnabled(t.Context(), jobs[0].ID, true); err != nil {
		realNativeFailure(t, err)
	}
	moved := filepath.Join(t.TempDir(), "moved-snapshot")
	if err := os.Rename(source, moved); err != nil {
		t.Fatal(err)
	}
	argv[2] = moved
	if _, err := captureStdout(t, func() error { return nativeCmd(argv) }); err != nil {
		realNativeFailure(t, err)
	}
	if active, err := s.Native.IsActivated(jobs[0].ID); err != nil || !active {
		t.Fatalf("retry changed local activation: %v %v", active, err)
	}
	if err := os.WriteFile(filepath.Join(moved, "flows", "inspect.yml"), []byte("about: Changed\nsteps:\n - id: inspect\n   run: git diff\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := captureStdout(t, func() error { return nativeCmd(argv) }); err == nil {
		t.Fatal("changed source overwrote an accepted definition")
	}
	current, err = c.Definition(t.Context(), "workflow", first.UID)
	if err != nil || current.DefinitionEventUID != first.DefinitionEventUID {
		t.Fatal("conflict changed native winner")
	}
}

func TestTask10ImportRefusesInstallationInsideSource(t *testing.T) {
	source := t.TempDir()
	t.Setenv("HERDR_KATA_HOME", filepath.Join(source, "local-installation"))
	err := nativeCmd([]string{"import", "--source", source, "--source-id", "example-installation", "--checkout-key", "primary"})
	if err == nil || !strings.Contains(err.Error(), "separate offline snapshot") {
		t.Fatalf("destination writer entered source: %v", err)
	}
	entries, err := os.ReadDir(source)
	if err != nil || len(entries) != 0 {
		t.Fatalf("source was modified: %v %v", entries, err)
	}
}

func TestRealTask10WorkflowReferenceAndExplicitCronTimezone(t *testing.T) {
	c, s := realNativeProduct(t)
	source := adoptionSource(t)
	old, err := store.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.PutJob(t.Context(), store.Job{ID: "nightly", Name: "Nightly", Workflow: "inspect", Input: "example-input", CWD: "/old/local/checkout", Enabled: true, Schedule: store.ScheduleCron, CronExpr: "0 7 * * *", Persistent: true, KeepContext: true}); err != nil {
		t.Fatal(err)
	}
	old.Close()
	argv := []string{"import", "--source", source, "--source-id", "example-installation", "--checkout-key", "primary"}
	if _, err := captureStdout(t, func() error { return nativeCmd(argv) }); err == nil || !strings.Contains(err.Error(), "cron-timezone") {
		t.Fatalf("implicit source timezone accepted: %v", err)
	}
	workflows, err := c.Definitions(t.Context(), "workflow", false)
	if err != nil || len(workflows) != 0 {
		t.Fatalf("invalid plan saved partial workflow: %+v %v", workflows, err)
	}
	argv = append(argv, "--cron-timezone", "Europe/London")
	if _, err := captureStdout(t, func() error { return nativeCmd(argv) }); err != nil {
		realNativeFailure(t, err)
	}
	jobs, err := s.Jobs(t.Context())
	if err != nil || len(jobs) != 1 || jobs[0].Enabled || !jobs[0].Persistent || !jobs[0].KeepContext || jobs[0].Input != "example-input" {
		t.Fatalf("workflow job projection: %+v %v", jobs, err)
	}
	workflows, err = c.Definitions(t.Context(), "workflow", false)
	if err != nil || len(workflows) != 1 || jobs[0].Workflow != workflows[0].UID {
		t.Fatalf("workflow reference identity mismatch: %+v %v", workflows, err)
	}
	var body struct {
		Trigger struct{ Kind, Cron, Timezone string }
	}
	if err := json.Unmarshal(jobs[0].NativeDefinition, &body); err != nil || body.Trigger.Kind != "cron" || body.Trigger.Cron != "0 7 * * *" || body.Trigger.Timezone != "Europe/London" {
		t.Fatalf("cron timezone: %+v %v", body, err)
	}
	if _, err := captureStdout(t, func() error { return nativeCmd(argv) }); err != nil {
		realNativeFailure(t, err)
	}
}
