// Package discovery creates the first local configuration from the route that
// is already selected by the caller. Discovery is read-only against Kata and
// Herdr; the only mutation is an atomic config file created when none exists.
package discovery

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
)

var (
	ErrSessionUnavailable = errors.New("current Herdr session unavailable")
	ErrHerdrUnavailable   = errors.New("herdr server unavailable")
	ErrCredentialRequired = errors.New("kata route credential unavailable or rejected")
	ErrProjectUnavailable = errors.New("kata project unavailable")
	ErrActorMismatch      = errors.New("kata authenticated actor differs from the selected actor")
	ErrRouteMismatch      = errors.New("kata route cannot be represented by discovered configuration")
)

const discoveryTimeout = 2 * time.Minute

// Project is the read-only active-project listing returned by Kata.
type Project struct {
	Name   string
	UID    string
	Active bool
}

// ProjectProbe is the identity and capability result for one project.
// CredentialEnv is a variable name only; its value is never passed to the
// configuration writer.
type ProjectProbe struct {
	UID            string
	InstanceUID    string
	Actor          string
	Daemon         string
	CredentialEnv  string
	CredentialFile string
	CronV1         bool
}

// KataClient contains only the operations required to discover configuration.
// Implementations must use the route already selected by the caller and must
// not create, update, claim or schedule Kata records.
type KataClient interface {
	WhoAmI(context.Context) (string, error)
	RouteIdentity(context.Context, string) (string, error)
	ListProjects(context.Context, string) ([]Project, error)
	ProbeProject(context.Context, string, string) (ProjectProbe, error)
	// HasDefinitions reports whether a project has any job or workflow.
	HasDefinitions(context.Context, string, string) (bool, error)
	Bin() string
	RouteEnv() map[string]string
	CredentialEnv() string
}

// HerdrClient is the read-only subset needed during first-start discovery.
type HerdrClient interface {
	Ping(context.Context) (herdr.Pong, error)
	AgentList(context.Context) ([]herdr.Agent, error)
}

// Discoverer coordinates a one-time configuration creation. Session is the
// named session parsed from the current Herdr socket path.
type Discoverer struct {
	Kata    KataClient
	Herdr   HerdrClient
	Session string
	// Named projects are configured even without definitions; each must be an
	// active compatible project on the selected route.
	Named       []string
	LookupAgent func(string) (string, error)
	NewExecutor func() (string, error)
}

