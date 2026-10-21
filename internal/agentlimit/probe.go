package agentlimit

import (
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Probe checks the newest local main-session file for doctor, once per kind.
// It is advisory: missing logs do not make an installation unusable. Runtime
// classification uses exact session lookup instead of this diagnostic scan.
func Probe(kind string) string {
	var pattern string
	switch kind {
	case "codex":
		root := HomeRoot(nil, "CODEX_HOME", ".codex")
		if root == "" {
			return "session format unavailable"
		}
		pattern = filepath.Join(regexp.QuoteMeta(root), "sessions", "*", "*", "*", "*.jsonl")
	case "claude":
		root := HomeRoot(nil, "CLAUDE_CONFIG_DIR", ".claude")
		if root == "" {
			return "session format unavailable"
		}
		pattern = filepath.Join(regexp.QuoteMeta(root), "projects", "*", "*.jsonl")
	default:
		return "quota/login detection unsupported"
	}
	paths, e := filepath.Glob(pattern)
	if e != nil || len(paths) == 0 {
		return "session format unavailable (no main session log)"
	}
	var newest string
	var at time.Time
	for _, path := range paths {
		info, e := os.Stat(path)
		if e != nil {
			return "session format unavailable"
		}
		if info.Mode().IsRegular() && (newest == "" || info.ModTime().After(at)) {
			newest, at = path, info.ModTime()
		}
	}
	recognized := false
	valid := reverseLines(newest, func(line []byte) (bool, bool) {
		recognized = recognizedRecord(line, kind)
		return true, recognized
	})
	if !valid || !recognized {
		return "session format unrecognized; quota/login detection unavailable"
	}
	return "session format recognized; quota/login detection available"
}
