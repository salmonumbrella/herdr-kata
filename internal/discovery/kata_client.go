package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"go.kenn.io/kit/secretref"
)

// CommandKata uses the already selected Kata CLI route for discovery. Raw
// inventory calls inherit the caller's environment selectors and working
// directory; project probes compare that route with config.Kata's clean child
// environment from a neutral directory before admitting it to the generated
// file.
type CommandKata struct {
	bin            string
	env            map[string]string
	daemon         string
	credentialEnv  string
	credentialFile string
	route          routeSelection
	workdir        string
}

func NewCommandKata(contexts ...context.Context) (*CommandKata, error) {
	bin, err := exec.LookPath("kata")
	if err != nil {
		return nil, fmt.Errorf("automatic discovery: kata executable is unavailable: %w", err)
	}
	bin, err = filepath.Abs(bin)
	if err != nil {
		return nil, fmt.Errorf("automatic discovery: resolve kata executable: %w", err)
	}
	workdir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("automatic discovery: resolve current working directory: %w", err)
	}
	ctx := context.Background()
	if len(contexts) > 0 && contexts[0] != nil {
		ctx = contexts[0]
	}
	catalog := catalogDaemonConfig()
	c := &CommandKata{bin: bin, workdir: workdir}
	// Do not translate KATA_AUTH_TOKEN_FILE into KATA_AUTH_TOKEN before route
	// diagnosis. Kata may give a catalog daemon token_env precedence over that
	// ambient file, and replacing it would make the inventory probe use a
	// different credential than the user's raw Kata route. Manual config can
	// still use credential_file; first-start discovery asks for the explicit
	// KATA_AUTH_TOKEN override when the route must be captured automatically.
	if strings.TrimSpace(os.Getenv("KATA_AUTH_TOKEN")) == "" && strings.TrimSpace(os.Getenv("KATA_AUTH_TOKEN_FILE")) != "" {
		return nil, errors.New("automatic discovery: KATA_AUTH_TOKEN_FILE is ambiguous with Kata catalog route credentials; set KATA_AUTH_TOKEN or use explicit config with credential_file")
	}
	// Ask Kata which selector won instead of walking .kata.local.toml or
	// reproducing its precedence rules. The source is metadata only; secrets
	// remain in the caller's environment or a private file.
	// Route diagnosis happens before Discoverer.Ensure can install its shared
	// discovery deadline, so bound this constructor probe independently.
	probeCtx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	route, err := c.selectedRoute(probeCtx)
	if err != nil {
		return nil, err
	}
	c.route = route
	credential, err := selectedCredentialSourceForRoute(catalog, route)
	if err != nil {
		return nil, err
	}
	routeEnv, err := currentRouteEnv()
	if err != nil {
		return nil, err
	}
	// KATA_DAEMON is not a Kata route selector. The selected route comes from
	// the caller's workspace and the route environment; passing it as a probe
	// flag could make the sanitized child use a different daemon than inventory.
	c.env = routeEnv
	c.credentialEnv = credential.Env
	c.credentialFile = credential.File
	return c, nil
}

type catalogDaemon struct {
	Name     string `toml:"name"`
	Local    bool   `toml:"local"`
	URL      string `toml:"url"`
	Token    string `toml:"token"`
	TokenEnv string `toml:"token_env"`
}

type catalogConfig struct {
	ActiveDaemon string          `toml:"active_daemon"`
	Daemons      []catalogDaemon `toml:"daemon"`
	Auth         catalogAuth     `toml:"auth"`
}

type catalogAuth struct {
	Token     string `toml:"token"`
	TokenFile string `toml:"token_file"`
	TokenEnv  string `toml:"token_env"`
}

type credentialSource struct {
	Env  string
	File string
}

type routeSelection struct {
	Source      string `json:"source"`
	Kind        string `json:"kind"`
	Profile     string `json:"profile"`
	State       string `json:"state"`
	Endpoint    string `json:"endpoint"`
	ActorPolicy string `json:"actor_policy"`
}

