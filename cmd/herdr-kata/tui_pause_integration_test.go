//go:build kata

package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os/exec"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"github.com/salmonumbrella/herdr-kata/internal/testenv"
)

func TestIntegBoardPauseReturnsSavedDefinitionAndGuard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	cfg := config.Config{Executor: "worker", Kata: config.Kata{Bin: bin, Project: "example-project", Actor: "worker", Env: map[string]string{"KATA_HOME": home}}}
	k := kata.New(cfg.Kata, home, nil, nil)
	command := func(ctx context.Context, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = append(k.Env(), "GOMAXPROCS=4")
		cmd.Dir = home
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Kata %v: %v: %s", args, err, out)
		}
	}
	command(t.Context(), "daemon", "start")
	t.Cleanup(func() { command(context.Background(), "daemon", "stop") })
	command(t.Context(), "projects", "create", "example-project", "--json")
	layout := state.Layout{Root: t.TempDir()}
	guard := kata.NewRouteGuard(cfg, k, state.OSFS{}, layout)
	if err := guard.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := &tuiService{CLI: k, cfg: cfg, guard: guard, ctx: t.Context()}
	original, err := s.SaveJob(t.Context(), kata.Job{UID: ident.NewRunUID(time.Now(), rand.Reader), Name: "example-job", Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"work"},"issue":{"kind":"per-run","title":"Example"},"overlap":"forbid","catchup":"latest"}`)}, "")
	if err != nil {
		t.Fatal(err)
	}
	paused, err := s.Pause(original)
	if err != nil {
		t.Fatal(err)
	}
	var d struct{ Enabled bool }
	if err = json.Unmarshal(paused.Definition, &d); err != nil || d.Enabled || paused.DefinitionEventUID == "" || paused.DefinitionEventUID == original.DefinitionEventUID {
		t.Fatalf("pause returned stale state: %+v: %v", paused, err)
	}
	resumed, err := s.Pause(paused)
	if err != nil {
		t.Fatal(err)
	}
	after, err := k.ShowJob(t.Context(), original.UID)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(after.Definition, &d); err != nil || !d.Enabled || after.DefinitionEventUID != resumed.DefinitionEventUID || after.DefinitionEventUID == paused.DefinitionEventUID {
		t.Fatalf("resume did not use saved guard: %+v: %v", after, err)
	}
}
