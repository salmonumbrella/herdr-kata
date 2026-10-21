//go:build kata

package tui

import (
	"context"
	"os/exec"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/testenv"
)

func TestIntegBindingGuardedSetAndRemove(t *testing.T) {
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	home := t.TempDir()
	c := cfg()
	c.Kata = config.Kata{Bin: bin, Project: "board-binding", Actor: "worker", Env: map[string]string{"KATA_HOME": home}}
	k := kata.New(c.Kata, home, nil, nil)
	command := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, append(args, "--json", "--as", "worker")...)
		cmd.Env = append(k.Env(), "GOMAXPROCS=2")
		cmd.Dir = home
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Kata %v: %v %s", args, err, out)
		}
	}
	command("daemon", "start")
	t.Cleanup(func() { command("daemon", "stop") })
	command("projects", "create", "board-binding")
	parent, err := k.CreateIssue(t.Context(), kata.CreateReq{Title: "Parent", IdempotencyKey: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	m := newParentForm(t.Context(), c, bindingCLI{k}, parent.UID, nil)
	m.Update(m.Init()())
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m.Update(cmd())
	after, err := k.ShowIssue(t.Context(), parent.UID)
	if err != nil || string(after.Metadata["herdr_kata.profile"]) != `"default"` {
		t.Fatalf("set failed: %s %v %+v", m.status, err, after.Metadata)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	_, cmd = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m.Update(cmd())
	after, err = k.ShowIssue(t.Context(), parent.UID)
	if _, ok := after.Metadata["herdr_kata.profile"]; err != nil || ok {
		t.Fatalf("unset failed: %s %v", m.status, err)
	}
}

type bindingCLI struct{ *kata.CLI }

func (k bindingCLI) MetaSet(ctx context.Context, ref, key, value string, c kata.Cond) error {
	_, err := k.CLI.MetaSet(ctx, ref, key, value, c)
	return err
}
func (k bindingCLI) MetaUnset(ctx context.Context, ref, key string, c kata.Cond) error {
	_, err := k.CLI.MetaUnset(ctx, ref, key, c)
	return err
}
