package main

import (
	"bytes"
	"encoding/json"
	"os"
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

// A report made inside a Claude session records its identity without changing the outcome.
func TestReportCapturesSession(t *testing.T) {
	t.Setenv("KATA_CRON_RUN", "01J00000000000000000000001")
	t.Setenv("KATA_CRON_STEP", "main")
	t.Setenv("KATA_CRON_ATTEMPT", "1")
	t.Setenv("HERDR_KATA_SESSIONS_ENV", "")
	t.Setenv("KATA_TRANSCRIPT_AGENT", "")
	t.Setenv("KATA_TRANSCRIPT_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("CODEX_SESSION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "00000000-0000-4000-8000-000000000001")
	path := filepath.Join(t.TempDir(), "result.json")
	var out bytes.Buffer
	if code := report([]string{"--result", path, "--status", "succeeded"}, &out); code != 0 {
		t.Fatalf("report: %d %s", code, &out)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Status  string `json:"status"`
		Session struct {
			Agent string `json:"agent"`
			ID    string `json:"session_id"`
		} `json:"session"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "succeeded" || got.Session.Agent != "claude" || got.Session.ID != "00000000-0000-4000-8000-000000000001" {
		t.Fatalf("result: %s", b)
	}
}
