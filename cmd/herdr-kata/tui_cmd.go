package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/inbox"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"github.com/salmonumbrella/herdr-kata/internal/tui"
)

type socketCaller interface {
	Call(context.Context, string, any, any) error
}

const agentSource = "custom:herdr-kata-board"

func openTUI(ctx context.Context, cfg config.Config, h socketCaller, startup, wide, pin bool) error {
	if startup && (!cfg.Board.Pin || herdr.SessionFromSocket(herdr.SocketPath(cfg.Session)) != cfg.Session) {
		return nil
	}
	if startup {
		root := state.DefaultRoot()
		if err := os.MkdirAll(root, 0700); err != nil {
			return err
		}
		lock, ok, err := state.TryLock(filepath.Join(root, "board-start.lock"))
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		defer lock.Unlock() //nolint:errcheck // Lock cleanup remains best-effort.
		var list struct {
			Panes []herdr.PaneInfo `json:"panes"`
		}
		if err := h.Call(ctx, "pane.list", nil, &list); err != nil {
			return err
		}
		for _, p := range list.Panes {
			if p.Tokens["herdr_kata_board"] == "pinned" {
				return nil
			}
		}
	}
	// The boot TUI takes focus and opens Kata over itself, so quitting Kata
	// lands on the board.
	params := map[string]any{"plugin_id": "kata", "entrypoint": "board", "placement": "tab", "focus": true}
	if pane := os.Getenv("HERDR_PANE_ID"); pane != "" && !startup {
		params["target_pane_id"] = pane
		params["placement"] = "split"
		params["direction"] = "right"
		if wide {
			params["direction"] = "down"
		}
	}
	if startup || pin {
		env := map[string]string{"HERDR_KATA_BOARD_PIN": "1"}
		if startup {
			env["HERDR_KATA_OPEN_KATA"] = "1"
		}
		params["env"] = env
	}
	var result struct {
		Pane struct {
			Pane herdr.PaneInfo `json:"pane"`
		} `json:"plugin_pane"`
	}
	if err := h.Call(ctx, "plugin.pane.open", params, &result); err != nil {
		var apiError *herdr.Error
		if startup && errors.As(err, &apiError) && apiError.Code == "no_active_workspace" {
			return nil
		}
		return err
	}
	if startup && result.Pane.Pane.PaneID != "" {
		if err := h.Call(ctx, "pane.report_metadata", map[string]any{"pane_id": result.Pane.Pane.PaneID, "source": agentSource, "tokens": map[string]string{"herdr_kata_board": "pinned"}, "ttl_ms": 10000}, nil); err != nil {
			slog.Warn("board opened; initial pin metadata unavailable", "error", err)
		}
	}
	return nil
}
func tuiCommand(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("tui", flag.ContinueOnError)
	f.SetOutput(stderr)
	var wide, pin, startup bool
	f.BoolVar(&wide, "wide", args[0] == "board-wide", "open a horizontal split of the caller's pane")
	f.BoolVar(&pin, "pin", false, "show a sidebar agent row")
	f.BoolVar(&startup, "startup", false, "open one configured pinned TUI without focus")
	if err := f.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 {
		fmt.Fprintln(stderr, "tui takes no positional arguments")
		return 2
	}
	cfg, err := config.Load(config.DefaultPath())
	if startup && errors.Is(err, fs.ErrNotExist) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &herdr.Client{Path: herdr.SocketPath(cfg.Session)}
	if startup || wide || !term.IsTerminal(os.Stdin.Fd()) {
		openCtx, done := context.WithTimeout(ctx, time.Second)
		defer done()
		if err := openTUI(openCtx, *cfg, h, startup, wide, pin); err != nil {
			fmt.Fprintf(stderr, "tui needs a terminal or an active Herdr session: %v\n", err)
			return 1
		}
		if !startup && !term.IsTerminal(os.Stdin.Fd()) {
			fmt.Fprintln(stdout, "herdr-kata: no TTY here — opened the TUI as a Herdr pane instead")
		}
		return 0
	}
	l := state.Layout{Root: state.DefaultRoot()}
	if _, err := state.ProjectLayouts(state.OSFS{}, l); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	opts := tui.Options{OpenKata: os.Getenv("HERDR_KATA_OPEN_KATA") == "1", Root: l.Root, Projects: map[string]*tui.Lane{}, Config: *cfg, Service: &tuiService{cfg: *cfg, h: h, ctx: ctx}}
	views := map[string]config.Config{}
	var displayLanes [][]string
	for _, entry := range cfg.ProjectEntries() {
		view := cfg.View(entry)
		views[entry.Name] = view
		displayLanes = append(displayLanes, kata.New(view.Kata, l.Root, nil, nil).Env())
	}
	makeOptions := func(view config.Config, uid string) tui.Lane {
		k := kata.New(view.Kata, l.Root, nil, nil)
		svc := &tuiService{CLI: k, cfg: view, h: h, ctx: ctx, projectUID: uid}
		return tui.Lane{Config: view, ProjectUID: uid, Project: view.Kata.Project, Service: svc}
	}
	for name, view := range views {
		lane := makeOptions(view, "")
		opts.Projects[name] = &lane
	}
	opts.ForProject = func(uid, name string) tui.Lane {
		view, ok := views[name]
		if !ok {
			return tui.Lane{}
		}
		return makeOptions(view, uid)
	}
	pane := os.Getenv("HERDR_PANE_ID")
	if (pin || os.Getenv("HERDR_KATA_BOARD_PIN") == "1") && pane != "" {
		opts.Pin = func(d tui.Data) error {
			c, done := context.WithTimeout(ctx, time.Second)
			defer done()
			if err := h.Call(c, "pane.report_metadata", map[string]any{"pane_id": pane, "source": agentSource, "tokens": map[string]string{"herdr_kata_board": "pinned"}, "ttl_ms": 10000}, nil); err != nil {
				return err
			}

			return h.Call(c, "pane.report_agent", map[string]any{"pane_id": pane, "source": agentSource, "agent": "kata", "state": "idle", "message": tui.Counts(d)}, nil)
		}
		defer func() {
			c, done := context.WithTimeout(context.Background(), time.Second)
			defer done()
			_ = h.Call(c, "pane.release_agent", map[string]string{"pane_id": pane, "source": agentSource, "agent": "kata"}, nil)
			_ = h.Call(c, "pane.report_metadata", map[string]any{"pane_id": pane, "source": agentSource, "tokens": map[string]any{"herdr_kata_board": nil}}, nil)
		}()
	}
	opts.Stdout, opts.DisplayLanes = stdout, displayLanes
	if err := tui.Run(ctx, opts); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

type tuiService struct {
	projectUID string
	*kata.CLI
	cfg   config.Config
	h     socketCaller
	guard *kata.RouteGuard
	ctx   context.Context
}

func (s *tuiService) SaveJob(ctx context.Context, j kata.Job, parent string) (kata.Job, error) {
	if err := s.check(ctx); err != nil {
		return kata.Job{}, err
	}
	var d defs.JobDef
	if err := json.Unmarshal(j.Definition, &d); err != nil {
		return kata.Job{}, &kata.Error{Code: "validation", Message: err.Error(), Exit: 6}
	}
	if d.Enabled && d.Action.Kind == "notify" && d.Issue != nil && d.Issue.Kind == "per-run" {
		i, err := s.CLI.ShowIssue(ctx, d.Issue.ParentUID)
		if err != nil {
			return kata.Job{}, err
		}
		if _, err = inbox.ResolveBinding(ctx, kata.GuardedReader{CLI: s.CLI, Guard: s.guard}, i, s.cfg); err != nil {
			return kata.Job{}, &kata.Error{Code: "validation", Message: fmt.Sprintf("parent binding: %v", err), Exit: 6}
		}
	}
	saved, err := s.CLI.SaveJob(ctx, j, parent)
	if err != nil && (kata.Class(err) == kata.Conflict || ambiguousWrite(err)) {
		newer, readErr := s.CLI.ShowJob(ctx, j.UID)
		if readErr != nil {
			err = fmt.Errorf("%w; readback failed: %v", err, readErr)
		}
		if readErr == nil && kata.SameDefinition(j, newer) {
			return newer, nil
		}
		return kata.Job{}, &tui.ConflictError{Pending: j, Newer: newer, Cause: err}
	}
	return saved, err
}
func (s *tuiService) SaveWorkflow(ctx context.Context, w kata.Workflow) (kata.Workflow, error) {
	if err := s.check(ctx); err != nil {
		return kata.Workflow{}, err
	}
	return s.CLI.SaveWorkflow(ctx, w)
}
func (s *tuiService) MetaSet(ctx context.Context, ref, key, value string, c kata.Cond) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	_, err := s.CLI.MetaSet(ctx, ref, key, value, c)
	return err
}
func (s *tuiService) MetaUnset(ctx context.Context, ref, key string, c kata.Cond) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	_, err := s.CLI.MetaUnset(ctx, ref, key, c)
	return err
}
func (s *tuiService) Pause(j kata.Job) (kata.Job, error) {
	var d map[string]json.RawMessage
	if err := json.Unmarshal(j.Definition, &d); err != nil {
		return kata.Job{}, err
	}
	var enabled bool
	if raw := d["enabled"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &enabled); err != nil {
			return kata.Job{}, err
		}
	}
	d["enabled"], _ = json.Marshal(!enabled)
	j.Definition, _ = json.Marshal(d)
	return s.SaveJob(s.ctx, j, "")
}
func (s *tuiService) Delete(j kata.Job) error {
	if err := s.check(s.ctx); err != nil {
		return err
	}
	err := s.CLI.DeleteJob(s.ctx, j)
	if err != nil && (kata.Class(err) == kata.Conflict || ambiguousWrite(err)) {
		newer, readErr := s.CLI.ShowJob(s.ctx, j.UID)
		if readErr != nil {
			err = fmt.Errorf("%w; readback failed: %v", err, readErr)
		}
		if readErr == nil && newer.DeletedAt != nil {
			return nil
		}
		return &tui.ConflictError{Pending: j, Newer: newer, Cause: err}
	}
	return err
}
func (s *tuiService) Focus(r runner.Run) error {
	if runner.AgentReference(&r).PaneID == "" {
		return fmt.Errorf("run has no agent pane")
	}
	id, err := herdr.ReadServerID(herdr.SocketPath(s.cfg.Session))
	if err != nil {
		return err
	}
	if runner.AgentReference(&r).ServerID == (herdr.ServerID{}) || id != runner.AgentReference(&r).ServerID {
		return fmt.Errorf("run belongs to an unproven or previous Herdr server")
	}

	err = s.h.Call(s.ctx, "pane.focus", map[string]string{"pane_id": runner.AgentReference(&r).PaneID}, nil)
	var apiErr *herdr.Error
	if errors.As(err, &apiErr) && apiErr.Code == "pane_not_found" {
		return fmt.Errorf("agent pane is gone; the run record is still available")
	}
	return err
}

