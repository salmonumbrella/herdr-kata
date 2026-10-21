package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"go.kenn.io/kit/secretref"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func doctorFixture(t *testing.T) (config.Config, *kata.CLI, *herdr.Client, state.Layout) {
	t.Helper()
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	s := herdrtest.New(t)
	t.Setenv("HERDR_SOCKET_PATH", s.Path)
	s.BeforeRequest = func(req herdrtest.Request) {
		if req.Method != "pane.send_input" {
			return
		}
		var p struct {
			Text string `json:"text"`
		}
		if e := json.Unmarshal(req.Params, &p); e != nil {
			t.Error(e)
			return
		}
		_, output, ok := strings.Cut(p.Text, " env-names '")
		if !ok {
			t.Error("unexpected probe invocation")
			return
		}
		if e := os.WriteFile(strings.TrimSuffix(output, "'"), []byte("[]"), 0600); e != nil {
			t.Error(e)
		}
	}
	l := state.Layout{Root: t.TempDir()}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := config.Config{Session: herdr.SessionFromSocket(s.Path), Executor: "test", Kata: config.Kata{Bin: bin, Actor: "actor", Project: "test", Env: map[string]string{"KATA_HOME": t.TempDir()}}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}, Checkouts: map[string]string{"repo": t.TempDir()}}
	r := func(_ context.Context, argv, env []string, dir string, stdin []byte) ([]byte, []byte, int, error) {
		args := strings.Join(argv, " ")
		var v any
		switch {
		case strings.Contains(args, "--help"):
			return []byte("cron capabilities events --tail"), nil, 0, nil
		case strings.Contains(args, "health"):
			v = map[string]any{"version": "test", "schema_version": 33}
		case strings.Contains(args, "capabilities"):
			v = map[string]any{"project_uid": "project", "event_features": []string{"cron_v1"}}
		case strings.Contains(args, "identity"):
			v = map[string]any{"instance_uid": "instance", "auth": map[string]string{"actor": "actor"}}
		case strings.Contains(args, "diagnose"):
			v = map[string]string{"state": "ready", "endpoint": "http://localhost:1234", "project_uid": "project"}
		case strings.Contains(args, " list --status all --label herdr-kata:lease "):
			v = map[string]any{"issues": []kata.IssueView{}}
		case strings.HasSuffix(args, " cron job list"):
			v = map[string]any{"jobs": []kata.Job{}}
		case strings.Contains(args, " cron run list --limit 100"):
			v = map[string]any{"runs": []kata.Run{}}
		default:
			t.Fatalf("unexpected call: %v", argv)
		}
		b, _ := json.Marshal(v)
		return b, nil, 0, nil
	}
	return c, kata.New(c.Kata, l.Root, r, nil), &herdr.Client{Path: s.Path}, l
}

