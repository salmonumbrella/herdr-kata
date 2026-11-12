package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/prompt"
	"github.com/salmonumbrella/herdr-kata/internal/session"
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

func clearSessionEnv(t *testing.T) {
	t.Helper()
	for _, name := range append(append([]string{}, session.BuiltinEnvNames...), "HERDR_KATA_SESSIONS_ENV", "PI_SESSION_ID") {
		t.Setenv(name, "")
	}
}

// A report made inside a Claude session records its identity without changing the outcome.
func TestReportCapturesSession(t *testing.T) {
	t.Setenv("KATA_CRON_RUN", "01J00000000000000000000001")
	t.Setenv("KATA_CRON_STEP", "main")
	t.Setenv("KATA_CRON_ATTEMPT", "1")
	clearSessionEnv(t)
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

// Optional identity cannot turn a baseline-valid report into a failure.
func TestReportSessionSizeBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		session bool
		size    int
		ok      bool
	}{
		{"near limit without session", false, 65400, true},
		{"near limit with session", true, 65400, true},
		{"oversized without session", false, 65536, false},
		{"baseline too large", true, 65536, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearSessionEnv(t)
			t.Setenv("KATA_CRON_RUN", "01J00000000000000000000001")
			t.Setenv("KATA_CRON_STEP", "main")
			t.Setenv("KATA_CRON_ATTEMPT", "1")
			if tc.session {
				t.Setenv("CLAUDE_CODE_SESSION_ID", "00000000-0000-4000-8000-000000000001")
			}
			path := filepath.Join(t.TempDir(), "result.json")
			var out bytes.Buffer
			code := report([]string{"--result", path, "--status", "succeeded", "--summary", strings.Repeat("a", tc.size)}, &out)
			if !tc.ok {
				if code == 0 {
					t.Fatal("oversized baseline accepted")
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("result exists: %v", err)
				}
				return
			}
			if code != 0 {
				t.Fatalf("report %d: %s", code, &out)
			}
			got, err := prompt.ReadResult(path, "01J00000000000000000000001", "main", 1)
			if err != nil || got.Summary != strings.Repeat("a", tc.size) || got.Session != nil {
				t.Fatalf("result: %+v %v", got, err)
			}
		})
	}
}

func TestReportSessionDiscoveryFailuresAreNonfatal(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"absent", nil},
		{"malformed mapping", map[string]string{"HERDR_KATA_SESSIONS_ENV": "{"}},
		{"invalid", map[string]string{"CLAUDE_CODE_SESSION_ID": "invalid"}},
		{"ambiguous", map[string]string{"CODEX_THREAD_ID": "00000000-0000-4000-8000-000000000001", "CODEX_SESSION_ID": "00000000-0000-4000-8000-000000000002"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearSessionEnv(t)
			t.Setenv("KATA_CRON_RUN", "01J00000000000000000000001")
			t.Setenv("KATA_CRON_STEP", "main")
			t.Setenv("KATA_CRON_ATTEMPT", "1")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			path := filepath.Join(t.TempDir(), "result.json")
			var out bytes.Buffer
			if code := report([]string{"--result", path, "--status", "succeeded"}, &out); code != 0 {
				t.Fatalf("report %d: %s", code, &out)
			}
			got, err := prompt.ReadResult(path, "01J00000000000000000000001", "main", 1)
			if err != nil || got.Status != "succeeded" || got.Session != nil {
				t.Fatalf("result %+v: %v", got, err)
			}
		})
	}
}