// Ensure creates path only when it does not already exist. A present file is
// treated as explicit user configuration and is never inspected or rewritten.
func (d Discoverer) Ensure(ctx context.Context, path string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if strings.TrimSpace(d.Session) == "" {
		return false, ErrSessionUnavailable
	}
	if d.Kata == nil {
		return false, errors.New("automatic discovery: Kata client unavailable")
	}
	if d.Herdr == nil {
		return false, ErrHerdrUnavailable
	}

	// A bounded ping proves that the socket selected for this first start is a
	// live Herdr session before any route or project is persisted.
	if _, err := d.Herdr.Ping(ctx); err != nil {
		return false, fmt.Errorf("%w: %v", ErrHerdrUnavailable, err)
	}
	agents, err := d.Herdr.AgentList(ctx)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrHerdrUnavailable, err)
	}
	profiles, err := DetectProfiles(agents, d.LookupAgent)
	if err != nil {
		return false, err
	}

	actor, err := d.Kata.WhoAmI(ctx)
	if err != nil || strings.TrimSpace(actor) == "" || strings.EqualFold(strings.TrimSpace(actor), "anonymous") {
		if errors.Is(err, ErrCredentialRequired) {
			return false, fmt.Errorf("automatic discovery: %w; restore or replace the selected credential source before first start", err)
		}
		if err == nil {
			err = errors.New("authenticated actor is empty or anonymous")
		}
		return false, fmt.Errorf("automatic discovery: Kata actor unavailable: %w", err)
	}
	actor = strings.TrimSpace(actor)
	routeInstanceUID, err := d.Kata.RouteIdentity(ctx, actor)
	if err != nil {
		if errors.Is(err, ErrCredentialRequired) {
			return false, fmt.Errorf("automatic discovery: %w; restore or replace the selected credential source before first start", err)
		}
		if errors.Is(err, ErrActorMismatch) {
			return false, fmt.Errorf("automatic discovery: %w; check the selected Kata route", err)
		}
		return false, fmt.Errorf("automatic discovery: cannot identify the selected Kata route: %w", err)
	}
	routeInstanceUID = strings.TrimSpace(routeInstanceUID)
	if routeInstanceUID == "" {
		return false, errors.New("automatic discovery: selected Kata route returned no instance UID")
	}
	projects, err := d.Kata.ListProjects(ctx, actor)
	if err != nil {
		return false, fmt.Errorf("automatic discovery: cannot list Kata projects: %w", err)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })

	named := map[string]bool{}
	for _, name := range d.Named {
		named[strings.TrimSpace(name)] = true
	}
	refuseNamed := func(name, reason string) error {
		return fmt.Errorf("automatic discovery: project %q named with --project %s on the selected Kata route", name, reason)
	}
	entries := make([]config.Project, 0, len(projects))
	var skipped []config.Project
	seen := map[string]bool{}
	for _, project := range projects {
		name := strings.TrimSpace(project.Name)
		if name == "" || seen[name] {
			continue
		}
		if !project.Active {
			if named[name] {
				return false, refuseNamed(name, "is not active")
			}
			continue
		}
		seen[name] = true
		probe, probeErr := d.Kata.ProbeProject(ctx, name, actor)
		if probeErr != nil {
			if err := discoveryProbeError(name, "probe", probeErr); err != nil {
				return false, err
			}
			if named[name] {
				return false, refuseNamed(name, "is not accessible")
			}
			// The list is a daemon-wide inventory. A project can be active in
			// that inventory while this actor has no route to it; continue with
			// the projects that are actually usable by this installation.
			continue
		}
		if !probe.CronV1 || strings.TrimSpace(probe.UID) == "" {
			if named[name] {
				return false, refuseNamed(name, "does not support cron_v1")
			}
			continue
		}
		if project.UID != "" && project.UID != probe.UID {
			return false, fmt.Errorf("automatic discovery: Kata project %q changed between inventory and identity probe (inventory UID %q, probe UID %q)", name, project.UID, probe.UID)
		}
		if routeInstanceUID != "" && probe.InstanceUID != "" && routeInstanceUID != probe.InstanceUID {
			return false, fmt.Errorf("automatic discovery: Kata route changed while probing project %q (inventory instance %q, probe instance %q)", name, routeInstanceUID, probe.InstanceUID)
		}
		projectActor := strings.TrimSpace(probe.Actor)
		if projectActor == "" {
			projectActor = actor
		}
		if strings.EqualFold(projectActor, "anonymous") {
			if named[name] {
				return false, refuseNamed(name, "has only an anonymous actor")
			}
			continue
		}
		credential := probe.CredentialEnv
		if credential == "" {
			credential = d.Kata.CredentialEnv()
		}
		entry := config.Project{Name: name, Actor: projectActor, Inbox: inboxForActor(projectActor), Daemon: probe.Daemon, CredentialEnv: credential, CredentialFile: probe.CredentialFile}
		if named[name] {
			entries = append(entries, entry)
			continue
		}
		// Every configured lane holds an event tail and route checks against
		// the hub, so first start adds only projects with work to run.
		defined, defsErr := d.Kata.HasDefinitions(ctx, name, projectActor)
		if defsErr != nil {
			if err := discoveryProbeError(name, "check definitions of", defsErr); err != nil {
				return false, err
			}
			continue
		}
		if defined {
			entries = append(entries, entry)
		} else {
			skipped = append(skipped, entry)
		}
	}
	for _, name := range d.Named {
		if name = strings.TrimSpace(name); !seen[name] {
			return false, refuseNamed(name, "is not listed")
		}
	}

	executor := d.NewExecutor
	if executor == nil {
		executor = stableExecutor
	}
	label, err := executor()
	if err != nil {
		return false, fmt.Errorf("automatic discovery: generate executor identity: %w", err)
	}
	if strings.TrimSpace(label) == "" {
		return false, errors.New("automatic discovery: generated executor identity is empty")
	}

	file := discoveredFile{
		Session:  d.Session,
		Executor: label,
		Kata:     discoveredKata{Bin: d.Kata.Bin(), Env: routeEnv(d.Kata.RouteEnv())},
		Projects: entries,
		Profiles: profiles,
	}
	if err := writeConfig(path, file, skipped); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// A concurrently-created explicit file wins without an overwrite.
			return false, nil
		}
		return false, fmt.Errorf("automatic discovery: write %s: %w", path, err)
	}
	return true, nil
}

// discoveryProbeError returns nil for a project this actor cannot use, which
// discovery skips, and a first-start failure for every other probe error.
func discoveryProbeError(name, operation string, err error) error {
	switch {
	case errors.Is(err, ErrCredentialRequired):
		return fmt.Errorf("automatic discovery: %w; restore or replace the selected credential source before first start", err)
	case errors.Is(err, ErrActorMismatch):
		return fmt.Errorf("automatic discovery: %w; check the selected Kata route", err)
	case errors.Is(err, ErrRouteMismatch):
		return fmt.Errorf("automatic discovery: %w; run from a shell with the selected Kata route expressed by KATA_HOME, KATA_SERVER or XDG_*", err)
	case errors.Is(err, ErrProjectUnavailable):
		return nil
	default:
		return fmt.Errorf("automatic discovery: %s Kata project %q: %w", operation, name, err)
	}
}

