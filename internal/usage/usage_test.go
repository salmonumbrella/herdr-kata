package usage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
)

func TestAgentsviewUsageWindow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTSVIEW_DATA_DIR", dir)
	t.Setenv("AGENTSVIEW_AUTH_TOKEN", "environment-token")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/sessions/example-session/usage" || r.Header.Get("Authorization") != "Bearer environment-token" {
			t.Error("wrong API request", r.URL)
			w.WriteHeader(400)
			return
		}
		fmt.Fprint(w, `{"has_token_data":true,"has_cost":true,"cost":{"microdollars":9999},"breakdown":[{"timestamp":"2026-01-02T03:58:00Z","input_tokens":999,"has_cost":true,"cost":{"microdollars":9000}},{"timestamp":"2026-01-02T03:59:01Z","input_tokens":2,"output_tokens":6,"cache_read_input_tokens":8,"has_cost":true,"cost":{"microdollars":125}},{"timestamp":"2026-01-02T03:59:02Z","input_tokens":3,"output_tokens":7,"cache_creation_input_tokens":9,"has_cost":true,"cost":{"microdollars":375}},{"timestamp":"2026-01-02T04:01:00Z","input_tokens":999,"has_cost":true,"cost":{"microdollars":499}}]}`)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	_, port, _ := strings.Cut(u.Host, ":")
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("host = \"127.0.0.1\"\nport = "+port+"\nauth_token = \"file-token\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	from, _ := time.Parse(time.RFC3339, "2026-01-02T03:59:00Z")
	until := from.Add(time.Minute)
	got := Collect(context.Background(), Request{Session: &herdr.AgentSession{Value: "example-session"}, StartedAt: from, EndedAt: until})
	if calls.Load() != 1 || !got.Found || got.Source != "agentsview" || got.InputTokens != 5 || got.OutputTokens != 13 || got.CacheReadTokens != 8 || got.CacheCreationTokens != 9 || got.CostMicroUSD == nil || *got.CostMicroUSD != 500 {
		t.Fatal(calls.Load(), got)
	}
}
func TestCodexCumulativeEventsExcludeEarlierWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	prompt := "/srv/projects/example/runs/run/steps/main/1/prompt.md"
	body := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":900,"cached_input_tokens":600,"output_tokens":130}}}}` + "\n"
	body += fmt.Sprintf("{\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"text\":%q}]}}\n", "Read "+prompt)
	body += strings.Repeat(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":650,"output_tokens":150}}}}`+"\n", 2)
	body += `{"type":"event_msg","payload":{"type":"user_message","message":"Read /srv/projects/example/runs/other/steps/main/1/prompt.md"}}` + "\n" + `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":3000,"cached_input_tokens":650,"output_tokens":500}}}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	got := readTranscript(path, prompt, "codex")
	if !got.Found || got.InputTokens != 50 || got.OutputTokens != 20 || got.CacheReadTokens != 50 || got.CacheCreationTokens != 0 {
		t.Fatal(got)
	}
}

func TestAgentsviewCodexCanonicalID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTSVIEW_DATA_DIR", dir)
	t.Setenv("AGENTSVIEW_AUTH_TOKEN", "")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/api/v1/sessions/example-session/usage" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path != "/api/v1/sessions/codex:example-session/usage" {
			t.Error("wrong canonical session", r.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(w, `{"has_token_data":true,"breakdown":[{"timestamp":"2026-01-02T03:59:01Z","input_tokens":17,"output_tokens":23}]}`)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("host = \"127.0.0.1\"\nport = "+u.Port()+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got := Collect(t.Context(), Request{Session: &herdr.AgentSession{Agent: "codex", Value: "example-session"}})
	if !got.Found || got.Source != "agentsview" || got.InputTokens != 17 || got.OutputTokens != 23 || calls.Load() != 2 {
		t.Fatal(got, calls.Load())
	}
}

func TestTranscriptStatePathWithSpaces(t *testing.T) {
	for _, tc := range []struct {
		agent, user, counters string
	}{
		{"claude", `{"type":"user","message":{"content":%q}}`, `{"type":"assistant","message":{"usage":{"input_tokens":17,"output_tokens":23}}}`},
		{"codex", `{"type":"event_msg","payload":{"type":"user_message","message":%q}}`, `{"type":"token_usage_record","payload":{"usage":{"input_tokens":17,"output_tokens":23}}}`},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			prompt := "/srv/example state/runs/run/steps/main/1/prompt.md"
			other := "/srv/example state/runs/other/steps/main/1/prompt.md"
			body := fmt.Sprintf(tc.user, "Read "+prompt) + "\n" + tc.counters + "\n" + fmt.Sprintf(tc.user, "Read "+other) + "\n" + tc.counters + "\n"
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			got := readTranscript(path, prompt, tc.agent)
			if !got.Found || got.InputTokens != 17 || got.OutputTokens != 23 {
				t.Fatal(got)
			}
		})
	}
}
