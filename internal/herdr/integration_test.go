//go:build herdr

package herdr_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/testenv"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
)

func TestRealHerdr(t *testing.T) {
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_HERDR_BIN")
	s := newRealHerdr(t, bin)
	s.start(t)
	c := s.client
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pong, err := c.Ping(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pong.Version != "0.9.5" || pong.Protocol != 22 {
		t.Fatalf("ping: %+v", pong)
	}
	id, err := herdr.ReadServerID(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := c.WorkspaceCreate(ctx, "Integration", s.home)
	if err != nil {
		t.Fatal(err)
	}
	gotWS, err := c.WorkspaceGet(ctx, ws.WorkspaceID)
	if err != nil || ws.WorkspaceID == "" || gotWS.WorkspaceID != ws.WorkspaceID || gotWS.Label != "Integration" || gotWS.TabCount != 1 || gotWS.PaneCount != 1 {
		t.Fatalf("workspace.get: %+v, %v (created %+v)", gotWS, err, ws)
	}
	cwd := filepath.Join(s.home, "checkout")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	// Herdr and the shell report resolved paths; macOS temp dirs are symlinked.
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	tab, pane, err := c.TabCreate(ctx, ws.WorkspaceID, "Run", cwd, map[string]string{"HERDR_KATA_TEST_VALUE": "quoted value ' with spaces"})
	if err != nil {
		t.Fatal(err)
	}
	gotTab, err := c.TabGet(ctx, tab.TabID)
	if err != nil || gotTab.TabID != tab.TabID || gotTab.WorkspaceID != ws.WorkspaceID || gotTab.Label != "Run" || gotTab.PaneCount != 1 {
		t.Fatalf("tab.get: %+v, %v", gotTab, err)
	}
	gotPane, err := c.PaneGet(ctx, pane.PaneID)
	if err != nil || gotPane.PaneID != pane.PaneID || gotPane.TabID != tab.TabID || gotPane.WorkspaceID != ws.WorkspaceID || gotPane.TerminalID == "" || gotPane.TerminalID != pane.TerminalID || gotPane.Cwd == nil || *gotPane.Cwd != cwd {
		t.Fatalf("pane.get: %+v, %v", gotPane, err)
	}
	process, err := c.PaneProcessInfo(ctx, pane.PaneID)
	if err != nil || process.PaneID != pane.PaneID || process.ShellPID == nil || *process.ShellPID == 0 || process.ForegroundProcessGroupID == nil || *process.ForegroundProcessGroupID == 0 || len(process.ForegroundProcesses) == 0 {
		t.Fatalf("pane.process_info: %+v, %v", process, err)
	}
	// Verify cwd and env in the actual shell, not just the creation response.
	if err := c.Call(ctx, "pane.send_input", map[string]any{
		"pane_id": pane.PaneID,
		"text":    `printf '%s\n' "$HERDR_KATA_TEST_VALUE" "$PWD" > launch.txt`,
		"keys":    []string{"enter"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	wantLaunch := "quoted value ' with spaces\n" + cwd + "\n"
	waitRealHerdr(t, ctx, func() error {
		b, err := os.ReadFile(filepath.Join(cwd, "launch.txt"))
		if err != nil {
			return err
		}
		if string(b) != wantLaunch {
			return fmt.Errorf("shell cwd/env: %q, want %q", b, wantLaunch)
		}
		return nil
	})
	tokens := map[string]string{"run": "integration-run", "step": "one"}
	if err := c.PaneReportMetadata(ctx, pane.PaneID, tokens); err != nil {
		t.Fatal(err)
	}
	gotPane, err = c.PaneGet(ctx, pane.PaneID)
	if err != nil || !reflect.DeepEqual(gotPane.Tokens, tokens) {
		t.Fatalf("pane metadata: %+v, %v", gotPane, err)
	}

	if !t.Run("agent-list", func(t *testing.T) {
		agents, err := c.AgentList(ctx)
		if err != nil || len(agents) != 0 {
			t.Fatalf("fresh agent.list: %+v, %v", agents, err)
		}
		// Hook reports exercise real agent.list serialization without launching
		// an agent or requiring credentials. Lifecycle remains in herdrtest.
		for seq, state := range []string{"working", "idle"} {
			if err := c.Call(ctx, "pane.report_agent", map[string]any{
				"pane_id": pane.PaneID, "source": "integration", "agent": "codex",
				"state": state, "seq": seq + 1,
			}, nil); err != nil {
				t.Fatal(err)
			}
			if state == "working" {
				agents, err := c.AgentList(ctx)
				if err != nil || len(agents) != 1 || agents[0].Status != "working" || agents[0].CompletionSeq != nil {
					t.Fatalf("working agent.list: %+v, %v", agents, err)
				}
			}
		}
		waitRealHerdr(t, ctx, func() error {
			agents, err := c.AgentList(ctx)
			if err != nil {
				return err
			}
			if len(agents) != 1 {
				return fmt.Errorf("agent.list: %+v", agents)
			}
			a := agents[0]
			if a.PaneID != pane.PaneID || a.TabID != tab.TabID || a.WorkspaceID != ws.WorkspaceID || a.TerminalID != pane.TerminalID || a.Status != "done" || a.CompletionSeq == nil || *a.CompletionSeq == 0 {
				return fmt.Errorf("agent.list decoding: %+v", a)
			}
			return nil
		})
	}) {
		return
	}

	t.Run("unknown-targets", func(t *testing.T) {
		for _, tt := range []struct {
			method, code string
			call         func() error
		}{
			{"workspace.get", "workspace_not_found", func() error { _, err := c.WorkspaceGet(ctx, "w999999"); return err }},
			{"tab.create", "workspace_not_found", func() error { _, _, err := c.TabCreate(ctx, "w999999", "Missing", cwd, nil); return err }},
			{"tab.get", "tab_not_found", func() error { _, err := c.TabGet(ctx, "w999999:t1"); return err }},
			{"tab.close", "tab_not_found", func() error { return c.TabClose(ctx, "w999999:t1") }},
			{"pane.get", "pane_not_found", func() error { _, err := c.PaneGet(ctx, "w999999:p1"); return err }},
			{"pane.process_info", "pane_not_found", func() error { _, err := c.PaneProcessInfo(ctx, "w999999:p1"); return err }},
			{"pane.report_metadata", "pane_not_found", func() error { return c.PaneReportMetadata(ctx, "w999999:p1", nil) }},
			{"agent.get", "agent_not_found", func() error { _, err := c.AgentGet(ctx, "missing-agent"); return err }},
		} {
			t.Run(tt.method, func(t *testing.T) { requireHerdrError(t, tt.call(), tt.code) })
		}
	})
	if err := c.TabClose(ctx, tab.TabID); err != nil {
		t.Fatal(err)
	}
	requireHerdrError(t, func() error { _, err := c.TabGet(ctx, tab.TabID); return err }(), "tab_not_found")
	requireHerdrError(t, func() error { _, err := c.PaneGet(ctx, pane.PaneID); return err }(), "pane_not_found")
	gotWS, err = c.WorkspaceGet(ctx, ws.WorkspaceID)
	if err != nil || gotWS.TabCount != 1 || gotWS.PaneCount != 1 {
		t.Fatalf("workspace after tab.close: %+v, %v", gotWS, err)
	}

	t.Run("server-identity-restart", func(t *testing.T) {
		stable, err := herdr.ReadServerID(c.Path)
		if err != nil || stable != id {
			t.Fatalf("identity changed without restart: %+v -> %+v, %v", id, stable, err)
		}
		s.stop(t)
		s.start(t)
		if _, err := c.Ping(ctx); err != nil {
			t.Fatal(err)
		}
		restarted, err := herdr.ReadServerID(c.Path)
		if err != nil || restarted == id {
			t.Fatalf("identity unchanged across restart: %+v -> %+v, %v", id, restarted, err)
		}
	})
}

func TestRealHerdrUnusableBinaryFails(t *testing.T) {
	testBin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, testBin, "-test.run=^TestRealHerdr$", "-test.v")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "HERDR_KATA_TEST_HERDR_BIN=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	// The shared gate accepts --version, but a process that never opens a
	// socket must still fail the real server startup check.
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(cmd.Env, "HERDR_KATA_TEST_HERDR_BIN="+trueBin)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if ctx.Err() != nil || !errors.As(err, &exitErr) || !strings.Contains(string(out), "--- FAIL: TestRealHerdr") || strings.Contains(string(out), "--- SKIP:") {
		t.Fatalf("unusable server must fail: %v\n%s", err, out)
	}
}

func requireHerdrError(t *testing.T, err error, code string) {
	t.Helper()
	var apiErr *herdr.Error
	if !errors.As(err, &apiErr) || apiErr.Code != code {
		t.Fatalf("error: %v, want %s", err, code)
	}
}

func waitRealHerdr(t *testing.T, ctx context.Context, check func() error) {
	t.Helper()
	for {
		err := check()
		if err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for Herdr: %v (last check: %v)", ctx.Err(), err)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

type realHerdr struct {
	bin, home, log string
	env            []string
	client         *herdr.Client
	cmd            *exec.Cmd
	done           chan struct{}
	waitErr        error // read only after done closes
}

func newRealHerdr(t *testing.T, bin string) *realHerdr {
	t.Helper()
	// A short root avoids Unix socket path limits, regardless of test name.
	home, err := os.MkdirTemp("/tmp", "hk-herdr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	config := filepath.Join(home, ".config", "herdr")
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "config.toml"), []byte("onboarding = false\n[terminal]\ndefault_shell = '/bin/sh'\nshell_mode = 'non_login'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &realHerdr{bin: bin, home: home, log: filepath.Join(home, "server.log"),
		client: &herdr.Client{Path: filepath.Join(config, "sessions", "integration", "herdr.sock")}}
	// Do not inherit live session routing, plugins, shell startup files, or
	// agent credentials. Every writable XDG directory belongs to this test.
	s.env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "SHELL=/bin/sh", "TERM=xterm-256color", "LANG=C.UTF-8",
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"), "XDG_CACHE_HOME=" + filepath.Join(home, ".cache")}
	t.Cleanup(func() {
		s.stop(t)
		if t.Failed() {
			b, _ := os.ReadFile(s.log)
			t.Logf("isolated Herdr server log:\n%s", b)
		}
	})
	return s
}

func (s *realHerdr) start(t *testing.T) {
	t.Helper()
	log, err := os.OpenFile(s.log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	s.cmd = exec.Command(s.bin, "--session", "integration", "server")
	s.cmd.Env, s.cmd.Dir = s.env, s.home
	s.cmd.Stdout, s.cmd.Stderr = log, log
	s.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := s.cmd.Start(); err != nil {
		log.Close()
		s.cmd = nil
		t.Fatalf("start HERDR_KATA_TEST_HERDR_BIN: %v", err)
	}
	s.done = make(chan struct{})
	go func() {
		s.waitErr = s.cmd.Wait()
		log.Close()
		close(s.done)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	waitRealHerdr(t, ctx, func() error {
		select {
		case <-s.done:
			t.Fatalf("headless Herdr exited before readiness: %v", s.waitErr)
		default:
		}
		_, err := s.client.Ping(ctx)
		return err
	})
}

func (s *realHerdr) stop(t *testing.T) {
	t.Helper()
	if s.cmd == nil {
		return
	}
	select {
	case <-s.done:
	default:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := s.client.Call(ctx, "server.stop", nil, nil)
		cancel()
		if err != nil {
			t.Errorf("stop isolated Herdr: %v", err)
		}
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			// Kill only the process group created by this fixture.
			syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
			<-s.done
			t.Error("isolated Herdr did not stop within 5s")
		}
	}
	if s.waitErr != nil && !strings.Contains(s.waitErr.Error(), "signal: killed") {
		t.Errorf("isolated Herdr exit: %v", s.waitErr)
	}
	s.cmd = nil
}
