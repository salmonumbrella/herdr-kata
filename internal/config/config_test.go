package config

import (
	"encoding/json"
	"fmt"
	"hegel.dev/go/hegel"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"
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

// Unknown project defaults quarantine the entry and identify the project and key.
func TestProjectDefaults(t *testing.T) {
	for _, tt := range []struct{ name, settings, wantReason string }{
		{"optional", "", ""},
		{"configured", "default_checkout = \"example-project\"\ndefault_agent = \"default\"\n", ""},
		{"unknown checkout", "default_checkout = \"missing-checkout\"\n", "missing-checkout"},
		{"unknown agent", "default_agent = \"missing-agent\"\n", "missing-agent"},
		{"unknown project key", "default_agnet = \"default\"\n", "unknown keys: default_agnet"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			text := strings.Replace(example, "commands = false\n", "commands = false\n"+tt.settings, 1)
			c, err := loadText(t, text)
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantReason == "" {
				if len(c.Projects) != 1 || len(c.Quarantined) != 0 {
					t.Fatalf("valid defaults quarantined: %+v", c)
				}
			} else if len(c.Projects) != 0 || len(c.Quarantined) != 1 || c.Quarantined[0].Name != "example-project" || !strings.Contains(c.Quarantined[0].Reason, tt.wantReason) {
				t.Fatalf("config = %+v, want project and %q", c, tt.wantReason)
			}
		})
	}
}

// Catch accepted typos/terminators and rejection of usable separate-value flags.
func TestProfileModelFlag(t *testing.T) {
	for _, tt := range []struct{ flag, wantError string }{
		{"", ""}, {"--model", ""}, {"-m", ""}, {"---", ""}, {"-模型", ""},
		{"model", "profile default model_flag must be a flag"},
		{"--with space", "profile default model_flag must be a flag"},
		{"--with\tspace", "profile default model_flag must be a flag"},
		{"--with\u2003space", "profile default model_flag must be a flag"},
		{"-", "profile default model_flag must be a flag"},
		{"--", "profile default model_flag must be a flag"},
		{"--model=", "profile default model_flag must be a flag"},
	} {
		t.Run(fmt.Sprintf("%q", tt.flag), func(t *testing.T) {
			_, err := loadText(t, example+fmt.Sprintf("model_flag = %q\n", tt.flag))
			if tt.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != tt.wantError {
				t.Fatalf("error %v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestModelFlagRejectsArgvTerminator(t *testing.T) {
	text := strings.Replace(example, `["codex", "--no-daemon"]`, `["codex", "--"]`, 1)
	_, err := loadText(t, text+`model_flag = "--model"`+"\n")
	if err == nil || err.Error() != "profile default model_flag cannot be used when argv ends in --" {
		t.Fatalf("error %v, want profile default model_flag cannot be used when argv ends in --", err)
	}
}

// The grammar is an operator contract, including unusual but valid flag names.
func TestModelFlagGrammar(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		flag := hegel.Draw(ht, hegel.Text())
		if hegel.Draw(ht, hegel.Integers(0, 1)) == 1 {
			flag = "-" + flag
		}
		encoded, _ := json.Marshal(flag)
		c, err := loadText(t, example+"model_flag = "+strings.ReplaceAll(string(encoded), "\x7f", `\u007f`)+"\n")
		valid := flag == "" || strings.HasPrefix(flag, "-") && flag != "-" && flag != "--"
		for _, r := range flag {
			if r == '=' || unicode.IsSpace(r) {
				valid = false
			}
		}
		if valid {
			if err != nil || c.Profiles["default"].ModelFlag != flag {
				ht.Fatalf("flag %q: config %v error %v", flag, c, err)
			}
		} else if err == nil || err.Error() != "profile default model_flag must be a flag" {
			ht.Fatalf("invalid flag %q: %v", flag, err)
		}
	})
}

// Keep the shrunk DEL case as valid TOML, so profile validation owns the error.
func TestModelFlagEscapedDEL(t *testing.T) {
	_, err := loadText(t, example+`model_flag = "\u007f"`+"\n")
	if err == nil || err.Error() != "profile default model_flag must be a flag" {
		t.Fatalf("error %v", err)
	}
}
