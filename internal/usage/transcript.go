package usage

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/agentlimit"
)

var attemptPromptRE = regexp.MustCompile(`/runs/[^/"\s\\]+/steps/[^/"\s\\]+/[0-9]+/prompt\.md`)

func transcriptUsage(req Request) Usage {
	agent := req.Agent
	if req.Session != nil {
		agent = req.Session.Agent
	}
	var paths []string
	if req.Session != nil {
		if p := agentlimit.SessionLogPath(*req.Session, req.Cwd, req.LaunchedAt, req.Env); p != "" {
			paths = []string{p}
		}
	}
	if len(paths) == 0 && agent == "claude" {
		root := agentlimit.HomeRoot(req.Env, "CLAUDE_CONFIG_DIR", ".claude")
		slug := regexp.MustCompile(`[^A-Za-z0-9-]`).ReplaceAllString(req.Cwd, "-")
		entries, _ := os.ReadDir(filepath.Join(root, "projects", slug))
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
				paths = append(paths, filepath.Join(root, "projects", slug, e.Name()))
			}
		}
	}
	total := Usage{}
	for _, path := range paths {
		total = *Add(&total, readTranscript(path, req.PromptPath, agent))
	}
	return total
}
func readTranscript(path, prompt, agent string) Usage {
	f, err := os.Open(path)
	if err != nil {
		return Usage{}
	}
	defer f.Close()
	total, legacy := Usage{}, Usage{}
	collecting := false
	seen := map[string]bool{}
	var prior codexTokens
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		var e struct {
			Type    string
			Message struct {
				ID      string
				Content json.RawMessage
				Usage   claudeTokens
			}
			Payload struct {
				Type, Role, Message string
				Content             []struct{ Text string }
				Response            string `json:"response_id"`
				Usage               codexTokens
				Info                struct {
					Total *codexTokens `json:"total_token_usage"`
					Last  *codexTokens `json:"last_token_usage"`
				}
			}
		}
		if json.Unmarshal(scanner.Bytes(), &e) != nil {
			continue
		}
		user := ""
		if agent == "claude" && e.Type == "user" {
			user = string(e.Message.Content)
		}
		if agent == "codex" && e.Type == "event_msg" && e.Payload.Type == "user_message" {
			user = e.Payload.Message
		}
		if agent == "codex" && e.Type == "response_item" && e.Payload.Type == "message" && e.Payload.Role == "user" {
			for _, c := range e.Payload.Content {
				user += c.Text + "\n"
			}
		}
		if prompt != "" && strings.Contains(user, prompt) {
			collecting = true
		} else if attemptPromptRE.MatchString(user) {
			// A different attempt ends attribution even when its state path has spaces.
			collecting = false
		}
		if agent == "claude" && e.Type == "assistant" && collecting {
			if e.Message.ID != "" {
				if seen[e.Message.ID] {
					continue
				}
				seen[e.Message.ID] = true
			}
			u := e.Message.Usage
			if u.Input != nil || u.Output != nil || u.Read != nil || u.Write != nil {
				total.Found = true
			}
			total.InputTokens += value(u.Input)
			total.OutputTokens += value(u.Output)
			total.CacheReadTokens += value(u.Read)
			total.CacheCreationTokens += value(u.Write)
		}
		if agent != "codex" {
			continue
		}
		if e.Type == "token_usage_record" && collecting && e.Payload.Usage.present() {
			if e.Payload.Response != "" {
				if seen[e.Payload.Response] {
					continue
				}
				seen[e.Payload.Response] = true
			}
			total = *Add(&total, e.Payload.Usage.normalized())
		}
		if e.Type == "event_msg" && e.Payload.Type == "token_count" && e.Payload.Info.Total != nil {
			next := *e.Payload.Info.Total
			if collecting {
				delta := codexTokens{Input: new(max(0, value(next.Input)-value(prior.Input))), Output: new(max(0, value(next.Output)-value(prior.Output))), Read: new(max(0, value(next.Read)-value(prior.Read))), Write: new(max(0, value(next.Write)-value(prior.Write)))}
				legacy = *Add(&legacy, delta.normalized())
			}
			prior = next
		}
	}
	if !total.Found && legacy.Found {
		total = legacy
	}
	if total.Found {
		total.Source = agent
	}
	return total
}

type claudeTokens struct {
	Input  *int64 `json:"input_tokens"`
	Output *int64 `json:"output_tokens"`
	Read   *int64 `json:"cache_read_input_tokens"`
	Write  *int64 `json:"cache_creation_input_tokens"`
}
type codexTokens struct {
	Input  *int64 `json:"input_tokens"`
	Output *int64 `json:"output_tokens"`
	Read   *int64 `json:"cached_input_tokens"`
	Write  *int64 `json:"cache_write_input_tokens"`
}

func value(n *int64) int64 {
	if n == nil {
		return 0
	}
	return max(0, *n)
}

func (u codexTokens) present() bool {
	return u.Input != nil || u.Output != nil || u.Read != nil || u.Write != nil
}
func (u codexTokens) normalized() Usage {
	input := max(0, value(u.Input)-value(u.Read))
	write := min(input, value(u.Write))
	return Usage{InputTokens: input - write, OutputTokens: value(u.Output), CacheReadTokens: value(u.Read), CacheCreationTokens: write, Found: u.present()}
}
