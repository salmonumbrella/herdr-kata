// Package config loads strict TOML configuration and local secret source references.
// Literal secret values are refused.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"go.kenn.io/kit/secretref"
)

type Board struct {
	Pin bool `toml:"pin"`
}

type Project struct {
	Name           string   `toml:"name"`
	Daemon         string   `toml:"daemon,omitempty"`
	Actor          string   `toml:"actor"`
	Inbox          string   `toml:"inbox"`
	CredentialEnv  string   `toml:"credential_env,omitempty"`
	CredentialFile string   `toml:"credential_file,omitempty"`
	Checkouts      []string `toml:"checkouts"`
	Profiles       []string `toml:"profiles"`
	Commands       *bool    `toml:"commands"`
	MaxActiveRuns  *int     `toml:"max_active_runs"`
}

type Config struct {
	Projects     []Project                `toml:"projects"`
	Commands     *bool                    `toml:"-"`
	ProjectLimit int                      `toml:"-"`
	Board        Board                    `toml:"board"`
	Session      string                   `toml:"session"`
	Executor     string                   `toml:"executor"`
	Kata         Kata                     `toml:"kata"`
	Limits       Limits                   `toml:"limits"`
	Checkouts    map[string]string        `toml:"checkouts"`
	Profiles     map[string]Profile       `toml:"profiles"`
	Secrets      map[string]secretref.Ref `toml:"secrets"`
}
type Kata struct {
	SecretEnvs      []string          `toml:"-"`
	CredentialNames []string          `toml:"-"`
	Bin             string            `toml:"bin"`
	Daemon          string            `toml:"daemon"`
	Project         string            `toml:"project"`
	Actor           string            `toml:"actor"`
	Inbox           string            `toml:"inbox"`
	CredentialEnv   string            `toml:"credential_env"`
	CredentialFile  string            `toml:"credential_file"`
	Env             map[string]string `toml:"env"`
}
type Limits struct {
	MaxActiveRuns  int
	DefaultTimeout time.Duration
	MaxTimeout     time.Duration
	MaxRunPrompts  int
}
type Profile struct {
	MaxRunPrompts *int     `toml:"max_run_prompts"`
	Kind          string   `toml:"kind"`
	Args          []string `toml:"args"`
}

func DefaultPath() string {
	if p := os.Getenv("HERDR_PLUGIN_CONFIG_DIR"); p != "" {
		return filepath.Join(p, "config.toml")
	}
	if p := os.Getenv("XDG_CONFIG_HOME"); p != "" {
		return filepath.Join(p, "herdr", "plugins", "config", "kata", "config.toml")
	}
	home := state.HomeDir()
	return filepath.Join(home, ".config", "herdr", "plugins", "config", "kata", "config.toml")
}

