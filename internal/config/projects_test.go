package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// §22.2: an omitted allowlist permits all; an explicit empty list permits none.
func TestProjectsViews(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	base := `session="work"
executor="worker"
[kata]
bin="/bin/kata"
[checkouts]
work="/srv/projects/example"
[profiles.default]
kind="codex"
[[projects]]
name="example-project"
actor="worker"
[[projects]]
name="team-project"
actor="team-worker"
checkouts=[]
profiles=[]
commands=false
max_active_runs=1
`
	if err := os.WriteFile(path, []byte(base), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	a, b := c.View(c.Projects[0]), c.View(c.Projects[1])
	if a.Kata.Project != "example-project" || a.Kata.Actor != "worker" || len(a.Checkouts) != 1 || len(a.Profiles) != 1 || !a.CommandsAllowed() {
		t.Fatalf("permissive view: %+v", a)
	}
	if b.Kata.Project != "team-project" || len(b.Checkouts) != 0 || len(b.Profiles) != 0 || b.CommandsAllowed() {
		t.Fatalf("restricted view: %+v", b)
	}
	for _, suffix := range []string{"\n[[projects]]\nname=\"example-project\"\nactor=\"worker\"", "\n[[projects]]\nname=\"other\"\nactor=\"worker\"\nprofiles=[\"missing\"]"} {
		if err := os.WriteFile(path, []byte(base+suffix), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatal("accepted invalid projects")
		}
	}
	mixed := strings.Replace(base, "bin=\"/bin/kata\"", "bin=\"/bin/kata\"\nactor=\"worker\"", 1)
	if err := os.WriteFile(path, []byte(mixed), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("accepted mixed legacy and projects config")
	}
}

func TestLegacyProjectViewKeepsAllCredentialNames(t *testing.T) {
	c := Config{Kata: Kata{Bin: "/bin/kata", Project: "example-project", Actor: "worker", Daemon: "example-hub", CredentialEnv: "TOKEN_A"}, Profiles: map[string]Profile{"default": {Kind: "codex"}}}
	entries := c.ProjectEntries()
	if len(entries) != 1 {
		t.Fatal(entries)
	}
	view := c.View(entries[0])
	if view.Kata.Project != "example-project" || view.Kata.Daemon != "example-hub" || len(view.Profiles) != 1 || !view.CommandsAllowed() || len(view.Kata.CredentialNames) != 1 || view.Kata.CredentialNames[0] != "TOKEN_A" {
		t.Fatal(view)
	}
	c.Projects = []Project{{Name: "example-project", Actor: "worker", CredentialEnv: "TOKEN_A"}, {Name: "team-project", Actor: "worker", CredentialEnv: "TOKEN_B"}}
	view = c.View(c.Projects[1])
	if len(view.Kata.CredentialNames) != 2 || view.Kata.CredentialEnv != "TOKEN_B" {
		t.Fatal(view)
	}
}

// Views must not widen the child environment or multiply secret-source entries.
func TestLoadedProjectViewsKeepCredentialFenceAndSecretSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	data := `session="work"
executor="worker"
[kata]
bin="/bin/kata"
[profiles.default]
kind="codex"
[secrets.api]
env="API_SECRET"
[secrets.other]
env="API_SECRET"
[[projects]]
name="project-a"
actor="worker"
credential_env="TOKEN_A"
[[projects]]
name="project-b"
actor="worker"
credential_env="TOKEN_B"
[[projects]]
name="project-c"
actor="worker"
credential_env="TOKEN_A"
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range c.Projects {
		view := c.View(project)
		for range 2 {
			if got := view.CredentialVariables(); !slices.Equal(got, []string{"TOKEN_A", "TOKEN_B"}) {
				t.Errorf("%s credentials = %v", project.Name, got)
			}
			if got := view.Kata.SecretEnvs; !slices.Equal(got, []string{"API_SECRET"}) {
				t.Errorf("%s secret sources = %v", project.Name, got)
			}
			view = view.View(project)
		}
	}
}

func TestLiteralConfigCredentialFence(t *testing.T) {
	c := Config{Projects: []Project{{Name: "project-a", CredentialEnv: "TOKEN_A"}, {Name: "project-b", CredentialEnv: "TOKEN_B"}}}
	if got := c.CredentialVariables(); !slices.Equal(got, []string{"TOKEN_A", "TOKEN_B"}) {
		t.Fatal(got)
	}
	view := c.View(c.Projects[0])
	nested := view.View(view.ProjectEntries()[0])
	if got := nested.CredentialVariables(); !slices.Equal(got, []string{"TOKEN_A", "TOKEN_B"}) {
		t.Fatal(got)
	}
}
