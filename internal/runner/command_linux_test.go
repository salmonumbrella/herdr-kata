//go:build linux

package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCommandTimeoutKillsGroup(t *testing.T) {
	// A subprocess contains Linux's subreaper setting and all its descendants,
	// so killed grandchildren are reaped even in containers with a passive PID 1.
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCommandTimeoutWorker$")
	cmd.Env = append(os.Environ(), "COMMAND_TIMEOUT_WORKER=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("timeout worker: %v\n%s", err, output)
	}
}

func TestCommandTimeoutWorker(t *testing.T) {
	if os.Getenv("COMMAND_TIMEOUT_WORKER") != "1" {
		return
	}
	commandSubreaper(t)
	spec := commandSpec(t, `trap 'printf term > terminated; wait; exit 0' TERM; sh -c 'trap "" TERM; exec sleep 60' & echo "$!" > child; wait`)
	spec.Deadline = time.Now().Add(time.Second)
	started := time.Now()
	receipt, err := RunCommand(context.Background(), spec)
	// Process completion precedes durable receipt I/O. Assert the TERM grace
	// and KILL timing using that event, independent of filesystem latency.
	if !errors.Is(err, context.DeadlineExceeded) || !receipt.Interrupted || receipt.EndedAt.Before(spec.Deadline.Add(10*time.Second)) || receipt.EndedAt.After(started.Add(20*time.Second)) {
		t.Fatalf("receipt=%+v err=%v process_elapsed=%v elapsed=%v", receipt, err, receipt.EndedAt.Sub(started), time.Since(started))
	}
	if _, err := os.Stat(filepath.Join(spec.Cwd, "terminated")); err != nil {
		t.Fatalf("shell did not receive TERM: %v", err)
	}
	child := commandPID(t, filepath.Join(spec.Cwd, "child"))
	var status syscall.WaitStatus
	if _, err := syscall.Wait4(child, &status, 0, nil); err != nil || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("descendant survived KILL: status=%v err=%v", status, err)
	}
	if !GroupGone(receipt.PGID) || !readReceipt(t, spec).Interrupted {
		t.Fatal("group not gone or deadline ambiguity lost")
	}
	// A shell that handles TERM by exiting zero still has an unknown outcome.
	spec = commandSpec(t, `trap 'exit 0' TERM; while :; do :; done`)
	spec.Deadline = time.Now().Add(100 * time.Millisecond)
	receipt, err = RunCommand(context.Background(), spec)
	if !errors.Is(err, context.DeadlineExceeded) || receipt.ExitCode != 0 || !readReceipt(t, spec).Interrupted {
		t.Fatalf("TERM exit zero falsely succeeded: receipt=%+v err=%v", receipt, err)
	}
}

func TestCommandDescendantRefusesRetry(t *testing.T) {
	for _, failure := range []string{"0", "1"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCommandDescendantWorker$")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Env = append(os.Environ(), "COMMAND_DESCENDANT_WORKER=1", "COMMAND_START_FAILURE="+failure)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("descendant worker (start failure %s): %v\n%s", failure, err, output)
		}
	}
}

func TestCommandDescendantWorker(t *testing.T) {
	if os.Getenv("COMMAND_DESCENDANT_WORKER") != "1" {
		return
	}
	commandSubreaper(t)
	spec := commandSpec(t, `sleep 60 & echo $! > child; exit 7`)
	if os.Getenv("COMMAND_START_FAILURE") == "1" {
		spec.Cwd = filepath.Join(spec.Cwd, "missing")
	}
	receipt, err := RunCommand(context.Background(), spec)
	t.Cleanup(func() {
		if receipt.PGID > 0 {
			_ = syscall.Kill(-receipt.PGID, syscall.SIGKILL)
			_, _ = syscall.Wait4(commandPID(t, filepath.Join(spec.Cwd, "child")), nil, 0, nil)
		}
	})
	if os.Getenv("COMMAND_START_FAILURE") == "1" {
		if err == nil || receipt.PGID != 0 {
			t.Fatalf("expected startup failure: receipt=%+v err=%v", receipt, err)
		}
		return
	}
	if !errors.Is(err, ErrCommandGroupAlive) || receipt.ExitCode != 7 || !receipt.GroupAlive || GroupGone(receipt.PGID) || !readReceipt(t, spec).GroupAlive {
		t.Fatalf("descendant permitted retry: receipt=%+v err=%v", receipt, err)
	}
}

func TestCommandEscapedPipeIsUnknown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCommandEscapedPipeWorker$")
	cmd.Env = append(os.Environ(), "COMMAND_ESCAPED_WORKER=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("escaped worker: %v\n%s", err, output)
	}
}

func TestCommandEscapedPipeWorker(t *testing.T) {
	if os.Getenv("COMMAND_ESCAPED_WORKER") != "1" {
		return
	}
	commandSubreaper(t)
	for _, exit := range []string{"0", "7", "deadline"} {
		t.Run(exit, func(t *testing.T) {
			code := exit
			if exit == "deadline" {
				code = "0"
			}
			spec := commandSpec(t, `setsid sh -c 'echo $$ > escaped; exec sleep 60' & while [ ! -s escaped ]; do sleep 0.01; done; exit `+code)
			if exit == "deadline" {
				spec.Deadline = time.Now().Add(100 * time.Millisecond)
			}
			receipt, err := RunCommand(context.Background(), spec)
			pid := commandPID(t, filepath.Join(spec.Cwd, "escaped"))
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL); _, _ = syscall.Wait4(pid, nil, 0, nil) })
			if err == nil || !GroupGone(receipt.PGID) {
				t.Fatalf("missing incomplete pipe error or escaped group not gone: %v", err)
			}
			if exit == "deadline" && (!receipt.Interrupted || !errors.Is(err, context.DeadlineExceeded)) {
				t.Fatalf("deadline ignored while draining: receipt=%+v err=%v", receipt, err)
			}
			if message := CommandCloseMessage("checks", "run-1", "occ", []CommandStepResult{{Command: spec.Command, Receipt: &receipt}}); message != "" {
				t.Fatalf("escaped descendant falsely succeeded: %q", message)
			}
			r := commandFixture()
			receipt = readReceipt(t, spec)
			receipt.Step = r.Steps[0].Key
			r.Steps[0].Receipt = &receipt
			_, payload := settlePayload(t, SettleOps(r, nil, settleTime), "observe")
			if payload.Observation.Status != "unknown" {
				t.Fatalf("escaped descendant outcome=%s", payload.Observation.Status)
			}
		})
	}
}

func commandSubreaper(t *testing.T) {
	t.Helper()
	// PR_SET_CHILD_SUBREAPER is confined to test processes; no production hook.
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, 36, 1, 0, 0, 0, 0); errno != 0 {
		t.Fatal(errno)
	}
	t.Cleanup(func() { _, _, _ = syscall.Syscall6(syscall.SYS_PRCTL, 36, 0, 0, 0, 0, 0) })
}

func commandPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid PID %q: %v", data, err)
	}
	return pid
}
