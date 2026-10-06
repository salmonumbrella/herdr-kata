package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalWorkflowCommandCreatesDraft(t *testing.T) {
	t.Setenv("HERDR_KATA_HOME", t.TempDir())
	command, ok := commands()["workflow"]
	if !ok {
		t.Fatal("workflow command missing")
	}
	out, err := captureStdout(t, func() error { return command([]string{"new", "inspect", "--about", "Inspect workspace"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "workflow save") {
		t.Fatalf("draft instructions: %s", out)
	}
	if _, err := os.Stat(filepath.Join(stateDir(), "drafts", "workflows", "inspect.yml")); err != nil {
		t.Fatal(err)
	}
}

func TestRetiredFlowCommandIsAbsent(t *testing.T) {
	if _, ok := commands()["flow"]; ok {
		t.Fatal("retired flow command remains")
	}
}