func TestDoctorSessionFormats(t *testing.T) {
	c, k, h, l := doctorFixture(t)
	root := t.TempDir()
	t.Setenv("CODEX_HOME", root)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	path := filepath.Join(root, "sessions", "2026", "09", "24", "rollout-example-00000000-0000-4000-8000-000000000001.jsonl")
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct{ data, want string }{
		{`{"timestamp":"2026-09-24T16:00:00Z","type":"event_msg","payload":{"type":"task_complete","last_agent_message":"PRIVATE TRANSCRIPT"}}` + "\n", "session format recognized"},
		{`{"changed_format":true}` + "\n", "session format unrecognized"},
		{`{"timestamp":"2026-09-24T16:00:00Z","type":"event_msg","payload":{"type":"task_complete","error":{"codex_error_info":42}}}` + "\n", "session format unrecognized"},
	} {
		if e := os.WriteFile(path, []byte(tc.data), 0600); e != nil {
			t.Fatal(e)
		}
		r := Check(t.Context(), c, k, h, l)
		found := false
		for _, check := range r.Checks {
			if check.Name == "agent limits codex" {
				found = strings.Contains(check.Detail, tc.want)
			}
		}
		b, _ := json.Marshal(r)
		if !found || strings.Contains(string(b), "PRIVATE TRANSCRIPT") || strings.Contains(string(b), root) {
			t.Fatalf("format check: %s", b)
		}
	}
}
func TestDoctorChecks(t *testing.T) {
	c, k, h, l := doctorFixture(t)
	r := Check(context.Background(), c, k, h, l)
	if !r.OK() {
		t.Fatalf("%+v", r)
	}
	c.Checkouts["missing"] = "/does-not-exist"
	r = Check(context.Background(), c, k, h, l)
	if r.OK() {
		t.Fatal("missing checkout passes")
	}
}
func TestDoctorKindsNotProbed(t *testing.T) {
	c, k, h, l := doctorFixture(t)
	r := Check(context.Background(), c, k, h, l)
	found := false
	for _, check := range r.Checks {
		if check.Name == "profiles" {
			found = strings.Contains(check.Detail, "codex: not probed")
		}
	}
	if !found {
		t.Fatalf("%+v", r)
	}
	if held, err := state.Held(l.WorkerLock()); err != nil || held {
		t.Fatal("doctor started worker")
	}
}

func TestDoctorTreatsFederationRoleAsInformation(t *testing.T) {
	for _, diagnosis := range []string{
		`{"state":"ready","endpoint":"local","project_uid":"project-a","federation_role":"spoke"}`,
		`{"state":"project_unverifiable","endpoint":"https://hub.example"}`,
	} {
		l := state.Layout{Root: t.TempDir()}
		cfg := config.Config{Executor: "worker", Kata: config.Kata{Actor: "worker", Project: "example-project"}}
		k := kata.New(cfg.Kata, l.Root, func(_ context.Context, args, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
			switch joined := strings.Join(args, " "); {
			case strings.Contains(joined, "--help"):
				return []byte("cron capabilities events --tail"), nil, 0, nil
			case strings.HasSuffix(joined, " health"):
				return []byte(`{"version":"test","schema_version":33}`), nil, 0, nil
			case strings.HasSuffix(joined, " cron capabilities"):
				return []byte(`{"project_uid":"project-a","event_features":["cron_v1"]}`), nil, 0, nil
			case strings.HasSuffix(joined, " federation identity"):
				return []byte(`{"instance_uid":"instance-a","auth":{"actor":"worker"}}`), nil, 0, nil
			case strings.HasSuffix(joined, " daemon diagnose --expect-project-uid project-a"):
				return []byte(diagnosis), nil, 0, nil
			case strings.Contains(joined, " list --status all --label herdr-kata:lease "):
				return []byte(`{"issues":[]}`), nil, 0, nil
			case strings.HasSuffix(joined, " cron job list"):
				return []byte(`{"jobs":[]}`), nil, 0, nil
			case strings.Contains(joined, " cron run list --limit 100"):
				return []byte(`{"runs":[]}`), nil, 0, nil
			default:
				t.Fatalf("unexpected Kata call: %v", args)
				return nil, nil, 1, nil
			}
		}, nil)
		report, uid := checkKata(t.Context(), cfg, k, l)
		if !report.OK() || uid != "project-a" {
			t.Fatal("doctor refused a configured route", report, uid)
		}
		if strings.Contains(diagnosis, "spoke") {
			visible := false
			for _, check := range report.Checks {
				visible = visible || check.Passed && strings.Contains(check.Detail, "spoke")
			}
			if !visible {
				t.Fatal("doctor hid the informational federation role", report)
			}
		}
	}
}

