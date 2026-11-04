package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func serviceFixture(t *testing.T) (string, string, string) {
	t.Helper()
	home := t.TempDir()
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(key, home)
	}
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"launchctl", "systemctl"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\necho called > \"$HOME/manager-called\"\nexit 99\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	cfg := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfg, []byte("executor = 'worker'\nhc_bin = '/bin/sh'\n[kata]\nbin = '/bin/sh'\ndaemon = 'example-project'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return home, cfg, filepath.Join(home, "state")
}

// Missing service dispatch or a print path that activates/writes breaks this.
func TestServicePrint(t *testing.T) {
	home, cfg, root := serviceFixture(t)
	var out bytes.Buffer
	code := run([]string{"--config", cfg, "--state", root, "service", "install", "--print"}, &out)
	if code != 0 {
		t.Fatalf("print exit %d: %s", code, &out)
	}
	want := "<key>ProgramArguments</key>"
	if runtime.GOOS == "linux" {
		want = "ExecStart=:"
	}
	if !strings.Contains(out.String(), want) || !strings.Contains(out.String(), "--config") || !strings.Contains(out.String(), cfg) {
		t.Fatalf("missing launcher argv: %s", &out)
	}
	for _, path := range []string{"state", "Library", "systemd", "manager-called"} {
		if _, err := os.Stat(filepath.Join(home, path)); !os.IsNotExist(err) {
			t.Fatalf("print side effect %s: %v", path, err)
		}
	}
}

func TestServiceUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"service"}, {"service", "unknown"}, {"service", "uninstall", "--print"}, {"service", "install", "--other"}, {"service", "install", "--print", "extra"}} {
		var out bytes.Buffer
		if code := run(args, &out); code != 2 || !strings.Contains(out.String(), "service") || !strings.Contains(out.String(), "usage:") {
			t.Fatalf("%q: exit/output %d %s", args, code, &out)
		}
	}
}

func TestServiceInvalidConfigWritesNothing(t *testing.T) {
	home, _, root := serviceFixture(t)
	var out bytes.Buffer
	if code := run([]string{"--config", filepath.Join(home, "missing.toml"), "--state", root, "service", "install"}, &out); code != 2 || !strings.Contains(out.String(), "missing.toml") {
		t.Fatalf("exit/output: %d %s", code, &out)
	}
	if _, err := os.Stat(filepath.Join(home, "manager-called")); !os.IsNotExist(err) {
		t.Fatalf("manager called on invalid config: %v", err)
	}
}

func TestServiceCanonicalStateIdentity(t *testing.T) {
	home, cfg, _ := serviceFixture(t)
	if err := os.Mkdir(filepath.Join(home, "real"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(home, "alias")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(home)
	var relative, absolute bytes.Buffer
	if code := run([]string{"--config", "config.toml", "--state", "alias/pending", "service", "install", "--print"}, &relative); code != 0 {
		t.Fatalf("relative alias: %d %s", code, &relative)
	}
	if code := run([]string{"--config", cfg, "--state", filepath.Join(home, "real", "pending"), "service", "install", "--print"}, &absolute); code != 0 {
		t.Fatalf("absolute root: %d %s", code, &absolute)
	}
	if relative.String() != absolute.String() {
		t.Fatalf("same canonical state produced different launchers:\n%s\n%s", &relative, &absolute)
	}
}

func TestDoctorServiceInfoPreservesFailure(t *testing.T) {
	_, cfg, root := serviceFixture(t)
	var out bytes.Buffer
	if code := run([]string{"--config", cfg, "--state", root, "doctor"}, &out); code != 1 || !strings.Contains(out.String(), "FAIL core") || !strings.Contains(out.String(), ": not installed\n") {
		t.Fatalf("doctor exit/output: %d %s", code, &out)
	}
}
