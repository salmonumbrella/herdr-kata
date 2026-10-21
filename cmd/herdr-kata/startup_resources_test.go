package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/doctor"
)

// If validation regresses, contain the staged subprocess: acknowledge serve
// without starting a supervisor or worker.
func init() {
	if marker := os.Getenv("HK_TEST_UP_RESOURCE_START"); marker != "" && len(os.Args) == 2 && os.Args[1] == "serve" {
		_ = os.WriteFile(marker, nil, 0600)
		os.Exit(0)
	}
}

func startupResourceConfig(t *testing.T) (root, bin, checkout string) {
	t.Helper()
	root = t.TempDir()
	bin, checkout = filepath.Join(root, "kata"), filepath.Join(root, "checkout")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(checkout, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", root)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(root, "sessions", "work", "herdr.sock"))
	t.Setenv("HK_TEST_UP_RESOURCE_START", filepath.Join(root, "started"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	return root, bin, checkout
}

func writeStartupConfig(t *testing.T, root, bin, checkout string) {
	t.Helper()
	data := fmt.Sprintf(`session = "work"
executor = "example-runtime"
[kata]
bin = %q
env = { KATA_HOME = %q }
[[projects]]
name = "example-project"
actor = "worker"
[checkouts]
repo = %q
[profiles.default]
kind = "codex"
`, bin, filepath.Join(root, "kata-home"), checkout)
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestUpRefusesInvalidLocalResourcesWithDoctorMessage(t *testing.T) {
	for _, fault := range []string{"missing binary", "nonexecutable binary", "binary directory", "missing checkout", "checkout file"} {
		t.Run(fault, func(t *testing.T) {
			root, bin, checkout := startupResourceConfig(t)
			resource := "kata.bin"
			switch fault {
			case "missing binary":
				bin = filepath.Join(root, "missing-kata")
			case "nonexecutable binary":
				if err := os.Chmod(bin, 0600); err != nil {
					t.Fatal(err)
				}
			case "binary directory":
				bin = checkout
			case "missing checkout":
				checkout, resource = filepath.Join(root, "missing-checkout"), "checkout repo"
			case "checkout file":
				checkout, resource = bin, "checkout repo"
			}
			writeStartupConfig(t, root, bin, checkout)
			var out, stderr bytes.Buffer
			if code := run([]string{"up"}, &out, &stderr); code != 1 {
				t.Fatalf("up exit %d, stdout %q, stderr %q", code, out.String(), stderr.String())
			}
			if out.Len() != 0 {
				t.Fatal("refusal printed startup success", out.String())
			}
			for _, name := range []string{"state", "started"} {
				if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
					t.Fatal("invalid startup created", name, err)
				}
			}
			upError := strings.TrimSpace(stderr.String())
			out.Reset()
			stderr.Reset()
			if code := run([]string{"doctor", "--json"}, &out, &stderr); code != 1 {
				t.Fatal("doctor did not reject invalid resource", code, out.String(), stderr.String())
			}
			var report doctor.Report
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err, out.String())
			}
			for _, check := range report.Checks {
				if check.Name == resource && !check.Passed && upError == check.Name+": "+check.Detail {
					return
				}
			}
			t.Fatal("up did not use doctor's resource message", upError, report)
		})
	}
}

func TestUpWrongSessionSkipsResourcePreflight(t *testing.T) {
	root, _, checkout := startupResourceConfig(t)
	writeStartupConfig(t, root, filepath.Join(root, "missing-kata"), checkout)
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(root, "sessions", "other", "herdr.sock"))
	var out, stderr bytes.Buffer
	if code := run([]string{"up"}, &out, &stderr); code != 0 || out.Len() != 0 || stderr.Len() != 0 {
		t.Fatal("wrong session did not quietly skip startup", code, out.String(), stderr.String())
	}
}

func TestMissingConfigExplainsSetup(t *testing.T) {
	for _, args := range [][]string{{"up"}, {"doctor"}, {"doctor", "--json"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := t.TempDir()
			configDir := filepath.Join(root, "configuration")
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", configDir)
			t.Setenv("HERDR_PLUGIN_STATE_DIR", filepath.Join(root, "state"))
			// The test is about config.Load's setup guidance. An inherited
			// named Herdr socket would intentionally trigger first-start
			// discovery for `up` before config.Load runs.
			t.Setenv("HERDR_SOCKET_PATH", filepath.Join(root, "not-a-herdr-socket"))
			var out, stderr bytes.Buffer
			if code := run(args, &out, &stderr); code != 1 {
				t.Fatal("missing config did not fail", code, out.String(), stderr.String())
			}
			message := stderr.String()
			if len(args) == 2 {
				var report doctor.Report
				if err := json.Unmarshal(out.Bytes(), &report); err != nil || len(report.Checks) != 1 || report.Checks[0].Name != "config" || report.Checks[0].Passed {
					t.Fatal("invalid JSON config diagnostic", err, out.String())
				}
				message = report.Checks[0].Detail
			}
			for _, want := range []string{filepath.Join(configDir, "config.toml"), "create it; see docs/get-started/quickstart.md"} {
				if !strings.Contains(message, want) {
					t.Errorf("missing %q in setup message: %s", want, message)
				}
			}
			_, example, found := strings.Cut(message, "```toml\n")
			example, _, closed := strings.Cut(example, "```")
			var setup struct {
				Session, Executor string
				Kata              struct {
					Bin string
					Env map[string]string
				}
				Projects []struct{ Name, Actor string }
			}
			if _, err := toml.Decode(example, &setup); !found || !closed || err != nil || setup.Session == "" || setup.Executor == "" || !filepath.IsAbs(setup.Kata.Bin) || setup.Kata.Env["KATA_HOME"] == "" || len(setup.Projects) != 1 || setup.Projects[0].Name == "" || setup.Projects[0].Actor == "" {
				t.Fatal("setup message has no usable minimal projects example", message, err)
			}
			examplePath := filepath.Join(root, "example.toml")
			if err := os.WriteFile(examplePath, []byte(example), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Load(examplePath); err != nil {
				t.Fatal("setup example is not a valid configuration", err)
			}
			if _, err := os.Stat(configDir); !os.IsNotExist(err) {
				t.Fatal("missing-config diagnostic created configuration", err)
			}
			if _, err := os.Stat(filepath.Join(root, "state")); !os.IsNotExist(err) {
				t.Fatal("missing-config diagnostic created runtime state", err)
			}
		})
	}
}
