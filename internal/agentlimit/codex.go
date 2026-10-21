package agentlimit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func codexPath(id string, launched time.Time, env Env) string {
	root := HomeRoot(env, "CODEX_HOME", ".codex")
	if root == "" {
		return ""
	}
	var found string
	for _, delta := range []int{-1, 0, 1} {
		day := launched.In(time.Local).AddDate(0, 0, delta)
		dir := filepath.Join(root, "sessions", day.Format("2006/01/02"))
		entries, e := os.ReadDir(dir)
		if e != nil && !os.IsNotExist(e) {
			return ""
		}
		for _, entry := range entries {
			// A revert can leave the original rollout beside the new one. The
			// thread id alone cannot identify which terminal record is current.
			if strings.HasPrefix(entry.Name(), "rollout-") && strings.Contains(entry.Name(), "-"+id+"_") && (strings.HasSuffix(entry.Name(), ".jsonl") || strings.HasSuffix(entry.Name(), ".jsonl.zst")) {
				return ""
			}
			if !entry.IsDir() && strings.HasPrefix(entry.Name(), "rollout-") && strings.HasSuffix(entry.Name(), "-"+id+".jsonl") {
				if found != "" {
					return ""
				}
				found = filepath.Join(dir, entry.Name())
			}
		}
	}
	return found
}

type codexLine struct {
	Timestamp string
	Type      string
	Payload   struct {
		Type  string
		Error *struct {
			Code codexErrorCode `json:"codex_error_info"`
		}
		RateLimits *struct {
			Primary, Secondary *struct {
				UsedPercent float64 `json:"used_percent"`
				ResetsAt    int64   `json:"resets_at"`
			}
		} `json:"rate_limits"`
	}
}

type codexErrorCode string

// Codex emits unit variants as strings and HTTP failures as tagged objects.
// Normalize authentication failures to the existing stable login code.
func (c *codexErrorCode) UnmarshalJSON(data []byte) error {
	var code string
	if json.Unmarshal(data, &code) == nil {
		*c = codexErrorCode(code)
		return nil
	}
	var info struct {
		HTTPConnectionFailed *struct {
			Status int `json:"http_status_code"`
		} `json:"http_connection_failed"`
	}
	if e := json.Unmarshal(data, &info); e != nil {
		return e
	}
	*c = ""
	if info.HTTPConnectionFailed != nil && (info.HTTPConnectionFailed.Status == 401 || info.HTTPConnectionFailed.Status == 403) {
		*c = "unauthorized"
	}
	return nil
}

func classifyCodex(path string, submitted time.Time) *Limit {
	var result *Limit
	terminal := false
	valid := reverseLines(path, func(line []byte) (bool, bool) {
		var r codexLine
		if json.Unmarshal(line, &r) != nil {
			return false, false
		}
		older, valid := recordTime(r.Timestamp, submitted)
		if !valid || older {
			return true, valid
		}
		if r.Type != "event_msg" {
			return false, true
		}
		if !terminal && (r.Payload.Type == "task_complete" || r.Payload.Type == "turn_aborted") {
			terminal = true
			if r.Payload.Error == nil {
				return true, true
			}
			code := string(r.Payload.Error.Code)
			switch code {
			case "usage_limit_exceeded", "rate_limit_exceeded":
				result = &Limit{Kind: "agent_quota", Code: "codex:" + code}
			case "unauthorized":
				result = &Limit{Kind: "agent_login", Code: "codex:" + code}
				return true, true
			default:
				return true, true
			}
		}
		if terminal && result != nil && r.Payload.Type == "token_count" && r.Payload.RateLimits != nil {
			var reset int64
			full := false
			for _, w := range []*struct {
				UsedPercent float64 `json:"used_percent"`
				ResetsAt    int64   `json:"resets_at"`
			}{r.Payload.RateLimits.Primary, r.Payload.RateLimits.Secondary} {
				if w != nil && w.UsedPercent >= 100 {
					full = true
					reset = max(reset, w.ResetsAt)
				}
			}
			if reset > 0 {
				at := time.Unix(reset, 0).UTC()
				// Invalid RFC3339 instants cannot enter the durable journal.
				if at.Year() < 1 || at.Year() > 9999 {
					return false, false
				}
				result.ResetsAt = &at
			}
			return full, true
		}
		return false, true
	})
	if !valid {
		return nil
	}
	return result
}
