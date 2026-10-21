package migration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// The upgrade contract requires preserving the complete directory trees and
// their lock inodes, never cloning an executor or overwriting a destination.
func fixture(t *testing.T) (string, string, string, string) {
	t.Helper()
	root := t.TempDir()
	cfg := filepath.Join(root, "config", "kata")
	data := filepath.Join(root, "state", "kata")
	oldCfg := filepath.Join(root, "config", "salmonumbrella.herdr-kata")
	oldData := filepath.Join(root, "state", "salmonumbrella.herdr-kata")
	for _, dir := range []string{cfg, data, oldCfg, oldData} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(oldCfg, "config.toml"), "executor = \"worker\"\n")
	for path, text := range map[string]string{
		"projects/project/state.json":      `{"version":1,"executor":"worker"}`,
		"projects/project/lease.json":      `{"held":true,"generation":7}`,
		"projects/project/defs.json":       `{"jobs":[{"name":"example-job"}]}`,
		"runs/run/steps/main/1/output.log": "original output\n",
		"runs/run/run.json":                `{"version":1,"uid":"run","phase":"settled"}`,
	} {
		write(t, filepath.Join(oldData, path), text)
	}
	return cfg, data, oldCfg, oldData
}
func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func content(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s: %q, %v; want %q", path, got, err, want)
	}
}
func TestAdoptPreservesIdentityHistoryAndLockInodes(t *testing.T) {
	cfg, data, oldCfg, oldData := fixture(t)
	lockPath := filepath.Join(oldData, "worker.lock")
	write(t, lockPath, "")
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if detail, err := Inspect(cfg, data); err == nil || !strings.Contains(detail, "pending") {
		t.Fatalf("pending: %s, %v", detail, err)
	}
	if err := Ensure(cfg, data); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Ensure(cfg, data); err != nil {
			t.Fatal(err)
		}
	}
	content(t, filepath.Join(cfg, "config.toml"), "executor = \"worker\"\n")
	content(t, filepath.Join(oldCfg, "config.toml"), "executor = \"worker\"\n")
	for _, root := range []string{data, oldData} {
		content(t, filepath.Join(root, "projects/project/state.json"), `{"version":1,"executor":"worker"}`)
		content(t, filepath.Join(root, "projects/project/lease.json"), `{"held":true,"generation":7}`)
		content(t, filepath.Join(root, "projects/project/defs.json"), `{"jobs":[{"name":"example-job"}]}`)
		content(t, filepath.Join(root, "runs/run/run.json"), `{"version":1,"uid":"run","phase":"settled"}`)
		content(t, filepath.Join(root, "runs/run/steps/main/1/output.log"), "original output\n")
	}
	after, err := os.Stat(filepath.Join(data, "worker.lock"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("lock replaced: %v", err)
	}
	lock, ok, err := state.TryLock(filepath.Join(data, "worker.lock"))
	if err != nil || !ok {
		t.Fatalf("lock: %t %v", ok, err)
	}
	defer lock.Unlock()
	if held, err := state.Held(filepath.Join(oldData, "worker.lock")); err != nil || !held {
		t.Fatalf("old install escaped shared lock: %t %v", held, err)
	}
	if detail, err := Inspect(cfg, data); err != nil || !strings.Contains(detail, "complete") || !strings.Contains(detail, "aliases") {
		t.Fatalf("complete: %s, %v", detail, err)
	}
}
func TestAdoptRefusesActiveLegacyRuntimeBeforeMovingConfig(t *testing.T) {
	for _, name := range []string{"up.lock", "serve.lock", "worker.lock"} {
		t.Run(name, func(t *testing.T) {
			cfg, data, oldCfg, oldData := fixture(t)
			lock, ok, err := state.TryLock(filepath.Join(oldData, name))
			if err != nil || !ok {
				t.Fatal(err)
			}
			defer lock.Unlock()
			if err := Ensure(cfg, data); err == nil || !strings.Contains(err.Error(), "stop") {
				t.Fatalf("active runtime: %v", err)
			}
			content(t, filepath.Join(oldCfg, "config.toml"), "executor = \"worker\"\n")
			if entries, err := os.ReadDir(cfg); err != nil || len(entries) != 0 {
				t.Fatalf("config moved while live: %v %v", entries, err)
			}
		})
	}
}
func TestAdoptNeverOverwritesOrMergesDestinations(t *testing.T) {
	for _, which := range []string{"config", "state"} {
		t.Run(which, func(t *testing.T) {
			cfg, data, oldCfg, oldData := fixture(t)
			dest := cfg
			if which == "state" {
				dest = data
			}
			write(t, filepath.Join(dest, "keep"), "independent install")
			if err := Ensure(cfg, data); err == nil {
				t.Fatal("merged independent installation")
			}
			content(t, filepath.Join(dest, "keep"), "independent install")
			content(t, filepath.Join(oldCfg, "config.toml"), "executor = \"worker\"\n")
			content(t, filepath.Join(oldData, "runs/run/run.json"), `{"version":1,"uid":"run","phase":"settled"}`)
		})
	}
}
func TestCustomDirectoriesDoNotAdoptSiblingLegacyData(t *testing.T) {
	cfg, data, oldCfg, _ := fixture(t)
	custom := filepath.Join(filepath.Dir(data), "custom")
	if err := Ensure(cfg, custom); err != nil {
		t.Fatal(err)
	}
	content(t, filepath.Join(oldCfg, "config.toml"), "executor = \"worker\"\n")
	if _, err := os.Stat(custom); !os.IsNotExist(err) {
		t.Fatalf("custom directory created: %v", err)
	}
	if detail, err := Inspect(cfg, custom); err != nil || !strings.Contains(detail, "custom") {
		t.Fatalf("custom: %s %v", detail, err)
	}
}
func TestFreshInstallationDoesNotNeedLegacyDirectories(t *testing.T) {
	root := t.TempDir()
	cfg, data := filepath.Join(root, "config", "kata"), filepath.Join(root, "state", "kata")
	if err := Ensure(cfg, data); err != nil {
		t.Fatal(err)
	}
	if detail, err := Inspect(cfg, data); err != nil || !strings.Contains(detail, "no legacy") {
		t.Fatalf("fresh: %s %v", detail, err)
	}
}
