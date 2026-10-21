package runner

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestCommandGroupMembersLifecycle(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "/bin/cat")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	joined := false
	t.Cleanup(func() {
		_ = input.Close()
		if !joined {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	pgid := cmd.Process.Pid
	if count, err := commandGroupMembers(pgid); err != nil || count != 1 {
		t.Fatalf("live members=%d err=%v", count, err)
	}
	if GroupGone(pgid) {
		t.Fatal("live command group proved absent")
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	joined = true
	if count, err := commandGroupMembers(pgid); err != nil || count != 0 {
		t.Fatalf("reaped members=%d err=%v", count, err)
	}
	if !GroupGone(pgid) {
		t.Fatal("reaped command group retained")
	}
}
