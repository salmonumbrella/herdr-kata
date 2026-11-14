package prompt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A FIFO with no writer must be rejected before it can stall settlement. The
// subprocess is killed and reaped if a blocking-open regression hangs the read.
func TestReadRegularRejectsFIFO(t *testing.T) {
	const pathEnv = "HERDR_KATA_TEST_READ_FIFO"
	if path := os.Getenv(pathEnv); path != "" {
		if got, err := ReadRegular(path, 64*1024); err == nil || len(got) != 0 {
			t.Fatalf("FIFO accepted as evidence: %q, %v", got, err)
		}
		t.Log("FIFO rejected")
		return
	}
	for _, name := range []string{"result.json", "receipt.json"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			if err := syscall.Mkfifo(path, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReadRegularRejectsFIFO$", "-test.v")
			cmd.Env = append(os.Environ(), pathEnv+"="+path)
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("FIFO read blocked waiting for a writer: %v\n%s", ctx.Err(), out)
			}
			if err != nil || !strings.Contains(string(out), "FIFO rejected") {
				t.Fatalf("FIFO rejection failed: %v\n%s", err, out)
			}
		})
	}
}

// Receipts and results must never read through a replacement symlink, even
// while an agent atomically publishes new versions of the same pathname.
func TestReadRegularRejectsConcurrentReplacement(t *testing.T) {
	dir := t.TempDir()
	path, replacement := filepath.Join(dir, "result"), filepath.Join(dir, "replacement")
	target := filepath.Join(dir, "unrelated")
	if err := os.WriteFile(target, []byte("unrelated evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("own evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	stop, done := make(chan struct{}), make(chan struct{})
	errors := make(chan error, 1)
	go func() {
		defer close(done)
		for range 2000 {
			select {
			case <-stop:
				return
			default:
			}
			if err := os.Symlink(target, replacement); err != nil {
				errors <- err
				return
			}
			if err := os.Rename(replacement, path); err != nil {
				errors <- err
				return
			}
			if err := os.WriteFile(replacement, []byte("own evidence"), 0600); err != nil {
				errors <- err
				return
			}
			if err := os.Rename(replacement, path); err != nil {
				errors <- err
				return
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-done })
	for {
		if got, err := ReadRegular(path, 64); err == nil && string(got) != "own evidence" {
			t.Fatalf("read through replacement symlink: %q", got)
		}
		select {
		case <-done:
			select {
			case err := <-errors:
				t.Fatal(err)
			default:
			}
			return
		default:
		}
	}
}
