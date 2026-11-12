package session

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
)

type Session struct {
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
}

var agentPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var envPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func ValidAgent(s string) bool      { return agentPattern.MatchString(s) }
func ValidSessionEnv(s string) bool { return envPattern.MatchString(s) }
func (s Session) Valid() bool {
	if !ValidAgent(s.Agent) {
		return false
	}
	if s.Agent == "claude" || s.Agent == "codex" {
		return uuidPattern.MatchString(s.SessionID)
	}
	return idPattern.MatchString(s.SessionID)
}

// BuiltinEnvNames are the session variables understood by discovery.
var BuiltinEnvNames = []string{"CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "CODEX_SESSION_ID", "KATA_TRANSCRIPT_AGENT", "KATA_TRANSCRIPT_SESSION_ID"}

// EnvNames returns session variables that must not be inherited by a prompt.
func EnvNames(mapped map[string]string) []string {
	names := slices.Clone(BuiltinEnvNames)
	for _, name := range mapped {
		names = append(names, name)
	}
	return names
}

// DiscoverSession reads only the invoking agent's environment, never transcript files.
func DiscoverSession() *Session {
	agent, id := os.Getenv(BuiltinEnvNames[3]), os.Getenv(BuiltinEnvNames[4])
	if agent != "" || id != "" {
		s := &Session{agent, id}
		if s.Valid() {
			return s
		}
		return nil
	}
	vars := []struct{ agent, name string }{{"claude", BuiltinEnvNames[0]}, {"codex", BuiltinEnvNames[1]}, {"codex", BuiltinEnvNames[2]}}
	var extra map[string]string
	if json.Unmarshal([]byte(os.Getenv("HERDR_KATA_SESSIONS_ENV")), &extra) != nil && os.Getenv("HERDR_KATA_SESSIONS_ENV") != "" {
		return nil
	}
	for a, name := range extra {
		vars = append(vars, struct{ agent, name string }{a, name})
	}
	for _, v := range vars {
		value := os.Getenv(v.name)
		if value == "" {
			continue
		}
		if id != "" && (agent != v.agent || !strings.EqualFold(id, value)) {
			return nil
		}
		if id == "" {
			agent, id = v.agent, value
		}
	}
	s := &Session{agent, id}
	if !s.Valid() {
		return nil
	}
	return s
}
