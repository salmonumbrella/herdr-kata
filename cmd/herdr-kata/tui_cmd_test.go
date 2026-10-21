package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type boardSocket struct {
	methods []string
	params  []map[string]any
	panes   []herdr.PaneInfo
}

type emptyWorkspaceSocket struct{ calls []string }

func (s *emptyWorkspaceSocket) Call(_ context.Context, method string, _, _ any) error {
	s.calls = append(s.calls, method)
	if method == "plugin.pane.open" {
		return &herdr.Error{Code: "no_active_workspace", Message: "no active workspace"}
	}
	return nil
}

func TestStartupBoardWithoutWorkspaceIsQuietNoop(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/sessions/work/herdr.sock")
	cfg := config.Config{Session: "work", Board: config.Board{Pin: true}}
	for _, startup := range []bool{true, false} {
		socket := &emptyWorkspaceSocket{}
		err := openTUI(t.Context(), cfg, socket, startup, false, false)
		if startup && err != nil || !startup && err == nil {
			t.Fatalf("startup=%t: %v", startup, err)
		}
		if socket.calls[len(socket.calls)-1] != "plugin.pane.open" {
			t.Fatal("did not exercise empty workspace response", socket.calls)
		}
	}
}

func (f *boardSocket) Call(_ context.Context, method string, p any, out any) error {
	f.methods = append(f.methods, method)
	raw, _ := json.Marshal(p)
	var params map[string]any
	json.Unmarshal(raw, &params)
	f.params = append(f.params, params)
	if method == "pane.list" {
		raw, _ = json.Marshal(map[string]any{"panes": f.panes})
		if out != nil {
			return json.Unmarshal(raw, out)
		}
	}
	return nil
}
func TestBoardStartupFocusedOpensKata(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/sessions/work/herdr.sock")
	cfg := config.Config{Session: "work", Board: config.Board{Pin: true}}
	f := &boardSocket{}
	if err := openTUI(t.Context(), cfg, f, true, false, false); err != nil {
		t.Fatal(err)
	}
	if len(f.methods) != 2 || f.methods[1] != "plugin.pane.open" || f.params[1]["focus"] != true || f.params[1]["placement"] != "tab" {
		t.Fatal(f)
	}
	// The boot TUI pins itself and opens Kata first.
	if env, _ := f.params[1]["env"].(map[string]any); env["HERDR_KATA_BOARD_PIN"] != "1" || env["HERDR_KATA_OPEN_KATA"] != "1" {
		t.Fatal(f.params[1])
	}
	f = &boardSocket{panes: []herdr.PaneInfo{{Tokens: map[string]string{"herdr_kata_board": "pinned"}}}}
	if err := openTUI(t.Context(), cfg, f, true, false, false); err != nil || len(f.methods) != 1 {
		t.Fatal(err, f)
	}
	for _, c := range []config.Config{{Session: "other", Board: config.Board{Pin: true}}, {Session: "work", Board: config.Board{Pin: false}}} {
		f = &boardSocket{}
		if err := openTUI(t.Context(), c, f, true, false, false); err != nil || len(f.methods) != 0 {
			t.Fatal(err, f)
		}
	}
}
func TestWideBoardUsesCallerPane(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/sessions/work/herdr.sock")
	f := &boardSocket{}
	if err := openTUI(t.Context(), config.Config{Session: "work"}, f, false, true, false); err != nil {
		t.Fatal(err)
	}
	p := f.params[0]
	if p["plugin_id"] != "kata" || p["target_pane_id"] != "w1:p1" || p["direction"] != "down" || p["placement"] != "split" || p["workspace_id"] != nil {
		t.Fatal(p)
	}
}

