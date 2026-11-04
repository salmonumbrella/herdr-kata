package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/prompt"
)

func TestReportWritesAtomically(t *testing.T) {
	t.Setenv("KATA_CRON_RUN", "01J00000000000000000000001")
	t.Setenv("KATA_CRON_STEP", "main")
	t.Setenv("KATA_CRON_ATTEMPT", "1")
	path := filepath.Join(t.TempDir(), "result.json")
	var out bytes.Buffer
	code := run([]string{"report", "--result", path, "--status", "succeeded", "--summary", "Completed work", "--note", "One line"}, &out)
	if code != 0 {
		t.Fatalf("report: %d %s", code, &out)
	}
	got, err := prompt.ReadResult(path, "01J00000000000000000000001", "main", 1)
	if err != nil || got.Summary != "Completed work" || got.Note != "One line" {
		t.Fatalf("result %+v %v", got, err)
	}
}

func TestReportRejectsInvalidNote(t *testing.T) {
	t.Setenv("KATA_CRON_RUN", "01J00000000000000000000001")
	t.Setenv("KATA_CRON_STEP", "main")
	t.Setenv("KATA_CRON_ATTEMPT", "1")
	for _, note := range []string{"two\nlines", strings.Repeat("a", 281), "\xff"} {
		path := filepath.Join(t.TempDir(), "result.json")
		var out bytes.Buffer
		code := run([]string{"report", "--result", path, "--status", "succeeded", "--summary", "Complete", "--note", note}, &out)
		if code == 0 || !strings.Contains(out.String(), "invalid_note") {
			t.Fatalf("invalid note accepted: %d %s", code, &out)
		}
		if _, err := prompt.ReadResult(path, "01J00000000000000000000001", "main", 1); err == nil {
			t.Fatal("rejected report wrote a readable result")
		}
	}
}