func catalogDaemonConfig() catalogConfig {
	path := filepath.Join(kataHome(), "config.toml")
	var parsed catalogConfig
	if _, err := toml.DecodeFile(path, &parsed); err != nil {
		return catalogConfig{}
	}
	return parsed
}

func kataHome() string {
	if home := strings.TrimSpace(os.Getenv("KATA_HOME")); home != "" {
		return home
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".kata")
}

func selectedCredentialEnv(catalog catalogConfig) string {
	source, _ := selectedCredentialSource(catalog)
	return source.Env
}

func selectedCredentialSource(catalog catalogConfig) (credentialSource, error) {
	source := "local runtime"
	if strings.TrimSpace(os.Getenv("KATA_SERVER")) != "" {
		source = "KATA_SERVER"
	} else if strings.TrimSpace(catalog.ActiveDaemon) != "" {
		source = "active_daemon"
	}
	return selectedCredentialSourceForRoute(catalog, routeSelection{Source: source})
}

func selectedCredentialSourceForRoute(catalog catalogConfig, route routeSelection) (credentialSource, error) {
	// Kata's global environment override wins over catalog credentials for
	// remote routes. Keep only its source name in the generated config.
	if source, ok, err := authTokenOverrideSource(); ok {
		return source, err
	}
	source := strings.ToLower(strings.TrimSpace(route.Source))
	switch source {
	case "kata_server", "server_env", "configured":
		return globalCredentialSource(catalog.Auth)
	case "active_daemon":
		selectedDaemon := strings.TrimSpace(catalog.ActiveDaemon)
		if selectedDaemon == "" {
			return globalCredentialSource(catalog.Auth)
		}
		return credentialSourceForDaemon(catalog, selectedDaemon)
	case "local runtime", "local_default", "local":
		return globalCredentialSource(catalog.Auth)
	case ".kata.local.toml", "local_config":
		if strings.TrimSpace(route.Profile) != "" {
			return credentialSourceForDaemon(catalog, route.Profile)
		}
		// Kata does not expose the selected [server].daemon name in its
		// diagnosis. Persisting the active daemon here could silently send a
		// generated config to a different route, so admit only an explicit
		// global credential and refuse the opaque workspace selector.
		credential, err := globalCredentialSource(catalog.Auth)
		if err != nil {
			return credentialSource{}, err
		}
		if credential.Env != "" || credential.File != "" {
			return credential, nil
		}
		return credentialSource{}, errors.New("automatic discovery: selected Kata route uses a workspace-local selector; set KATA_AUTH_TOKEN or use a supported global credential before first start")
	case "daemon_flag":
		return credentialSource{}, errors.New("automatic discovery: selected Kata route uses an explicit daemon selector; automatic setup cannot preserve that selector")
	case "":
		return credentialSource{}, errors.New("automatic discovery: Kata route diagnosis returned no source")
	default:
		return credentialSource{}, fmt.Errorf("automatic discovery: unsupported Kata route source %q", strings.TrimSpace(route.Source))
	}
}

func authTokenOverrideSource() (credentialSource, bool, error) {
	if value, ok := os.LookupEnv("KATA_AUTH_TOKEN"); ok && strings.TrimSpace(value) != "" {
		return credentialSource{Env: "KATA_AUTH_TOKEN"}, true, nil
	}
	return credentialSource{}, false, nil
}

func credentialSourceForDaemon(catalog catalogConfig, name string) (credentialSource, error) {
	for _, daemon := range catalog.Daemons {
		if name != daemon.Name {
			continue
		}
		if config.ChildVariable(daemon.TokenEnv) {
			return credentialSource{Env: strings.TrimSpace(daemon.TokenEnv)}, nil
		}
		if strings.TrimSpace(daemon.Token) != "" {
			return credentialSource{}, errors.New("automatic discovery: selected Kata daemon uses an inline credential; configure token_env or KATA_AUTH_TOKEN before first start")
		}
		if daemon.Local {
			return globalCredentialSource(catalog.Auth)
		}
		return credentialSource{}, nil
	}
	return credentialSource{}, nil
}

