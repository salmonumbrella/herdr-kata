package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestForkStateIsolation(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, ".bermuda")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("BERMUDA_STATE_DIR", legacy)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", filepath.Join(home, "plugin"))
	t.Setenv("HERDR_KATA_HOME", "")
	if got, want := stateDir(), filepath.Join(home, ".herdr-kata"); got != want {
		t.Fatalf("default state = %q, want isolated fork state %q", got, want)
	}
	override := t.TempDir()
	t.Setenv("HERDR_KATA_HOME", override)
	if got := stateDir(); got != override {
		t.Fatalf("override state = %q, want %q", got, override)
	}
}

func TestForkExecutableIdentity(t *testing.T) {
	if os.Getenv("HERDR_KATA_IDENTITY_CHILD") == "1" {
		os.Args = []string{"herdr-kata", "--version"}
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestForkExecutableIdentity$")
	cmd.Env = append(os.Environ(), "HERDR_KATA_IDENTITY_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("version command: %v: %s", err, out)
	}
	if !strings.HasPrefix(string(out), "herdr-kata ") {
		t.Fatalf("version identifies another product: %s", out)
	}
}
