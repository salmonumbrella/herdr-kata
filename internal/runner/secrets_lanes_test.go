package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
)

// §22.4: secrets sit between the shared environment and runtime routing;
// every project's credential is removed after all overlays.
func TestCommandLaneSecretEnvironment(t *testing.T) {
	spec := commandSpec(t, `printf '%s|%s|%s|%s|%s|%s|%s|%s' "$KATA_PROJECT" "${KATA_DAEMON-unset}" "$KATA_SERVER" "${TOKEN_A-unset}" "${TOKEN_B-unset}" "$GITHUB_TOKEN" "$KATA_HOME" "$HERDR_KATA_RUN"`)
	spec.Env = []string{"PATH=/usr/bin:/bin", "KATA_PROJECT=other", "KATA_DAEMON=inherited", "KATA_SERVER=other", "TOKEN_A=fixture-a", "TOKEN_B=fixture-b", "KATA_HOME=fixture-home"}
	spec.KataEnv = map[string]string{"KATA_SERVER": "https://hub.example", "KATA_DAEMON": "shared", "TOKEN_A": "fixture-shared"}
	spec.Secrets = map[string]string{"GITHUB_TOKEN": "fixture-job", "TOKEN_B": "fixture-job-credential", "HERDR_KATA_RUN": "wrong"}
	spec.CredentialEnvs = []string{"TOKEN_A", "TOKEN_B"}
	spec.Project = "example-project"
	receipt, err := RunCommand(t.Context(), spec)
	if err != nil || receipt.ExitCode != 0 {
		t.Fatal(receipt, err)
	}
	b, err := os.ReadFile(filepath.Join(spec.AttemptDir, "output.log"))
	if err != nil || string(b) != "example-project|unset|https://hub.example|unset|unset|fixture-job|fixture-home|run-1" {
		t.Fatal(string(b), err)
	}
}

func TestPromptLaneSecretEnvironment(t *testing.T) {
	f := newExecution(t)
	f.m.cfg.Kata.Project = "example-project"
	f.m.cfg.Kata.CredentialEnv = "TOKEN_A"
	f.m.cfg.Kata.CredentialNames = []string{"TOKEN_A", "TOKEN_B"}
	f.m.cfg.Kata.Env = map[string]string{"KATA_DAEMON": "shared", "KATA_SERVER": "https://hub.example", "TOKEN_A": "fixture-a", "TOKEN_B": "fixture-b"}
	// Exercise the socket map with a resolved secret and adversarial overlays.
	if err := f.m.launchPrepared(t.Context(), f.uid, 0, map[string]string{"GITHUB_TOKEN": "fixture-job", "TOKEN_B": "fixture-secret", "KATA_PROJECT": "other"}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, req := range f.h.Requests() {
		if req.Method != "tab.create" {
			continue
		}
		var p struct{ Env map[string]string }
		if err := json.Unmarshal(req.Params, &p); err != nil {
			t.Fatal(err)
		}
		found = true
		if p.Env["GITHUB_TOKEN"] != "fixture-job" || p.Env["KATA_PROJECT"] != "example-project" || p.Env["KATA_SERVER"] != "https://hub.example" {
			t.Fatal(p.Env)
		}
		for _, key := range []string{"KATA_DAEMON", "TOKEN_A", "TOKEN_B", "KATA_AUTH_TOKEN"} {
			if _, ok := p.Env[key]; ok {
				t.Fatalf("tab received forbidden %s", key)
			}
		}
	}
	if !found {
		t.Fatal("no tab created")
	}
}

func TestProjectEnvironmentProbeCachesPerLane(t *testing.T) {
	f := newExecution(t)
	f.m.environment = &herdr.EnvironmentProbe{}
	f.h.BeforeRequest = func(req herdrtest.Request) {
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
		if err := os.WriteFile(strings.TrimSuffix(path, "'"), []byte("[]"), 0600); err != nil {
			t.Error(err)
		}
	}
	aCfg, bCfg := f.m.cfg, f.m.cfg
	aCfg.Kata.Project = "example-project"
	bCfg.Kata.Project = "team-project"
	bCfg.Kata.Daemon = "example-route"
	a, err := f.m.RegisterProject("project-a", aCfg, nil, nil, f.s.layout.Project("project-a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.m.RegisterProject("project-b", bCfg, nil, nil, f.s.layout.Project("project-b"))
	if err != nil {
		t.Fatal(err)
	}
	for _, lane := range []*Manager{a, b, a} {
		if _, err = lane.checkEnvironment(t.Context(), f.s.layout.Root, ""); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.count("tab.create"); got != 2 {
		t.Fatalf("expected one probe per unchanged lane, got %d", got)
	}
}

func TestPromptProbeRejectsOtherLaneCredential(t *testing.T) {
	f := newExecution(t)
	f.m.environment = &herdr.EnvironmentProbe{}
	f.m.cfg.Kata.CredentialEnv = "TOKEN_A"
	f.m.cfg.Kata.CredentialNames = []string{"TOKEN_A", "TOKEN_B"}
	f.h.BeforeRequest = func(req herdrtest.Request) {
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
	f.launch(t)
	r := loaded(t, f.s, f.uid)
	if r.Phase != Held || r.Reason != "herdr_env_unclean" || !strings.Contains(r.Steps[0].Reason, "TOKEN_B") || f.count("agent.start") != 0 {
		t.Fatalf("other lane credential reached prompt launch: phase=%s reason=%s starts=%d", r.Phase, r.Reason, f.count("agent.start"))
	}
}