func globalCredentialSource(auth catalogAuth) (credentialSource, error) {
	if source, ok, err := authTokenOverrideSource(); ok {
		return source, err
	}
	if strings.TrimSpace(auth.Token) != "" {
		return credentialSource{}, errors.New("automatic discovery: selected Kata auth uses an inline credential; configure token_env, token_file or KATA_AUTH_TOKEN before first start")
	}
	if path := strings.TrimSpace(auth.TokenFile); path != "" {
		file, err := normalizeCredentialFile(path)
		if err != nil {
			return credentialSource{}, err
		}
		return credentialSource{Env: "KATA_AUTH_TOKEN", File: file}, nil
	}
	if config.ChildVariable(auth.TokenEnv) {
		return credentialSource{Env: strings.TrimSpace(auth.TokenEnv)}, nil
	}
	return credentialSource{}, nil
}

func normalizeCredentialFile(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("automatic discovery: selected Kata credential file home is unavailable")
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("automatic discovery: selected Kata credential file must be absolute or use ~/path")
	}
	return path, nil
}

func (c *CommandKata) selectedRoute(ctx context.Context) (routeSelection, error) {
	var result routeSelection
	if err := c.call(ctx, &result, "daemon", "diagnose"); err != nil {
		return routeSelection{}, fmt.Errorf("automatic discovery: inspect selected Kata route: %w", err)
	}
	result.Source = strings.TrimSpace(result.Source)
	result.Kind = strings.TrimSpace(result.Kind)
	result.Profile = strings.TrimSpace(result.Profile)
	if result.Source == "" {
		return routeSelection{}, errors.New("automatic discovery: Kata route diagnosis returned no source")
	}
	return result, nil
}

func (c *CommandKata) Bin() string                 { return c.bin }
func (c *CommandKata) RouteEnv() map[string]string { return cloneMap(c.env) }
func (c *CommandKata) CredentialEnv() string       { return c.credentialEnv }

func (c *CommandKata) RouteIdentity(ctx context.Context, expectedActor string) (string, error) {
	expectedActor = strings.TrimSpace(expectedActor)
	identity, err := c.routeIdentity(ctx, expectedActor)
	if err != nil {
		return "", err
	}
	if identity.Actor != expectedActor {
		return "", fmt.Errorf("%w: federation identity reported actor %q, expected %q", ErrActorMismatch, identity.Actor, expectedActor)
	}
	return identity.InstanceUID, nil
}

type routeIdentity struct {
	InstanceUID string
	Actor       string
}

func (c *CommandKata) routeIdentity(ctx context.Context, expectedActor string) (routeIdentity, error) {
	expectedActor = strings.TrimSpace(expectedActor)
	if expectedActor == "" {
		return routeIdentity{}, fmt.Errorf("%w: expected actor is empty", ErrActorMismatch)
	}
	var result struct {
		InstanceUID string `json:"instance_uid"`
		Auth        struct {
			Actor string `json:"actor"`
			Kind  string `json:"kind"`
		} `json:"auth"`
	}
	if err := c.callAs(ctx, &result, expectedActor, "federation", "identity"); err != nil {
		if credentialFailure(err) {
			return routeIdentity{}, fmt.Errorf("%w: selected Kata route authentication failed", ErrCredentialRequired)
		}
		return routeIdentity{}, err
	}
	actor := strings.TrimSpace(result.Auth.Actor)
	if actor == "" && (c.route.Kind == "local" || c.route.Kind == "local_profile") && c.route.State != "ready" {
		// The identity call may have auto-started the selected local daemon.
		// Refresh its policy without accepting a changed route.
		refreshed, err := c.selectedRoute(ctx)
		if err != nil {
			return routeIdentity{}, err
		}
		if refreshed.Source != c.route.Source || refreshed.Kind != c.route.Kind || refreshed.Profile != c.route.Profile || (c.route.Endpoint != "" && refreshed.Endpoint != c.route.Endpoint) {
			return routeIdentity{}, fmt.Errorf("%w: selected local route changed after startup", ErrRouteMismatch)
		}
		c.route = refreshed
		if refreshed.State != "ready" {
			return routeIdentity{}, fmt.Errorf("selected Kata route is not ready (%s); run kata daemon diagnose --json on that route", refreshed.State)
		}
	}
	if actor == "" {
		if !c.acceptsLocalRequestActor(result.Auth.Kind) {
			return routeIdentity{}, fmt.Errorf("%w: federation identity did not report an authenticated actor", ErrActorMismatch)
		}
		// A local Kata daemon with request actor policy has no remote auth
		// actor to echo. The actor requested on the command line is the
		// identity that the local route will use; remote routes must still
		// report and match an authenticated actor above.
		actor = expectedActor
	}
	if strings.TrimSpace(result.InstanceUID) == "" {
		return routeIdentity{}, errors.New("federation identity returned no instance UID")
	}
	return routeIdentity{InstanceUID: strings.TrimSpace(result.InstanceUID), Actor: actor}, nil
}

