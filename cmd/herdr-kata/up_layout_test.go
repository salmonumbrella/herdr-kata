package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestTakeoverRefusesIncompatibleLayoutBeforeStop(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", root)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/sessions/work/herdr.sock")
	cfg := `session = "work"
executor = "here"
[kata]
bin = "/usr/bin/true"
[[projects]]
name = "example-project"
actor = "worker"
[profiles.default]
kind = "codex"
`
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	l := state.Layout{Root: state.DefaultRoot()}
	if err := os.MkdirAll(l.Root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.StateFile(), []byte(`{"version":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"up", "--takeover"}, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "move the state directory aside and run herdr-kata up again") {
		t.Fatal(code, fmt.Sprint(&out), fmt.Sprint(&stderr))
	}
	if _, err := os.Stat(l.StopFile()); !os.IsNotExist(err) {
		t.Fatal("takeover stopped incompatible runtime", err)
	}
}
