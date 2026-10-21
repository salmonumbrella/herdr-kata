//go:build linux || darwin

package kata

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestTailPipePreservesCommand(t *testing.T) {
	for _, code := range []int{0, 7} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", `printf '%s|%s|%s|%s' "$1" "$2" "$TAIL_EXAMPLE" "$PWD"; printf warning >&2; exit "$3"`, "tail", "argument with spaces", "literal $HOME", strconv.Itoa(code))
			cmd.Env, cmd.Dir = append(os.Environ(), "TAIL_EXAMPLE=environment"), "/"
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			stop, err := configureTailPipe(cmd)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			err = cmd.Run()
			var exit *exec.ExitError
			if code == 0 && err != nil || code != 0 && (!errors.As(err, &exit) || exit.ExitCode() != code) {
				t.Fatalf("exit=%d err=%v", code, err)
			}
			if stdout.String() != "argument with spaces|literal $HOME|environment|/" || stderr.String() != "warning" {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

// Killing a wrapper and then its child must let the reader see EOF, so the
// transport can close the watchdog pipe and restart the lane's event stream.
func TestTailPipeWrapperLossDoesNotKeepStdoutOpen(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", `echo $$; exec sleep 60`)
	stop, err := configureTailPipe(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() }()
	var child int
	if _, err := fmt.Fscanln(out, &child); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(child, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	eof := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, out); eof <- err }()
	select {
	case err := <-eof:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watchdog kept stdout open after wrapper and tail exited")
	}
}
