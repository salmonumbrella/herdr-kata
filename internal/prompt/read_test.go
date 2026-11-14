package prompt

import (
	"os"
	"path/filepath"
	"testing"
)

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