func TestBoardWriteChecksFreshRouteAndNativeBinding(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Executor: "board", Kata: config.Kata{Bin: "/fake", Actor: "actor", Project: "test"}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}
	route := kata.Route{Bin: "/fake", Env: map[string]string{}, Endpoint: "local", DiagnoseState: "ready"}
	if err := state.WriteJSON(state.OSFS{}, filepath.Join(root, "state.json"), kata.Installation{Version: 1, CreatedAt: time.Now(), Executor: cfg.Executor, InstanceUID: "instance", ProjectUID: "project", Actor: "actor", Route: route}); err != nil {
		t.Fatal(err)
	}
	calls, writes := 0, 0
	identity := "instance"
	k := kata.New(cfg.Kata, root, func(_ context.Context, a, e []string, d string, in []byte) ([]byte, []byte, int, error) {
		calls++
		args := strings.Join(a, " ")
		var out string
		switch {
		case strings.HasSuffix(args, " health"):
			out = `{"version":"test","schema_version":33}`
		case strings.Contains(args, "cron capabilities"):
			out = `{"project_uid":"project","event_features":["cron_v1"]}`
		case strings.Contains(args, "federation identity"):
			out = `{"instance_uid":"` + identity + `","auth":{"actor":"actor"}}`
		case strings.Contains(args, "daemon diagnose"):
			out = `{"state":"ready","endpoint":"local","project_uid":"project"}`
		case strings.Contains(args, "cron job update"):
			writes++
			out = `{"job":{"uid":"job"}}`
		default:
			t.Fatalf("unexpected Kata call: %s", args)
		}
		return []byte(out), nil, 0, nil
	}, nil)
	s := &tuiService{CLI: k, cfg: cfg, guard: kata.NewRouteGuard(cfg, k, state.OSFS{}, state.Layout{Root: root}), ctx: t.Context()}
	j := kata.Job{UID: "job", DefinitionEventUID: "event", Definition: json.RawMessage(`{"enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"work"}}`)}
	if _, err := s.SaveJob(t.Context(), j, ""); err != nil || writes != 1 || calls != 5 {
		t.Fatal(err, writes, calls)
	}
	identity = "other"
	if _, err := s.SaveJob(t.Context(), j, ""); err == nil || writes != 1 {
		t.Fatal("route mismatch wrote", err, writes)
	}
}

func TestBoardHeadlessCLIAndManifest(t *testing.T) {
	for _, command := range []string{"tui", "board", "board-wide"} {
		t.Run(command, func(t *testing.T) {
			var out, stderr bytes.Buffer
			if code := run([]string{command, "--help"}, &out, &stderr); code != 0 {
				t.Fatal(code, stderr.String())
			}
			for _, flag := range []string{"-wide", "-pin", "-startup"} {
				if !strings.Contains(stderr.String(), flag) {
					t.Fatalf("missing %s in help: %s", flag, stderr.String())
				}
			}
			if code := run([]string{command, "extra"}, &out, &stderr); code != 2 {
				t.Fatal(code, stderr.String())
			}
		})
	}
	var out, stderr bytes.Buffer
	stderr.Reset()
	if code := run([]string{"--help"}, &out, &stderr); code != 0 || !strings.Contains(stderr.String(), "|tui|") || strings.Contains(stderr.String(), "board") {
		t.Fatal(code, stderr.String())
	}
}

func TestTUIHeadlessDispatchAndAliases(t *testing.T) {
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	original := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = original }()
	for _, tc := range []struct {
		args               []string
		wide, pin, startup bool
	}{
		{args: []string{"tui"}},
		{args: []string{"tui", "--wide"}, wide: true},
		{args: []string{"tui", "--pin"}, pin: true},
		{args: []string{"tui", "--startup"}, startup: true},
		{args: []string{"board"}},
		{args: []string{"board-wide"}, wide: true},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			// Keep Unix socket paths below macOS's length limit too.
			root, err := os.MkdirTemp("/tmp", "hk-tui-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", root)
			t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
			t.Setenv("HERDR_PANE_ID", "w1:p1")
			path := filepath.Join(root, "sessions", "work", "herdr.sock")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HERDR_SOCKET_PATH", path)
			if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte("session = \"work\"\nexecutor = \"test\"\n[kata]\nbin = \"/fake\"\nproject = \"test\"\nactor = \"actor\"\n[profiles.default]\nkind = \"codex\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			requests := make(chan struct {
				Method string
				Params map[string]any
			}, 2)
			nextRequest := func() (string, map[string]any) {
				select {
				case req := <-requests:
					return req.Method, req.Params
				case <-time.After(time.Second):
					t.Fatal("no Herdr request received")
					return "", nil
				}
			}
			go func() {
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					var req struct {
						Method string
						Params map[string]any
					}
					if err := json.NewDecoder(conn).Decode(&req); err == nil {
						requests <- req
						_, _ = io.WriteString(conn, "{\"id\":\"1\",\"result\":{\"panes\":[]}}\n")
					}
					_ = conn.Close()
				}
			}()
			var out, stderr bytes.Buffer
			if code := run(tc.args, &out, &stderr); code != 0 {
				t.Fatal(code, stderr.String())
			}
			if tc.startup {
				if method, p := nextRequest(); method != "pane.list" {
					t.Fatal(method, p)
				}
			}
			method, p := nextRequest()
			// Every open takes focus; the boot TUI opens Kata over itself next.
			if method != "plugin.pane.open" || p["entrypoint"] != "board" || p["focus"] != true {
				t.Fatal(method, p)
			}
			if tc.startup {
				if p["placement"] != "tab" || p["target_pane_id"] != nil {
					t.Fatal(p)
				}
			} else {
				direction := "right"
				if tc.wide {
					direction = "down"
				}
				if p["placement"] != "split" || p["direction"] != direction || p["plugin_id"] != "kata" || p["target_pane_id"] != "w1:p1" {
					t.Fatal(p)
				}
			}
			if tc.pin || tc.startup {
				if env, ok := p["env"].(map[string]any); !ok || env["HERDR_KATA_BOARD_PIN"] != "1" {
					t.Fatal(p)
				}
			}
		})
	}
}

