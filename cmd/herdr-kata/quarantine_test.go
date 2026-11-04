package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/control"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestServeQuarantineWarningsAfterLockBeforeTick(t *testing.T) {
	_, l := operatorFixture(t)
	c := config.Config{Quarantined: []config.Quarantine{{Entry: 2, Name: "broken-project", Reason: "name and actor are required"}, {Entry: 3, Reason: "not a table"}}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out bytes.Buffer
	if err := serve(ctx, c, journal.New(l), nil, nil, "/bin/false", &out); err != nil {
		t.Fatal(err)
	}
	want := "warning: quarantined project entry 2 (broken-project): name and actor are required\nwarning: quarantined project entry 3: not a table\ncontext canceled\n"
	if out.String() != want {
		t.Fatalf("output %q, want %q", out.String(), want)
	}
	if _, err := os.Stat(l.PID()); !os.IsNotExist(err) {
		t.Fatalf("PID not cleaned up: %v", err)
	}
	release, err := journal.Lock(l.Lock())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	out.Reset()
	if err := serve(ctx, c, journal.New(l), nil, nil, "/bin/false", &out); err == nil {
		t.Fatal("second worker acquired lock")
	}
	if out.Len() != 0 {
		t.Fatalf("locked serve warned: %q", out.String())
	}
}

func appendBadProject(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = fmt.Fprint(f, "\n[[projects]]\nname=\"broken-project\"\n"); err != nil {
		t.Fatal(err)
	}
}

func TestUpQuarantineWarningsBeforeWorkerAddress(t *testing.T) {
	path, l := operatorFixture(t)
	appendBadProject(t, path)
	root, err := canonical(l.Root)
	if err != nil {
		t.Fatal(err)
	}
	l = state.Layout{Root: root}
	release, err := journal.Lock(l.Lock())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := state.WriteFile(l.PID(), fmt.Appendf(nil, "%d\n", os.Getpid())); err != nil {
		t.Fatal(err)
	}
	// Only the exact get for the already locked worker can succeed. Every other
	// hc operation fails, so this test cannot accidentally launch a real worker.
	bin := filepath.Join(t.TempDir(), "fake-hc")
	script := fmt.Sprintf(`#!/bin/sh
[ "$#" = 3 ] && [ "$1" = get ] && [ "$2" = --json ] && [ "$3" = %s ] || exit 9
printf '%%s\n' '{"address":"t-worker","state":"live","foreground":{"pid":%d},"labels":{"kata.worker":"%s"}}'
`, control.Name(l.Root), os.Getpid(), control.Name(l.Root))
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(b), `hc_bin="/bin/false"`, `hc_bin="`+bin+`"`, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := run([]string{"--config", path, "--state", l.Root, "up"}, &out)
	want := "warning: quarantined project entry 2 (broken-project): name and actor are required\nworker t-worker\n"
	if code != 0 || out.String() != want {
		t.Fatalf("up exit=%d output=%q, want %q", code, out.String(), want)
	}
}

func TestFailedUpHasNoQuarantineWarnings(t *testing.T) {
	path, l := operatorFixture(t)
	appendBadProject(t, path)
	var out bytes.Buffer
	if code := run([]string{"--config", path, "--state", l.Root, "up"}, &out); code != 1 || strings.Contains(out.String(), "quarantined") {
		t.Fatalf("up exit=%d output=%q", code, out.String())
	}
}

func TestDoctorQuarantineExitOne(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	path, l := operatorFixture(t)
	appendBadProject(t, path)
	var out bytes.Buffer
	if code := run([]string{"--config", path, "--state", l.Root, "doctor"}, &out); code != 1 || !strings.Contains(out.String(), "FAIL config: quarantined project entry 2 (broken-project): name and actor are required\n") {
		t.Fatalf("doctor exit=%d output=%q", code, out.String())
	}
}
