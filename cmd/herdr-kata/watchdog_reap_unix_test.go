//go:build unix

package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestDetachedRoleExitHelper(t *testing.T) {
	if os.Getenv("HERDR_KATA_TEST_EXIT_CHILD") == "1" {
		os.Exit(0)
	}
}

func TestDetachedRoleReapsExitedChildWhileParentLives(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_KATA_HOME", t.TempDir())
	t.Setenv(allowScratchDaemonEnv, "1")
	t.Setenv("HERDR_KATA_TEST_EXIT_CHILD", "1")
	// The spawned executable is this test binary. Select only an immediate-exit
	// helper, so the regression never starts a scheduler or provider agent.
	if err := spawnRole("-test.run=^TestDetachedRoleExitHelper$"); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(strayLog())
	if err != nil {
		t.Fatal(err)
	}
	records := readStrays(f)
	f.Close()
	if len(records) != 1 {
		t.Fatalf("expected one owned child, got %v", records)
	}
	pid := records[0].PID
	// Reap only this test's child if the regression fails and leaves a zombie.
	t.Cleanup(func() { _, _ = syscall.Wait4(pid, nil, syscall.WNOHANG, nil) })
	deadline := time.Now().Add(3 * time.Second)
	for pidAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if pidAlive(pid) {
		t.Fatalf("exited detached child %d remains a zombie under its live parent", pid)
	}
	if got := liveStrays(records); len(got) != 0 {
		t.Fatalf("doctor reports an exited scheduler: %v", got)
	}
}
