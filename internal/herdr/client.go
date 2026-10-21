package herdr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/crashpoint"
	"io"
	"net"
	"time"
)

// Client is safe for concurrent calls. Path must remain unchanged while in use.
// Every call, including a long wait, owns a separate connection. Calls are never
// retried: a lost reply can follow a successful mutation.
type Client struct{ Path string }

// Call sends one JSON line and reads one response line. The response payload is
// bounded to 4 MiB, excluding the newline delimiter.
// Context cancellation closes the connection even without a deadline.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	crashpoint.Hit("herdr." + method + ".before")
	defer crashpoint.Hit("herdr." + method + ".after")
	if c.Path == "" {
		return fmt.Errorf("herdr: socket path unavailable; set HERDR_SOCKET_PATH or HOME")
	}
	if params == nil {
		params = struct{}{}
	}
	request := struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}{"1", method, params}
	data, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("herdr %s request: %w", method, err)
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", c.Path)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return err
		}
	}
	transportErr := func(err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("herdr %s: %w", method, err)
	}
	if _, err := io.Copy(conn, bytes.NewReader(append(data, '\n'))); err != nil {
		return transportErr(err)
	}
	const maxResponse = 4 << 20
	line, err := bufio.NewReader(io.LimitReader(conn, maxResponse+1)).ReadBytes('\n')
	if len(bytes.TrimSuffix(line, []byte{'\n'})) > maxResponse {
		return fmt.Errorf("herdr %s: response exceeds 4 MiB", method)
	}
	if err != nil {
		return transportErr(err)
	}
	var response struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *Error          `json:"error"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		return fmt.Errorf("herdr %s response: %w", method, err)
	}
	if response.ID != request.ID {
		return fmt.Errorf("herdr %s: mismatched response id %q", method, response.ID)
	}
	if response.Error != nil {
		if len(response.Result) != 0 || response.Error.Code == "" {
			return fmt.Errorf("herdr %s: malformed error envelope", method)
		}
		return response.Error
	}
	if len(response.Result) == 0 || bytes.Equal(bytes.TrimSpace(response.Result), []byte("null")) {
		return fmt.Errorf("herdr %s: missing result", method)
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(response.Result, result); err != nil {
		return fmt.Errorf("herdr %s result: %w", method, err)
	}
	return nil
}

// result checks the protocol discriminator and required payload independently
// of the optional fields inside that payload. Unknown fields are tolerated.
func (c *Client) result(ctx context.Context, method string, params any, typ, field string, out any) error {
	var obj map[string]json.RawMessage
	if err := c.Call(ctx, method, params, &obj); err != nil {
		return err
	}
	var got string
	if err := json.Unmarshal(obj["type"], &got); err != nil || got != typ {
		return fmt.Errorf("herdr %s: unexpected result type %q", method, got)
	}
	if field == "" {
		b, _ := json.Marshal(obj)
		return json.Unmarshal(b, out)
	}
	b := obj[field]
	if len(b) == 0 || bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return fmt.Errorf("herdr %s: missing %s", method, field)
	}
	return json.Unmarshal(b, out)
}
func (c *Client) Ping(ctx context.Context) (Pong, error) {
	var wire struct {
		Version      *string          `json:"version"`
		Protocol     *uint32          `json:"protocol"`
		Capabilities *json.RawMessage `json:"capabilities"`
	}
	if err := c.result(ctx, "ping", nil, "pong", "", &wire); err != nil {
		return Pong{}, err
	}
	if wire.Version == nil || *wire.Version == "" || wire.Protocol == nil {
		return Pong{}, fmt.Errorf("herdr ping: missing version or protocol")
	}
	return Pong{Version: *wire.Version, Protocol: *wire.Protocol, Capabilities: wire.Capabilities}, nil
}
func (c *Client) WorkspaceCreate(ctx context.Context, label, cwd string) (WorkspaceInfo, error) {
	var v WorkspaceInfo
	err := c.result(ctx, "workspace.create", map[string]any{"label": label, "cwd": cwd, "focus": false}, "workspace_created", "workspace", &v)
	return v, err
}
func (c *Client) WorkspaceGet(ctx context.Context, id string) (WorkspaceInfo, error) {
	var v WorkspaceInfo
	err := c.result(ctx, "workspace.get", map[string]string{"workspace_id": id}, "workspace_info", "workspace", &v)
	return v, err
}
func (c *Client) WorkspaceList(ctx context.Context) ([]WorkspaceInfo, error) {
	var v []WorkspaceInfo
	err := c.result(ctx, "workspace.list", nil, "workspace_list", "workspaces", &v)
	return v, err
}
func (c *Client) TabCreate(ctx context.Context, ws, label, cwd string, env map[string]string) (TabInfo, PaneInfo, error) {
	var v struct {
		Tab  TabInfo  `json:"tab"`
		Pane PaneInfo `json:"root_pane"`
	}
	params := map[string]any{"workspace_id": ws, "label": label, "cwd": cwd, "focus": false}
	if env != nil {
		params["env"] = env
	}
	err := c.result(ctx, "tab.create", params, "tab_created", "", &v)
	if err == nil && (v.Tab.TabID == "" || v.Pane.PaneID == "") {
		err = fmt.Errorf("herdr tab.create: missing tab or root pane")
	}
	return v.Tab, v.Pane, err
}
func (c *Client) TabGet(ctx context.Context, id string) (TabInfo, error) {
	var v TabInfo
	err := c.result(ctx, "tab.get", map[string]string{"tab_id": id}, "tab_info", "tab", &v)
	return v, err
}
func (c *Client) TabClose(ctx context.Context, id string) error {
	return c.result(ctx, "tab.close", map[string]string{"tab_id": id}, "ok", "", &struct{}{})
}
func (c *Client) PaneGet(ctx context.Context, id string) (PaneInfo, error) {
	var v PaneInfo
	err := c.result(ctx, "pane.get", map[string]string{"pane_id": id}, "pane_info", "pane", &v)
	return v, err
}
func (c *Client) PaneProcessInfo(ctx context.Context, id string) (ProcessInfo, error) {
	var v ProcessInfo
	err := c.result(ctx, "pane.process_info", map[string]string{"pane_id": id}, "pane_process_info", "process_info", &v)
	return v, err
}
func (c *Client) PaneReportMetadata(ctx context.Context, id string, tokens map[string]string) error {
	params := map[string]any{"pane_id": id, "source": "herdr-kata"}
	if tokens != nil {
		params["tokens"] = tokens
	}
	return c.result(ctx, "pane.report_metadata", params, "ok", "", &struct{}{})
}
func (c *Client) AgentStart(ctx context.Context, name, kind, pane string, args []string) (Agent, error) {
	if args == nil {
		args = []string{}
	}
	var v Agent
	err := c.result(ctx, "agent.start", map[string]any{"name": name, "kind": kind, "pane_id": pane, "args": args}, "agent_started", "agent", &v)
	return v, err
}
func (c *Client) AgentGet(ctx context.Context, target string) (Agent, error) {
	var v Agent
	err := c.result(ctx, "agent.get", map[string]string{"target": target}, "agent_info", "agent", &v)
	return v, err
}
func (c *Client) AgentList(ctx context.Context) ([]Agent, error) {
	var v []Agent
	err := c.result(ctx, "agent.list", nil, "agent_list", "agents", &v)
	return v, err
}
func waitParams(target string, until []string, timeout time.Duration) (map[string]any, error) {
	if timeout < 0 {
		return nil, fmt.Errorf("herdr: negative wait timeout")
	}
	p := map[string]any{"target": target}
	if len(until) > 0 {
		p["until"] = until
	}
	if timeout > 0 {
		ms := uint64(timeout / time.Millisecond)
		if timeout%time.Millisecond != 0 {
			ms++
		}
		p["timeout_ms"] = ms
	}
	return p, nil
}

// AgentPrompt submits once and waits for a settled status. A zero timeout uses
// the server's indefinite wait; the context still bounds the local connection.
func (c *Client) AgentPrompt(ctx context.Context, target, text string, until []string, timeout time.Duration) (Agent, error) {
	wait, err := waitParams(target, until, timeout)
	if err != nil {
		return Agent{}, err
	}
	delete(wait, "target")
	var v Agent
	err = c.result(ctx, "agent.prompt", map[string]any{"target": target, "text": text, "wait": wait}, "agent_prompted", "agent", &v)
	return v, err
}
func (c *Client) AgentWait(ctx context.Context, target string, until []string, timeout time.Duration) (Agent, error) {
	p, err := waitParams(target, until, timeout)
	if err != nil {
		return Agent{}, err
	}
	var v Agent
	err = c.result(ctx, "agent.wait", p, "agent_info", "agent", &v)
	return v, err
}
func (c *Client) AgentRead(ctx context.Context, target, source string, lines *uint32) (ReadResult, error) {
	p := map[string]any{"target": target, "source": source, "format": "text", "strip_ansi": true}
	if lines != nil {
		p["lines"] = *lines
	}
	var v ReadResult
	err := c.result(ctx, "agent.read", p, "pane_read", "read", &v)
	return v, err
}
