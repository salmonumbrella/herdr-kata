//go:build herdr

package herdr_test

import (
	"context"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/testenv"
	"time"
)

func TestRealHerdrEnvironmentProbeReplacement(t *testing.T) {
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_HERDR_BIN")
	helper := filepath.Join(t.TempDir(), "herdr-kata")
	build := exec.Command("go", "build", "-o", helper, "../../cmd/herdr-kata")
	build.Env = append(os.Environ(), "GOMAXPROCS=2", "GOFLAGS=-p=2")
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build probe helper: %v %s", e, out)
	}
	s := newRealHerdr(t, bin)
	s.start(t)
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	env := map[string]string{"HERDR_KATA_RUN": "probe", "HERDR_KATA_RUN_DIR": s.home, "HERDR_KATA_REPORT": helper, "KATA_PROJECT": "example-project"}
	probes := 0
	ws := func(ctx context.Context) (string, error) {
		probes++
		w, e := s.client.WorkspaceCreate(ctx, "Fixture", s.home)
		return w.WorkspaceID, e
	}
	var probe herdr.EnvironmentProbe
	proven, e := probe.Check(ctx, s.client, ws, env, []string{"EXAMPLE_CREDENTIAL"}, helper, s.home)
	current, readErr := herdr.ReadServerID(s.client.Path)
	if e != nil || readErr != nil || proven == (herdr.ServerID{}) || proven != current {
		t.Fatalf("probe generation: %+v current %+v errors %v %v", proven, current, e, readErr)
	}
	if _, e := probe.Check(ctx, s.client, ws, env, []string{"EXAMPLE_CREDENTIAL"}, helper, s.home); e != nil {
		t.Fatal(e)
	}
	if probes != 1 {
		t.Fatalf("unchanged server was probed %d times", probes)
	}
	originalPath := s.client.Path
	s.client.Path += "-missing"
	if _, e := probe.Check(ctx, s.client, ws, env, []string{"EXAMPLE_CREDENTIAL"}, helper, s.home); e == nil {
		t.Fatal("unreadable identity accepted")
	}
	s.client.Path = originalPath
	if _, e := probe.Check(ctx, s.client, ws, env, []string{"EXAMPLE_CREDENTIAL"}, helper, s.home); e != nil {
		t.Fatal(e)
	}
	if probes != 2 {
		t.Fatal("unreadable identity did not invalidate proof")
	}
	env["XDG_EXAMPLE"] = "fixture"
	if _, e := probe.Check(ctx, s.client, ws, env, []string{"EXAMPLE_CREDENTIAL"}, helper, s.home); e != nil {
		t.Fatal(e)
	}
	if probes != 3 {
		t.Fatal("launch map change did not invalidate proof")
	}
	// Keep the same probe (worker) alive across both replacements.
	clean := append([]string{}, s.env...)
	s.stop(t)
	s.env = append(append([]string{}, clean...), "KATA_DAEMON=fixture-route", "KATA_SERVER=fixture-server", "EXAMPLE_CREDENTIAL=fixture-credential")
	s.start(t)
	_, e = probe.Check(ctx, s.client, ws, env, []string{"EXAMPLE_CREDENTIAL"}, helper, s.home)
	if e == nil {
		t.Fatal("cached clean proof survived contaminated server replacement")
	}
	for _, name := range []string{"KATA_DAEMON", "KATA_SERVER", "EXAMPLE_CREDENTIAL"} {
		if !strings.Contains(e.Error(), name) {
			t.Errorf("missing leaked name %s: %v", name, e)
		}
	}
	if strings.Contains(e.Error(), "fixture-") {
		t.Fatal("probe printed a value")
	}
	s.stop(t)
	s.env = clean
	s.start(t)
	if _, e := probe.Check(ctx, s.client, ws, env, []string{"EXAMPLE_CREDENTIAL"}, helper, s.home); e != nil {
		t.Fatal("clean replacement did not repair probe", e)
	}
	// Credential-name changes invalidate a successful proof on the same server.
	s.stop(t)
	s.env = append(append([]string{}, clean...), "EXAMPLE_OTHER_CREDENTIAL=fixture-other")
	s.start(t)
	if _, e := probe.Check(ctx, s.client, ws, env, []string{"EXAMPLE_CREDENTIAL"}, helper, s.home); e != nil {
		t.Fatal(e)
	}
	_, e = probe.Check(ctx, s.client, ws, env, []string{"EXAMPLE_CREDENTIAL", "EXAMPLE_OTHER_CREDENTIAL"}, helper, s.home)
	if e == nil || !strings.Contains(e.Error(), "EXAMPLE_OTHER_CREDENTIAL") {
		t.Fatal("configuration change reused proof", e)
	}
}

// Mac acceptance D1: startup files can contaminate a clean server's panes.
func TestRealHerdrEnvironmentProbeShellStartupRepair(t *testing.T) {
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_HERDR_BIN")
	shell, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is required for the shell startup regression")
	}
	helper := filepath.Join(t.TempDir(), "herdr-kata")
	build := exec.Command("go", "build", "-o", helper, "../../cmd/herdr-kata")
	build.Env = append(os.Environ(), "GOMAXPROCS=2", "GOFLAGS=-p=2")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build probe helper: %v %s", err, out)
	}
	s := newRealHerdr(t, bin)
	cfg := "onboarding = false\n[terminal]\ndefault_shell = '" + shell + "'\nshell_mode = 'non_login'\n"
	if err := os.WriteFile(filepath.Join(s.home, ".config", "herdr", "config.toml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	rc := filepath.Join(s.home, ".zshrc")
	exports := "export KATA_AUTHOR=fixture-author KATA_HTTP_TIMEOUT=7s KATA_TELEMETRY_ENABLED=false\n"
	if err := os.WriteFile(rc, []byte(exports), 0600); err != nil {
		t.Fatal(err)
	}
	s.start(t)
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	ws, err := s.client.WorkspaceCreate(ctx, "Fixture", s.home)
	if err != nil {
		t.Fatal(err)
	}
	workspace := func(context.Context) (string, error) { return ws.WorkspaceID, nil }
	env := map[string]string{"HERDR_KATA_RUN": "probe", "KATA_PROJECT": "example-project"}
	var probe herdr.EnvironmentProbe
	_, err = probe.Check(ctx, s.client, workspace, env, nil, helper, s.home)
	if err == nil {
		t.Fatal("clean server masked shell startup contamination")
	}
	for _, want := range []string{"KATA_AUTHOR", "KATA_HTTP_TIMEOUT", "KATA_TELEMETRY_ENABLED", "~/.zshrc", "guard"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in shell startup diagnostic: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "fixture-author") {
		t.Fatal("probe printed an environment value")
	}
	// The runtime marks probe and agent tabs before their shells read rc files.
	guarded := "if [ -z \"${HERDR_KATA_RUN:-}\" ]; then\n" + exports + "fi\n"
	if err := os.WriteFile(rc, []byte(guarded), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := probe.Check(ctx, s.client, workspace, env, nil, helper, s.home); err != nil {
		t.Fatalf("rc repair did not clean the next pane without a server restart: %v", err)
	}
}