func TestDoctorListsLocalAndQuarantinedEvidenceOffline(t *testing.T) {
	c, _, h, l := doctorFixture(t)
	// Force all remote checks to fail; local resolution must still be visible.
	k := kata.New(c.Kata, l.Root, func(context.Context, []string, []string, string, []byte) ([]byte, []byte, int, error) {
		return []byte(`{"error":{"code":"transport","message":"offline"}}`), nil, 7, nil
	}, nil)
	r := runner.Run{Version: 1, UID: "held", Phase: runner.Held, Reason: "admission_rejected", IssueUID: "orphan", IssueRef: "example-project#task", Ops: []runner.Op{{ID: "admission", Kind: "observe", Payload: []byte(`{}`), FirstSent: ptrTime()}}}
	if err := state.WriteJSON(state.OSFS{}, l.RunFile(r.UID), r); err != nil {
		t.Fatal(err)
	}
	r.UID = "quarantined"
	r.Reason = ""
	r.Ops[0].ID = "maker-comment"
	r.Ops[0].Kind = "comment"
	r.Ops[0].SigningRefused = true
	if err := state.WriteJSON(state.OSFS{}, filepath.Join(l.QuarantineRunDir(r.UID), "run.json"), r); err != nil {
		t.Fatal(err)
	}
	report := Check(t.Context(), c, k, h, l)
	b, _ := json.Marshal(report)
	for _, want := range []string{"admission_rejected", "orphan", "herdr-kata stop", "herdr-kata abandon held --local", "quarantined", "maker-comment", "signing_refused"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("doctor missing %q: %s", want, b)
		}
	}
}
func ptrTime() *time.Time { at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC); return &at }

func TestDoctorShowsRetiredSigningOperation(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	r := runner.Run{Version: 1, UID: "retired", Phase: runner.Settled, Ops: []runner.Op{{ID: "create", Kind: "create", Payload: json.RawMessage(`{}`), Done: true, SigningRefused: true}}}
	if err := state.WriteJSON(state.OSFS{}, l.RunFile(r.UID), r); err != nil {
		t.Fatal(err)
	}
	checks, err := LocalChecks(state.OSFS{}, l)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || !strings.Contains(checks[0].Detail, "create (retired signing_refused)") {
		t.Fatal("doctor hid the retained signing evidence", checks)
	}
}

func TestDoctorSigningRecoveryUsesFrozenProject(t *testing.T) {
	c, k, h, l := doctorFixture(t)
	c.Kata.Project = ""
	c.Projects = []config.Project{{Name: "example-project", Actor: "worker"}, {Name: "team-project", Actor: "worker"}}
	for _, entry := range []struct{ uid, name string }{{"project-a", "example-project"}, {"project-b", "team-project"}} {
		if e := state.WriteJSON(state.OSFS{}, l.Project(entry.uid).StateFile(), map[string]any{"version": 1, "name": entry.name, "project_uid": entry.uid}); e != nil {
			t.Fatal(e)
		}
		r := runner.Run{Version: 1, UID: entry.uid, Phase: runner.Held, Target: runner.Target{Identity: kata.Identity{ProjectUID: entry.uid}}, Ops: []runner.Op{{ID: "create", Kind: "create", Payload: json.RawMessage(`{}`), SigningRefused: true}}}
		if e := state.WriteJSON(state.OSFS{}, l.RunFile(r.UID), r); e != nil {
			t.Fatal(e)
		}
	}
	checks := Check(t.Context(), c, k, h, l).Checks
	for _, want := range []struct{ uid, name string }{{"project-a", "example-project"}, {"project-b", "team-project"}} {
		found := false
		for _, check := range checks {
			if check.Name == "local run "+want.uid {
				found = strings.Contains(check.Detail, "repin --project "+want.name)
			}
		}
		if !found {
			t.Error("missing project-scoped signing recovery", want, checks)
		}
	}
}

