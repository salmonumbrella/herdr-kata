package session

import "testing"

func TestDiscoverSession(t *testing.T) {
	const one = "00000000-0000-4000-8000-000000000001"
	const two = "00000000-0000-4000-8000-000000000002"
	cases := []struct {
		name      string
		env       map[string]string
		agent, id string
	}{
		{"absent", nil, "", ""},
		{"claude", map[string]string{"CLAUDE_CODE_SESSION_ID": one}, "claude", one},
		{"codex precedence", map[string]string{"CODEX_THREAD_ID": one, "CODEX_SESSION_ID": one}, "codex", one},
		{"codex conflict", map[string]string{"CODEX_THREAD_ID": one, "CODEX_SESSION_ID": two}, "", ""},
		{"different agents", map[string]string{"CODEX_THREAD_ID": one, "CLAUDE_CODE_SESSION_ID": one}, "", ""},
		{"explicit override", map[string]string{"KATA_TRANSCRIPT_AGENT": "pi", "KATA_TRANSCRIPT_SESSION_ID": "ses_1", "CLAUDE_CODE_SESSION_ID": one}, "pi", "ses_1"},
		{"partial override", map[string]string{"KATA_TRANSCRIPT_AGENT": "pi", "CLAUDE_CODE_SESSION_ID": one}, "", ""},
		{"mapping", map[string]string{"HERDR_KATA_SESSIONS_ENV": `{"pi":"PI_SESSION_ID"}`, "PI_SESSION_ID": "ses_1"}, "pi", "ses_1"},
		{"invalid", map[string]string{"CLAUDE_CODE_SESSION_ID": "bad"}, "", ""},
		{"case insensitive agreement", map[string]string{"CODEX_THREAD_ID": "AAAAAAAA-0000-4000-8000-000000000001", "CODEX_SESSION_ID": "aaaaaaaa-0000-4000-8000-000000000001"}, "codex", "AAAAAAAA-0000-4000-8000-000000000001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{"KATA_TRANSCRIPT_AGENT", "KATA_TRANSCRIPT_SESSION_ID", "CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "CODEX_SESSION_ID", "HERDR_KATA_SESSIONS_ENV", "PI_SESSION_ID"} {
				t.Setenv(k, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got := DiscoverSession()
			if tc.agent == "" {
				if got != nil {
					t.Fatalf("unexpected session: %+v", got)
				}
				return
			}
			if got == nil || got.Agent != tc.agent || got.SessionID != tc.id {
				t.Fatalf("session: %+v", got)
			}
		})
	}
}
