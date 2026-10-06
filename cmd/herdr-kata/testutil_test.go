package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = w
	runErr := fn()
	os.Stdout = previous
	w.Close()
	out, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(out), runErr
}

// Capture the known missing-executable fallback, while rejecting any additional
// diagnostic line or a different failure cause.
func captureExpectedWorkflowFallback(t *testing.T, workflowID string, fn func()) {
	t.Helper()
	output := captureStderr(t, fn)
	missing := exec.Command(os.Getenv("HERDR_BIN_PATH")).Run()
	if !errors.Is(missing, os.ErrNotExist) {
		t.Fatalf("fallback fixture is not a missing executable: %v", missing)
	}
	line := strings.TrimSuffix(output, "\n")
	prefix := "herdr-kata: could not open a space for this workflow, so its steps have no workspace: herdr [workspace create --label WORKFLOWS:" + workflowID + ":"
	if strings.Contains(line, "\n") || !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, ": "+missing.Error()+": ") {
		t.Fatalf("unexpected workflow fallback diagnostics: %q", output)
	}
}
