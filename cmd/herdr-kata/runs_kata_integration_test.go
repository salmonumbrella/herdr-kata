//go:build kata

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/testenv"
)

// Kata's show command accepts a ULID; the CLI's promised name filter must resolve it first.
func TestIntegRunsResolvesJobName(t *testing.T) {
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	home := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	k := kata.New(config.Kata{Bin: bin, Project: "example-project", Actor: "worker", Env: map[string]string{"KATA_HOME": home}}, home, nil, nil)
	raw := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, append(args, "--json", "--as", "worker")...)
		cmd.Env = append(k.Env(), "GOMAXPROCS=4")
		cmd.Dir = home
		data, err := cmd.Output()
		if err != nil {
			t.Fatalf("Kata %v: %v %s", args, err, data)
		}
		return data
	}
	raw("daemon", "start")
	t.Cleanup(func() { raw("daemon", "stop") })
	raw("projects", "create", "example-project")
	path := filepath.Join(home, "job.json")
	if err := os.WriteFile(path, []byte(`{"name":"Nightly review","definition":{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Review"},"issue":{"kind":"per-run","title":"Review"},"overlap":"forbid","catchup":"skip"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Job kata.Job `json:"job"`
	}
	if err := json.Unmarshal(raw("--project", "example-project", "cron", "job", "create", "--file", path), &saved); err != nil {
		t.Fatal(err)
	}
	admitted, _, err := k.Observe(t.Context(), "01K00000000000000000000000", kata.Observation{JobUID: saved.Job.UID, DefinitionEventUID: saved.Job.DefinitionEventUID, OccurrenceKey: "manual:test", Actor: "worker", ExecutorLabel: "worker", Status: "running", Summary: kata.Summary{Version: 1, Message: "admitted"}})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := listRuns(t.Context(), k, "Nightly review", 10, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), admitted.UID) || !strings.Contains(out.String(), saved.Job.UID) {
		t.Fatal(out.String())
	}
}
