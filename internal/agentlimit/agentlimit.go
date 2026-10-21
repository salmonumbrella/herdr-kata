// Package agentlimit recognizes structured quota and login failures without
// retaining transcript text. A missing or unfamiliar log is never a run error.
package agentlimit

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
)

type Limit struct {
	Kind     string
	Code     string
	ResetsAt *time.Time `json:",omitempty"`
}

var uuid = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Classify returns the failure that ended this submission, if its main session
// log can be located and read within the bounds. Call only after stopped proof,
// or for an informational blocked notification.
func Classify(session herdr.AgentSession, cwd string, launchedAt, submittedAt time.Time, env Env) *Limit {
	if submittedAt.IsZero() {
		return nil
	}
	path := SessionLogPath(session, cwd, launchedAt, env)
	switch session.Agent {
	case "codex":
		return classifyCodex(path, submittedAt)
	case "claude":
		return classifyClaude(path, submittedAt)
	}
	return nil
}

// Env reads the agent's environment, which can differ from the worker's: its
// CODEX_HOME or CLAUDE_CONFIG_DIR may come from the Herdr server alone. Nil
// reads the worker's own environment.
type Env func(string) string

// SessionLogPath resolves the same exact, unambiguous main log for quota and usage.
func SessionLogPath(session herdr.AgentSession, cwd string, launchedAt time.Time, env Env) string {
	if session.Source != "herdr:"+session.Agent || session.Kind != "id" || !uuid.MatchString(session.Value) {
		return ""
	}
	switch session.Agent {
	case "codex":
		if !launchedAt.IsZero() {
			return codexPath(session.Value, launchedAt, env)
		}
	case "claude":
		return claudePath(session.Value, cwd, env)
	}
	return ""
}

// HomeRoot returns an agent's home directory: key, else fallback under HOME.
func HomeRoot(env Env, key, fallback string) string {
	if env == nil {
		env = os.Getenv
	}
	if root := env(key); root != "" {
		return root
	}
	home := env("HOME")
	if home == "" {
		var e error
		if home, e = os.UserHomeDir(); e != nil {
			return ""
		}
	}
	return filepath.Join(home, fallback)
}

const (
	chunkBytes   = 64 << 10
	maxTailBytes = 8 << 20
	maxLineBytes = 1 << 20
)

// reverseLines snapshots EOF, then visits complete lines newest first. It never
// accepts a partial tail or a line cut by the read bound as a complete record.
func reverseLines(path string, visit func([]byte) (stop, valid bool)) bool {
	if path == "" {
		return false
	}
	f, e := os.Open(path)
	if e != nil {
		return false
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		return false
	}
	pos, read := info.Size(), int64(0)
	var pending []byte
	for pos > 0 {
		n := min(int64(chunkBytes), pos, int64(maxTailBytes)-read)
		if n <= 0 {
			return false
		}
		pos -= n
		buf := make([]byte, int(n))
		if _, e = f.ReadAt(buf, pos); e != nil {
			return false
		}
		read += n
		buf = append(buf, pending...)
		for {
			i := bytes.LastIndexByte(buf, '\n')
			if i < 0 {
				break
			}
			line := buf[i+1:]
			if len(line) > maxLineBytes {
				return false
			}
			if len(bytes.TrimSpace(line)) > 0 {
				stop, valid := visit(line)
				if !valid {
					return false
				}
				if stop {
					return true
				}
			}
			buf = buf[:i]
		}
		if len(buf) > maxLineBytes {
			return false
		}
		pending = buf
	}
	if len(bytes.TrimSpace(pending)) > 0 {
		_, valid := visit(pending)
		return valid
	}
	return true
}

func recordTime(raw string, submitted time.Time) (older, valid bool) {
	at, e := time.Parse(time.RFC3339Nano, raw)
	return at.Before(submitted), e == nil
}

// recognizedRecord checks supported code and number fields as well as the
// envelope, so doctor can detect format drift. It never decodes message text.
func recognizedRecord(line []byte, agent string) bool {
	if agent == "codex" {
		var r codexLine
		if json.Unmarshal(line, &r) != nil {
			return false
		}
		_, valid := recordTime(r.Timestamp, time.Time{})
		return valid && r.Type == "event_msg" && r.Payload.Type != ""
	}
	var r claudeLine
	if json.Unmarshal(line, &r) != nil {
		return false
	}
	_, valid := recordTime(r.Timestamp, time.Time{})
	return valid && (r.Type == "assistant" || r.Type == "user" || r.Type == "system")
}