// KataCommand uses the worker's recorded route without CLI probes. The board
// starts this command with a PTY covering its entire body rectangle.
func (s *tuiService) KataCommand(issue string) (*exec.Cmd, error) {
	pin, err := kata.RecordedRoute(s.cfg, s.projectUID)
	if err != nil {
		return nil, err
	}
	args := []string{"--project", pin.Name, "--as", pin.Actor, "--teammate", "herdr-kata"}
	if pin.Route.Daemon != "" {
		args = append(args, "--daemon", pin.Route.Daemon)
	}
	args = append(args, "tui")
	if issue != "" {
		args = append(args, issue)
	}
	cmd := exec.Command(pin.Route.Bin, args...)
	cmd.Dir = state.DefaultRoot()
	cmd.Env = kata.TerminalEnv(s.CLI.Env(), os.Environ())
	return cmd, nil
}

func (s *tuiService) check(ctx context.Context) error {
	if s.CLI == nil {
		return fmt.Errorf("project is not configured")
	}
	if s.guard == nil {
		root := state.Layout{Root: state.DefaultRoot()}
		if _, err := state.ProjectLayouts(state.OSFS{}, root); err != nil {
			return err
		}
		if s.projectUID == "" {
			identity, _, err := kata.ResolveIdentity(ctx, s.CLI)
			if err != nil {
				return err
			}
			s.projectUID = identity.ProjectUID
		}
		if !state.SafeComponent(s.projectUID) {
			return fmt.Errorf("invalid project UID")
		}
		pinLayout := root.Project(s.projectUID)
		s.guard = kata.NewRouteGuard(s.cfg, s.CLI, state.OSFS{}, pinLayout, s.projectUID)
	}
	if err := s.guard.Check(ctx); err != nil {
		return &kata.Error{Code: "board_route_refused", Message: err.Error(), Exit: 6}
	}
	return nil
}
func ambiguousWrite(err error) bool {
	return kata.Ambiguous(err)
}

