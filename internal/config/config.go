// Package config loads the operator's explicit, strictly decoded configuration.
package config

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
	"github.com/salmonumbrella/herdr-kata/internal/session"
)

// Quarantine describes a project entry excluded from the loaded configuration.
type Quarantine struct {
	Entry  int
	Name   string
	Reason string
}

func (q Quarantine) String() string {
	name := ""
	if q.Name != "" {
		name = " (" + q.Name + ")"
	}
	return fmt.Sprintf("project entry %d%s: %s", q.Entry, name, q.Reason)
}

type Sessions struct {
	Env map[string]string `toml:"env"`
}

type Config struct {
	Sessions    Sessions     `toml:"sessions"`
	Quarantined []Quarantine `toml:"-"`

	Executor  string             `toml:"executor"`
	HCSession string             `toml:"hc_session"`
	HCBin     string             `toml:"hc_bin"`
	Kata      KataCfg            `toml:"kata"`
	Projects  []Project          `toml:"projects"`
	Checkouts map[string]string  `toml:"checkouts"`
	Profiles  map[string]Profile `toml:"profiles"`
	Limits    Limits             `toml:"limits"`
}

type KataCfg struct {
	Bin    string            `toml:"bin"`
	Daemon string            `toml:"daemon"`
	Env    map[string]string `toml:"env"`
}

type Project struct {
	Name            string `toml:"name"`
	Actor           string `toml:"actor"`
	Commands        bool   `toml:"commands"`
	MaxActiveRuns   int    `toml:"max_active_runs"`
	DefaultCheckout string `toml:"default_checkout"`
	DefaultAgent    string `toml:"default_agent"`
}

type Profile struct {
	Argv      []string `toml:"argv"`
	ModelFlag string   `toml:"model_flag"`
}

type Limits struct {
	DefaultTimeout time.Duration `toml:"default_timeout"`
	MaxTimeout     time.Duration `toml:"max_timeout"`
	MaxActiveRuns  int           `toml:"max_active_runs"`
	UnsetEnv       []string      `toml:"unset_env"`
}

func DefaultPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "herdr-kata", "config.toml")
}

func Load(path string) (*Config, error) {
	c := &Config{HCSession: "default", Limits: Limits{DefaultTimeout: 2 * time.Hour, MaxTimeout: 24 * time.Hour, MaxActiveRuns: 4}}
	file := struct {
		*Config
		Projects []toml.Primitive `toml:"projects"`
	}{Config: c}
	md, err := toml.DecodeFile(path, &file)
	if err != nil {
		return nil, err
	}
	projectKey := ""
	for _, key := range md.Keys() {
		if len(key) == 1 && strings.EqualFold(key[0], "projects") {
			if projectKey != "" && projectKey != key[0] {
				return nil, fmt.Errorf("conflicting projects keys")
			}
			projectKey = key[0]
		}
	}
	for _, key := range md.Undecoded() {
		if !strings.EqualFold(key[0], "projects") {
			return nil, fmt.Errorf("unknown configuration keys: %v", key)
		}
	}
	if c.Executor == "" || c.HCSession == "" || c.Kata.Daemon == "" {
		return nil, fmt.Errorf("executor, hc_session and kata.daemon are required")
	}
	for _, b := range []struct {
		p        *string
		fallback string
	}{{&c.HCBin, "hc"}, {&c.Kata.Bin, "kata"}} {
		if *b.p == "" {
			resolved, e := exec.LookPath(b.fallback)
			if e != nil {
				return nil, e
			}
			*b.p = resolved
		}
		if !filepath.IsAbs(*b.p) {
			return nil, fmt.Errorf("binary path must be absolute: %s", *b.p)
		}
	}
	for agent, name := range c.Sessions.Env {
		if !session.ValidAgent(agent) || !session.ValidSessionEnv(name) {
			return nil, fmt.Errorf("invalid sessions.env mapping: %s", agent)
		}
	}
	for k, v := range c.Kata.Env {
		if k != "KATA_HOME" && k != "KATA_SERVER" && !strings.HasPrefix(k, "XDG_") {
			return nil, fmt.Errorf("kata.env key is not allowed: %s", k)
		}
		if (k == "KATA_HOME" || strings.HasPrefix(k, "XDG_")) && !filepath.IsAbs(v) {
			return nil, fmt.Errorf("%s path must be absolute", k)
		}
	}
	for key, path := range c.Checkouts {
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("checkout %s path must be absolute", key)
		}
	}
	for key, p := range c.Profiles {
		if len(p.Argv) == 0 || p.Argv[0] == "" {
			return nil, fmt.Errorf("profile %s requires argv", key)
		}
		if p.ModelFlag != "" && (!strings.HasPrefix(p.ModelFlag, "-") || p.ModelFlag == "-" || p.ModelFlag == "--" || strings.Contains(p.ModelFlag, "=") || strings.ContainsFunc(p.ModelFlag, unicode.IsSpace)) {
			return nil, fmt.Errorf("profile %s model_flag must be a flag", key)
		}
		if p.ModelFlag != "" && p.Argv[len(p.Argv)-1] == "--" {
			return nil, fmt.Errorf("profile %s model_flag cannot be used when argv ends in --", key)
		}
	}
	if c.Limits.DefaultTimeout <= 0 || c.Limits.MaxTimeout <= 0 || c.Limits.DefaultTimeout > c.Limits.MaxTimeout || c.Limits.MaxActiveRuns <= 0 {
		return nil, fmt.Errorf("invalid limits")
	}
	c.loadProjects(md, file.Projects)
	return c, nil
}

