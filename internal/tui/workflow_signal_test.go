package tui

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// A subprocess exercises Run's signal handling while Bubble Tea has released
// the terminal to an editor. In-process model cleanup cannot catch this leak.
func TestWorkflowEditorHangupProcess(t *testing.T) {
	root := os.Getenv("HERDR_KATA_HANGUP_TEST_ROOT")
	if root == "" {
		return
	}
	_ = Run(context.Background(), Options{Root: root, Stdout: os.Stdout,
		Config: cfg(), ProjectUID: "p", Project: "example-project", Service: &fakeActions{},
	})
}

func TestWorkflowEditorHangupCleansDraft(t *testing.T) {
	for _, mode := range []string{"original", "atomic"} {
		t.Run(mode, func(t *testing.T) { testWorkflowEditorHangup(t, mode) })
	}
}

func testWorkflowEditorHangup(t *testing.T, mode string) {
	root := t.TempDir()
	lane := filepath.Join(root, "projects", "p")
	if err := os.MkdirAll(lane, 0700); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(state.OSFS{}, filepath.Join(lane, "state.json"), map[string]any{"version": 1, "name": "example-project", "project_uid": "p"}); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(state.OSFS{}, filepath.Join(lane, "defs.json"), defs.Snapshot{Version: 2, Workflows: []kata.Workflow{{UID: "flow", Name: "Example flow", Definition: []byte(checkedWorkflow)}}}); err != nil {
		t.Fatal(err)
	}
	editor := filepath.Join(root, "editor")
	script := "#!/bin/sh\n"
	if mode == "atomic" {
		script += `printf '{}' > "$1.replacement"
mv "$1.replacement" "$1"
`
	}
	script += "printf 'EDITOR_READY:%s\\n' \"$1\"\nread ignored\n"
	if err := os.WriteFile(editor, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestWorkflowEditorHangupProcess$")
	cmd.Env = append(os.Environ(), "HERDR_KATA_HANGUP_TEST_ROOT="+root, "EDITOR="+editor, "TMPDIR="+root, "TERM=xterm-256color")
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 100})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = tty.Close() })
	chunks := make(chan string, 64)
	go func() {
		defer close(chunks)
		buf := make([]byte, 4096)
		for {
			n, err := tty.Read(buf)
			if n > 0 {
				chunks <- string(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	var output string
	until := func(want string, ready func(string) bool) {
		t.Helper()
		deadline := time.NewTimer(10 * time.Second)
		defer deadline.Stop()
		for !ready(ansi.Strip(output)) {
			select {
			case part, ok := <-chunks:
				if !ok {
					t.Fatalf("TUI exited waiting for %q: %s", want, ansi.Strip(output))
				}
				output += part
			case <-deadline.C:
				t.Fatalf("TUI did not show %q: %s", want, ansi.Strip(output))
			}
		}
	}
	until("jobs", func(text string) bool { return strings.Contains(text, "jobs") })
	if _, err := io.WriteString(tty, "4"); err != nil {
		t.Fatal(err)
	}
	until("Example flow", func(text string) bool { return strings.Contains(text, "Example flow") })
	if _, err := io.WriteString(tty, "e"); err != nil {
		t.Fatal(err)
	}
	until("complete editor announcement", func(text string) bool {
		_, tail, found := strings.Cut(text, "EDITOR_READY:")
		return found && strings.Contains(tail, "\n")
	})
	_, tail, _ := strings.Cut(ansi.Strip(output), "EDITOR_READY:")
	line, _, _ := strings.Cut(tail, "\n")
	draft := strings.TrimSpace(line)
	if draft == "" {
		t.Fatal("editor announced an empty draft path")
	}
	if _, err := os.Stat(draft); err != nil {
		t.Fatal("editor draft missing before hangup", err, draft)
	}
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// Keep reading the terminal while the TUI shuts down so its final frame
	// never blocks on a full pty, and allow a loaded race-enabled runner time
	// for Bubble Tea's terminal restore.
	timeout := time.After(30 * time.Second)
wait:
	for {
		select {
		case <-done:
			break wait
		case _, ok := <-chunks:
			if !ok {
				chunks = nil
			}
		case <-timeout:
			t.Fatal("TUI did not stop after hangup")
		}
	}
	if _, err := os.Stat(draft); !os.IsNotExist(err) {
		t.Fatal("hangup left workflow draft behind", err, draft)
	}
}
