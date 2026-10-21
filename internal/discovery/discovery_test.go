package discovery

import (
	"context"
	"errors"
	"fmt"
	"github.com/BurntSushi/toml"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
)

type fakeKata struct {
	actor       string
	whoamiErr   error
	routeUID    string
	routeErr    error
	projects    []Project
	probes      map[string]ProjectProbe
	probeErr    map[string]error
	bin         string
	routeEnv    map[string]string
	credential  string
	whoamiCalls int
	listCalls   int
	probed      []string
	// empty names projects without definitions; others have at least one.
	empty     map[string]bool
	defsErr   map[string]error
	defsCheck []string
}

func (f *fakeKata) WhoAmI(context.Context) (string, error) {
	f.whoamiCalls++
	return f.actor, f.whoamiErr
}
func (f *fakeKata) RouteIdentity(context.Context, string) (string, error) {
	if f.routeErr != nil {
		return "", f.routeErr
	}
	if f.routeUID == "" {
		return "instance", nil
	}
	return f.routeUID, nil
}
func (f *fakeKata) ListProjects(context.Context, string) ([]Project, error) {
	f.listCalls++
	return f.projects, nil
}
func (f *fakeKata) ProbeProject(_ context.Context, name, _ string) (ProjectProbe, error) {
	f.probed = append(f.probed, name)
	if err := f.probeErr[name]; err != nil {
		return ProjectProbe{}, err
	}
	return f.probes[name], nil
}
func (f *fakeKata) HasDefinitions(_ context.Context, name, actor string) (bool, error) {
	f.defsCheck = append(f.defsCheck, name+" as "+actor)
	if err := f.defsErr[name]; err != nil {
		return false, err
	}
	return !f.empty[name], nil
}
func (f *fakeKata) Bin() string { return f.bin }
func (f *fakeKata) RouteEnv() map[string]string {
	return f.routeEnv
}
func (f *fakeKata) CredentialEnv() string { return f.credential }

type fakeHerdr struct {
	pong   herdr.Pong
	agents []herdr.Agent
	err    error
}

func (f fakeHerdr) Ping(context.Context) (herdr.Pong, error) { return f.pong, f.err }
func (f fakeHerdr) AgentList(context.Context) ([]herdr.Agent, error) {
	return f.agents, f.err
}