func TestHeadlessPinPropagated(t *testing.T) {
	f := &boardSocket{}
	if err := openTUI(t.Context(), config.Config{}, f, false, false, true); err != nil {
		t.Fatal(err)
	}
	env := f.params[0]["env"].(map[string]any)
	if env["HERDR_KATA_BOARD_PIN"] != "1" {
		t.Fatal(env)
	}
}

func TestFocusRequiresServerProof(t *testing.T) {
	path := herdrtest.SocketPath(t)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("HERDR_SOCKET_PATH", path)
	h := &boardSocket{}
	s := &tuiService{cfg: config.Config{Session: "work"}, h: h, ctx: t.Context()}
	r := runner.Run{Herdr: runner.HerdrRef{PaneID: "w1:p1"}}
	if err := s.Focus(r); err == nil || len(h.methods) > 0 {
		t.Fatal("unproven server focused", err, h.methods)
	}
	r.Herdr.ServerID, err = herdr.ReadServerID(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Focus(r); err != nil || len(h.methods) != 1 {
		t.Fatal(err, h.methods)
	}
}

func TestBoardLocalFocusServiceRefusesKataActions(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	s := &tuiService{cfg: config.Config{Session: "work"}, h: &boardSocket{}, ctx: t.Context()}
	if _, err := s.KataCommand(""); err == nil {
		t.Fatal("local focus service opened an unbound Kata pane")
	}
	if err := s.Delete(kata.Job{UID: "job"}); err == nil {
		t.Fatal("local focus service deleted an unbound job")
	}
}

func mutationService(t *testing.T, handler kata.Runner) *tuiService {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{Executor: "board", Kata: config.Kata{Bin: "/fake", Actor: "actor", Project: "test"}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}
	route := kata.Route{Bin: "/fake", Env: map[string]string{}, Endpoint: "local", DiagnoseState: "ready"}
	if err := state.WriteJSON(state.OSFS{}, filepath.Join(root, "state.json"), kata.Installation{Version: 1, CreatedAt: time.Now(), Executor: cfg.Executor, InstanceUID: "instance", ProjectUID: "project", Actor: "actor", Route: route}); err != nil {
		t.Fatal(err)
	}
	k := kata.New(cfg.Kata, root, func(ctx context.Context, a, e []string, d string, in []byte) ([]byte, []byte, int, error) {
		var out string
		switch args := strings.Join(a, " "); {
		case strings.HasSuffix(args, " health"):
			out = `{"version":"test","schema_version":33}`
		case strings.Contains(args, "cron capabilities"):
			out = `{"project_uid":"project","event_features":["cron_v1"]}`
		case strings.Contains(args, "federation identity"):
			out = `{"instance_uid":"instance","auth":{"actor":"actor"}}`
		case strings.Contains(args, "daemon diagnose"):
			out = `{"state":"ready","endpoint":"local","project_uid":"project"}`
		default:
			return handler(ctx, a, e, d, in)
		}
		return []byte(out), nil, 0, nil
	}, nil)
	return &tuiService{CLI: k, cfg: cfg, guard: kata.NewRouteGuard(cfg, k, state.OSFS{}, state.Layout{Root: root}), ctx: t.Context()}
}
func TestAmbiguousDefinitionWritesReadBackWithoutRetry(t *testing.T) {
	for _, which := range []string{"matched", "different", "deleted"} {
		t.Run(which, func(t *testing.T) {
			j := kata.Job{UID: "job", Name: "name", DefinitionEventUID: "old", Definition: json.RawMessage(`{"enabled":false,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"work"}}`)}
			writes, reads := 0, 0
			s := mutationService(t, func(_ context.Context, a, e []string, d string, in []byte) ([]byte, []byte, int, error) {
				args := strings.Join(a, " ")
				if strings.Contains(args, "cron job show") {
					reads++
					newer := j
					newer.DefinitionEventUID = "new"
					if which == "different" {
						newer.Name = "other"
					}
					if which == "deleted" {
						now := time.Now()
						newer.DeletedAt = &now
					}
					out, _ := json.Marshal(map[string]any{"job": newer})
					return out, nil, 0, nil
				}
				writes++
				return []byte(`{"error":{"code":"timeout","message":"lost"}}`), nil, 7, nil
			})
			var err error
			if which == "deleted" {
				err = s.Delete(j)
			} else {
				_, err = s.SaveJob(t.Context(), j, "")
			}
			if writes != 1 || reads != 1 || (which == "different") != (err != nil) {
				t.Fatal(which, writes, reads, err)
			}
		})
	}
}
func TestPauseMissingEnabledTreatsItAsDisabled(t *testing.T) {
	writes := 0
	s := mutationService(t, func(_ context.Context, a, e []string, d string, in []byte) ([]byte, []byte, int, error) {
		writes++
		var body struct{ Definition struct{ Enabled bool } }
		if err := json.Unmarshal(in, &body); err != nil || !body.Definition.Enabled {
			t.Fatal("did not enable", err, string(in))
		}
		return []byte(`{"job":{"uid":"job"}}`), nil, 0, nil
	})
	_, err := s.Pause(kata.Job{UID: "job", DefinitionEventUID: "event", Definition: json.RawMessage(`{"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"work"}}`)})
	if err != nil || writes != 1 {
		t.Fatal(err, writes)
	}
}

type failedPinSocket struct{ boardSocket }

func (h *failedPinSocket) Call(ctx context.Context, method string, p, out any) error {
	if method == "pane.report_metadata" {
		return fmt.Errorf("temporary metadata failure")
	}
	if method == "plugin.pane.open" {
		return json.Unmarshal([]byte(`{"plugin_pane":{"pane":{"pane_id":"w1:p2"}}}`), out)
	}
	return h.boardSocket.Call(ctx, method, p, out)
}
func TestOpenedStartupBoardSurvivesInitialPinFailure(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/sessions/work/herdr.sock")
	if err := openTUI(t.Context(), config.Config{Session: "work", Board: config.Board{Pin: true}}, &failedPinSocket{}, true, false, false); err != nil {
		t.Fatal("opened board reported as failed", err)
	}
}

func TestBoardRunNowPublishesWorkerRequest(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	s := &tuiService{}
	requester, ok := any(s).(interface{ RunNow(string) error })
	if !ok {
		t.Fatal("board service has no worker request path")
	}
	if err := requester.RunNow("job"); err == nil || !strings.Contains(err.Error(), "runtime not running") {
		t.Fatal(err)
	}
	l := state.Layout{Root: state.DefaultRoot()}
	lock, held, err := state.TryLock(l.WorkerLock())
	if err != nil || !held {
		t.Fatal(held, err)
	}
	defer lock.Unlock()
	worker := exec.Command("sleep", "30")
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = worker.Process.Kill(); _ = worker.Wait() }()
	if err := state.WriteJSON(state.OSFS{}, l.ServeInfo(), map[string]int{"worker_pid": worker.Process.Pid}); err != nil {
		t.Fatal(err)
	}
	if err := requester.RunNow("job"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(l.RequestsDir())
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	var req runner.Request
	if err := state.ReadJSON(state.OSFS{}, filepath.Join(l.RequestsDir(), entries[0].Name()), 4096, &req); err != nil {
		t.Fatal(err)
	}
	if req.Kind != "run" || req.Job != "job" || req.RunUID == "" || req.RunUID == "job" {
		t.Fatalf("invalid run request: %+v", req)
	}
}

func TestFocusKeepsFreshAgentAcrossParkedCommand(t *testing.T) {
	path := herdrtest.SocketPath(t)
	listener, e := net.Listen("unix", path)
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	t.Setenv("HERDR_SOCKET_PATH", path)
	id, e := herdr.ReadServerID(path)
	if e != nil {
		t.Fatal(e)
	}
	h := &boardSocket{}
	s := &tuiService{cfg: config.Config{Session: "work"}, h: h, ctx: t.Context()}
	r := runner.Run{ActiveStep: "check", Steps: []runner.StepState{{Key: "first", Kind: "prompt", Submission: runner.Accepted, Status: "succeeded", Herdr: &runner.HerdrRef{PaneID: "fresh-pane", ServerID: id}}, {Key: "check", Kind: "command", Status: "failed"}}}
	if e = s.Focus(r); e != nil || len(h.methods) != 1 || h.methods[0] != "pane.focus" || h.params[0]["pane_id"] != "fresh-pane" {
		t.Fatal("focus lost retained agent", e, h)
	}
}

func TestFocusGonePaneHasPlainRemedy(t *testing.T) {
	server := herdrtest.New(t)
	server.Fail("pane.focus", herdr.Error{Code: "pane_not_found", Message: "pane missing"})
	t.Setenv("HERDR_SOCKET_PATH", server.Path)
	id, err := herdr.ReadServerID(server.Path)
	if err != nil {
		t.Fatal(err)
	}
	s := &tuiService{cfg: config.Config{Session: "work"}, h: &herdr.Client{Path: server.Path}, ctx: t.Context()}
	err = s.Focus(runner.Run{Herdr: runner.HerdrRef{PaneID: "w9:p9", ServerID: id}})
	if err == nil || err.Error() != "agent pane is gone; the run record is still available" {
		t.Fatalf("gone pane: %v", err)
	}
	requests := server.Requests()
	if len(requests) != 1 || requests[0].Method != "pane.focus" {
		t.Fatalf("focus requests: %+v", requests)
	}
}

func TestBoardBindingAccessLossFencesOnlyPinnedLane(t *testing.T) {
	root := state.Layout{Root: t.TempDir()}
	lane := root.Project("project")
	cfg := config.Config{Executor: "board", Kata: config.Kata{Bin: "/fake", Actor: "worker", Project: "example-project"}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}
	probes := 0
	k := kata.New(cfg.Kata, root.Root, func(_ context.Context, args, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
		var body string
		switch command := strings.Join(args[8:], " "); command {
		case "health":
			body = `{"schema_version":33}`
		case "cron capabilities":
			probes++
			if probes > 2 {
				return []byte(`{"error":{"code":"not_found","message":"access withheld"}}`), nil, 1, nil
			}
			body = `{"project_uid":"project","event_features":["cron_v1"]}`
		case "federation identity":
			body = `{"instance_uid":"instance","auth":{"actor":"worker"}}`
		case "daemon diagnose --expect-project-uid project":
			body = `{"state":"ready","endpoint":"local","project_uid":"project"}`
		case "show -- parent":
			body = `{"issue":{"uid":"parent","metadata":{"herdr_kata.workflow":"01K00000000000000000000001"}}}`
		case "cron workflow show 01K00000000000000000000001", "cron workflow list":
			return []byte(`{"error":{"code":"not_found","message":"access withheld"}}`), nil, 1, nil
		default:
			t.Fatalf("unexpected binding command %q", command)
		}
		return []byte(body), nil, 0, nil
	}, nil)
	guard := kata.NewRouteGuard(cfg, k, state.OSFS{}, lane, "project")
	if err := guard.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := &tuiService{CLI: k, cfg: cfg, guard: guard, projectUID: "project", ctx: t.Context()}
	j := kata.Job{UID: "job", DefinitionEventUID: "event", Definition: json.RawMessage(`{"enabled":true,"action":{"kind":"notify"},"issue":{"kind":"per-run","parent_uid":"parent"}}`)}
	if _, err := s.SaveJob(t.Context(), j, ""); err == nil {
		t.Fatal("missing binding accepted")
	}
	var status kata.RouteStatus
	if err := state.ReadJSON(state.OSFS{}, lane.RouteFile(), 4096, &status); err != nil {
		t.Fatal(err)
	}
	if probes != 3 || guard.Allowed() || status.Allowed || !status.AccessLost {
		t.Fatalf("binding access loss left lane enabled: probes=%d status=%+v", probes, status)
	}
	for _, path := range []string{root.RouteFile(), root.Project("other-project").RouteFile()} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("binding wrote outside pinned lane: %s: %v", path, err)
		}
	}
}

