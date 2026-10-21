package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestPublicSubcommandHelpIsLocal(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	for _, command := range []string{"up", "stop", "status", "doctor", "run", "resume", "release", "abandon", "runs", "logs", "tui", "open-kata", "report", "version"} {
		t.Run(command, func(t *testing.T) {
			var out, stderr bytes.Buffer
			if code := run([]string{command, "--help"}, &out, &stderr); code != 0 || !strings.Contains(strings.ToLower(out.String()+stderr.String()), "usage") {
				t.Fatalf("help: code=%d stdout=%s stderr=%s", code, out.String(), stderr.String())
			}
		})
	}
}

func TestAbandonInvalidUIDIsUsageBeforeConfig(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	for _, args := range [][]string{{"abandon", "bad"}, {"abandon", "--local", "bad"}, {"abandon", "--unknown"}} {
		var out, stderr bytes.Buffer
		if code := run(args, &out, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage: herdr-kata abandon") {
			t.Fatal(args, code, stderr.String())
		}
	}
}

func TestRunAndRunsAgreeOnUnknownProject(t *testing.T) {
	c := config.Config{Projects: []config.Project{{Name: "example-project", Actor: "worker"}}}
	l := state.Layout{Root: t.TempDir()}
	_, _, runErr := resolveRunJob(t.Context(), c, state.OSFS{}, l, "job", "missing")
	var out bytes.Buffer
	runsErr := listRunsProjects(t.Context(), c, "missing", "job", 10, &out, func(config.Config) runsReader { t.Fatal("unknown project made a Kata client"); return nil })
	if runErr == nil || runsErr == nil || runErr.Error() != `unknown project "missing"` || runErr.Error() != runsErr.Error() {
		t.Fatal(runErr, runsErr)
	}
}