func TestDoctorRefusesDuplicateProjectUIDs(t *testing.T) {
	c, k, h, l := doctorFixture(t)
	c.Projects = []config.Project{{Name: "project-a", Actor: "actor"}, {Name: "project-b", Actor: "actor"}}
	r := checkProjects(t.Context(), c, func(config.Config) *kata.CLI { return k }, h, l)
	found := false
	for _, check := range r.Checks {
		if check.Name == "duplicate project UID" && !check.Passed && strings.Contains(check.Detail, "project-a") && strings.Contains(check.Detail, "project-b") {
			found = true
		}
	}
	if !found {
		t.Fatal("duplicate routes accepted", r)
	}
}

func TestDoctorUsesExplicitSingleProjectBeforeFirstStartup(t *testing.T) {
	c, _, h, _ := doctorFixture(t)
	c.Kata.Project, c.Kata.Actor = "", ""
	c.Projects = []config.Project{{Name: "example-project", Actor: "actor", Daemon: "example-hub", CredentialEnv: "KATA_TOKEN_ONE"}}
	t.Setenv("KATA_TOKEN_ONE", "sentinel")
	l := state.Layout{Root: filepath.Join(t.TempDir(), "not-created")}
	k := kata.New(c.Kata, l.Root, func(_ context.Context, args, env []string, dir string, _ []byte) ([]byte, []byte, int, error) {
		joined := " " + strings.Join(args, " ") + " "
		for _, want := range []string{" --project example-project ", " --as actor ", " --daemon example-hub "} {
			if !strings.Contains(joined, want) {
				t.Error("doctor used shared transport as project binding", args)
			}
		}
		if _, e := os.Stat(dir); e != nil {
			t.Error("doctor used nonexistent working directory", dir, e)
		}
		found := false
		for _, item := range env {
			found = found || item == "KATA_TOKEN_ONE=sentinel"
		}
		if !found {
			t.Error("project credential absent")
		}
		var value any
		switch {
		case strings.Contains(joined, "--help"):
			return []byte("cron capabilities events --tail"), nil, 0, nil
		case strings.Contains(joined, " health "):
			value = kata.Health{Version: "test", SchemaVersion: 33}
		case strings.Contains(joined, " cron capabilities "):
			value = kata.Capabilities{ProjectUID: "project", EventFeatures: []string{"cron_v1"}}
		case strings.Contains(joined, " federation identity "):
			value = map[string]any{"instance_uid": "instance", "auth": map[string]string{"actor": "actor"}}
		case strings.Contains(joined, " daemon diagnose "):
			value = map[string]string{"state": "ready", "endpoint": "local", "project_uid": "project"}
		case strings.Contains(joined, " list --status all --label herdr-kata:lease "):
			value = map[string]any{"issues": []kata.IssueView{}}
		case strings.Contains(joined, " cron job list "):
			value = map[string]any{"jobs": []kata.Job{}}
		case strings.Contains(joined, " cron run list --limit 100 "):
			value = map[string]any{"runs": []kata.Run{}}
		default:
			t.Error("unexpected doctor call", args)
			return nil, nil, 1, errors.New("unexpected call")
		}
		data, e := json.Marshal(value)
		return data, nil, 0, e
	}, nil)
	if r := Check(t.Context(), c, k, h, l); !r.OK() {
		t.Fatal(r)
	}
}

func TestDoctorSecretSources(t *testing.T) {
	c, k, h, l := doctorFixture(t)
	t.Setenv("EXAMPLE_SOURCE", "fixture-secret-only")
	c.Secrets = map[string]secretref.Ref{"review": {Env: "EXAMPLE_SOURCE"}, "missing": {File: "/srv/secrets/example-missing"}}
	report := Check(t.Context(), c, k, h, l)
	seen := map[string]CheckResult{}
	for _, check := range report.Checks {
		seen[check.Name] = check
		if strings.Contains(check.Detail, "fixture-secret-only") {
			t.Fatal("doctor printed secret")
		}
	}
	if !seen["secret review"].Passed || !strings.Contains(seen["secret review"].Detail, "env:EXAMPLE_SOURCE") || seen["secret missing"].Passed || !strings.Contains(seen["secret missing"].Detail, "file:/srv/secrets/example-missing") {
		t.Fatalf("secret diagnostics absent: %+v", seen)
	}
}

