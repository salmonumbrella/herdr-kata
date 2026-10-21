//go:build linux && crashpoint

package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestCrashCommandSideEffectBeforeReceipt(t *testing.T) {
	crashCommand(t, false)
}

func TestCrashCommandDescendantSurvives(t *testing.T) {
	crashCommand(t, true)
}

func crashCommand(t *testing.T, descendant bool) {
	t.Helper()
	commandSubreaper(t)
	dir := t.TempDir()
	worker := exec.Command(os.Args[0], "-test.run=^TestCrashCommandWorker$")
	worker.Env = append(os.Environ(), "COMMAND_CRASH_DIR="+dir)
	if descendant {
		worker.Env = append(worker.Env, "COMMAND_CRASH_DESCENDANT=1")
	}
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	// The shell writes its own PGID before its side effect. Synchronize with
	// the filesystem instead of a guessed delay before killing the runner.
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(filepath.Join(dir, "marker"))
		if err == nil && string(data) == "once\n" {
			break
		}
		if time.Now().After(deadline) {
			_ = worker.Process.Kill()
			_ = worker.Wait()
			t.Fatal("command never reached its side effect")
		}
		time.Sleep(10 * time.Millisecond)
	}
	pgid := commandPID(t, filepath.Join(dir, "group"))
	t.Cleanup(func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		for {
			if _, err := syscall.Wait4(-pgid, nil, 0, nil); err != nil {
				break
			}
		}
	})
	if err := worker.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := worker.Wait(); err == nil {
		t.Fatal("runner did not crash")
	}
	if descendant {
		pid := commandPID(t, filepath.Join(dir, "descendant"))
		if err := syscall.Kill(pid, 0); err != nil || GroupGone(pgid) {
			t.Fatalf("descendant not present after runner crash: %v", err)
		}
		// Prove it is alive rather than merely an unreaped zombie.
		if err := syscall.Kill(pid, syscall.SIGUSR1); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			if data, _ := os.ReadFile(filepath.Join(dir, "alive")); string(data) == "alive" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("descendant did not respond after runner death")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	var receipt Receipt
	if err := state.ReadJSON(state.OSFS{}, filepath.Join(dir, "runs", "run-1", "steps", "check", "1", "receipt.json"), 4096, &receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("crashed command has a receipt: %+v err=%v", receipt, err)
	}
	// Re-enter the executor with the saved sending barrier. Without a receipt,
	// neither the original command nor a retry may execute.
	s := NewStore(state.OSFS{}, state.Layout{Root: dir})
	m := NewManager(config.Config{}, nil, nil, s, nil, nil, RealClock{})
	if err := m.Execute(t.Context(), "run-1"); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	r := loaded(t, s, "run-1")
	if r.Phase != Held || r.Outcome != "unknown" || r.Steps[0].Attempt != 1 {
		t.Fatal(r)
	}
	marker, err := os.ReadFile(filepath.Join(dir, "marker"))
	if err != nil || string(marker) != "once\n" {
		t.Fatalf("side effect repeated: marker=%q err=%v", marker, err)
	}
}

func TestCrashCommandWorker(t *testing.T) {
	dir := os.Getenv("COMMAND_CRASH_DIR")
	if dir == "" {
		return
	}
	command := `echo $$ > group; printf 'once\n' >> marker; while :; do :; done`
	if os.Getenv("COMMAND_CRASH_DESCENDANT") == "1" {
		command = `echo $$ > group; sh -c 'trap "" TERM; trap "printf alive > alive" USR1; echo $$ > descendant; printf ready > ready; while :; do sleep 0.05; done' & while [ ! -f ready ]; do sleep 0.01; done; printf 'once\n' >> marker; wait`
	}
	s := NewStore(state.OSFS{}, state.Layout{Root: dir})
	r := &Run{Version: 1, UID: "run-1", Phase: Admitted, Cwd: dir, ExpiresAt: time.Now().Add(time.Minute), JobDef: defs.JobDef{Action: defs.Action{Kind: "execute", WorkflowUID: "workflow"}}, WFDef: &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "check", Kind: "command", Command: command, Retries: 3}}}, Steps: []StepState{{Key: "check", Kind: "command", Attempt: 1, Submission: None}}}
	if err := s.Create(r); err != nil {
		t.Fatal(err)
	}
	m := NewManager(config.Config{}, nil, nil, s, nil, nil, RealClock{})
	if err := m.Execute(context.Background(), r.UID); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	t.Fatal("command unexpectedly returned before crash")
}
