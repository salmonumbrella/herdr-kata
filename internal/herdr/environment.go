package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// agentHomes are the variables that place an agent's session logs. Their
// absolute paths are the only values the probe reports.
var agentHomes = []string{"HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR"}

// WriteEnvironmentNames runs in the probe pane. Only names cross back to the
// worker, plus NAME=path entries for the agent home directories; no other
// value enters a file or terminal output. An older worker reads those
// entries as names that are never credentials.
func WriteEnvironmentNames(out io.Writer, env []string) error {
	names := make([]string, 0, len(env))
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		names = append(names, name)
		if slices.Contains(agentHomes, name) && filepath.IsAbs(value) && !strings.ContainsAny(value, "\n\r") {
			names = append(names, name+"="+value)
		}
	}
	sort.Strings(names)
	return json.NewEncoder(out).Encode(names)
}

// parseEnvironmentNames separates the probe's names from its agent homes.
func parseEnvironmentNames(entries []string) ([]string, map[string]string) {
	names, homes := make([]string, 0, len(entries)), map[string]string{}
	for _, entry := range entries {
		if name, value, ok := strings.Cut(entry, "="); ok {
			if slices.Contains(agentHomes, name) && filepath.IsAbs(value) {
				homes[name] = value
			}
			continue
		}
		names = append(names, entry)
	}
	return names, homes
}

// EnvironmentProbe caches proof only for one server generation and launch map.
// Failed proofs are retried so repairing the launch environment needs no restart.
type EnvironmentProbe struct {
	mu        sync.Mutex
	server    ServerID
	signature string
	clean     bool
	homes     map[string]string // from the last clean probe
}

// AgentHomes returns the agent home directories the last clean probe saw,
// or nil before any probe has passed.
func (p *EnvironmentProbe) AgentHomes() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return maps.Clone(p.homes)
}

func (p *EnvironmentProbe) Check(ctx context.Context, c *Client, workspace func(context.Context) (string, error), env map[string]string, credentials []string, bin, cwd string) (proven ServerID, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c == nil {
		return ServerID{}, errors.New("herdr client unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
			proven = ServerID{}
			p.clean = false
		}
	}()
	id, err := ReadServerID(c.Path)
	if err != nil {
		p.clean = false
		return ServerID{}, errors.New("cannot prove Herdr server identity")
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	creds := slices.Clone(credentials)
	sort.Strings(creds)
	signatureBytes, _ := json.Marshal([]any{keys, creds, bin, cwd})
	signature := string(signatureBytes)
	if p.clean && p.server == id && p.signature == signature {
		return id, nil
	}
	p.clean = false
	ws, err := workspace(ctx)
	if err != nil {
		return ServerID{}, errors.New("cannot create environment probe workspace")
	}
	root, err := os.MkdirTemp("", "hk-env-")
	if err != nil {
		return ServerID{}, errors.New("cannot create environment probe directory")
	}
	defer os.RemoveAll(root)
	output := filepath.Join(root, "names.json")
	tab, pane, err := c.TabCreate(ctx, ws, "Environment check", cwd, env)
	if err != nil {
		return ServerID{}, errors.New("cannot create environment probe tab")
	}
	defer func() {
		// Only close the tab in the server generation which created it.
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if current, e := ReadServerID(c.Path); e == nil && current == id {
			_ = c.TabClose(closeCtx, tab.TabID)
		}
	}()
	// Wait for the actual shell before sending the harmless helper invocation.
	for {
		info, e := c.PaneProcessInfo(ctx, pane.PaneID)
		if e != nil {
			return ServerID{}, errors.New("cannot inspect environment probe shell")
		}
		if info.ShellPID != nil && info.ForegroundProcessGroupID != nil && *info.ShellPID == *info.ForegroundProcessGroupID {
			break
		}
		select {
		case <-ctx.Done():
			return ServerID{}, errors.New("environment probe shell timed out")
		case <-time.After(25 * time.Millisecond):
		}
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	text := quote(bin) + " env-names " + quote(output)
	if err := c.Call(ctx, "pane.send_input", map[string]any{"pane_id": pane.PaneID, "text": text, "keys": []string{"enter"}}, nil); err != nil {
		return ServerID{}, errors.New("cannot start environment probe")
	}
	var names []string
	for {
		data, e := os.ReadFile(output)
		if e == nil && len(data) <= 1<<20 && json.Unmarshal(data, &names) == nil && names != nil {
			break
		}
		select {
		case <-ctx.Done():
			return ServerID{}, errors.New("environment probe did not return names")
		case <-time.After(25 * time.Millisecond):
		}
	}
	names, homes := parseEnvironmentNames(names)
	current, err := ReadServerID(c.Path)
	if err != nil || current != id {
		return ServerID{}, errors.New("herdr server changed during environment probe")
	}
	var leaks []string
	for _, name := range names {
		// Credentials are never permitted, even if a bad map supplied one.
		credential := name == "KATA_AUTH_TOKEN" || slices.Contains(creds, name)
		if credential || strings.HasPrefix(name, "KATA_") {
			if _, set := env[name]; credential || !set {
				leaks = append(leaks, name)
			}
		}
	}
	sort.Strings(leaks)
	leaks = slices.Compact(leaks)
	if len(leaks) > 0 {
		return ServerID{}, fmt.Errorf("herdr launch environment leaks %s from shell startup files (such as ~/.zshrc) or the server environment; unset them in rc files or guard exports so they are skipped in agent panes (HERDR_KATA_RUN is set); if inherited from the server, restart Herdr without them", strings.Join(leaks, ", "))
	}
	p.server, p.signature, p.clean, p.homes = id, signature, true, homes
	return id, nil
}
