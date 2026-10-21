package agentlimit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
)

const sessionID = "00000000-0000-4000-8000-000000000001"

var submitted = time.Date(2026, 9, 24, 16, 0, 0, 0, time.UTC)

func writeLog(t *testing.T, agent, data string) string {
	t.Helper()
	root := t.TempDir()
	var path string
	if agent == "codex" {
		t.Setenv("CODEX_HOME", root)
		path = filepath.Join(root, "sessions", "2026", "09", "24", "rollout-example-"+sessionID+".jsonl")
	} else {
		t.Setenv("CLAUDE_CONFIG_DIR", root)
		path = filepath.Join(root, "projects", "-srv-projects-example", sessionID+".jsonl")
	}
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(data), 0600); e != nil {
		t.Fatal(e)
	}
	return path
}

// §21.9: structured errors classify only the last terminal record in this
// submission's span. Fixtures retain code/number fields and redact all prose.
func TestClassifyFixtures(t *testing.T) {
	for _, tc := range []struct{ file, agent, kind, code, reset string }{
		{"codex-0.155.1-quota.jsonl", "codex", "agent_quota", "codex:usage_limit_exceeded", "2026-09-24T17:23:00Z"},
		{"codex-0.162.1-login.jsonl", "codex", "agent_login", "codex:unauthorized", ""},
		{"codex-0.162.1-http-login.jsonl", "codex", "agent_login", "codex:unauthorized", ""},
		{"codex-2025.jsonl", "codex", "", "", ""},
		{"claude-2.1.280-quota.jsonl", "claude", "agent_quota", "claude:rate_limit", ""},
		{"claude-2.1.296-login.jsonl", "claude", "agent_login", "claude:authentication_failed", ""},
	} {
		t.Run(tc.file, func(t *testing.T) {
			b, e := os.ReadFile(filepath.Join("testdata", tc.file))
			if e != nil {
				t.Fatal(e)
			}
			writeLog(t, tc.agent, string(b))
			got := Classify(herdr.AgentSession{Source: "herdr:" + tc.agent, Agent: tc.agent, Kind: "id", Value: sessionID}, "/srv/projects/example", submitted, submitted, nil)
			if tc.kind == "" {
				if got != nil {
					t.Fatal(got)
				}
				return
			}
			if got == nil || got.Kind != tc.kind || got.Code != tc.code {
				t.Fatalf("classification: %+v", got)
			}
			reset := ""
			if got.ResetsAt != nil {
				reset = got.ResetsAt.Format(time.RFC3339)
			}
			if reset != tc.reset {
				t.Fatalf("reset %q, want %q", reset, tc.reset)
			}
		})
	}
}

func TestCodexHTTPErrorStatus(t *testing.T) {
	for _, tc := range []struct {
		status string
		login  bool
	}{{"401", true}, {"403", true}, {"429", false}, {"500", false}, {"null", false}} {
		t.Run(tc.status, func(t *testing.T) {
			writeLog(t, "codex", `{"timestamp":"2026-09-24T16:01:00Z","type":"event_msg","payload":{"type":"task_complete","error":{"codex_error_info":{"http_connection_failed":{"http_status_code":`+tc.status+`}}}}}`+"\n")
			got := Classify(herdr.AgentSession{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: sessionID}, "", submitted, submitted, nil)
			if tc.login {
				if got == nil || got.Kind != "agent_login" || got.Code != "codex:unauthorized" {
					t.Fatalf("HTTP %s: %+v", tc.status, got)
				}
			} else if got != nil {
				t.Fatalf("non-login HTTP %s classified: %+v", tc.status, got)
			}
		})
	}
}

func codexRecord(at, kind, code string) string {
	return `{"timestamp":"` + at + `","type":"event_msg","payload":{"type":"` + kind + `","error":{"message":"PRIVATE TEXT","codex_error_info":"` + code + `"}}}` + "\n"
}
func claudeRecord(code string, api bool) string {
	flag := "false"
	if api {
		flag = "true"
	}
	return `{"timestamp":"2026-09-24T16:01:00Z","type":"assistant","isApiErrorMessage":` + flag + `,"error":"` + code + `","message":{"content":"PRIVATE TEXT"}}` + "\n"
}