// DetectProfiles discovers profile kinds without starting an agent. Existing
// agents prove their kind is usable; an empty session falls back to the two
// agent kinds supported by the runtime when their executable is on PATH.
func DetectProfiles(agents []herdr.Agent, lookup func(string) (string, error)) (map[string]config.Profile, error) {
	if lookup == nil {
		lookup = exec.LookPath
	}
	kinds := map[string]bool{}
	for _, agent := range agents {
		if agent.Session == nil || !profileKind.MatchString(agent.Session.Agent) {
			continue
		}
		kinds[agent.Session.Agent] = true
	}
	if len(kinds) == 0 {
		for _, kind := range []string{"codex", "claude"} {
			if _, err := lookup(kind); err == nil {
				kinds[kind] = true
			}
		}
	}
	if len(kinds) == 0 {
		return nil, errors.New("automatic discovery: no usable Herdr agent profile found; install codex or claude and retry")
	}
	ordered := make([]string, 0, len(kinds))
	for kind := range kinds {
		ordered = append(ordered, kind)
	}
	slices.Sort(ordered)
	profiles := make(map[string]config.Profile, len(ordered)+1)
	for _, kind := range ordered {
		profiles[kind] = config.Profile{Kind: kind}
	}
	defaultKind := ordered[0]
	if kinds["codex"] {
		defaultKind = "codex"
	}
	profiles["default"] = config.Profile{Kind: defaultKind}
	return profiles, nil
}

var profileKind = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func inboxForActor(actor string) string {
	if candidate := strings.TrimSpace(os.Getenv("KATA_INBOX_USER")); candidate != "" {
		if candidate == actor {
			return candidate
		}
		if suffix, ok := strings.CutPrefix(candidate, actor+"/"); ok && strings.TrimSpace(suffix) != "" {
			return candidate
		}
	}
	return actor + "/herdr-kata"
}

// The route map is deliberately narrower than the process environment. These
// are routing selectors accepted by config.Kata; credential and actor values
// are represented separately by their variable names or project fields.
func routeEnv(env map[string]string) map[string]string {
	allowed := map[string]bool{
		"KATA_HOME":       true,
		"KATA_SERVER":     true,
		"XDG_CONFIG_HOME": true,
		"XDG_DATA_HOME":   true,
		"XDG_STATE_HOME":  true,
		"XDG_CACHE_HOME":  true,
	}
	out := make(map[string]string)
	for key, value := range env {
		if allowed[key] && value != "" {
			out[key] = value
		}
	}
	return out
}

func stableExecutor() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "herdr-kata-" + hex.EncodeToString(raw[:]), nil
}

type discoveredFile struct {
	Session  string                    `toml:"session"`
	Executor string                    `toml:"executor"`
	Kata     discoveredKata            `toml:"kata"`
	Projects []config.Project          `toml:"projects,omitempty"`
	Profiles map[string]config.Profile `toml:"profiles"`
}

type discoveredKata struct {
	Bin string            `toml:"bin"`
	Env map[string]string `toml:"env"`
}

// writeAddGuide appends comments that explain how to add a lane. Projects
// that were reachable but had no definitions are written as commented-out
// entries, so adding one later is removing the "# " from its block.
func writeAddGuide(buf *bytes.Buffer, none bool, skipped []config.Project) error {
	if !none && len(skipped) == 0 {
		return nil
	}
	buf.WriteString("\n")
	if none {
		buf.WriteString("# No project is configured, so herdr-kata up starts nothing.\n")
	}
	buf.WriteString("# To add a project, add a [[projects]] entry with its name and actor, then\n")
	buf.WriteString("# run herdr-kata stop and herdr-kata up.\n")
	if len(skipped) == 0 {
		return nil
	}
	buf.WriteString("# These projects had no job or workflow definitions at first start. To\n")
	buf.WriteString("# serve one, remove the leading \"# \" from each line of its block.\n")
	var block bytes.Buffer
	if err := toml.NewEncoder(&block).Encode(struct {
		Projects []config.Project `toml:"projects"`
	}{skipped}); err != nil {
		return err
	}
	for line := range strings.Lines(block.String()) {
		if strings.TrimSpace(line) == "" {
			buf.WriteString("#\n")
			continue
		}
		buf.WriteString("# " + line)
	}
	return nil
}

func writeConfig(path string, file discoveredFile, skipped []config.Project) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(file); err != nil {
		return err
	}
	if err := writeAddGuide(&buf, len(file.Projects) == 0, skipped); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".config.toml.discovery-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	remove := true
	defer func() {
		_ = f.Close()
		if remove {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// Publish with a no-replace operation after the complete private temp file
	// is durable. Rename would overwrite an explicit config; a hard link gives
	// us atomic visibility while preserving the first writer's ownership.
	if err := os.Link(tmp, path); err != nil {
		return err
	}
	if err := os.Remove(tmp); err != nil {
		return fmt.Errorf("remove discovery staging file: %w", err)
	}
	remove = false
	return nil
}