func TestEnsureCreatesCompleteConfigForEveryAccessibleProject(t *testing.T) {
	t.Setenv("KATA_INBOX_USER", "")
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	k := &fakeKata{
		actor:    "worker",
		projects: []Project{{Name: "zeta", Active: true}, {Name: "inactive", Active: false}, {Name: "alpha", Active: true}, {Name: "unavailable", Active: true}},
		probes: map[string]ProjectProbe{
			"alpha": {UID: "project-alpha", Actor: "worker", CronV1: true, Daemon: "route-a"},
			"zeta":  {UID: "project-zeta", Actor: "worker", CronV1: true, Daemon: "route-z"},
		},
		probeErr:   map[string]error{"unavailable": ErrProjectUnavailable},
		bin:        "/usr/local/bin/kata",
		routeEnv:   map[string]string{"KATA_HOME": "/srv/kata", "KATA_SERVER": "https://hub.example"},
		credential: "KATA_AUTH_TOKEN",
	}
	h := fakeHerdr{pong: herdr.Pong{Version: "0.9.5", Protocol: 22}, agents: []herdr.Agent{{Session: &herdr.AgentSession{Agent: "codex"}}}}
	d := Discoverer{Kata: k, Herdr: h, Session: "work", LookupAgent: func(string) (string, error) { return "/usr/local/bin/codex", nil }}
	created, err := d.Ensure(t.Context(), path)
	if err != nil || !created {
		t.Fatalf("ensure = %t, %v", created, err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Session != "work" || c.Kata.Bin != "/usr/local/bin/kata" || c.Kata.Env["KATA_SERVER"] != "https://hub.example" || c.Kata.Env["KATA_HOME"] != "/srv/kata" {
		t.Fatalf("route config = %+v", c)
	}
	if len(c.Projects) != 2 || c.Projects[0].Name != "alpha" || c.Projects[1].Name != "zeta" {
		t.Fatalf("projects = %+v", c.Projects)
	}
	for _, p := range c.Projects {
		if p.Actor != "worker" || p.CredentialEnv != "KATA_AUTH_TOKEN" || p.Daemon == "" || p.Inbox != "worker/herdr-kata" {
			t.Fatalf("project = %+v", p)
		}
	}
	if c.Profiles["default"].Kind != "codex" || c.Profiles["codex"].Kind != "codex" {
		t.Fatalf("profiles = %+v", c.Profiles)
	}
	if got := strings.Join(k.probed, ","); got != "alpha,unavailable,zeta" {
		t.Fatalf("probe order = %q", got)
	}
	if mode := fileMode(t, path); mode != 0600 {
		t.Fatalf("config mode = %o", mode)
	}
}

func TestEnsureRejectsInventoryAndProbeRouteMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	k := &fakeKata{
		actor:    "worker",
		routeUID: "route-a",
		projects: []Project{{Name: "same-name", UID: "project-a", Active: true}},
		probes:   map[string]ProjectProbe{"same-name": {UID: "project-a", InstanceUID: "route-b", Actor: "worker", CronV1: true}},
		bin:      "/usr/local/bin/kata",
	}
	d := Discoverer{Kata: k, Herdr: fakeHerdr{pong: herdr.Pong{Version: "0.9.5", Protocol: 22}, agents: []herdr.Agent{{Session: &herdr.AgentSession{Agent: "codex"}}}}, Session: "work"}
	created, err := d.Ensure(t.Context(), path)
	if created || err == nil || !strings.Contains(err.Error(), "route changed") {
		t.Fatalf("ensure = %t, %v", created, err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("mismatched route wrote config: %v", statErr)
	}
}

func TestEnsureFailsTransientProjectProbe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	k := &fakeKata{
		actor:    "worker",
		projects: []Project{{Name: "example-project", Active: true}},
		probeErr: map[string]error{"example-project": errors.New("daemon unavailable")},
		bin:      "/usr/local/bin/kata",
	}
	d := Discoverer{Kata: k, Herdr: fakeHerdr{pong: herdr.Pong{Version: "0.9.5", Protocol: 22}, agents: []herdr.Agent{{Session: &herdr.AgentSession{Agent: "codex"}}}}, Session: "work"}
	created, err := d.Ensure(t.Context(), path)
	if created || err == nil || !strings.Contains(err.Error(), "probe Kata project") {
		t.Fatalf("ensure = %t, %v", created, err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("transient probe wrote config: %v", statErr)
	}
}

func TestEnsureDoesNotOverwriteExplicitConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := "session=\"explicit\"\n"
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	d := Discoverer{Kata: &fakeKata{}, Herdr: fakeHerdr{}, Session: "work"}
	created, err := d.Ensure(t.Context(), path)
	if err != nil || created {
		t.Fatalf("ensure = %t, %v", created, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != contents {
		t.Fatalf("explicit config changed: %q, %v", got, err)
	}
}

func TestEnsureFailsBeforeWritingWhenCredentialIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	k := &fakeKata{
		actor:    "worker",
		projects: []Project{{Name: "example-project", Active: true}},
		probeErr: map[string]error{"example-project": ErrCredentialRequired},
		bin:      "/usr/local/bin/kata",
	}
	d := Discoverer{Kata: k, Herdr: fakeHerdr{pong: herdr.Pong{Version: "0.9.5", Protocol: 22}}, Session: "work", LookupAgent: func(string) (string, error) { return "/usr/local/bin/codex", nil }}
	created, err := d.Ensure(t.Context(), path)
	if created || !errors.Is(err, ErrCredentialRequired) || !strings.Contains(err.Error(), "credential") {
		t.Fatalf("ensure = %t, %v", created, err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("partial config exists: %v", statErr)
	}
}

func TestEnsureRejectsMissingSessionOrHerdr(t *testing.T) {
	k := &fakeKata{actor: "worker", projects: []Project{{Name: "example-project", Active: true}}}
	for _, tc := range []struct {
		name    string
		session string
		err     error
	}{
		{name: "missing session", err: ErrSessionUnavailable},
		{name: "herdr ping", session: "work", err: ErrHerdrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := fakeHerdr{err: tc.err}
			d := Discoverer{Kata: k, Herdr: h, Session: tc.session}
			created, err := d.Ensure(t.Context(), filepath.Join(t.TempDir(), "config.toml"))
			if created || !errors.Is(err, tc.err) {
				t.Fatalf("ensure = %t, %v", created, err)
			}
		})
	}
}

func TestDetectProfilesUsesExistingAgentsAndPathFallback(t *testing.T) {
	agents := []herdr.Agent{
		{Session: &herdr.AgentSession{Agent: "codex"}},
		{Session: &herdr.AgentSession{Agent: "codex"}},
		{Session: &herdr.AgentSession{Agent: "claude"}},
	}
	profiles, err := DetectProfiles(agents, func(kind string) (string, error) {
		return "/usr/local/bin/" + kind, nil
	})
	if err != nil || profiles["default"].Kind != "codex" || profiles["codex"].Kind != "codex" || profiles["claude"].Kind != "claude" {
		t.Fatalf("profiles = %+v, %v", profiles, err)
	}
	profiles, err = DetectProfiles(nil, func(kind string) (string, error) {
		if kind == "claude" {
			return "", errors.New("missing")
		}
		return "/usr/local/bin/codex", nil
	})
	if err != nil || profiles["default"].Kind != "codex" {
		t.Fatalf("fallback profiles = %+v, %v", profiles, err)
	}
}

func TestDetectProfilesRequiresAUsableAgent(t *testing.T) {
	_, err := DetectProfiles(nil, func(string) (string, error) { return "", errors.New("missing") })
	if err == nil || !strings.Contains(err.Error(), "usable Herdr agent") {
		t.Fatalf("missing profile error = %v", err)
	}
}

func TestEnsureDoesNotPersistCredentialValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	k := &fakeKata{
		actor:      "worker",
		projects:   []Project{{Name: "example-project", Active: true}},
		probes:     map[string]ProjectProbe{"example-project": {UID: "project", Actor: "worker", CronV1: true}},
		bin:        "/usr/local/bin/kata",
		credential: "KATA_AUTH_TOKEN",
		routeEnv:   map[string]string{"KATA_HOME": "/srv/kata"},
	}
	d := Discoverer{Kata: k, Herdr: fakeHerdr{pong: herdr.Pong{Version: "0.9.5", Protocol: 22}, agents: []herdr.Agent{{Session: &herdr.AgentSession{Agent: "codex"}}}}, Session: "work", LookupAgent: func(string) (string, error) { return "/usr/local/bin/codex", nil }}
	if _, err := d.Ensure(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret-token") || strings.Contains(string(b), "token-value") {
		t.Fatalf("credential value persisted: %q", b)
	}
}

func TestEnsureUsesMatchingInboxEnvironment(t *testing.T) {
	t.Setenv("KATA_INBOX_USER", "worker/review-bot")
	path := filepath.Join(t.TempDir(), "config.toml")
	k := &fakeKata{
		actor:    "worker",
		projects: []Project{{Name: "example-project", Active: true}},
		probes:   map[string]ProjectProbe{"example-project": {UID: "project", Actor: "worker", CronV1: true}},
		bin:      "/usr/local/bin/kata",
	}
	d := Discoverer{Kata: k, Herdr: fakeHerdr{pong: herdr.Pong{Version: "0.9.5", Protocol: 22}, agents: []herdr.Agent{{Session: &herdr.AgentSession{Agent: "codex"}}}}, Session: "work"}
	if _, err := d.Ensure(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil || len(c.Projects) != 1 || c.Projects[0].Inbox != "worker/review-bot" {
		t.Fatalf("projects = %+v, err = %v", c.Projects, err)
	}
}

func TestEnsureAcceptsMoreThanEightProjects(t *testing.T) {
	projects := make([]Project, 12)
	probes := make(map[string]ProjectProbe, len(projects))
	for i := range projects {
		name := "project-" + string(rune('a'+i))
		projects[i] = Project{Name: name, Active: true}
		probes[name] = ProjectProbe{UID: name + "-uid", Actor: "worker", CronV1: true}
	}
	k := &fakeKata{actor: "worker", projects: projects, probes: probes, bin: "/usr/local/bin/kata"}
	d := Discoverer{Kata: k, Herdr: fakeHerdr{pong: herdr.Pong{Version: "0.9.5", Protocol: 22}, agents: []herdr.Agent{{Session: &herdr.AgentSession{Agent: "codex"}}}}, Session: "work", LookupAgent: func(string) (string, error) { return "/usr/local/bin/codex", nil }}
	path := filepath.Join(t.TempDir(), "config.toml")
	if _, err := d.Ensure(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil || len(c.Projects) != 12 {
		t.Fatalf("projects = %d, %v", len(c.Projects), err)
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestProfilesAreStableAndHandChecked(t *testing.T) {
	want := map[string]config.Profile{"default": {Kind: "codex"}, "codex": {Kind: "codex"}}
	got, err := DetectProfiles([]herdr.Agent{{Session: &herdr.AgentSession{Agent: "codex"}}}, func(string) (string, error) { return "", errors.New("unused") })
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v, err %v", got, want, err)
	}
}

func TestEnsureCredentialFailureNamesProjectOnceAndOffersReplacement(t *testing.T) {
	k := &fakeKata{actor: "worker", projects: []Project{{Name: "example-project", Active: true}}, probeErr: map[string]error{"example-project": fmt.Errorf("%w: project %q", ErrCredentialRequired, "example-project")}}
	d := Discoverer{Kata: k, Herdr: fakeHerdr{agents: []herdr.Agent{{Session: &herdr.AgentSession{Agent: "codex"}}}}, Session: "work"}
	created, err := d.Ensure(t.Context(), filepath.Join(t.TempDir(), "config.toml"))
	if created || err == nil || strings.Count(err.Error(), "example-project") != 1 || !strings.Contains(err.Error(), "replace") {
		t.Fatalf("credential refusal = %t, %v", created, err)
	}
}

func TestEnsureOmitsUnusedCredentialAndDaemonOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	k := &fakeKata{actor: "worker", projects: []Project{{Name: "example-project", Active: true}}, probes: map[string]ProjectProbe{"example-project": {UID: "project", CronV1: true}}, bin: "/usr/local/bin/kata"}
	d := Discoverer{Kata: k, Herdr: fakeHerdr{agents: []herdr.Agent{{Session: &herdr.AgentSession{Agent: "codex"}}}}, Session: "work"}
	if created, err := d.Ensure(t.Context(), path); !created || err != nil {
		t.Fatal(created, err)
	}
	var fields map[string]any
	if _, err := toml.DecodeFile(path, &fields); err != nil {
		t.Fatal(err)
	}
	project := fields["projects"].([]map[string]any)[0]
	for _, name := range []string{"daemon", "credential_env", "credential_file"} {
		if _, found := project[name]; found {
			t.Errorf("unused option %s written: %v", name, project)
		}
	}
}

func TestEnsureActorCredentialFailureOffersReplacement(t *testing.T) {
	k := &fakeKata{whoamiErr: ErrCredentialRequired}
	d := Discoverer{Kata: k, Herdr: fakeHerdr{agents: []herdr.Agent{{Session: &herdr.AgentSession{Agent: "codex"}}}}, Session: "work"}
	created, err := d.Ensure(t.Context(), filepath.Join(t.TempDir(), "config.toml"))
	if created || !errors.Is(err, ErrCredentialRequired) || !strings.Contains(err.Error(), "replace") {
		t.Fatal(created, err)
	}
}

func codexHerdr() fakeHerdr {
	return fakeHerdr{pong: herdr.Pong{Version: "0.9.5", Protocol: 22}, agents: []herdr.Agent{{Session: &herdr.AgentSession{Agent: "codex"}}}}
}

func definitionFixture() *fakeKata {
	return &fakeKata{
		actor:    "worker",
		projects: []Project{{Name: "alpha", Active: true}, {Name: "beta", Active: true}, {Name: "gamma", Active: true}},
		probes: map[string]ProjectProbe{
			"alpha": {UID: "project-alpha", Actor: "worker", CronV1: true},
			"beta":  {UID: "project-beta", Actor: "worker", CronV1: true, Daemon: "route-b"},
			"gamma": {UID: "project-gamma", Actor: "worker", CronV1: true},
		},
		empty:      map[string]bool{"beta": true, "gamma": true},
		bin:        "/usr/local/bin/kata",
		credential: "KATA_AUTH_TOKEN",
	}
}

func projectNames(c *config.Config) string {
	names := []string{}
	for _, p := range c.ProjectEntries() {
		names = append(names, p.Name)
	}
	return strings.Join(names, ",")
}

// uncomment turns every commented TOML line back into configuration, the edit
// the generated file tells the user to make.
func uncomment(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		if rest, ok := strings.CutPrefix(line, "# "); ok && (strings.HasPrefix(rest, "[[projects]]") || strings.Contains(rest, " = ")) {
			lines[i] = rest
		}
	}
	out := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(out, []byte(strings.Join(lines, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEnsureConfiguresOnlyProjectsWithDefinitions(t *testing.T) {
	t.Setenv("KATA_INBOX_USER", "")
	path := filepath.Join(t.TempDir(), "config.toml")
	k := definitionFixture()
	d := Discoverer{Kata: k, Herdr: codexHerdr(), Session: "work"}
	if created, err := d.Ensure(t.Context(), path); !created || err != nil {
		t.Fatalf("ensure = %t, %v", created, err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := projectNames(c); got != "alpha" {
		t.Fatalf("configured projects = %q, want alpha", got)
	}
	if got := strings.Join(k.defsCheck, ","); got != "alpha as worker,beta as worker,gamma as worker" {
		t.Fatalf("definition checks = %q", got)
	}
	// The skipped projects are ready to add: uncommenting yields valid lanes
	// with the probed route values.
	c, err = config.Load(uncomment(t, path))
	if err != nil {
		t.Fatal(err)
	}
	if got := projectNames(c); got != "alpha,beta,gamma" {
		t.Fatalf("uncommented projects = %q", got)
	}
	beta := c.Projects[1]
	if beta.Actor != "worker" || beta.Inbox != "worker/herdr-kata" || beta.Daemon != "route-b" || beta.CredentialEnv != "KATA_AUTH_TOKEN" {
		t.Fatalf("commented project = %+v", beta)
	}
}

func TestEnsureConfiguresNamedProjectWithoutDefinitions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	k := definitionFixture()
	d := Discoverer{Kata: k, Herdr: codexHerdr(), Session: "work", Named: []string{"gamma"}}
	if created, err := d.Ensure(t.Context(), path); !created || err != nil {
		t.Fatalf("ensure = %t, %v", created, err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := projectNames(c); got != "alpha,gamma" {
		t.Fatalf("configured projects = %q, want alpha,gamma", got)
	}
	if got := strings.Join(k.defsCheck, ","); got != "alpha as worker,beta as worker" {
		t.Fatalf("definition checks = %q; a named project needs none", got)
	}
}

func TestEnsureWritesNoLanesWhenNoProjectQualifies(t *testing.T) {
	for name, k := range map[string]*fakeKata{
		"no definitions": {actor: "worker", projects: []Project{{Name: "beta", Active: true}}, probes: map[string]ProjectProbe{"beta": {UID: "project-beta", CronV1: true}}, empty: map[string]bool{"beta": true}, bin: "/usr/local/bin/kata"},
		"no compatible":  {actor: "worker", projects: []Project{{Name: "old", Active: true}}, probes: map[string]ProjectProbe{"old": {UID: "project-old"}}, bin: "/usr/local/bin/kata"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			d := Discoverer{Kata: k, Herdr: codexHerdr(), Session: "work"}
			if created, err := d.Ensure(t.Context(), path); !created || err != nil {
				t.Fatalf("ensure = %t, %v", created, err)
			}
			c, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := projectNames(c); got != "" {
				t.Fatalf("configured projects = %q, want none", got)
			}
			raw, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(raw), "herdr-kata up") {
				t.Fatalf("config does not explain how to add a project: %s, %v", raw, err)
			}
		})
	}
}

func TestEnsureRefusesUnusableNamedProject(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeKata)
	}{
		{"missing", func(*fakeKata) {}},
		{"inactive", func(k *fakeKata) { k.projects = append(k.projects, Project{Name: "missing", Active: false}) }},
		{"unavailable", func(k *fakeKata) {
			k.projects = append(k.projects, Project{Name: "missing", Active: true})
			k.probeErr = map[string]error{"missing": fmt.Errorf("%w: project %q", ErrProjectUnavailable, "missing")}
		}},
		{"incompatible", func(k *fakeKata) {
			k.projects = append(k.projects, Project{Name: "missing", Active: true})
			k.probes["missing"] = ProjectProbe{UID: "project-missing"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			k := definitionFixture()
			tc.setup(k)
			d := Discoverer{Kata: k, Herdr: codexHerdr(), Session: "work", Named: []string{"missing"}}
			created, err := d.Ensure(t.Context(), path)
			if created || err == nil || !strings.Contains(err.Error(), `"missing"`) || !strings.Contains(err.Error(), "--project") {
				t.Fatalf("ensure = %t, %v", created, err)
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("refused named project wrote config: %v", statErr)
			}
		})
	}
}

func TestEnsureDefinitionCheckFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"credential", fmt.Errorf("%w: project %q", ErrCredentialRequired, "alpha"), "replace"},
		{"transient", errors.New("daemon unavailable"), "check definitions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			k := definitionFixture()
			k.defsErr = map[string]error{"alpha": tc.err}
			d := Discoverer{Kata: k, Herdr: codexHerdr(), Session: "work"}
			created, err := d.Ensure(t.Context(), path)
			if created || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ensure = %t, %v", created, err)
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("failed definition check wrote config: %v", statErr)
			}
		})
	}
}

func TestEnsureSkipsProjectLostDuringDefinitionCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	k := definitionFixture()
	k.empty = nil
	k.defsErr = map[string]error{"beta": fmt.Errorf("%w: project %q", ErrProjectUnavailable, "beta")}
	d := Discoverer{Kata: k, Herdr: codexHerdr(), Session: "work"}
	if created, err := d.Ensure(t.Context(), path); !created || err != nil {
		t.Fatalf("ensure = %t, %v", created, err)
	}
	c, err := config.Load(uncomment(t, path))
	if err != nil {
		t.Fatal(err)
	}
	if got := projectNames(c); got != "alpha,gamma" {
		t.Fatalf("projects = %q, want alpha,gamma without the lost project", got)
	}
}
