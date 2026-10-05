// Package katacli calls the installed Kata public CLI. It owns no daemon state.
package katacli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

// Target is explicit local routing. Token and Home are never definition fields.
type Target struct {
	Server, Daemon, Project, Workspace, Actor, Teammate, Home, Token string
	TrustPrivateNetwork                                              bool
}
type Client struct {
	Executable  string
	Target      Target
	Timeout     time.Duration
	OutputLimit int
}
type CommandError struct {
	ExitCode int
	Stdout   json.RawMessage
	Stderr   string
	Cause    error
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("kata command failed (exit %d): %v", e.ExitCode, e.Cause)
}
func (e *CommandError) Unwrap() error { return e.Cause }

var ErrOutputLimit = errors.New("kata output exceeds limit")

func (t Target) Validate() error {
	if (t.Server == "") == (t.Daemon == "") || strings.TrimSpace(t.Project) == "" || strings.TrimSpace(t.Workspace) == "" || strings.TrimSpace(t.Actor) == "" {
		return errors.New("configure one Kata server or daemon, project, workspace and actor before saving")
	}
	return nil
}
func (c *Client) command(ctx context.Context, args []string, jsonOutput bool) (*exec.Cmd, error) {
	if err := c.Target.Validate(); err != nil {
		return nil, err
	}
	bin := c.Executable
	if bin == "" {
		bin = "kata"
	}
	// Resolve on every invocation. Do not retain an executable inode or version.
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("install or configure Kata: %w", err)
	}
	t := c.Target
	argv := []string{}
	if t.Daemon != "" {
		argv = append(argv, "--daemon", t.Daemon)
	}
	argv = append(argv, "--project", t.Project, "--workspace", t.Workspace, "--as", t.Actor, "--teammate", t.Teammate)
	if jsonOutput {
		argv = append(argv, "--json")
	}
	argv = append(argv, args...)
	if err := validateProcessArguments(argv, runtime.GOOS == "windows"); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, resolved, argv...)
	// Preserve OS/terminal setup and native user configuration, but remove every
	// ambient Kata override, credential, proxy and hosted daemon selector.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "PATH", "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "SYSTEMROOT", "WINDIR", "COMSPEC", "TEMP", "TMP", "TMPDIR", "TERM", "COLORTERM", "LANG", "LC_ALL", "LC_CTYPE", "PATHEXT", "VISUAL", "EDITOR", "TERM_PROGRAM", "TERM_PROGRAM_VERSION", "COLORFGBG", "NO_COLOR", "CLICOLOR", "CLICOLOR_FORCE":
			cmd.Env = append(cmd.Env, entry)
		}
	}
	if t.Server != "" {
		cmd.Env = append(cmd.Env, "KATA_SERVER="+t.Server)
	}
	if t.Home != "" {
		cmd.Env = append(cmd.Env, "KATA_HOME="+t.Home)
	}
	if t.Token != "" {
		cmd.Env = append(cmd.Env, "KATA_AUTH_TOKEN="+t.Token)
	}
	if t.TrustPrivateNetwork {
		cmd.Env = append(cmd.Env, "KATA_TRUST_PRIVATE_NETWORK=1")
	}
	return cmd, nil
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
	cancel   context.CancelFunc
}

func (b *boundedBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *boundedBuffer) String() string { return b.buffer.String() }
func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if len(p) > remaining {
		b.buffer.Write(p[:remaining])
		b.exceeded = true
		b.cancel()
		return remaining, ErrOutputLimit
	}
	return b.buffer.Write(p)
}

// Call returns decoded exact JSON only after a successful finite command.
// Failed commands retain bounded raw output for conflict/receipt inspection.
func (c *Client) Call(ctx context.Context, args []string, body json.RawMessage, out any) error {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	childCtx, stop := context.WithCancel(ctx)
	defer stop()
	cmd, err := c.command(childCtx, args, true)
	if err != nil {
		return err
	}
	limit := c.OutputLimit
	if limit <= 0 {
		limit = 2 * 1024 * 1024
	}
	stdout := &boundedBuffer{limit: limit, cancel: stop}
	stderr := &boundedBuffer{limit: limit, cancel: stop}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = bytes.NewReader(body)
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	} else if stdout.exceeded || stderr.exceeded {
		err = ErrOutputLimit
	}
	if err != nil {
		code := -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
		return &CommandError{ExitCode: code, Stdout: append(json.RawMessage(nil), stdout.Bytes()...), Stderr: stderr.String(), Cause: err}
	}
	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	dec.UseNumber()
	if out == nil {
		out = new(any)
	}
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("invalid Kata JSON: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return errors.New("Kata returned trailing JSON")
	}
	return nil
}

// TUICommand uses the installed public interactive command and its own config.
// Callers attach terminal IO; the finite adapter timeout does not stop a TUI.
func (c *Client) TUICommand(ctx context.Context, issue string) (*exec.Cmd, error) {
	if _, err := c.Capabilities(ctx); err != nil {
		return nil, err
	}
	args := []string{"tui"}
	if issue != "" {
		args = append(args, "--", issue)
	}
	return c.command(ctx, args, false)
}

func validateProcessArguments(args []string, windows bool) error {
	if windows {
		for _, arg := range args {
			if !utf8.ValidString(arg) {
				return errors.New("Windows Kata arguments must contain valid UTF-8")
			}
		}
	}
	return nil
}