func (c *CommandKata) acceptsLocalRequestActor(authKind string) bool {
	if !strings.EqualFold(strings.TrimSpace(authKind), "none") || !strings.EqualFold(strings.TrimSpace(c.route.ActorPolicy), "request") {
		return false
	}
	kind := strings.ToLower(strings.TrimSpace(c.route.Kind))
	if kind != "local" && kind != "local_profile" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(c.route.State), "ready") {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.route.Endpoint)), "unix://")
}

func (c *CommandKata) WhoAmI(ctx context.Context) (string, error) {
	var result struct {
		Actor  string `json:"actor"`
		Source string `json:"source"`
	}
	if err := c.call(ctx, &result, "whoami"); err != nil {
		if credentialFailure(err) {
			return "", fmt.Errorf("%w: selected Kata route authentication failed", ErrCredentialRequired)
		}
		return "", err
	}
	actor := strings.TrimSpace(result.Actor)
	if actor == "" {
		return "", errors.New("kata whoami returned no actor")
	}
	// USER and git are local defaults and can differ from the actor carried by
	// the selected daemon's authenticated federation identity. Resolve that
	// actor before it is persisted into each discovered project. KATA_AUTHOR
	// is an explicit assertion and remains subject to the mismatch check in
	// RouteIdentity.
	if !explicitActorAssertion(result.Source) {
		identity, err := c.routeIdentity(ctx, actor)
		if err != nil {
			return "", err
		}
		return identity.Actor, nil
	}
	return actor, nil
}

func explicitActorAssertion(source string) bool {
	return strings.EqualFold(strings.TrimSpace(source), "env") && strings.TrimSpace(os.Getenv("KATA_AUTHOR")) != ""
}

func (c *CommandKata) ListProjects(ctx context.Context, actor string) ([]Project, error) {
	var result struct {
		Projects []struct {
			UID    string `json:"uid"`
			Name   string `json:"name"`
			Active bool   `json:"active"`
		} `json:"projects"`
	}
	if err := c.callAs(ctx, &result, actor, "projects", "list"); err != nil {
		return nil, err
	}
	projects := make([]Project, 0, len(result.Projects))
	for _, p := range result.Projects {
		projects = append(projects, Project{UID: p.UID, Name: p.Name, Active: p.Active})
	}
	return projects, nil
}

