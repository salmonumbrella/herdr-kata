package agentlimit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var slugChars = regexp.MustCompile(`[^A-Za-z0-9]`)

func claudePath(id, cwd string, env Env) string {
	root := HomeRoot(env, "CLAUDE_CONFIG_DIR", ".claude")
	if root == "" {
		return ""
	}
	path := filepath.Join(root, "projects", slugChars.ReplaceAllString(cwd, "-"), id+".jsonl")
	if info, e := os.Stat(path); e == nil && info.Mode().IsRegular() {
		return path
	}
	// Escape glob metacharacters in the environment-owned root.
	root = regexp.QuoteMeta(root)
	paths, e := filepath.Glob(filepath.Join(root, "projects", "*", id+".jsonl"))
	if e != nil || len(paths) != 1 {
		return ""
	}
	return paths[0]
}

type claudeLine struct {
	Timestamp, Type, Error string
	APIError               bool `json:"isApiErrorMessage"`
}

func classifyClaude(path string, submitted time.Time) *Limit {
	var result *Limit
	valid := reverseLines(path, func(line []byte) (bool, bool) {
		var r claudeLine
		if json.Unmarshal(line, &r) != nil {
			return false, false
		}
		older, valid := recordTime(r.Timestamp, submitted)
		if !valid || older {
			return true, valid
		}
		if r.Type != "assistant" {
			return false, true
		}
		if !r.APIError {
			return true, true
		}
		kind := ""
		switch r.Error {
		case "rate_limit", "billing_error":
			kind = "agent_quota"
		case "authentication_failed", "unauthenticated", "invalid_grant":
			kind = "agent_login"
		}
		if kind != "" {
			result = &Limit{Kind: kind, Code: "claude:" + r.Error}
		}
		return true, true
	})
	if !valid {
		return nil
	}
	return result
}