func (c *Config) loadProjects(md toml.MetaData, entries []toml.Primitive) {
	projects := make([]Project, len(entries))
	names := make([]string, len(entries))
	nameSets := make([]map[string]bool, len(entries))
	reasons := make([][]string, len(entries))
	counts := make(map[string]int)
	fields := reflect.VisibleFields(reflect.TypeFor[Project]())
	for i, entry := range entries {
		var raw any
		_ = md.PrimitiveDecode(entry, &raw) // Decoding into any cannot fail.
		table, ok := raw.(map[string]any)
		if !ok {
			reasons[i] = []string{"not a table"}
			continue
		}
		var unknown []string
		nameSets[i] = make(map[string]bool)
		for _, key := range slices.Sorted(maps.Keys(table)) {
			if strings.EqualFold(key, "name") {
				if name, ok := table[key].(string); ok {
					names[i] = name
					nameSets[i][name] = true
				}
			}
			if !slices.ContainsFunc(fields, func(f reflect.StructField) bool { return strings.EqualFold(key, f.Tag.Get("toml")) }) {
				unknown = append(unknown, key)
			}
		}
		for name := range nameSets[i] {
			counts[name]++
		}
		if len(nameSets[i]) > 1 {
			reasons[i] = append(reasons[i], "invalid value: conflicting name keys")
		}
		if len(unknown) > 0 {
			slices.Sort(unknown)
			reasons[i] = append(reasons[i], "unknown keys: "+strings.Join(unknown, ", "))
		}
		p := &projects[i]
		if err := md.PrimitiveDecode(entry, p); err != nil {
			reasons[i] = append(reasons[i], "invalid value: "+err.Error())
			continue
		}
		p.Name = names[i]
		for _, check := range []struct {
			bad    bool
			reason string
		}{
			{p.Name == "" || p.Actor == "", "name and actor are required"},
			{p.MaxActiveRuns < 0, "max_active_runs must not be negative"},
			{p.DefaultCheckout != "" && c.Checkouts[p.DefaultCheckout] == "", "default_checkout not configured: " + p.DefaultCheckout},
			{p.DefaultAgent != "" && len(c.Profiles[p.DefaultAgent].Argv) == 0, "default_agent not configured: " + p.DefaultAgent},
		} {
			if check.bad {
				reasons[i] = append(reasons[i], check.reason)
			}
		}
	}
	for i, p := range projects {
		if slices.ContainsFunc(slices.Collect(maps.Keys(nameSets[i])), func(name string) bool { return name != "" && counts[name] > 1 }) {
			reasons[i] = append(reasons[i], "duplicate project name")
		}
		if len(reasons[i]) > 0 {
			c.Quarantined = append(c.Quarantined, Quarantine{i + 1, names[i], strings.Join(reasons[i], "; ")})
			continue
		}
		if p.MaxActiveRuns == 0 {
			p.MaxActiveRuns = 1
		}
		c.Projects = append(c.Projects, p)
	}
}
