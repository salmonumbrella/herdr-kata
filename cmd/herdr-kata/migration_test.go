package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/doctor"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestUpMigratesBeforeDiscovery(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "config", "kata")
	data := filepath.Join(root, "state", "kata")
	oldCfg := filepath.Join(root, "config", "salmonumbrella.herdr-kata")
	if err := os.MkdirAll(oldCfg, 0700); err != nil {
		t.Fatal(err)
	}
	config := "session=\"work\"\nexecutor=\"worker\"\n[kata]\nbin=\"/usr/local/bin/kata\"\n[profiles.default]\nkind=\"codex\"\n"
	if err := os.WriteFile(filepath.Join(oldCfg, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", cfg)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", data)
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/sessions/work/herdr.sock")
	var out, stderr bytes.Buffer
	if code := run([]string{"up"}, &out, &stderr); code != 0 || !strings.Contains(out.String(), "nothing started") {
		t.Fatalf("up: %d %s %s", code, out.String(), stderr.String())
	}
	got, err := os.ReadFile(filepath.Join(cfg, "config.toml"))
	if err != nil || string(got) != config {
		t.Fatalf("adopted config: %s %v", got, err)
	}
}
func TestDoctorExplainsPendingMigrationWithoutMovingFiles(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "config", "kata")
	data := filepath.Join(root, "state", "kata")
	oldCfg := filepath.Join(root, "config", "salmonumbrella.herdr-kata")
	if err := os.MkdirAll(oldCfg, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldCfg, "config.toml"), []byte("original config"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", cfg)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", data)
	var out, stderr bytes.Buffer
	if code := run([]string{"doctor", "--json"}, &out, &stderr); code != 1 {
		t.Fatalf("doctor: %d %s %s", code, out.String(), stderr.String())
	}
	var report doctor.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, check := range report.Checks {
		if check.Name == "plugin ID migration" {
			found = !check.Passed && strings.Contains(check.Detail, "pending")
		}
	}
	if !found {
		t.Fatalf("missing migration remedy: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(cfg, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("doctor moved config: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(oldCfg, "config.toml"))
	if err != nil || string(got) != "original config" {
		t.Fatalf("doctor changed legacy config: %s %v", got, err)
	}
}

// Removing the final lane still requires startup to retire its migrated pin.
// A missing Kata binary must reach preflight, rather than report no work.
func TestUpMigratesRemovedLaneBeforeRetirementPreflight(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "config", "kata")
	data := filepath.Join(root, "state", "kata")
	oldCfg := filepath.Join(root, "config", "salmonumbrella.herdr-kata")
	oldData := state.Layout{Root: filepath.Join(root, "state", "salmonumbrella.herdr-kata")}
	if err := os.MkdirAll(oldCfg, 0700); err != nil {
		t.Fatal(err)
	}
	text := "session=\"work\"\nexecutor=\"worker\"\n[kata]\nbin=\"/missing/kata\"\n[profiles.default]\nkind=\"codex\"\n"
	if err := os.WriteFile(filepath.Join(oldCfg, "config.toml"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	if err := state.InitializeLayout(state.OSFS{}, oldData); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(state.OSFS{}, oldData.Project("project").StateFile(), kata.Installation{Version: 1, ProjectUID: "project", Name: "removed-project"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", cfg)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", data)
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/sessions/work/herdr.sock")
	var out, stderr bytes.Buffer
	if code := run([]string{"up"}, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "kata.bin") {
		t.Fatalf("retirement startup skipped: exit=%d stdout=%s stderr=%s", code, out.String(), stderr.String())
	}
	var pin kata.Installation
	if err := state.ReadJSON(state.OSFS{}, state.Layout{Root: data}.Project("project").StateFile(), 65536, &pin); err != nil || pin.Name != "removed-project" {
		t.Fatalf("removed lane pin lost: %+v %v", pin, err)
	}
}