func TestDoctorClosesProbeWorkspace(t *testing.T) {
	c, k, _, l := doctorFixture(t)
	s := herdrtest.New(t)
	s.BeforeRequest = func(req herdrtest.Request) {
		if req.Method != "pane.send_input" {
			return
		}
		var p struct {
			Text string `json:"text"`
		}
		if e := json.Unmarshal(req.Params, &p); e != nil {
			t.Error(e)
			return
		}
		_, path, _ := strings.Cut(p.Text, " env-names '")
		if e := os.WriteFile(strings.TrimSuffix(path, "'"), []byte("[]"), 0600); e != nil {
			t.Error(e)
		}
	}
	report := Check(t.Context(), c, k, &herdr.Client{Path: s.Path}, l)
	if !report.OK() {
		t.Fatalf("doctor failed: %+v", report)
	}
	created, closed := 0, 0
	for _, req := range s.Requests() {
		if req.Method == "workspace.create" {
			created++
		}
		if req.Method == "workspace.close" {
			closed++
		}
	}
	if created != 1 || closed != 1 {
		t.Fatalf("doctor left probe workspace: created %d closed %d", created, closed)
	}
}

// Mac acceptance D1: a clean server does not prevent an interactive shell's
// startup files from adding these names. Doctor must explain both sources.
func TestDoctorEnvironmentLeakExplainsShellStartupRepair(t *testing.T) {
	c, k, _, l := doctorFixture(t)
	server := herdrtest.New(t)
	server.BeforeRequest = func(req herdrtest.Request) {
		if req.Method != "pane.send_input" {
			return
		}
		var p struct{ Text string }
		if err := json.Unmarshal(req.Params, &p); err != nil {
			t.Error(err)
			return
		}
		_, path, ok := strings.Cut(p.Text, " env-names '")
		if !ok {
			t.Error("unexpected probe invocation")
			return
		}
		if err := os.WriteFile(strings.TrimSuffix(path, "'"), []byte(`["KATA_AUTHOR","KATA_HTTP_TIMEOUT","KATA_TELEMETRY_ENABLED"]`), 0600); err != nil {
			t.Error(err)
		}
	}
	report := Check(t.Context(), c, k, &herdr.Client{Path: server.Path}, l)
	for _, check := range report.Checks {
		if check.Name != "Herdr launch environment" {
			continue
		}
		if check.Passed {
			t.Fatal("unexpected KATA_* names passed the launch probe")
		}
		for _, want := range []string{"KATA_AUTHOR", "KATA_HTTP_TIMEOUT", "KATA_TELEMETRY_ENABLED", "shell startup files", "~/.zshrc", "server environment", "unset", "guard", "agent panes"} {
			if !strings.Contains(check.Detail, want) {
				t.Errorf("missing %q in repair instructions: %s", want, check.Detail)
			}
		}
		return
	}
	t.Fatal("doctor omitted launch environment check")
}

func TestDoctorKeepsEnvironmentHoldVisibleAfterRepair(t *testing.T) {
	c, k, h, l := doctorFixture(t)
	r := runner.Run{Version: 1, UID: "held", Phase: runner.Held, Reason: "herdr_env_unclean", ActiveStep: "main", Steps: []runner.StepState{{Key: "main", Reason: "Herdr launch environment leaks KATA_SERVER"}}}
	if err := state.WriteJSON(state.OSFS{}, l.RunFile(r.UID), r); err != nil {
		t.Fatal(err)
	}
	report := Check(t.Context(), c, k, h, l)
	clean, held := false, false
	for _, check := range report.Checks {
		if check.Name == "Herdr launch environment" {
			clean = check.Passed
		}
		if check.Name == "local run held" {
			held = !check.Passed && strings.Contains(check.Detail, "herdr_env_unclean") && strings.Contains(check.Detail, "KATA_SERVER") && strings.Contains(check.Detail, "herdr-kata resume held") && !strings.Contains(check.Detail, "abandon")
		}
	}
	if !clean || !held {
		t.Fatalf("repaired environment hid a run that still requires resume: %+v", report)
	}
}

