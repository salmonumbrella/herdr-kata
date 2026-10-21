package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestEvalHelpIncludesOperands(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	for _, tc := range []struct{ command, synopsis string }{
		{"run", "run <job> [--project <name>] [--wait] [--input text]"},
		{"resume", "resume <run> [--reset-loops]"}, {"release", "release <run>"},
		{"runs", "runs [--project <name>] [--job <job>] [--limit N]"}, {"logs", "logs <run> [--follow]"},
		{"abandon", "abandon <run> [--local] [--project <name>]"},
	} {
		for _, help := range []string{"--help", "-h"} {
			var out, stderr bytes.Buffer
			if code := run([]string{tc.command, help}, &out, &stderr); code != 0 || !strings.Contains(stderr.String(), "usage: herdr-kata "+tc.synopsis) {
				t.Errorf("%s: %d %s", tc.command, code, stderr.String())
			}
		}
	}
}

func TestEvalControlInvalidUIDBeforeState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	for _, command := range []string{"resume", "release"} {
		for _, uid := range []string{"foo", "../outside", "81K00000000000000000000000"} {
			var out, stderr bytes.Buffer
			if code := run([]string{command, uid}, &out, &stderr); code != 2 || out.Len() != 0 || !strings.Contains(stderr.String(), "usage: herdr-kata "+command+" <run>") {
				t.Errorf("%s %s: %d %s %s", command, uid, code, out.String(), stderr.String())
			}
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("invalid controls wrote state", entries, err)
	}
}

func TestEvalEnvNamesHelpDoesNotWrite(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, help := range []string{"--help", "-h"} {
		var out, stderr bytes.Buffer
		if code := run([]string{"env-names", help}, &out, &stderr); code != 0 || !strings.Contains(stderr.String(), "env-names <path>") {
			t.Errorf("%d %s", code, stderr.String())
		}
		if _, err := os.Stat(help); !os.IsNotExist(err) {
			t.Errorf("help created %s: %v", help, err)
		}
	}
}

func TestEvalAbandonFlagForms(t *testing.T) {
	for _, args := range [][]string{{"-local"}, {"--local=true"}} {
		l := state.Layout{Root: t.TempDir()}
		t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
		t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
		const uid = "01K00000000000000000000000"
		if err := state.WriteJSON(state.OSFS{}, l.RunFile(uid), runner.Run{Version: 1, UID: uid, Phase: runner.Held, Reason: "create_rejected"}); err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		if code := run(append([]string{"abandon", uid}, args...), &out, &stderr); code != 0 {
			t.Errorf("%v: %d %s", args, code, stderr.String())
		}
		if !state.Quarantined(state.OSFS{}, l, uid) {
			t.Error("local abandon did not preserve quarantine")
		}
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"abandon", "01K00000000000000000000000", "--project"}, &out, &stderr); code != 2 || !strings.Contains(stderr.String(), "--project requires a value") {
		t.Fatal(code, stderr.String())
	}
}

func TestEvalReadCommandsRefuseIncompatibleLayout(t *testing.T) {
	for _, marker := range []string{`{"version":2}`, `broken`} {
		l, _, uid, path := logsFixture(t)
		t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
		t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
		if err := os.WriteFile(l.StateFile(), []byte(marker), 0600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"logs", uid}, {"runs"}, {"abandon", uid, "--local"}} {
			var out, stderr bytes.Buffer
			if code := run(args, &out, &stderr); code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "state") {
				t.Errorf("%v: %d %s %s", args, code, out.String(), stderr.String())
			}
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal("layout refusal moved artifacts", err)
		}
	}
}

func TestEvalLogsSkipUnstartedStep(t *testing.T) {
	l, s, uid, _ := logsFixture(t)
	if _, err := s.Transition(uid, func(r *runner.Run) error {
		r.Steps = append(r.Steps, runner.StepState{Key: "never", Kind: "command", Attempt: 1, Status: "skipped", Reason: "dependency_not_succeeded"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := streamRunLogs(t.Context(), l, uid, false, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "[check · attempt 1]\nfirst\n" {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(filepath.Join(l.AttemptDir(uid, "never", 1), "output.log")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestEvalAbandonUnknownProject(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", configDir)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	contents := fmt.Sprintf(`session="work"
executor="worker"
[kata]
bin=%q
[[projects]]
name="example-project"
actor="worker"
[profiles.default]
kind="codex"
`, "/missing/kata")
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	for _, selector := range [][]string{{"--project=missing"}, {"-project", "missing"}} {
		var out, stderr bytes.Buffer
		args := append([]string{"abandon", "01K00000000000000000000000"}, selector...)
		if code := run(args, &out, &stderr); code != 1 || strings.TrimSpace(stderr.String()) != `unknown project "missing"` {
			t.Error(code, stderr.String())
		}
	}
}
