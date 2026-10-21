package discovery

import (
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandKataRouteIdentityRequiresFederationActor(t *testing.T) {
	bin := kataFixture(t, "instance-a", "project-a", "endpoint-a", "")
	c := &CommandKata{bin: bin, workdir: t.TempDir()}
	_, err := c.RouteIdentity(t.Context(), "other-worker")
	if err == nil || !errors.Is(err, ErrActorMismatch) {
		t.Fatalf("route identity error = %v, want actor mismatch", err)
	}
}

func TestCommandKataRouteIdentityAcceptsLocalRequestActor(t *testing.T) {
	bin := localActorlessFixture(t)
	c := &CommandKata{bin: bin, workdir: t.TempDir()}
	route, err := c.selectedRoute(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	c.route = route
	instance, err := c.RouteIdentity(t.Context(), "worker")
	if err != nil {
		t.Fatal(err)
	}
	if instance != "instance-local" {
		t.Fatalf("instance UID = %q, want local instance", instance)
	}

	c.route.Kind = "remote"
	if _, err := c.RouteIdentity(t.Context(), "worker"); err == nil || !errors.Is(err, ErrActorMismatch) {
		t.Fatalf("remote actorless route error = %v, want actor mismatch", err)
	}
}

func TestCommandKataWhoAmIUsesAuthenticatedActorForImplicitSelection(t *testing.T) {
	bin := whoamiFixture(t, "local-worker", "user", "remote-worker")
	c := &CommandKata{bin: bin, workdir: t.TempDir()}
	actor, err := c.WhoAmI(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if actor != "remote-worker" {
		t.Fatalf("actor = %q, want authenticated actor", actor)
	}
	if _, err := c.RouteIdentity(t.Context(), actor); err != nil {
		t.Fatalf("route identity for authenticated actor: %v", err)
	}
}

func TestCommandKataWhoAmIHonorsExplicitAuthorAssertion(t *testing.T) {
	t.Setenv("KATA_AUTHOR", "local-worker")
	bin := whoamiFixture(t, "local-worker", "env", "remote-worker")
	c := &CommandKata{bin: bin, workdir: t.TempDir()}
	actor, err := c.WhoAmI(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if actor != "local-worker" {
		t.Fatalf("actor = %q, want explicit author", actor)
	}
	if _, err := c.RouteIdentity(t.Context(), actor); err == nil || !errors.Is(err, ErrActorMismatch) {
		t.Fatalf("route identity error = %v, want actor mismatch", err)
	}
}

func TestCommandKataProbeRejectsWorkspaceNeutralRouteMismatch(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bin := kataFixture(t, "instance-a", "project-a", "endpoint-a", workspace)
	c := &CommandKata{bin: bin, workdir: workspace}
	_, err = c.ProbeProject(t.Context(), "example-project", "worker")
	if err == nil || !errors.Is(err, ErrRouteMismatch) || !strings.Contains(err.Error(), "different Kata identity") {
		t.Fatalf("probe error = %v, want explicit route mismatch", err)
	}
}

func TestSelectedCredentialEnvUsesNamedDaemonMetadata(t *testing.T) {
	t.Setenv("KATA_DAEMON", "")
	t.Setenv("KATA_SERVER", "")
	t.Setenv("KATA_AUTH_TOKEN", "")
	t.Setenv("KATA_TEAM_TOKEN", "selected-secret")
	got := selectedCredentialEnv(catalogConfig{ActiveDaemon: "team-hub", Daemons: []catalogDaemon{{Name: "team-hub", TokenEnv: "KATA_TEAM_TOKEN"}}})
	if got != "KATA_TEAM_TOKEN" {
		t.Fatalf("credential source = %q, want named daemon source", got)
	}
	if strings.Contains(got, "selected-secret") || strings.Contains(got, "wrong-fallback") {
		t.Fatal("credential value leaked into source name")
	}
}

func TestSelectedCredentialEnvGlobalOverrideWinsForActiveRoute(t *testing.T) {
	t.Setenv("KATA_DAEMON", "")
	t.Setenv("KATA_SERVER", "")
	t.Setenv("KATA_AUTH_TOKEN", "generic-secret")
	if got := selectedCredentialEnv(catalogConfig{ActiveDaemon: "unknown-hub", Daemons: []catalogDaemon{{Name: "team-hub", TokenEnv: "KATA_TEAM_TOKEN"}}}); got != "KATA_AUTH_TOKEN" {
		t.Fatalf("credential source = %q, want global override source", got)
	}
}

func TestSelectedCredentialEnvUsesGlobalAuthTokenEnvForServerRoute(t *testing.T) {
	t.Setenv("KATA_SERVER", "https://hub.example")
	t.Setenv("KATA_AUTH_TOKEN", "")
	got := selectedCredentialEnv(catalogConfig{
		ActiveDaemon: "team-hub",
		Auth:         catalogAuth{TokenEnv: "EXAMPLE_GLOBAL_TOKEN"},
		Daemons:      []catalogDaemon{{Name: "team-hub", TokenEnv: "KATA_TEAM_TOKEN"}},
	})
	if got != "EXAMPLE_GLOBAL_TOKEN" {
		t.Fatalf("credential source = %q, want global auth source", got)
	}
}

func TestSelectedCredentialSourceDoesNotUseAmbientAuthTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	t.Setenv("KATA_SERVER", "https://hub.example")
	t.Setenv("KATA_AUTH_TOKEN", "")
	t.Setenv("KATA_AUTH_TOKEN_FILE", path)
	source, err := selectedCredentialSource(catalogConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if source.Env != "" || source.File != "" {
		t.Fatalf("credential source = %+v, want no ambient file override", source)
	}
}

func TestSelectedCredentialSourceRejectsInlineAuth(t *testing.T) {
	t.Setenv("KATA_SERVER", "https://hub.example")
	t.Setenv("KATA_AUTH_TOKEN", "")
	t.Setenv("KATA_AUTH_TOKEN_FILE", "")
	if _, err := selectedCredentialSource(catalogConfig{Auth: catalogAuth{Token: "inline-secret"}}); err == nil || !strings.Contains(err.Error(), "inline credential") {
		t.Fatalf("credential source error = %v, want explicit inline credential failure", err)
	}
}

func TestSelectedCredentialSourceRefusesOpaqueWorkspaceSelector(t *testing.T) {
	t.Setenv("KATA_SERVER", "")
	t.Setenv("KATA_AUTH_TOKEN", "")
	_, err := selectedCredentialSourceForRoute(catalogConfig{
		ActiveDaemon: "active-hub",
		Daemons:      []catalogDaemon{{Name: "active-hub", TokenEnv: "ACTIVE_TOKEN"}},
	}, routeSelection{Source: ".kata.local.toml", Kind: "remote"})
	if err == nil || !strings.Contains(err.Error(), "workspace-local selector") {
		t.Fatalf("credential source error = %v, want explicit workspace selector refusal", err)
	}
}

func TestSelectedCredentialSourceUsesDiagnosedActiveDaemon(t *testing.T) {
	t.Setenv("KATA_SERVER", "")
	t.Setenv("KATA_AUTH_TOKEN", "")
	t.Setenv("KATA_TEAM_TOKEN", "selected-secret")
	source, err := selectedCredentialSourceForRoute(catalogConfig{
		ActiveDaemon: "team-hub",
		Daemons:      []catalogDaemon{{Name: "team-hub", TokenEnv: "KATA_TEAM_TOKEN"}},
	}, routeSelection{Source: "active_daemon", Kind: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	if source.Env != "KATA_TEAM_TOKEN" {
		t.Fatalf("credential source = %+v, want diagnosed active daemon source", source)
	}
}

func TestCommandKataCommandEnvInjectsFileAsAuthToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("file-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KATA_AUTH_TOKEN", "")
	t.Setenv("KATA_AUTH_TOKEN_FILE", "")
	c := &CommandKata{credentialEnv: "KATA_AUTH_TOKEN", credentialFile: path}
	env, err := c.commandEnv()
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, pair := range env {
		if key, value, ok := strings.Cut(pair, "="); ok && key == "KATA_AUTH_TOKEN" {
			got = value
		}
		if strings.HasPrefix(pair, "KATA_AUTH_TOKEN_FILE=") {
			t.Fatal("file source leaked as a pathname environment variable")
		}
	}
	if got != "file-secret" {
		t.Fatalf("injected token = %q, want file content", got)
	}
}

func TestCurrentRouteEnvRejectsDiscardedServerParts(t *testing.T) {
	t.Setenv("KATA_SERVER", "https://hub.example/ignored?token=fixture-secret#fragment")
	_, err := currentRouteEnv()
	if err == nil || !strings.Contains(err.Error(), "server origin") {
		t.Fatalf("route environment error = %v, want origin validation", err)
	}
	if strings.Contains(err.Error(), "fixture-secret") || strings.Contains(err.Error(), "ignored") {
		t.Fatalf("route environment error leaked URL data: %v", err)
	}
}

func TestCurrentRouteEnvCanonicalizesRootServerPath(t *testing.T) {
	t.Setenv("KATA_SERVER", "HTTPS://Hub.Example/")
	env, err := currentRouteEnv()
	if err != nil {
		t.Fatal(err)
	}
	if got := env["KATA_SERVER"]; got != "https://Hub.Example" {
		t.Fatalf("canonical server = %q", got)
	}
}

// kataFixture emits enough of the read-only route contract for
// kata.ResolveIdentity. The workspace directory deliberately resolves to one
// route while a neutral directory resolves to another.
func kataFixture(t *testing.T, instance, project, endpoint, workspace string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kata")
	workspaceLiteral := shellQuote(workspace)
	projectJSON := fmt.Sprintf("%q", project)
	content := fmt.Sprintf(`#!/bin/sh
if [ "$PWD" = %s ]; then
  instance=%s
  endpoint=%s
else
  instance=instance-neutral
  endpoint=endpoint-neutral
fi
case "$*" in
  *" health") printf '%%s\n' '{"schema_version":33,"version":"test"}' ;;
  *" cron capabilities") printf '%%s\n' '{"project_uid":%s,"event_features":["cron_v1"]}' ;;
  *" federation identity") printf '%%s\n' "{\"instance_uid\":\"$instance\",\"auth\":{\"actor\":\"worker\"}}" ;;
  *" daemon diagnose "*) printf '%%s\n' '{"state":"ready","endpoint":"'"$endpoint"'","project_uid":%s}' ;;
  *) exit 99 ;;
esac
`, workspaceLiteral, shellQuote(instance), shellQuote(endpoint), projectJSON, projectJSON)
	if err := os.WriteFile(path, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func whoamiFixture(t *testing.T, actor, source, authenticatedActor string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kata")
	content := fmt.Sprintf(`#!/bin/sh
case "$*" in
  *" whoami") printf '%%s\n' '{"actor":%q,"source":%q}' ;;
  *" federation identity") printf '%%s\n' '{"instance_uid":"instance","auth":{"actor":%q}}' ;;
  *) exit 99 ;;
esac
`, actor, source, authenticatedActor)
	if err := os.WriteFile(path, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func localActorlessFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kata")
	content := `#!/bin/sh
case "$*" in
  *" daemon diagnose"*) printf '%s\n' '{"state":"ready","source":"local runtime","kind":"local","endpoint":"unix:///tmp/kata.sock","actor_policy":"request"}' ;;
  *" federation identity"*) printf '%s\n' '{"instance_uid":"instance-local","auth":{"kind":"none","token_audit_read":true}}' ;;
  *) exit 99 ;;
esac
`
	if err := os.WriteFile(path, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// A successful identity request can auto-start Kata after the constructor's
// diagnosis. Discovery must use the refreshed local actor policy.
func TestCommandKataStoppedLocalRouteRefreshesAfterIdentity(t *testing.T) {
	for _, state := range []string{"ready", "local_stopped"} {
		t.Run(state, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "kata")
			body := fmt.Sprintf(`#!/bin/sh
case "$*" in
 *" federation identity") printf '%%s\n' '{"instance_uid":"instance-local","auth":{"kind":"none"}}' ;;
 *" daemon diagnose") printf '%%s\n' '{"state":%q,"source":"local runtime","kind":"local","endpoint":"unix:///tmp/kata.sock","actor_policy":"request"}' ;;
 *) exit 99 ;;
esac
`, state)
			if err := os.WriteFile(bin, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			c := &CommandKata{bin: bin, workdir: t.TempDir(), route: routeSelection{State: "local_stopped", Source: "local runtime", Kind: "local", Endpoint: "unix:///tmp/kata.sock"}}
			instance, err := c.RouteIdentity(t.Context(), "worker")
			if state == "ready" {
				if err != nil || instance != "instance-local" {
					t.Fatalf("auto-start identity = %q, %v", instance, err)
				}
			} else if err == nil || errors.Is(err, ErrActorMismatch) || !strings.Contains(err.Error(), "local_stopped") {
				t.Fatalf("not-ready route should name its state, got %v", err)
			}
		})
	}
}

func TestCommandKataRejectedTokenIsCredentialFailure(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "kata")
	body := `#!/bin/sh
case "$*" in
 *" federation identity") printf '%s\n' '{"error":{"kind":"token_invalid","message":"token invalid"}}'; exit 1 ;;
 *) exit 99 ;;
esac
`
	if err := os.WriteFile(bin, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	c := &CommandKata{bin: bin, workdir: t.TempDir()}
	if _, err := c.RouteIdentity(t.Context(), "worker"); !errors.Is(err, ErrCredentialRequired) {
		t.Fatal(err)
	}
}

func TestCommandKataWhoAmICredentialFailureOffersReplacement(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "kata")
	body := `#!/bin/sh
case "$*" in
 *" whoami") printf '%s\n' '{"error":{"kind":"token_invalid","message":"token invalid"}}'; exit 1 ;;
 *) exit 99 ;;
esac
`
	if err := os.WriteFile(bin, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	c := &CommandKata{bin: bin, workdir: t.TempDir()}
	d := Discoverer{Kata: c, Herdr: fakeHerdr{agents: []herdr.Agent{{Session: &herdr.AgentSession{Agent: "codex"}}}}, Session: "work"}
	created, err := d.Ensure(t.Context(), filepath.Join(t.TempDir(), "config.toml"))
	if created || !errors.Is(err, ErrCredentialRequired) || !strings.Contains(err.Error(), "replace") {
		t.Fatal(created, err)
	}
}

// definitionsFixture answers the two definition lists and logs each call.
func definitionsFixture(t *testing.T, jobs, workflows string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path, calls := filepath.Join(dir, "kata"), filepath.Join(dir, "calls")
	content := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %s
case "$*" in
  *" cron job list") printf '%%s\n' '{"jobs":%s}' ;;
  *" cron workflow list") printf '%%s\n' '{"workflows":%s}' ;;
  *) exit 99 ;;
esac
`, shellQuote(calls), jobs, workflows)
	if err := os.WriteFile(path, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	return path, calls
}

func TestCommandKataHasDefinitions(t *testing.T) {
	for _, tc := range []struct {
		name, jobs, workflows string
		want                  bool
		lists                 []string
	}{
		{"job", `[{"uid":"job-a"}]`, `[]`, true, []string{"cron job list"}},
		{"workflow only", `[]`, `[{"uid":"flow-a"}]`, true, []string{"cron job list", "cron workflow list"}},
		{"none", `[]`, `[]`, false, []string{"cron job list", "cron workflow list"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, calls := definitionsFixture(t, tc.jobs, tc.workflows)
			c := &CommandKata{bin: bin, workdir: t.TempDir()}
			got, err := c.HasDefinitions(t.Context(), "example-project", "worker")
			if err != nil || got != tc.want {
				t.Fatalf("has definitions = %t, %v; want %t", got, err, tc.want)
			}
			raw, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(lines) != len(tc.lists) {
				t.Fatalf("calls = %q, want %q", lines, tc.lists)
			}
			for i, line := range lines {
				if !strings.HasSuffix(line, " "+tc.lists[i]) || !strings.Contains(line, "example-project") || !strings.Contains(line, "worker") {
					t.Fatalf("call %d = %q, want %q for example-project as worker", i, line, tc.lists[i])
				}
			}
		})
	}
}

func TestCommandKataHasDefinitionsClassifiesLostProject(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "kata")
	body := `#!/bin/sh
printf '%s\n' '{"error":{"kind":"project_not_found","message":"project not found"}}'; exit 1
`
	if err := os.WriteFile(bin, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	c := &CommandKata{bin: bin, workdir: t.TempDir()}
	if _, err := c.HasDefinitions(t.Context(), "example-project", "worker"); !errors.Is(err, ErrProjectUnavailable) {
		t.Fatalf("lost project error = %v, want unavailable", err)
	}
}