// §22.4: doctor probes each launch map and treats any lane's credential as a
// leak in every lane. A shared daemon cannot supply a missing lane daemon.
func TestDoctorEnvironmentPerLane(t *testing.T) {
	c, k, _, l := doctorFixture(t)
	c.Kata.Project, c.Kata.Actor = "", ""
	c.Kata.Env = map[string]string{"KATA_DAEMON": "shared", "TOKEN_A": "fixture-a", "TOKEN_B": "fixture-b"}
	c.Projects = []config.Project{{Name: "example-project", Actor: "actor", Daemon: "example-route", CredentialEnv: "TOKEN_A"}, {Name: "team-project", Actor: "actor", CredentialEnv: "TOKEN_B"}}
	server := herdrtest.New(t)
	server.BeforeRequest = func(req herdrtest.Request) {
		if req.Method != "pane.send_input" {
			return
		}
		var p struct{ Text string }
		if err := json.Unmarshal(req.Params, &p); err != nil {
			t.Error(err)
			return
		}
		_, path, ok := strings.Cut(p.Text, " env-names '")
		if !ok {
			t.Error("unexpected probe invocation")
			return
		}
		if err := os.WriteFile(strings.TrimSuffix(path, "'"), []byte(`["TOKEN_B"]`), 0600); err != nil {
			t.Error(err)
		}
	}
	report := Check(t.Context(), c, k, &herdr.Client{Path: server.Path}, l)
	for _, name := range []string{"example-project: Herdr launch environment", "team-project: Herdr launch environment"} {
		found := false
		for _, check := range report.Checks {
			if check.Name == name {
				found = !check.Passed && strings.Contains(check.Detail, "TOKEN_B")
			}
		}
		if !found {
			t.Errorf("missing lane credential leak: %s; checks %+v", name, report.Checks)
		}
	}
	seen := map[string]bool{}
	for _, req := range server.Requests() {
		if req.Method != "tab.create" {
			continue
		}
		var p struct{ Env map[string]string }
		if err := json.Unmarshal(req.Params, &p); err != nil {
			t.Fatal(err)
		}
		project := p.Env["KATA_PROJECT"]
		seen[project] = true
		for _, key := range []string{"TOKEN_A", "TOKEN_B"} {
			if _, ok := p.Env[key]; ok {
				t.Errorf("probe map contains credential %s", key)
			}
		}
		if project == "example-project" && p.Env["KATA_DAEMON"] != "example-route" {
			t.Error("probe lost lane daemon")
		}
		if project == "team-project" {
			if _, ok := p.Env["KATA_DAEMON"]; ok {
				t.Error("probe inherited shared daemon")
			}
		}
	}
	if len(seen) != 2 || !seen["example-project"] || !seen["team-project"] {
		t.Fatal("doctor did not probe both lanes", seen)
	}
}

func TestDoctorRejectsWrongCallingSession(t *testing.T) {
	c, k, h, l := doctorFixture(t)
	c.Session = "work"
	t.Setenv("HERDR_SOCKET_PATH", "/srv/config/herdr/sessions/other/herdr.sock")
	report := Check(t.Context(), c, k, h, l)
	for _, check := range report.Checks {
		if check.Name == "session" && !check.Passed && strings.Contains(check.Detail, `session = "work"`) && strings.Contains(check.Detail, "other") {
			return
		}
	}
	t.Fatalf("wrong calling session accepted: %+v", report)
}