var envKey = regexp.MustCompile(`^(KATA_[A-Z0-9_]+|XDG_[A-Z_]+)$`)
var profileKind = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func Load(path string) (*Config, error) {
	// A wire representation keeps the exported duration type stable while TOML
	// accepts human-readable durations and distinguishes absent from zero limits.
	raw := struct {
		Board    Board     `toml:"board"`
		Session  string    `toml:"session"`
		Executor string    `toml:"executor"`
		Kata     Kata      `toml:"kata"`
		Projects []Project `toml:"projects"`
		Limits   struct {
			MaxActiveRuns  int    `toml:"max_active_runs"`
			DefaultTimeout string `toml:"default_timeout"`
			MaxTimeout     string `toml:"max_timeout"`
			MaxRunPrompts  int    `toml:"max_run_prompts"`
		} `toml:"limits"`
		Secrets   map[string]secretref.Ref `toml:"secrets"`
		Checkouts map[string]string        `toml:"checkouts"`
		Profiles  map[string]Profile       `toml:"profiles"`
	}{}
	raw.Board.Pin = true
	raw.Limits.MaxActiveRuns = 2
	raw.Limits.DefaultTimeout = "2h"
	raw.Limits.MaxTimeout = "8h"
	raw.Limits.MaxRunPrompts = 12
	meta, err := toml.DecodeFile(path, &raw)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w\nrun herdr-kata up from a live named Herdr session to discover it, or create it; see docs/get-started/quickstart.md\n\nExample minimal configuration (replace paths, project and actor):\n```toml\n%s\n```", err, `session = "work"
executor = "work-runtime"

[kata]
bin = "/usr/local/bin/kata"
env = { KATA_HOME = "/srv/kata" }

[[projects]]
name = "example-project"
actor = "worker"

[profiles.default]
kind = "codex"`)
		}
		return nil, err
	}
	if keys := meta.Undecoded(); len(keys) > 0 {
		return nil, fmt.Errorf("unknown configuration keys: %v", keys)
	}
	timeout, err := time.ParseDuration(raw.Limits.DefaultTimeout)
	if err != nil {
		return nil, fmt.Errorf("limits.default_timeout: %w", err)
	}
	maxTimeout, err := time.ParseDuration(raw.Limits.MaxTimeout)
	if err != nil {
		return nil, fmt.Errorf("limits.max_timeout: %w", err)
	}
	c := &Config{Projects: raw.Projects, Board: raw.Board, Session: raw.Session, Executor: raw.Executor, Kata: raw.Kata, Limits: Limits{MaxActiveRuns: raw.Limits.MaxActiveRuns, DefaultTimeout: timeout, MaxTimeout: maxTimeout, MaxRunPrompts: raw.Limits.MaxRunPrompts}, Secrets: raw.Secrets, Checkouts: raw.Checkouts, Profiles: raw.Profiles}
	required := []struct{ name, value string }{{"session", c.Session}, {"executor", c.Executor}, {"kata.bin", c.Kata.Bin}}
	for _, r := range required {
		if strings.TrimSpace(r.value) == "" {
			return nil, fmt.Errorf("%s is required", r.name)
		}
	}
	if len(c.Executor) > 256 || strings.ContainsFunc(c.Executor, unicode.IsSpace) {
		return nil, fmt.Errorf("executor must be at most 256 bytes and contain no whitespace")
	}
	if !filepath.IsAbs(c.Kata.Bin) {
		return nil, fmt.Errorf("kata.bin must be absolute")
	}
	for name, ref := range c.Secrets {
		if !PortableSecretName(name) {
			return nil, fmt.Errorf("invalid secret name %q", name)
		}
		if meta.IsDefined("secrets", name, "value") || ref.Value != "" || (ref.Env == "" && ref.File == "") {
			return nil, fmt.Errorf("secret %q requires env or file; literal values are refused", name)
		}
		if err := ref.Validate(); err != nil {
			return nil, fmt.Errorf("secret %q: %w", name, err)
		}
		if ref.Env != "" && !ChildVariable(ref.Env) {
			return nil, fmt.Errorf("secret %q has invalid source variable", name)
		}
		if ref.Env != "" && !slices.Contains(c.Kata.SecretEnvs, ref.Env) {
			c.Kata.SecretEnvs = append(c.Kata.SecretEnvs, ref.Env)
		}
		if ref.File != "" && !filepath.IsAbs(ref.File) && !strings.HasPrefix(ref.File, "~/") {
			return nil, fmt.Errorf("secret %q file must be absolute or start with ~/", name)
		}
	}
	for key, path := range c.Checkouts {
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("checkout %q must be absolute", key)
		}
	}
	for key := range c.Kata.Env {
		if !envKey.MatchString(key) || key == "KATA_AUTH_TOKEN" {
			return nil, fmt.Errorf("kata.env key %q is not allowed", key)
		}
	}
	if c.Limits.MaxActiveRuns <= 0 || c.Limits.DefaultTimeout <= 0 || c.Limits.MaxTimeout <= 0 {
		return nil, fmt.Errorf("limits must be positive")
	}
	if c.Limits.DefaultTimeout > c.Limits.MaxTimeout {
		return nil, fmt.Errorf("limits.default_timeout exceeds limits.max_timeout")
	}
	if c.Limits.MaxRunPrompts < 1 || c.Limits.MaxRunPrompts > 100 {
		return nil, fmt.Errorf("limits.max_run_prompts must be 1–100")
	}
	if _, ok := c.Profiles["default"]; !ok {
		return nil, fmt.Errorf("profiles.default is required")
	}
	for name, p := range c.Profiles {
		if p.MaxRunPrompts != nil && (*p.MaxRunPrompts < 1 || *p.MaxRunPrompts > 100) {
			return nil, fmt.Errorf("profile %q max_run_prompts must be 1–100", name)
		}
		if !profileKind.MatchString(p.Kind) {
			return nil, fmt.Errorf("profile %q has invalid kind %q", name, p.Kind)
		}
		if slices.Contains(p.Args, "") {
			return nil, fmt.Errorf("profile %q has an empty argument", name)
		}
	}
	if len(c.Projects) == 0 {
		// A config with neither form has no lanes; up starts nothing until a
		// project is added.
		if (strings.TrimSpace(c.Kata.Project) == "") != (strings.TrimSpace(c.Kata.Actor) == "") {
			return nil, fmt.Errorf("add [[projects]] with name and actor; kata.project and kata.actor are required when using the single-project form")
		}
	} else {
		for _, key := range []string{"project", "actor", "daemon", "inbox", "credential_env", "credential_file"} {
			if meta.IsDefined("kata", key) {
				return nil, fmt.Errorf("kata.%s cannot be mixed with projects", key)
			}
		}
	}
	seen := map[string]bool{}
	for _, p := range c.ProjectEntries() {
		if strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.Actor) == "" {
			return nil, fmt.Errorf("project name and actor are required")
		}
		if seen[p.Name] {
			return nil, fmt.Errorf("duplicate project name %q", p.Name)
		}
		seen[p.Name] = true
		for _, key := range p.Checkouts {
			if _, ok := c.Checkouts[key]; !ok {
				return nil, fmt.Errorf("project %s: unknown checkout %q", p.Name, key)
			}
		}
		for _, key := range p.Profiles {
			if _, ok := c.Profiles[key]; !ok {
				return nil, fmt.Errorf("project %s: unknown profile %q", p.Name, key)
			}
		}
		if p.MaxActiveRuns != nil && (*p.MaxActiveRuns <= 0 || *p.MaxActiveRuns > c.Limits.MaxActiveRuns) {
			return nil, fmt.Errorf("project %s: max_active_runs must be positive and at most the global limit", p.Name)
		}
		if p.CredentialFile != "" && (!filepath.IsAbs(p.CredentialFile) || p.CredentialEnv == "") {
			return nil, fmt.Errorf("project %s: credential_file must be absolute and requires credential_env", p.Name)
		}
		if p.CredentialEnv != "" && !ChildVariable(p.CredentialEnv) {
			return nil, fmt.Errorf("project %s: credential_env must name an environment variable", p.Name)
		}
		if p.CredentialEnv != "" && !slices.Contains(c.Kata.CredentialNames, p.CredentialEnv) {
			c.Kata.CredentialNames = append(c.Kata.CredentialNames, p.CredentialEnv)
		}
	}
	for name, ref := range c.Secrets {
		if ref.Env != "" && (ref.Env == "KATA_AUTH_TOKEN" || slices.Contains(c.CredentialVariables(), ref.Env)) {
			return nil, fmt.Errorf("secret %q aliases a Kata credential; use a separate secret source", name)
		}
		for _, project := range c.ProjectEntries() {
			if ref.File != "" && project.CredentialFile != "" && sameFileSource(ref.File, project.CredentialFile) {
				return nil, fmt.Errorf("secret %q aliases a Kata credential; use a separate secret source", name)
			}
		}
	}
	return c, nil
}

// ProjectEntries includes the legacy single-project entry without changing its defaults.
func (c Config) ProjectEntries() []Project {
	if len(c.Projects) > 0 {
		return c.Projects
	}
	if strings.TrimSpace(c.Kata.Project) == "" && strings.TrimSpace(c.Kata.Actor) == "" {
		return nil
	}
	return []Project{{Name: c.Kata.Project, Actor: c.Kata.Actor, Daemon: c.Kata.Daemon, Inbox: c.Kata.Inbox, CredentialEnv: c.Kata.CredentialEnv, CredentialFile: c.Kata.CredentialFile}}
}

// HasProject preserves unnamed legacy pins until a configured lane can resolve
// and save their name. A missing name alone is not proof of lane removal.
func (c Config) HasProject(name string) bool {
	for _, entry := range c.ProjectEntries() {
		if name == "" || entry.Name == name {
			return true
		}
	}
	return false
}
func (c Config) CommandsAllowed() bool { return c.Commands == nil || *c.Commands }
func (c Config) View(p Project) Config {
	c.Kata.CredentialNames = c.CredentialVariables()
	env := map[string]string{}
	credentials := map[string]bool{}
	for _, name := range c.Kata.CredentialNames {
		credentials[name] = true
	}
	for key, value := range c.Kata.Env {
		if !credentials[key] {
			env[key] = value
		}
	}
	c.Projects = nil
	c.Kata = Kata{SecretEnvs: c.Kata.SecretEnvs, Bin: c.Kata.Bin, Env: env, Project: p.Name, Actor: p.Actor, Daemon: p.Daemon, Inbox: p.Inbox, CredentialEnv: p.CredentialEnv, CredentialFile: p.CredentialFile, CredentialNames: c.Kata.CredentialNames}
	if p.Checkouts != nil {
		filtered := map[string]string{}
		for _, key := range p.Checkouts {
			if v, ok := c.Checkouts[key]; ok {
				filtered[key] = v
			}
		}
		c.Checkouts = filtered
	}
	if p.Profiles != nil {
		filtered := map[string]Profile{}
		for _, key := range p.Profiles {
			if v, ok := c.Profiles[key]; ok {
				filtered[key] = v
			}
		}
		c.Profiles = filtered
	}
	c.Commands = p.Commands
	if p.MaxActiveRuns != nil {
		c.ProjectLimit = *p.MaxActiveRuns
	}
	return c
}
func (c Config) SelectProject(name string) (Config, error) {
	entries := c.ProjectEntries()
	if len(entries) == 0 {
		return Config{}, NoProjectsError()
	}
	if name == "" && len(entries) == 1 {
		return c.View(entries[0]), nil
	}
	for _, p := range entries {
		if p.Name == name {
			return c.View(p), nil
		}
	}
	return Config{}, fmt.Errorf("select a configured project with --project")
}

// NoProjectsError explains how to add the first lane to a config with none.
func NoProjectsError() error {
	return fmt.Errorf("no projects are configured; add a [[projects]] entry with name and actor to %s, then run herdr-kata up", DefaultPath())
}

var portableSecretName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,256}$`)
var childVariable = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func PortableSecretName(name string) bool {
	return portableSecretName.MatchString(name) && name != "." && name != ".."
}
func ChildVariable(name string) bool { return childVariable.MatchString(name) }
func (c Config) ReservedVariable(name string) bool {
	if strings.HasPrefix(name, "KATA_") || strings.HasPrefix(name, "HERDR_KATA_") {
		return true
	}
	return slices.Contains(c.CredentialVariables(), name)
}

// SecretSource describes a configured source without resolving or exposing its value.
func SecretSource(ref secretref.Ref) string {
	if ref.Env != "" {
		return "env:" + ref.Env
	}
	if ref.File != "" {
		return "file:" + ref.File
	}
	return "unmapped"
}

// CredentialVariables fences credentials from every project, including views.
func (c Config) CredentialVariables() []string {
	names := []string{}
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	for _, name := range c.Kata.CredentialNames {
		add(name)
	}
	// Literal configs have not passed through Load. Capture their full fence
	// before View replaces the project list with the selected entry.
	if len(c.Kata.CredentialNames) == 0 {
		for _, entry := range c.ProjectEntries() {
			add(entry.CredentialEnv)
		}
	}
	add(c.Kata.CredentialEnv)
	return names
}
