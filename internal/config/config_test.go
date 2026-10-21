package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/valid.toml")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func loadText(t *testing.T, s string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}
func TestLoadValid(t *testing.T) {
	c, err := Load("testdata/valid.toml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Session != "work" || c.Kata.Actor != "runner" || c.Limits.MaxActiveRuns != 2 || c.Limits.DefaultTimeout != 2*time.Hour || c.Limits.MaxTimeout != 8*time.Hour || c.Limits.MaxRunPrompts != 12 {
		t.Fatalf("config = %+v", c)
	}
	c, err = loadText(t, fixture(t)+"\n[limits]\nmax_active_runs = 3\ndefault_timeout = \"30m\"\n")
	if err != nil || c.Limits.MaxActiveRuns != 3 || c.Limits.DefaultTimeout != 30*time.Minute {
		t.Fatalf("override = %+v, %v", c, err)
	}
}
func TestLoadRejectsUnknownKey(t *testing.T) {
	for _, s := range []string{"typo = true\n" + fixture(t), fixture(t) + "\nunknown = true\n"} {
		if _, err := loadText(t, s); err == nil {
			t.Fatal("unknown key accepted")
		}
	}
}
func TestLoadRequired(t *testing.T) {
	for _, line := range []string{`session = "work"`, `executor = "work-runtime"`, `bin = "/usr/local/bin/kata"`, `project = "tasks"`, `actor = "runner"`} {
		t.Run(line, func(t *testing.T) {
			if _, err := loadText(t, strings.Replace(fixture(t), line, "", 1)); err == nil {
				t.Fatal("missing value accepted")
			}
		})
	}
	if _, err := loadText(t, strings.ReplaceAll(fixture(t), "profiles.default", "profiles.other")); err == nil {
		t.Fatal("missing default accepted")
	}
}
func TestLoadRejectsRelativePaths(t *testing.T) {
	for _, p := range []string{"/usr/local/bin/kata", "/tmp/project"} {
		if _, err := loadText(t, strings.Replace(fixture(t), p, "relative", 1)); err == nil {
			t.Fatal("relative path accepted")
		}
	}
}
func TestLoadRejectsBadEnvKey(t *testing.T) {
	for _, key := range []string{"HOME", "KATA_AUTH_TOKEN", "KATA_lower", "XDG_123", "KATA_"} {
		if _, err := loadText(t, strings.Replace(fixture(t), "KATA_HOME", key, 1)); err == nil {
			t.Fatalf("accepted %s", key)
		}
	}
}
func TestLoadRejectsBadExecutor(t *testing.T) {
	for _, v := range []string{"", "has space", "has\ttab", strings.Repeat("x", 257), "has\u2003space"} {
		if _, err := loadText(t, strings.Replace(fixture(t), "work-runtime", v, 1)); err == nil {
			t.Fatalf("accepted %q", v)
		}
	}
}
func TestLoadProfileSyntax(t *testing.T) {
	for _, v := range []string{"Codex", "has space", "", "1bad", "a_b"} {
		if _, err := loadText(t, strings.Replace(fixture(t), `kind = "codex"`, `kind = "`+v+`"`, 1)); err == nil {
			t.Fatalf("accepted kind %q", v)
		}
	}
	if _, err := loadText(t, strings.Replace(fixture(t), `args = ["-m", "example-model"]`, `args = [""]`, 1)); err == nil {
		t.Fatal("empty argument accepted")
	}
}
func TestLoadRejectsNonpositiveLimits(t *testing.T) {
	for _, s := range []string{"max_active_runs = 0", "max_active_runs = -1", `default_timeout = "0s"`, `default_timeout = "-1h"`, `default_timeout = "bad"`} {
		if _, err := loadText(t, fixture(t)+"\n[limits]\n"+s); err == nil {
			t.Fatalf("accepted %s", s)
		}
	}
}
func TestDefaultPath(t *testing.T) {
	t.Setenv("HOME", "/tmp/home")
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	if got := DefaultPath(); got != "/tmp/home/.config/herdr/plugins/config/kata/config.toml" {
		t.Fatalf("default = %s", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-config")
	if got := DefaultPath(); got != "/tmp/xdg-config/herdr/plugins/config/kata/config.toml" {
		t.Fatalf("XDG = %s", got)
	}
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", "/tmp/plugin")
	if got := DefaultPath(); got != "/tmp/plugin/config.toml" {
		t.Fatalf("override = %s", got)
	}
}

func TestLoadConstraintBoundaries(t *testing.T) {
	for _, v := range []string{strings.Repeat("x", 256), "é" + strings.Repeat("x", 254)} {
		if _, err := loadText(t, strings.Replace(fixture(t), "work-runtime", v, 1)); err != nil {
			t.Fatalf("valid executor rejected: %v", err)
		}
	}
	for _, key := range []string{"KATA_0", "XDG_A_"} {
		if _, err := loadText(t, strings.Replace(fixture(t), "KATA_HOME", key, 1)); err != nil {
			t.Fatalf("valid env %s rejected: %v", key, err)
		}
	}
}

func TestDefaultPathWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	if got := DefaultPath(); !filepath.IsAbs(got) {
		t.Fatalf("HOME-less config path is relative: %s", got)
	}
}

func TestBoardPinDefaultAndOverride(t *testing.T) {
	c, err := loadText(t, fixture(t))
	if err != nil || !c.Board.Pin {
		t.Fatalf("%+v %v", c, err)
	}
	c, err = loadText(t, fixture(t)+"\n[board]\npin = false\n")
	if err != nil || c.Board.Pin {
		t.Fatalf("%+v %v", c, err)
	}
}

// Cost limits are strict config, with an optional profile cap that only lowers it.
func TestLoadCostLimits(t *testing.T) {
	for _, limits := range []string{
		`max_timeout = "8h"
max_run_prompts = 12`,
		`max_timeout = "2h"
max_run_prompts = 1`,
		`max_timeout = "24h"
max_run_prompts = 100`,
	} {
		if _, err := loadText(t, fixture(t)+"\n[limits]\n"+limits); err != nil {
			t.Fatalf("valid cost limits rejected: %v", err)
		}
	}
	for _, cap := range []string{"1", "4", "100"} {
		if _, err := loadText(t, strings.Replace(fixture(t), `kind = "codex"`, `max_run_prompts = `+cap+"\n"+`kind = "codex"`, 1)); err != nil {
			t.Fatalf("valid profile cap rejected: %v", err)
		}
	}
	for _, limits := range []string{`max_timeout = "0s"`, `max_timeout = "-1h"`, `max_timeout = "bad"`, `max_timeout = "1h"`, `max_run_prompts = 0`, `max_run_prompts = -1`, `max_run_prompts = 101`} {
		if _, err := loadText(t, fixture(t)+"\n[limits]\n"+limits); err == nil {
			t.Fatalf("invalid limits accepted: %s", limits)
		}
	}
	for _, cap := range []string{"0", "-1", "101"} {
		if _, err := loadText(t, strings.Replace(fixture(t), `kind = "codex"`, `max_run_prompts = `+cap+"\n"+`kind = "codex"`, 1)); err == nil {
			t.Fatalf("invalid profile cap accepted: %s", cap)
		}
	}
}

func TestSecretSources(t *testing.T) {
	for _, source := range []string{`{env="EXAMPLE_SECRET"}`, `{file="/srv/secrets/review"}`} {
		if _, err := loadText(t, fixture(t)+"\n[secrets]\nreview = "+source+"\n"); err != nil {
			t.Fatalf("valid source: %v", err)
		}
	}
	for _, source := range []string{`"literal-fixture"`, `{value="literal-fixture"}`, `{value=""}`, `{value="",env="EXAMPLE_SECRET"}`, `""`, `{env="EXAMPLE_SECRET",file="/srv/secrets/review"}`, `{}`, `{env=""}`, `{env="BAD=NAME"}`, `{file="relative"}`} {
		if _, err := loadText(t, fixture(t)+"\n[secrets]\nreview = "+source+"\n"); err == nil {
			t.Fatalf("accepted source %s", source)
		}
	}
	for _, name := range []string{"'.'", "'..'", "'bad name'"} {
		if _, err := loadText(t, fixture(t)+"\n[secrets]\n"+name+` = {env="EXAMPLE_SECRET"}`); err == nil {
			t.Fatalf("accepted name %s", name)
		}
	}
}

func TestConfigWithoutProjectsLoadsWithNoLanes(t *testing.T) {
	text := strings.Replace(fixture(t), `project = "tasks"`, "", 1)
	text = strings.Replace(text, `actor = "runner"`, "", 1)
	c, err := loadText(t, text)
	if err != nil {
		t.Fatal(err)
	}
	if entries := c.ProjectEntries(); len(entries) != 0 {
		t.Fatalf("project entries = %+v, want none", entries)
	}
	if _, err := c.SelectProject(""); err == nil || !strings.Contains(err.Error(), "no projects are configured") {
		t.Fatalf("select without projects = %v", err)
	}
}

func TestHalfLegacyProjectNamesCurrentConfiguration(t *testing.T) {
	for _, line := range []string{`project = "tasks"`, `actor = "runner"`} {
		_, err := loadText(t, strings.Replace(fixture(t), line, "", 1))
		if err == nil || !strings.Contains(err.Error(), "add [[projects]] with name and actor") {
			t.Fatalf("without %s: %v", line, err)
		}
	}
}

func TestMissingConfigExplainsDiscovery(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "config.toml"))
	if err == nil || !strings.Contains(err.Error(), "herdr-kata up") || !strings.Contains(err.Error(), "live named Herdr session") {
		t.Fatal(err)
	}
}
