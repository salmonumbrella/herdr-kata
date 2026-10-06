//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstalledHookWorksWithRelativeStateOverride(t *testing.T) {
	// The shell's PWD is physical. Use the same directory spelling for this
	// relative-path test on macOS, where the default temp root is a symlink.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)
	dir := "state"
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	_, e := hookEvent(t, dir)
	writeHook(t, dir, "cat > received.json\nprintf '%s\\n' \"$PWD\" \"$HERDR_KATA_HOME\" > paths.txt\nprintf ok > \"$HERDR_KATA_HOME/via-env.txt\"\n")
	skipped, err := runSettledHook(context.Background(), stateDir(), e, time.Second)
	if err != nil || skipped {
		t.Fatalf("installed hook must execute with relative HERDR_KATA_HOME: skipped=%v err=%v", skipped, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "received.json")); err != nil {
		t.Fatal(err)
	}

	want, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := os.ReadFile(filepath.Join(dir, "paths.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(paths) != strings.Repeat(want+"\n", 2) {
		t.Fatalf("hook cwd/state paths=%q, want %q", paths, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "via-env.txt")); err != nil {
		t.Fatal(err)
	}
}