func (s *tuiService) RunNow(jobUID string) error {
	l := state.Layout{Root: state.DefaultRoot()}
	project, job := "", jobUID
	present, err := state.LayoutPresent(state.OSFS{}, l)
	if err != nil {
		return err
	}
	if present {
		selection := s.projectUID
		if selection == "" {
			selection = s.cfg.Kata.Project
		}
		project, job, err = runner.ResolveJob(s.cfg, state.OSFS{}, l, jobUID, selection)
		if err != nil {
			return err
		}
	}
	_, err = runner.SendProjectRequest(state.OSFS{}, l, "run", job, project)
	return err
}

func (s *tuiService) Resume(uid string) error {
	_, err := runner.SendResumeRequest(state.OSFS{}, state.Layout{Root: state.DefaultRoot()}, uid, false)
	return err
}

func (s *tuiService) ShowIssue(ctx context.Context, ref string) (kata.IssueView, error) {
	if err := s.check(ctx); err != nil {
		return kata.IssueView{}, err
	}
	return s.CLI.ShowIssue(ctx, ref)
}
func (s *tuiService) ShowWorkflow(ctx context.Context, uid string) (kata.Workflow, error) {
	if err := s.check(ctx); err != nil {
		return kata.Workflow{}, err
	}
	return s.CLI.ShowWorkflow(ctx, uid)
}
