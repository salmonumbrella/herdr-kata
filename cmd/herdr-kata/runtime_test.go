package main

import (
	"bytes"
	"encoding/json"
	"github.com/salmonumbrella/herdr-kata/internal/agentlimit"
	"github.com/salmonumbrella/herdr-kata/internal/daemon"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestManifestRuntimeCommands(t *testing.T) {
	var m struct {
		ID, Name, Version, MinHerdrVersion string
		Platforms                          []string
		Build, Startup, Actions, Panes     []struct {
			Command              []string
			ID, Placement, Title string
		}
	}
	if _, err := toml.DecodeFile("../../herdr-plugin.toml", &m); err != nil {
		t.Fatal(err)
	}
	if m.ID == "" || m.Name == "" || m.Version == "" || len(m.Platforms) != 2 {
		t.Fatalf("invalid manifest %+v", m)
	}
	// Human labels stay concise while Herdr's installation and command selectors stay stable.
	if m.ID != "kata" || m.Name != "kata" {
		t.Errorf("plugin identity/display = %q / %q", m.ID, m.Name)
	}
	wantTitles := map[string]string{
		"up":         "kata: start or upgrade runtime",
		"board-wide": "kata: open a wide TUI",
		"open-kata":  "Open Kata",
		"status":     "kata status",
		"board":      "kata TUI",
	}
	for _, item := range append(m.Actions, m.Panes...) {
		if want, ok := wantTitles[item.ID]; !ok || item.Title != want {
			t.Errorf("%s title = %q, want %q", item.ID, item.Title, want)
		}
		delete(wantTitles, item.ID)
	}
	if len(wantTitles) != 0 {
		t.Errorf("missing entry points: %v", wantTitles)
	}

	for _, group := range [][]struct {
		Command              []string
		ID, Placement, Title string
	}{m.Startup, m.Actions, m.Panes} {
		for _, item := range group {
			if len(item.Command) < 2 || item.Command[0] != "./bin/herdr-kata" {
				t.Fatalf("runtime command %+v", item)
			}
			if item.Command[1] == "board" || item.Command[1] == "board-wide" {
				t.Fatalf("manifest uses hidden alias: %+v", item)
			}
		}
	}
	if len(m.Startup) != 2 || !slices.Equal(m.Startup[1].Command, []string{"./bin/herdr-kata", "tui", "--startup"}) {
		t.Fatalf("TUI startup: %+v", m.Startup)
	}
	for id, want := range map[string][]string{
		"board":      {"./bin/herdr-kata", "tui"},
		"board-wide": {"./bin/herdr-kata", "tui", "--wide"},
	} {
		found := false
		for _, item := range append(m.Panes, m.Actions...) {
			if item.ID == id {
				found = true
				if !slices.Equal(item.Command, want) {
					t.Fatalf("%s command = %v, want %v", id, item.Command, want)
				}
			}
		}
		if !found {
			t.Fatalf("missing manifest ID %s", id)
		}
	}
}

func TestStatusCompactRunsAndFullJSON(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	r := &runner.Run{Version: 1, UID: "01K00000000000000000000000", Job: kata.Job{Name: "Parser maintenance"}, Phase: runner.Held, Outcome: "unknown", Reason: "agent_start_failed: invalid_params", IssueRef: "project#abcd", Herdr: runner.HerdrRef{AgentName: "hk-01k00000000000000000000000"}}
	r.Target.Identity.ProjectUID = "project"
	setupStatusLane(t, root)
	l := state.Layout{Root: root}
	if err := state.WriteJSON(state.OSFS{}, l.RunFile(r.UID), r); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := runCommandStatus(&out, &stderr, false); code != 0 {
		t.Fatal(code, stderr.String())
	}
	line := "run 01K00000 | project | Parser maintenance | parked | unknown | agent_start_failed: invalid_params | project#abcd | hk-01k00000000000000000000000"
	if !strings.Contains(out.String(), line+"\n") || strings.Contains(out.String(), "preview") || strings.Contains(out.String(), `"Herdr"`) {
		t.Fatalf("human status:\n%s", out.String())
	}
	out.Reset()
	stderr.Reset()
	if code := runCommandStatus(&out, &stderr, true); code != 0 {
		t.Fatal(code, stderr.String())
	}
	var v map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if _, exists := v["preview"]; exists {
		t.Fatal("runtime incorrectly advertises preview")
	}
	var rows []runner.Run
	if err := json.Unmarshal(v["runs"], &rows); err != nil || len(rows) != 1 || rows[0].UID != r.UID || rows[0].Herdr.AgentName != r.Herdr.AgentName {
		t.Fatal(rows, err)
	}
}

func TestStatusQuotaHoldShowsReset(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	reset := time.Date(2026, 9, 24, 17, 23, 0, 0, time.UTC)
	r := runner.Run{Version: 1, UID: "quota-run", Phase: runner.Held, Outcome: "unknown", Reason: "agent_quota", ActiveStep: "main", Steps: []runner.StepState{{Key: "main", Limit: &agentlimit.Limit{Kind: "agent_quota", Code: "codex:usage_limit_exceeded", ResetsAt: &reset}}}}
	r.Target.Identity.ProjectUID = "project"
	setupStatusLane(t, root)
	if e := state.WriteJSON(state.OSFS{}, state.Layout{Root: root}.RunFile(r.UID), r); e != nil {
		t.Fatal(e)
	}
	var out, stderr bytes.Buffer
	if code := runCommandStatus(&out, &stderr, false); code != 0 {
		t.Fatal(code, stderr.String())
	}
	if !strings.Contains(out.String(), "agent_quota (codex usage limit; resets 2026-09-24T17:23Z)") {
		t.Fatal(out.String())
	}
}

func TestStatusKeepsMultilineReasonOnOneRow(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	r := runner.Run{Version: 1, UID: "example-run", Phase: runner.Settled, Outcome: "failed", Reason: "first line\nsecond\tline"}
	r.Target.Identity.ProjectUID = "project"
	setupStatusLane(t, root)
	if e := state.WriteJSON(state.OSFS{}, state.Layout{Root: root}.RunFile(r.UID), r); e != nil {
		t.Fatal(e)
	}
	var out, stderr bytes.Buffer
	if code := runCommandStatus(&out, &stderr, false); code != 0 {
		t.Fatal(code, stderr.String())
	}
	if !strings.Contains(out.String(), " | first line second line | ") {
		t.Fatal("compact reason split the row", out.String())
	}
}

func runCommandStatus(out, stderr *bytes.Buffer, asJSON bool) int {
	args := []string{"status"}
	if asJSON {
		args = append(args, "--json")
	}
	return run(args, out, stderr)
}
func TestCLIStatusDoesNotStart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent")
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	var out, err bytes.Buffer
	if code := run([]string{"status", "--json"}, &out, &err); code != 0 {
		t.Fatalf("code %d: %s", code, &err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"running":false`)) {
		t.Fatal(out.String())
	}
	if _, e := os.Stat(root); !os.IsNotExist(e) {
		t.Fatal("status created state")
	}
}
func TestCLIRejectsRuntimeArguments(t *testing.T) {
	for _, args := range [][]string{{"up", "extra"}, {"serve", "extra"}, {"worker", "extra"}, {"stop", "extra"}, {"status", "extra"}, {"doctor", "extra"}} {
		var out, err bytes.Buffer
		if code := run(args, &out, &err); code != 2 {
			t.Fatalf("%v exits %d: %s", args, code, &err)
		}
	}
}

func TestStatusShowsFreshAttemptAgent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	r := runner.Run{Version: 1, UID: "run", Phase: runner.Held, ActiveStep: "fresh", Herdr: runner.HerdrRef{AgentName: "shared"}, Steps: []runner.StepState{{Key: "fresh", Herdr: &runner.HerdrRef{AgentName: "fresh-agent"}}}}
	r.Target.Identity.ProjectUID = "project"
	setupStatusLane(t, root)
	if e := state.WriteJSON(state.OSFS{}, state.Layout{Root: root}.RunFile(r.UID), r); e != nil {
		t.Fatal(e)
	}
	var out, stderr bytes.Buffer
	if code := runCommandStatus(&out, &stderr, false); code != 0 || !strings.Contains(out.String(), "fresh-agent") {
		t.Fatal(code, out.String(), stderr.String())
	}
}

func TestStatusKeepsFreshAgentAcrossParkedCommand(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	r := runner.Run{Version: 1, UID: "01K00000000000000000000000", Phase: runner.Held, ActiveStep: "check", Steps: []runner.StepState{{Key: "first", Kind: "prompt", Attempt: 1, Submission: runner.Accepted, Status: "succeeded", Herdr: &runner.HerdrRef{AgentName: "fresh-checker"}}, {Key: "check", Kind: "command", Attempt: 1, Status: "failed"}}}
	r.Target.Identity.ProjectUID = "project"
	setupStatusLane(t, root)
	if e := state.WriteJSON(state.OSFS{}, (state.Layout{Root: root}).RunFile(r.UID), r); e != nil {
		t.Fatal(e)
	}
	var out, stderr bytes.Buffer
	if code := runCommandStatus(&out, &stderr, false); code != 0 || !strings.Contains(out.String(), " | fresh-checker\n") {
		t.Fatal("status lost retained agent", code, out.String(), stderr.String())
	}
}

func TestStatusShowsPreflightFailureDetail(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	r := &runner.Run{Version: 1, UID: "01K00000000000000000000000", Phase: runner.Held, Reason: "herdr_env_unclean", ActiveStep: "main", Steps: []runner.StepState{{Key: "main", Reason: "Herdr launch environment leaks KATA_SERVER; start the Herdr server without these variables"}}}
	r.Target.Identity.ProjectUID = "project"
	setupStatusLane(t, root)
	if e := state.WriteJSON(state.OSFS{}, (state.Layout{Root: root}).RunFile(r.UID), r); e != nil {
		t.Fatal(e)
	}
	var out, stderr bytes.Buffer
	if code := runCommandStatus(&out, &stderr, false); code != 0 {
		t.Fatal(code, stderr.String())
	}
	if !strings.Contains(out.String(), "KATA_SERVER") || !strings.Contains(out.String(), "start the Herdr server") {
		t.Fatal(out.String())
	}
}

func setupStatusLane(t *testing.T, root string) state.Layout {
	t.Helper()
	l := state.Layout{Root: root}
	if err := state.InitializeLayout(state.OSFS{}, l); err != nil {
		t.Fatal(err)
	}
	lane := l.Project("project")
	if err := state.WriteJSON(state.OSFS{}, lane.StateFile(), kata.Installation{Version: 1, ProjectUID: "project"}); err != nil {
		t.Fatal(err)
	}
	return lane
}

func TestStatusPrintsAutomaticAuthorGate(t *testing.T) {
	var out bytes.Buffer
	printProjectStatus(&out, daemon.Status{Definitions: []defs.JobEntry{{UID: "job", Supported: true, Warnings: []string{"automatic scheduling disabled: original author does not match lane actor"}}}})
	if !strings.Contains(out.String(), "automatic scheduling disabled: original author does not match lane actor") {
		t.Fatal(out.String())
	}
}
