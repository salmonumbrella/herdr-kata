//go:build kata

package kata

import (
	"context"
	"os/exec"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"github.com/salmonumbrella/herdr-kata/internal/testenv"
)

func TestIntegRoutePinsAuthority(t *testing.T) {
	home := t.TempDir()
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	cfg := config.Config{Executor: "test", Kata: config.Kata{Bin: bin, Project: "worker-test", Actor: "worker", Env: map[string]string{"KATA_HOME": home}}}
	k := New(cfg.Kata, home, nil, nil)
	command := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), bin, args...)
		cmd.Env = append(k.Env(), "GOMAXPROCS=2")
		cmd.Dir = home
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
	command("daemon", "start")
	t.Cleanup(func() {
		cmd := exec.CommandContext(context.Background(), bin, "daemon", "stop")
		cmd.Env = append(k.Env(), "GOMAXPROCS=2")
		cmd.Dir = home
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("stop: %v %s", err, out)
		}
	})
	command("projects", "create", "worker-test", "--json")
	l := state.Layout{Root: t.TempDir()}
	g := NewRouteGuard(cfg, k, state.OSFS{}, l)
	if err := g.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := g.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := k.CheckCommands(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Executor = "changed"
	changed := NewRouteGuard(cfg, k, state.OSFS{}, l)
	if err := changed.Check(t.Context()); err == nil {
		t.Fatal("changed executor accepted")
	}
}
