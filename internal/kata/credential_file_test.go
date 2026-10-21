package kata

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/config"
)

func TestCredentialFileIsReadOnlyForKataChildrenAndCanBeRepaired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	cfg := config.Kata{Bin: "/bin/kata", Project: "example-project", Actor: "worker", CredentialEnv: "KATA_LANE_TOKEN", CredentialFile: path}
	calls := 0
	c := New(cfg, t.TempDir(), func(_ context.Context, _ []string, env []string, _ string, _ []byte) ([]byte, []byte, int, error) {
		calls++
		if !slices.Contains(env, "KATA_LANE_TOKEN=fixture-token") {
			t.Error("Kata child did not receive file credential")
		}
		return []byte(`{"schema_version":34}`), nil, 0, nil
	}, nil)
	if _, err := c.Health(t.Context()); err == nil || !strings.Contains(err.Error(), "credential_file") || calls != 0 {
		t.Fatal("missing credential reached transport", err, calls)
	}
	if err := os.WriteFile(path, []byte("fixture-token\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Health(t.Context()); err == nil || calls != 0 {
		t.Fatal("public credential file accepted", err, calls)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Health(t.Context()); err != nil || calls != 1 {
		t.Fatal("repaired credential did not recover", err, calls)
	}
	if os.Getenv("KATA_LANE_TOKEN") != "" {
		t.Fatal("credential leaked into worker environment")
	}
	route := c.ResolveRoute()
	if strings.Contains(strings.Join(c.argv("health"), " "), "fixture-token") || len(route.Env) != 0 {
		t.Fatal("credential exposed in arguments or pin", route)
	}
}
