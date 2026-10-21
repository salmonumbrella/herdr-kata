package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaneCredentialFileLoadsAndStaysOutOfRoutingEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(path, []byte("fixture-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	text := fmt.Sprintf(`session="work"
executor="worker"
[kata]
bin="/bin/kata"
[[projects]]
name="example-project"
actor="worker"
credential_env="KATA_LANE_TOKEN"
credential_file=%q
[profiles.default]
kind="codex"
`, path)
	cfg, err := loadText(t, text)
	if err != nil {
		t.Fatal("supported hook credential rejected", err)
	}
	view := cfg.View(cfg.ProjectEntries()[0])
	value, err := view.Kata.Credential()
	if err != nil || value != "fixture-token" {
		t.Fatal("file credential not resolved", err)
	}
	if len(view.Kata.Env) != 0 {
		t.Fatal("credential became a pinned route value")
	}
	for _, bad := range []string{"relative", ""} {
		broken := strings.Replace(text, fmt.Sprintf("credential_file=%q", path), fmt.Sprintf("credential_file=%q", bad), 1)
		if bad == "" {
			broken = strings.Replace(text, "credential_env=\"KATA_LANE_TOKEN\"", "credential_env=\"\"", 1)
		}
		if _, err := loadText(t, broken); err == nil {
			t.Fatal("unsafe credential configuration accepted", bad)
		}
	}
}
