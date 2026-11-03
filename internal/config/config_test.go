package config

import (
	"fmt"
	"hegel.dev/go/hegel"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const example = `executor = "work-runtime"
hc_bin = "/bin/sh"
[kata]
bin = "/bin/sh"
daemon = "hub"
[kata.env]
KATA_HOME = "/srv/kata-home"
[[projects]]
name = "example-project"
actor = "worker"
commands = false
max_active_runs = 1
[checkouts]
example-project = "/srv/projects/example"
[profiles.default]
argv = ["codex", "--no-daemon"]
`

func loadText(t *testing.T, text string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

// Protect the published example and the defaults operators can omit.
func TestLoadSpecExample(t *testing.T) {
	c, err := loadText(t, example)
	if err != nil {
		t.Fatal(err)
	}
	if c.HCSession != "default" || c.Limits.DefaultTimeout != 2*time.Hour || c.Limits.MaxTimeout != 24*time.Hour || c.Limits.MaxActiveRuns != 4 {
		t.Fatalf("defaults: %+v", c)
	}
	if c.Projects[0].Actor != "worker" || c.Checkouts["example-project"] != "/srv/projects/example" || c.Profiles["default"].Argv[1] != "--no-daemon" {
		t.Fatalf("example decoded incorrectly: %+v", c)
	}
}

func TestRejectsUnknownKey(t *testing.T) {
	for _, text := range []string{"typo = true\n" + example, example + "typo = true\n"} {
		if _, err := loadText(t, text); err == nil {
			t.Fatal("accepted unknown key")
		}
	}
}

func TestRejectsRelativePaths(t *testing.T) {
	for _, abs := range []string{"/bin/sh", "/srv/kata-home", "/srv/projects/example"} {
		if _, err := loadText(t, strings.ReplaceAll(example, abs, "relative")); err == nil {
			t.Fatalf("accepted relative path replacing %s", abs)
		}
	}
}

func TestKataEnvAllowlist(t *testing.T) {
	for _, key := range []string{"KATA_AUTH_TOKEN", "PATH", "KATA_PROJECT"} {
		if _, err := loadText(t, strings.ReplaceAll(example, "KATA_HOME", key)); err == nil {
			t.Fatalf("accepted %s", key)
		}
	}
	for _, key := range []string{"KATA_SERVER", "XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR"} {
		if _, err := loadText(t, strings.ReplaceAll(example, "KATA_HOME", key)); err != nil {
			t.Fatalf("rejected %s: %v", key, err)
		}
	}
}

func TestProfileArgvRequired(t *testing.T) {
	for _, value := range []string{"[]", "[\"\"]"} {
		if _, err := loadText(t, strings.ReplaceAll(example, `["codex", "--no-daemon"]`, value)); err == nil {
			t.Fatal("accepted empty profile")
		}
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := DefaultPath(); got != filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "herdr-kata", "config.toml") {
		t.Fatalf("path: %s", got)
	}
}

// Generated positive limits must survive strict TOML decoding exactly.
func TestConfiguredLimitsPreserved(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		n := hegel.Draw(ht, hegel.Integers(1, 1<<31-1))
		c, err := loadText(t, example+fmt.Sprintf("\n[limits]\nmax_active_runs = %d\n", n))
		if err != nil {
			ht.Fatal(err)
		}
		if c.Limits.MaxActiveRuns != n {
			ht.Fatalf("capacity %d became %d", n, c.Limits.MaxActiveRuns)
		}
	})
}

// Project defaults must be usable mappings, with errors identifying the project and key.
func TestProjectDefaults(t *testing.T) {
	for _, tt := range []struct{ name, settings, wantError string }{
		{"optional", "", ""},
		{"configured", "default_checkout = \"example-project\"\ndefault_agent = \"default\"\n", ""},
		{"unknown checkout", "default_checkout = \"missing-checkout\"\n", "missing-checkout"},
		{"unknown agent", "default_agent = \"missing-agent\"\n", "missing-agent"},
		{"unknown project key", "default_agnet = \"default\"\n", "unknown configuration keys"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			text := strings.Replace(example, "commands = false\n", "commands = false\n"+tt.settings, 1)
			_, err := loadText(t, text)
			if tt.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantError) || (tt.name != "unknown project key" && !strings.Contains(err.Error(), "example-project")) {
				t.Fatalf("error = %v, want project and %q", err, tt.wantError)
			}
		})
	}
}
