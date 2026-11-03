// Package config loads the operator's explicit, strictly decoded configuration.
package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
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
	Argv []string `toml:"argv"`
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
	md, err := toml.DecodeFile(path, c)
	if err != nil {
		return nil, err
	}
	if keys := md.Undecoded(); len(keys) != 0 {
		return nil, fmt.Errorf("unknown configuration keys: %v", keys)
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
	}
	if c.Limits.DefaultTimeout <= 0 || c.Limits.MaxTimeout <= 0 || c.Limits.DefaultTimeout > c.Limits.MaxTimeout || c.Limits.MaxActiveRuns <= 0 {
		return nil, fmt.Errorf("invalid limits")
	}
	seen := make(map[string]bool)
	for i := range c.Projects {
		p := &c.Projects[i]
		if p.Name == "" || p.Actor == "" || seen[p.Name] || p.MaxActiveRuns < 0 {
			return nil, fmt.Errorf("invalid or duplicate project %q", p.Name)
		}
		if _, ok := c.Checkouts[p.DefaultCheckout]; p.DefaultCheckout != "" && !ok {
			return nil, fmt.Errorf("project %s default_checkout not configured: %s", p.Name, p.DefaultCheckout)
		}
		if _, ok := c.Profiles[p.DefaultAgent]; p.DefaultAgent != "" && !ok {
			return nil, fmt.Errorf("project %s default_agent not configured: %s", p.Name, p.DefaultAgent)
		}
		seen[p.Name] = true
		if p.MaxActiveRuns == 0 {
			p.MaxActiveRuns = 1
		}
	}
	return c, nil
}