func (c *CommandKata) ProbeProject(ctx context.Context, name, actor string) (ProjectProbe, error) {
	if strings.TrimSpace(c.workdir) == "" {
		return ProjectProbe{}, errors.New("automatic discovery: current working directory is unavailable")
	}
	view := config.Kata{Bin: c.bin, Project: name, Actor: actor, Daemon: c.daemon, Env: cloneMap(c.env), CredentialEnv: c.credentialEnv, CredentialFile: c.credentialFile}
	workspaceCLI := kata.New(view, c.workdir, nil, nil)
	workspaceIdentity, workspaceRoute, err := kata.ResolveIdentity(ctx, workspaceCLI)
	if err != nil {
		if credentialFailure(err) {
			return ProjectProbe{}, fmt.Errorf("%w: project %q", ErrCredentialRequired, name)
		}
		return ProjectProbe{}, classifyProjectProbeError(err, name)
	}
	if workspaceIdentity.Actor != actor {
		return ProjectProbe{}, fmt.Errorf("%w: project %q authenticated actor %q, expected %q", ErrActorMismatch, name, workspaceIdentity.Actor, actor)
	}

	// The config written by discovery cannot encode a workspace-local Kata
	// selector. Resolve the same project from a neutral directory and require
	// that it reaches the exact route selected by the inventory command. A
	// mismatch is explicit so first start never silently switches hubs or
	// projects when the generated config is later read from another directory.
	neutralCLI := kata.New(view, os.TempDir(), nil, nil)
	neutralIdentity, neutralRoute, err := kata.ResolveIdentity(ctx, neutralCLI)
	if err != nil {
		return ProjectProbe{}, fmt.Errorf("%w: project %q neutral route probe failed: %v", ErrRouteMismatch, name, err)
	}
	if neutralIdentity.Actor != actor {
		return ProjectProbe{}, fmt.Errorf("%w: project %q neutral route authenticated actor %q, expected %q", ErrRouteMismatch, name, neutralIdentity.Actor, actor)
	}
	if workspaceIdentity.InstanceUID != neutralIdentity.InstanceUID || workspaceIdentity.ProjectUID != neutralIdentity.ProjectUID {
		return ProjectProbe{}, fmt.Errorf("%w: project %q workspace and neutral probes resolved different Kata identity", ErrRouteMismatch, name)
	}
	if workspaceRoute.Daemon != neutralRoute.Daemon || workspaceRoute.Endpoint != neutralRoute.Endpoint || workspaceRoute.DiagnoseState != neutralRoute.DiagnoseState {
		return ProjectProbe{}, fmt.Errorf("%w: project %q workspace and neutral probes reported different daemon diagnosis", ErrRouteMismatch, name)
	}
	return ProjectProbe{UID: neutralIdentity.ProjectUID, InstanceUID: neutralIdentity.InstanceUID, Actor: neutralIdentity.Actor, Daemon: neutralRoute.Daemon, CredentialEnv: c.credentialEnv, CredentialFile: c.credentialFile, CronV1: slices.Contains(neutralIdentity.Features, "cron_v1")}, nil
}

func (c *CommandKata) call(ctx context.Context, result any, args ...string) error {
	return c.callAs(ctx, result, "", args...)
}

func (c *CommandKata) callAs(ctx context.Context, result any, actor string, args ...string) error {
	argv := []string{c.bin, "--json"}
	if strings.TrimSpace(actor) != "" {
		argv = append(argv, "--as", actor)
	}
	argv = append(argv, args...)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// The inventory route is the user's current CLI route. No value from
	// this environment is copied to the generated config except the
	// whitelisted selectors returned by currentRouteEnv.
	env, err := c.commandEnv()
	if err != nil {
		return err
	}
	cmd.Env = env
	// Inventory must use the caller's workspace route. ProbeProject compares it
	// with the neutral route that the generated config can actually express.
	cmd.Dir = c.workdir
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &commandError{operation: args[0], detail: commandErrorDetail(out, err)}
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(out, result); err != nil {
		return fmt.Errorf("kata discovery response: %w", err)
	}
	return nil
}

func (c *CommandKata) commandEnv() ([]string, error) {
	env := os.Environ()
	if strings.TrimSpace(c.credentialFile) == "" {
		return env, nil
	}
	if strings.TrimSpace(c.credentialEnv) == "" {
		return nil, errors.New("automatic discovery: credential_file requires credential_env")
	}
	secret, err := (secretref.Ref{File: c.credentialFile}).Resolve()
	if err != nil {
		return nil, fmt.Errorf("automatic discovery: credential_file: %w", err)
	}
	filtered := env[:0]
	prefix := c.credentialEnv + "="
	for _, pair := range env {
		key, _, _ := strings.Cut(pair, "=")
		if key == "KATA_AUTH_TOKEN_FILE" || strings.HasPrefix(pair, prefix) {
			continue
		}
		filtered = append(filtered, pair)
	}
	return append(filtered, prefix+secret.Value), nil
}

