// Package herdr speaks Herdr's line-delimited JSON socket protocol (version 22).
package herdr

import "encoding/json"

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

type ServerID struct {
	Dev, Ino uint64
	CtimeNs  int64
}
type AgentSession struct {
	Source string `json:"source"`
	Agent  string `json:"agent"`
	Kind   string `json:"kind"`
	Value  string `json:"value"`
}

// Name retains the frozen runtime interface; a missing name is unproven.
// Missing session and sequence data remain nil, including on older servers.
type Agent struct {
	Name             string        `json:"name"`
	PaneID           string        `json:"pane_id"`
	TerminalID       string        `json:"terminal_id"`
	Status           string        `json:"agent_status"`
	InteractiveReady bool          `json:"interactive_ready"`
	LaunchPending    bool          `json:"launch_pending"`
	CompletionSeq    *uint64       `json:"completion_seq,omitempty"`
	Session          *AgentSession `json:"agent_session,omitempty"`
	WorkspaceID      string        `json:"workspace_id"`
	TabID            string        `json:"tab_id"`
}
type Pong struct {
	Version      string           `json:"version"`
	Protocol     uint32           `json:"protocol"`
	Capabilities *json.RawMessage `json:"capabilities,omitempty"`
}
type WorkspaceInfo struct {
	WorkspaceID string            `json:"workspace_id"`
	Number      uint64            `json:"number"`
	Label       string            `json:"label"`
	Focused     bool              `json:"focused"`
	PaneCount   uint64            `json:"pane_count"`
	TabCount    uint64            `json:"tab_count"`
	ActiveTabID string            `json:"active_tab_id"`
	Status      string            `json:"agent_status"`
	Tokens      map[string]string `json:"tokens,omitempty"`
	Worktree    *json.RawMessage  `json:"worktree,omitempty"`
}
type TabInfo struct {
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Number      uint64 `json:"number"`
	Label       string `json:"label"`
	Focused     bool   `json:"focused"`
	PaneCount   uint64 `json:"pane_count"`
	Status      string `json:"agent_status"`
}
type PaneInfo struct {
	PaneID      string            `json:"pane_id"`
	TerminalID  string            `json:"terminal_id"`
	WorkspaceID string            `json:"workspace_id"`
	TabID       string            `json:"tab_id"`
	Focused     bool              `json:"focused"`
	Status      string            `json:"agent_status"`
	Revision    uint64            `json:"revision"`
	Label       *string           `json:"label,omitempty"`
	Cwd         *string           `json:"cwd,omitempty"`
	Session     *AgentSession     `json:"agent_session,omitempty"`
	Tokens      map[string]string `json:"tokens,omitempty"`
}
type ProcessInfo struct {
	PaneID                   string    `json:"pane_id"`
	ShellPID                 *uint32   `json:"shell_pid,omitempty"`
	ForegroundProcessGroupID *uint32   `json:"foreground_process_group_id,omitempty"`
	TTY                      *string   `json:"tty,omitempty"`
	ForegroundProcesses      []Process `json:"foreground_processes,omitempty"`
}
type Process struct {
	PID     uint32    `json:"pid"`
	Name    string    `json:"name"`
	Argv0   *string   `json:"argv0,omitempty"`
	Argv    *[]string `json:"argv,omitempty"`
	Cmdline *string   `json:"cmdline,omitempty"`
	Cwd     *string   `json:"cwd,omitempty"`
}
type ReadResult struct {
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
	Source      string `json:"source"`
	Format      string `json:"format"`
	Text        string `json:"text"`
	Revision    uint64 `json:"revision"`
	Truncated   bool   `json:"truncated"`
}