func TestDoctorMissingKataBinAvoidsRemoteCascade(t *testing.T) {
	c, _, h, l := doctorFixture(t)
	c.Kata.Bin = filepath.Join(t.TempDir(), "missing-kata")
	calls := 0
	k := kata.New(c.Kata, l.Root, func(context.Context, []string, []string, string, []byte) ([]byte, []byte, int, error) {
		calls++
		return nil, nil, 0, errors.New("must not run")
	}, nil)
	report := Check(t.Context(), c, k, h, l)
	if calls != 0 {
		t.Fatalf("missing kata.bin caused %d remote checks", calls)
	}
	for _, check := range report.Checks {
		if check.Name == "kata.bin" && !check.Passed && strings.Contains(check.Detail, "kata.bin") && strings.Contains(check.Detail, "config.toml") {
			return
		}
	}
	t.Fatal(report)
}

func TestDoctorUnsupportedKataStopsAtCommands(t *testing.T) {
	c, _, _, l := doctorFixture(t)
	k := kata.New(c.Kata, l.Root, func(_ context.Context, args, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
		joined := strings.Join(args, " ")
		if strings.HasSuffix(joined, " health") {
			return []byte(`{"version":"old","schema_version":33}`), nil, 0, nil
		}
		if strings.HasSuffix(joined, "cron --help") {
			return []byte("unknown command cron"), nil, 1, nil
		}
		t.Errorf("unsupported binary probed beyond help: %v", args)
		return nil, nil, 1, nil
	}, nil)
	report, _ := checkKata(t.Context(), c, k, l)
	for _, check := range report.Checks {
		if check.Name == "kata commands" && !check.Passed && strings.Contains(check.Detail, "kata.bin") && strings.Contains(check.Detail, "0.19.0") {
			return
		}
	}
	t.Fatal(report)
}

func TestDoctorUnavailableKataNamesRouteAndRepair(t *testing.T) {
	c, _, _, l := doctorFixture(t)
	c.Kata.Daemon = "example-hub"
	k := kata.New(c.Kata, l.Root, func(context.Context, []string, []string, string, []byte) ([]byte, []byte, int, error) {
		return []byte(`{"error":{"code":"transport","message":"offline"}}`), nil, 7, nil
	}, nil)
	report, _ := checkKata(t.Context(), c, k, l)
	if report.OK() {
		t.Fatal(report)
	}
	for _, check := range report.Checks {
		if !check.Passed && strings.Contains(check.Detail, "example-hub") && strings.Contains(check.Detail, "daemon diagnose") && strings.Contains(check.Detail, "kata.env") {
			return
		}
	}
	t.Fatal(report)
}

func TestDoctorNeverExistingProjectIsUnresolved(t *testing.T) {
	c, _, _, l := doctorFixture(t)
	c.Kata.Project = "missing"
	k := kata.New(c.Kata, l.Root, func(_ context.Context, args, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
		joined := strings.Join(args, " ")
		if strings.HasSuffix(joined, " health") {
			return []byte(`{"version":"test","schema_version":33}`), nil, 0, nil
		}
		if strings.Contains(joined, "--help") {
			return []byte("capabilities --tail"), nil, 0, nil
		}
		if strings.HasSuffix(joined, "cron capabilities") {
			return []byte(`{"error":{"code":"not_found","message":"project selector did not match any project"}}`), nil, 4, nil
		}
		t.Errorf("unexpected call %v", args)
		return nil, nil, 1, nil
	}, nil)
	report, _ := checkKata(t.Context(), c, k, l)
	data, _ := json.Marshal(report)
	if report.OK() || strings.Contains(string(data), "access_lost") || !strings.Contains(string(data), "missing") {
		t.Fatal(string(data))
	}
	for _, check := range report.Checks {
		if check.Name == "capabilities" || check.Name == "route" || check.Name == "identity and route" {
			if check.Passed || !strings.Contains(check.Detail, "kata not_found (exit 4): project selector did not match any project") {
				t.Fatalf("project resolution lost Kata's diagnostic: %+v", check)
			}
		}
	}
}