func TestAttemptSpanAndLastTerminal(t *testing.T) {
	errorLine := codexRecord("2026-09-24T16:01:00Z", "task_complete", "usage_limit_exceeded")
	for _, tc := range []struct {
		name, data string
		want       bool
	}{
		{"new quota", errorLine, true},
		{"at submission", codexRecord("2026-09-24T16:00:00Z", "task_complete", "usage_limit_exceeded"), true},
		{"old quota", codexRecord("2026-09-24T15:59:59Z", "task_complete", "usage_limit_exceeded"), false},
		{"recovered", errorLine + codexRecord("2026-09-24T16:02:00Z", "task_complete", ""), false},
		{"aborted", codexRecord("2026-09-24T16:01:00Z", "turn_aborted", "rate_limit_exceeded"), true},
		{"overload", codexRecord("2026-09-24T16:01:00Z", "task_complete", "server_overloaded"), false},
		{"other", codexRecord("2026-09-24T16:01:00Z", "task_complete", "other"), false},
		{"truncated", errorLine + `{"timestamp":`, false},
		{"no timestamp", strings.ReplaceAll(errorLine, `"timestamp":"2026-09-24T16:01:00Z",`, ""), false},
		{"bad timestamp", strings.ReplaceAll(errorLine, "2026-09-24T16:01:00Z", "invalid"), false},
		{"long line", strings.Repeat("x", (1<<20)+1) + "\n" + errorLine, false},
		{"chunk boundary", strings.Repeat(" ", 65500) + errorLine, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeLog(t, "codex", tc.data)
			got := Classify(herdr.AgentSession{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: sessionID}, "", submitted, submitted, nil)
			if (got != nil) != tc.want {
				t.Fatalf("classification %+v, want match %v", got, tc.want)
			}
		})
	}
}

func TestClaudeCodesAndFallback(t *testing.T) {
	for _, code := range []string{"rate_limit", "billing_error", "authentication_failed", "unauthenticated", "invalid_grant", "server_error"} {
		t.Run(code, func(t *testing.T) {
			path := writeLog(t, "claude", claudeRecord(code, true))
			// A shortened cwd slug must still resolve the exact session id.
			got := Classify(herdr.AgentSession{Source: "herdr:claude", Agent: "claude", Kind: "id", Value: sessionID}, "/different/cwd", submitted, submitted, nil)
			if code == "server_error" {
				if got != nil {
					t.Fatal(got)
				}
				return
			}
			if got == nil || got.Code != "claude:"+code || got.ResetsAt != nil {
				t.Fatalf("classification %+v", got)
			}
			if e := os.WriteFile(path, []byte(claudeRecord(code, true)+claudeRecord("", false)), 0600); e != nil {
				t.Fatal(e)
			}
			if got := Classify(herdr.AgentSession{Source: "herdr:claude", Agent: "claude", Kind: "id", Value: sessionID}, "", submitted, submitted, nil); got != nil {
				t.Fatal("recovered error classified", got)
			}
		})
	}
}

func TestLookupFailsSoft(t *testing.T) {
	for _, tc := range []string{"unsupported", "source", "path kind", "bad id", "missing", "compressed", "revert", "original and revert", "adjacent day", "ambiguous"} {
		t.Run(tc, func(t *testing.T) {
			path := writeLog(t, "codex", codexRecord("2026-09-24T16:01:00Z", "task_complete", "unauthorized"))
			session := herdr.AgentSession{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: sessionID}
			want := false
			switch tc {
			case "unsupported":
				session.Agent = "other"
			case "source":
				session.Source = "other"
			case "path kind":
				session.Kind = "path"
			case "bad id":
				session.Value = "../../outside"
			case "missing":
				session.Value = "00000000-0000-4000-8000-000000000002"
			case "compressed":
				if e := os.Rename(path, path+".zst"); e != nil {
					t.Fatal(e)
				}
			case "revert":
				if e := os.Rename(path, strings.TrimSuffix(path, ".jsonl")+"_revert.jsonl"); e != nil {
					t.Fatal(e)
				}
			case "original and revert":
				if e := os.WriteFile(strings.TrimSuffix(path, ".jsonl")+"_revert.jsonl", []byte(codexRecord("2026-09-24T16:02:00Z", "task_complete", "")), 0600); e != nil {
					t.Fatal(e)
				}
			case "adjacent day":
				next := filepath.Join(filepath.Dir(filepath.Dir(path)), "25", filepath.Base(path))
				if e := os.MkdirAll(filepath.Dir(next), 0700); e != nil {
					t.Fatal(e)
				}
				if e := os.Rename(path, next); e != nil {
					t.Fatal(e)
				}
				want = true
			case "ambiguous":
				b, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(filepath.Join(filepath.Dir(path), "rollout-other-"+sessionID+".jsonl"), b, 0600); e != nil {
					t.Fatal(e)
				}
			}
			if got := Classify(session, "", submitted, submitted, nil); (got != nil) != want {
				t.Fatalf("classification %+v, want match %v", got, want)
			}
		})
	}
}