// commandError keeps command diagnostics available for classification without
// echoing arbitrary Kata stderr (which can contain paths or other private
// details) into the first-start error. The caller gets a stable operation-only
// message, while credential and project classifiers can inspect detail.
type commandError struct {
	operation string
	detail    string
}

func (e *commandError) Error() string {
	return fmt.Sprintf("kata discovery command %s failed", e.operation)
}

func commandErrorDetail(stdout []byte, err error) string {
	parts := []string{}
	if len(stdout) > 0 {
		parts = append(parts, string(stdout))
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		parts = append(parts, string(exitErr.Stderr))
	}
	if len(parts) == 0 {
		parts = append(parts, err.Error())
	}
	return strings.ToLower(strings.Join(parts, "\n"))
}

func errorDetail(err error) string {
	if commandErr, ok := errors.AsType[*commandError](err); ok {
		return commandErr.detail + "\n" + strings.ToLower(err.Error())
	}
	return strings.ToLower(err.Error())
}

func credentialFailure(err error) bool {
	if kataErr, ok := errors.AsType[*kata.Error](err); ok {
		code := strings.ToLower(kataErr.Code)
		message := strings.ToLower(kataErr.Message)
		for _, text := range []string{"auth", "credential", "token", "forbidden", "unauthor"} {
			if strings.Contains(code, text) || strings.Contains(message, text) {
				return true
			}
		}
	}
	text := errorDetail(err)
	for _, word := range []string{"credential_env", "credential_file", "credential", "authentication", "unauthorized", "forbidden", "token required", "invalid token", "token_invalid", "token invalid"} {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

func classifyProjectProbeError(err error, name string) error {
	if credentialFailure(err) {
		return fmt.Errorf("%w: project %q", ErrCredentialRequired, name)
	}
	if unsupportedProjectError(err) || inaccessibleProjectError(err) {
		return fmt.Errorf("%w: project %q", ErrProjectUnavailable, name)
	}
	return err
}

func unsupportedProjectError(err error) bool {
	return strings.Contains(errorDetail(err), "cron_v1 and project uid required")
}

func inaccessibleProjectError(err error) bool {
	if kataErr, ok := errors.AsType[*kata.Error](err); ok {
		switch strings.ToLower(kataErr.Code) {
		case "not_found", "project_not_found", "access_lost", "access_denied", "project_access_denied", "permission_denied", "restricted_project":
			return true
		}
	}
	text := errorDetail(err)
	for _, phrase := range []string{"project not found", "project unavailable"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func currentRouteEnv() (map[string]string, error) {
	out := map[string]string{}
	for _, key := range []string{"KATA_HOME", "KATA_SERVER", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			if key == "KATA_SERVER" {
				canonical, err := canonicalServerOrigin(value)
				if err != nil {
					return nil, err
				}
				value = canonical
			}
			out[key] = value
		}
	}
	return out, nil
}

func canonicalServerOrigin(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return "", errors.New("automatic discovery: KATA_SERVER must be a server origin")
	}
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("automatic discovery: KATA_SERVER path, query and fragment are not accepted; use the server origin")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func cloneMap(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	maps.Copy(dst, src)
	return dst
}

// HasDefinitions lists the project's jobs, then its workflows, through the
// neutral route that the generated configuration will use.
func (c *CommandKata) HasDefinitions(ctx context.Context, name, actor string) (bool, error) {
	view := config.Kata{Bin: c.bin, Project: name, Actor: actor, Daemon: c.daemon, Env: cloneMap(c.env), CredentialEnv: c.credentialEnv, CredentialFile: c.credentialFile}
	cli := kata.New(view, os.TempDir(), nil, nil)
	jobs, err := cli.ListJobs(ctx)
	if err != nil {
		return false, classifyProjectProbeError(err, name)
	}
	if len(jobs) > 0 {
		return true, nil
	}
	workflows, err := cli.ListWorkflows(ctx)
	if err != nil {
		return false, classifyProjectProbeError(err, name)
	}
	return len(workflows) > 0, nil
}