func TestDoctorMissingCheckoutNamesExactConfigKey(t *testing.T) {
	c, _, _, _ := doctorFixture(t)
	c.Checkouts = map[string]string{"build": filepath.Join(t.TempDir(), "missing")}
	for _, check := range ResourceChecks(c) {
		if check.Name == "checkout build" {
			if check.Passed || !strings.Contains(check.Detail, "checkouts.build") || !strings.Contains(check.Detail, config.DefaultPath()) {
				t.Fatal(check)
			}
			return
		}
	}
	t.Fatal("checkout check missing")
}

func TestDoctorReportsConfiguredProjects(t *testing.T) {
	for _, tc := range []struct {
		name, project, want string
	}{
		{"none", "", "no projects are configured; add a [[projects]] entry"},
		{"one", "test", "configured: test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, k, h, l := doctorFixture(t)
			c.Kata.Project = tc.project
			if tc.project == "" {
				c.Kata.Actor = ""
			}
			report := Check(t.Context(), c, k, h, l)
			for _, check := range report.Checks {
				if check.Name == "projects" {
					if !check.Passed || !strings.Contains(check.Detail, tc.want) {
						t.Fatalf("projects check = %+v, want passed with %q", check, tc.want)
					}
					return
				}
			}
			t.Fatalf("no projects check in %+v", report.Checks)
		})
	}
}

// Codex reports its Herdr session through its hooks. A profile that turns
// them off never proves an agent stopped, so every agent switch holds.
func TestDoctorWarnsWhenCodexHooksAreOff(t *testing.T) {
	for _, args := range [][]string{
		{"-c", "features.hooks=false"},
		{"--config", "features.hooks=false"},
		{"--disable", "hooks"},
		{"--disable=hooks"},
		{"--config=features.hooks=false"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			c, k, h, l := doctorFixture(t)
			c.Profiles["nohooks"] = config.Profile{Kind: "codex", Args: append([]string{"-m", "example-model"}, args...)}
			r := Check(t.Context(), c, k, h, l)
			var warned []CheckResult
			for _, check := range r.Checks {
				if strings.Contains(check.Detail, "hooks") && strings.HasPrefix(check.Detail, "warning:") {
					warned = append(warned, check)
				}
			}
			if len(warned) != 1 || !warned[0].Passed || !strings.Contains(warned[0].Detail, "profiles.nohooks") || !strings.Contains(warned[0].Detail, "remove "+strings.Join(args, " ")) || !strings.Contains(warned[0].Detail, "agent_switch_unproven") {
				t.Fatalf("%+v", r.Checks)
			}
		})
	}
	c, k, h, l := doctorFixture(t)
	c.Profiles["hooks"] = config.Profile{Kind: "codex", Args: []string{"-c", "features.hooks=true", "--disable", "web_search"}}
	for _, check := range Check(t.Context(), c, k, h, l).Checks {
		if strings.Contains(check.Detail, "warning:") && strings.Contains(check.Detail, "hooks") {
			t.Fatalf("enabled hooks warned: %+v", check)
		}
	}
}

// A credential file that is already private but failed its ACL read must not
// be told to restore mode 0600.
func TestCredentialRemedyFollowsTheCause(t *testing.T) {
	file := config.Kata{CredentialFile: "/srv/projects/example/token"}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{errors.New("secretref: file must be private to its owner (mode 0600)"), "restore an owner-readable file with mode 0600"},
		{config.ErrACLRead, "run herdr-kata doctor again"},
		{config.ErrExtendedACL, "remove the extended ACL"},
	} {
		if got := credentialRemedy(file, tc.err); got != tc.want {
			t.Errorf("%v: remedy %q, want %q", tc.err, got, tc.want)
		}
	}
	if got := credentialRemedy(config.Kata{CredentialEnv: "KATA_TOKEN"}, errors.New("unset")); !strings.Contains(got, "export the configured variable") {
		t.Error(got)
	}
}