func TestCodexResetWindowAndBounds(t *testing.T) {
	quota := codexRecord("2026-09-24T16:01:00Z", "task_complete", "usage_limit_exceeded")
	full := `{"timestamp":"2026-09-24T16:00:01Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":100,"resets_at":1790270580}}}}` + "\n"
	for _, tc := range []struct {
		name, data, reset string
		match             bool
	}{
		{"full window alone is not a signal", full, "", false},
		{"before error", full + quota, "2026-09-24T17:23:00Z", true},
		{"after error", quota + full, "", true},
		{"old window", strings.Replace(full, "16:00:01Z", "15:59:59Z", 1) + quota, "", true},
		{"not full", strings.Replace(full, `"used_percent":100`, `"used_percent":99`, 1) + quota, "", true},
		{"newest full has no reset", full + strings.Replace(full, `"resets_at":1790270580`, `"resets_at":0`, 1) + quota, "", true},
		{"tail bound", strings.Repeat(full, 60000) + quota, "2026-09-24T17:23:00Z", true},
		{"scan bound", strings.Repeat(strings.Replace(full, `"used_percent":100`, `"used_percent":99`, 1), 60000) + quota, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeLog(t, "codex", tc.data)
			got := Classify(herdr.AgentSession{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: sessionID}, "", submitted, submitted, nil)
			if (got != nil) != tc.match {
				t.Fatalf("classification %+v", got)
			}
			if got == nil {
				return
			}
			reset := ""
			if got.ResetsAt != nil {
				reset = got.ResetsAt.Format(time.RFC3339)
			}
			if reset != tc.reset {
				t.Fatalf("reset %s, want %s", reset, tc.reset)
			}
		})
	}
}

func TestClaudeEscapedRootAndSlug(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config[1]")
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	path := filepath.Join(root, "projects", "-srv-example-project-v1", sessionID+".jsonl")
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(claudeRecord("billing_error", true)), 0600); e != nil {
		t.Fatal(e)
	}
	for _, cwd := range []string{"/srv/example.project:v1", "/shortened"} {
		if got := Classify(herdr.AgentSession{Source: "herdr:claude", Agent: "claude", Kind: "id", Value: sessionID}, cwd, submitted, submitted, nil); got == nil || got.Kind != "agent_quota" {
			t.Fatalf("cwd %s: %+v", cwd, got)
		}
	}
}

// The agent's CODEX_HOME locates its log even when the worker's differs.
func TestSessionLogFollowsAgentEnvironment(t *testing.T) {
	path := writeLog(t, "codex", "{}\n")
	agentHome := os.Getenv("CODEX_HOME")
	t.Setenv("CODEX_HOME", t.TempDir())
	session := herdr.AgentSession{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: sessionID}
	if got := SessionLogPath(session, "", submitted, nil); got != "" {
		t.Fatalf("worker home found %s", got)
	}
	env := func(key string) string { return map[string]string{"CODEX_HOME": agentHome}[key] }
	if got := SessionLogPath(session, "", submitted, env); got != path {
		t.Fatalf("agent home: got %q, want %q", got, path)
	}
}
