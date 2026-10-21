package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestUpWithoutProjectsStillPreparesLaneRetirement(t *testing.T) {
	root, _, _ := startupResourceConfig(t)
	writeNoProjectConfig(t, root, filepath.Join(root, "missing-kata"))
	l := state.Layout{Root: filepath.Join(root, "state")}
	if err := state.InitializeLayout(state.OSFS{}, l); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(state.OSFS{}, l.Project("project").StateFile(), kata.Installation{Version: 1, ProjectUID: "project", Name: "removed-project"}); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"up"}, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "kata.bin") {
		t.Fatalf("up skipped retirement startup preflight: exit=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
	}
}

func TestUpWithoutProjectsExplainsRetainedLanes(t *testing.T) {
	root, bin, _ := startupResourceConfig(t)
	writeNoProjectConfig(t, root, bin)
	l := state.Layout{Root: filepath.Join(root, "state")}
	if err := state.InitializeLayout(state.OSFS{}, l); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(state.OSFS{}, l.Project("project").StateFile(), kata.Installation{Version: 1, ProjectUID: "project", Name: "removed-project"}); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"up"}, &out, &stderr); code != 0 {
		t.Fatalf("up exit=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
	}
	// startupResourceConfig contains serve in a child that only writes this
	// marker. Waiting for it also keeps that child inside the test's lifetime.
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(filepath.Join(root, "started")); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatal("retained lanes did not start the runtime")
		case <-tick.C:
		}
	}
	if got, want := out.String(), "started: no projects configured; retained lanes need retirement and saved reports\n"; got != want {
		t.Fatalf("retirement startup = %q, want %q", got, want)
	}
}

func writeNoProjectConfig(t *testing.T, root, bin string) {
	t.Helper()
	data := fmt.Sprintf(`session = "work"
executor = "example-runtime"
[kata]
bin = %q
[profiles.default]
kind = "codex"
`, bin)
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestUpWithoutProjectsStartsNothing(t *testing.T) {
	for _, args := range [][]string{{"up"}, {"up", "--takeover"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root, bin, _ := startupResourceConfig(t)
			writeNoProjectConfig(t, root, bin)
			var out, stderr bytes.Buffer
			if code := run(args, &out, &stderr); code != 0 {
				t.Fatalf("up exit %d, stdout %q, stderr %q", code, out.String(), stderr.String())
			}
			text := out.String()
			if !strings.Contains(text, "no projects are configured") || !strings.Contains(text, filepath.Join(root, "config.toml")) || !strings.Contains(text, "[[projects]]") {
				t.Fatalf("up did not explain the empty configuration: %q", text)
			}
			for _, name := range []string{"state", "started"} {
				if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
					t.Fatal("up without projects created", name, err)
				}
			}
		})
	}
}

func TestUpProjectFlagRefusesExistingConfig(t *testing.T) {
	root, bin, checkout := startupResourceConfig(t)
	writeStartupConfig(t, root, bin, checkout)
	before, err := os.ReadFile(filepath.Join(root, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"up", "--project", "team-project"}, &out, &stderr); code != 1 {
		t.Fatalf("up exit %d, stdout %q, stderr %q", code, out.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "[[projects]]") || !strings.Contains(stderr.String(), filepath.Join(root, "config.toml")) {
		t.Fatalf("refusal does not name the edit: %q", stderr.String())
	}
	after, err := os.ReadFile(filepath.Join(root, "config.toml"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("existing config changed: %v", err)
	}
	for _, name := range []string{"state", "started"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatal("refused up created", name, err)
		}
	}
}

// discoveryKata answers first-start discovery for two projects on one local
// route: alpha has a job and beta has no definitions.
func discoveryKata(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "kata")
	content := `#!/bin/sh
case "$*" in
  *"--project alpha "*) uid=project-alpha; jobs='[{"uid":"job-a"}]' ;;
  *"--project beta "*) uid=project-beta; jobs='[]' ;;
esac
case "$*" in
  "--json daemon diagnose") printf '%s\n' '{"source":"local runtime","kind":"local","state":"ready","endpoint":"unix:///tmp/kata.sock"}' ;;
  "--json whoami") printf '%s\n' '{"actor":"worker","source":"env"}' ;;
  "--json --as worker federation identity") printf '%s\n' '{"instance_uid":"instance","auth":{"actor":"worker"}}' ;;
  "--json --as worker projects list") printf '%s\n' '{"projects":[{"name":"alpha","uid":"project-alpha","active":true},{"name":"beta","uid":"project-beta","active":true}]}' ;;
  *" health") printf '%s\n' '{"schema_version":33,"version":"test"}' ;;
  *" cron capabilities") printf '%s\n' '{"project_uid":"'"$uid"'","event_features":["cron_v1"]}' ;;
  *" federation identity") printf '%s\n' '{"instance_uid":"instance","auth":{"actor":"worker"}}' ;;
  *" daemon diagnose "*) printf '%s\n' '{"state":"ready","endpoint":"unix:///tmp/kata.sock","project_uid":"'"$uid"'"}' ;;
  *" cron job list") printf '%s\n' '{"jobs":'"$jobs"'}' ;;
  *" cron workflow list") printf '%s\n' '{"workflows":[]}' ;;
  *) exit 99 ;;
esac
`
	if err := os.WriteFile(path, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDiscoverConfigAddsNamedProjects(t *testing.T) {
	for _, tc := range []struct {
		named []string
		want  string
	}{
		{nil, "alpha"},
		{[]string{"beta"}, "alpha,beta"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			dir := t.TempDir()
			discoveryKata(t, dir)
			for _, pair := range os.Environ() {
				if name, _, _ := strings.Cut(pair, "="); strings.HasPrefix(name, "KATA_") {
					t.Setenv(name, "")
				}
			}
			t.Setenv("KATA_HOME", filepath.Join(dir, "kata-home"))
			t.Setenv("KATA_AUTHOR", "worker")
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Chdir(dir)
			server := herdrtest.New(t)
			server.SetAgent(herdr.Agent{Name: "existing", Session: &herdr.AgentSession{Agent: "codex"}})
			t.Setenv("HERDR_SOCKET_PATH", server.Path)
			path := filepath.Join(dir, "config", "config.toml")
			if created, err := discoverConfig(t.Context(), path, tc.named); !created || err != nil {
				t.Fatalf("discover = %t, %v", created, err)
			}
			c, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			names := []string{}
			for _, p := range c.ProjectEntries() {
				names = append(names, p.Name)
			}
			if got := strings.Join(names, ","); got != tc.want {
				t.Fatalf("projects = %q, want %q", got, tc.want)
			}
		})
	}
}
