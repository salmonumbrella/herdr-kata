package stepexec

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/journal"
)

func TestExecStepHelper(t *testing.T) {
	if os.Getenv("HK_EXEC_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			if os.Args[i+1] == "output" {
				fmt.Print("HEAD\n" + strings.Repeat("x", 1400*1024) + "\nTAIL\n")
				os.Exit(0)
			}
			os.Exit(Run(os.Args[i+1], os.Args[i+2:]))
		}
	}
	os.Exit(2)
}
func helper(t *testing.T, dir string, argv ...string) *exec.Cmd {
	t.Helper()
	args := append([]string{"-test.run=^TestExecStepHelper$", "--", dir}, argv...)
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "HK_EXEC_HELPER=1")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd
}
func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("file not ready: %s", path)
		case <-ticker.C:
		}
	}
}
func TestExecStepReceiptOnExit(t *testing.T) {
	dir := t.TempDir()
	err := helper(t, dir, "/bin/sh", "-c", "exit 7").Run()
	if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 7 {
		t.Fatalf("exit %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r journal.Receipt
	if err = json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if r.ExitCode != 7 || r.StartedAt.IsZero() || r.EndedAt.Before(r.StartedAt) {
		t.Fatalf("receipt %+v", r)
	}
}
func TestExecStepNoReceiptWhenSignalled(t *testing.T) {
	dir := t.TempDir()
	script := `trap 'echo trapped > "$1/caught"; exit 0' HUP; echo ready > "$1/ready"; while :; do sleep 1; done`
	cmd := helper(t, dir, "/bin/sh", "-c", script, "fixture", dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	waitFile(t, filepath.Join(dir, "ready"))
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	waitFile(t, filepath.Join(dir, "caught"))
	if _, err := os.Stat(filepath.Join(dir, "receipt.json")); !os.IsNotExist(err) {
		t.Fatalf("signalled child left receipt: %v", err)
	}
}
func TestExecStepGroupSignalled(t *testing.T) {
	dir := t.TempDir()
	script := `trap 'exit 0' HUP; /bin/sh -c 'trap '\''echo caught > "$1/grandchild"; exit 0'\'' HUP; echo ready > "$1/ready"; while :; do sleep 1; done' fixture "$1" & wait`
	cmd := helper(t, dir, "/bin/sh", "-c", script, "fixture", dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	waitFile(t, filepath.Join(dir, "ready"))
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	waitFile(t, filepath.Join(dir, "grandchild"))
}
func TestExecStepOutputHeadTail(t *testing.T) {
	dir := t.TempDir()
	if err := helper(t, dir, os.Args[0], "-test.run=^TestExecStepHelper$", "--", "output").Run(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), "HEAD\n") || !strings.HasSuffix(string(b), "\nTAIL\n") || !strings.Contains(string(b), "[output truncated]") || len(b) > 1024*1024+256*1024+100 {
		t.Fatalf("head/tail length %d", len(b))
	}
}
