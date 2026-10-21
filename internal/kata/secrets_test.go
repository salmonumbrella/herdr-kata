package kata

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/config"
)

// Job source variables must not ride the inherited XDG allowlist
// into Kata. The separately selected authentication credential remains available.
func TestSecretSourceEnvironmentExcludedFromKata(t *testing.T) {
	t.Setenv("XDG_SERVICE_TOKEN", "fixture-job-secret")
	t.Setenv("XDG_AUTH_TOKEN", "fixture-auth")
	path := filepath.Join(t.TempDir(), "config.toml")
	text := `session="work"
executor="worker"
[kata]
bin="/bin/kata"
project="example-project"
actor="worker"
credential_env="XDG_AUTH_TOKEN"
env={KATA_HOME="/tmp/example"}
[profiles.default]
kind="codex"
[secrets]
review={env="XDG_SERVICE_TOKEN"}
`
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	view := cfg.View(cfg.ProjectEntries()[0])
	for _, entry := range []struct {
		name string
		cfg  config.Kata
	}{{"loaded", cfg.Kata}, {"project-view", view.Kata}} {
		t.Run(entry.name, func(t *testing.T) {
			calls := 0
			cli := New(entry.cfg, t.TempDir(), func(_ context.Context, _ []string, env []string, _ string, _ []byte) ([]byte, []byte, int, error) {
				calls++
				auth := false
				for _, pair := range env {
					if strings.HasPrefix(pair, "XDG_SERVICE_TOKEN=") {
						t.Error("job secret source reached Kata")
					}
					auth = auth || pair == "XDG_AUTH_TOKEN=fixture-auth"
				}
				if !auth {
					t.Error("selected authentication credential was stripped")
				}
				return []byte(`{"project_uid":"project","event_features":["cron_v1"]}`), nil, 0, nil
			}, nil)
			if _, err := cli.Capabilities(t.Context()); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatal("Kata transport not exercised", calls)
			}
		})
	}
}