func TestTUIStartupWithMissingConfigIsQuiet(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	root := filepath.Join(t.TempDir(), "state")
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	var out, stderr bytes.Buffer
	if code := run([]string{"tui", "--startup"}, &out, &stderr); code != 0 || out.Len() != 0 || stderr.Len() != 0 {
		t.Fatal(code, out.String(), stderr.String())
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("startup created state: %v", err)
	}
	// Explicit opens must still explain how to configure the plugin.
	if code := run([]string{"tui"}, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "config.toml") {
		t.Fatal(code, stderr.String())
	}
}

func TestTUIStartupRejectsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("invalid = ["), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"tui", "--startup"}, &out, &stderr); code != 1 || stderr.Len() == 0 {
		t.Fatal(code, stderr.String())
	}
}

// The pinned board owns a Herdr sidebar row for the lifetime of the TUI.
func TestPinnedBoardReportsKataLabel(t *testing.T) {
	if os.Getenv("HERDR_KATA_LABEL_TEST") == "1" {
		os.Exit(tuiCommand([]string{"tui", "--pin"}, os.Stdout, os.Stderr))
	}
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", root)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	t.Setenv("HERDR_KATA_OPEN_KATA", "")
	t.Setenv("TERM", "xterm-256color")
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte("session = \"work\"\nexecutor = \"board\"\n[kata]\nbin = \"/fake\"\nproject = \"example-project\"\nactor = \"worker\"\n[profiles.default]\nkind = \"codex\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	server := herdrtest.New(t)
	server.SetPane(herdr.PaneInfo{PaneID: "w1:p1"})
	requests := make(chan herdrtest.Request, 10)
	server.BeforeRequest = func(r herdrtest.Request) {
		if r.Method == "pane.report_agent" || r.Method == "pane.release_agent" {
			requests <- r
		}
	}
	t.Setenv("HERDR_SOCKET_PATH", server.Path)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPinnedBoardReportsKataLabel$")
	cmd.Env = append(os.Environ(), "HERDR_KATA_LABEL_TEST=1")
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	log, err := os.Create(filepath.Join(root, "terminal.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	go func() { _, _ = io.Copy(log, terminal) }()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waited := false
	t.Cleanup(func() {
		cancel()
		if !waited {
			<-done
		}
	})
	next := func(method string) {
		t.Helper()
		select {
		case r := <-requests:
			var params map[string]string
			if err := json.Unmarshal(r.Params, &params); err != nil {
				t.Fatal(err)
			}
			if r.Method != method || params["pane_id"] != "w1:p1" || params["source"] != "custom:herdr-kata-board" || params["agent"] != "kata" {
				t.Errorf("sidebar request %s: %s", r.Method, r.Params)
			}
			if method == "pane.report_agent" && params["state"] != "idle" {
				t.Errorf("board state: %s", r.Params)
			}
		case <-ctx.Done():
			output, _ := os.ReadFile(log.Name())
			t.Fatalf("missing sidebar request %s; requests: %+v; terminal: %s", method, server.Requests(), output)
		}
	}
	next("pane.report_agent")
	if _, err := terminal.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	next("pane.release_agent")
	select {
	case err := <-done:
		waited = true
		if err != nil {
			t.Fatal("board did not quit cleanly", err)
		}
	case <-ctx.Done():
		t.Fatal("board did not exit after release")
	}
}
