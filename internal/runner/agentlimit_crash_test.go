//go:build crashpoint

package runner

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/crashpoint"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestAgentLimitCrashBeforeHold(t *testing.T) {
	f, _, _ := limitExecution(t, "usage_limit_exceeded")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAgentLimitCrashWorker$")
	cmd.Env = append(os.Environ(), "HK_LIMIT_ROOT="+f.s.layout.Root, "HK_LIMIT_SOCKET="+f.h.Path, "HK_LIMIT_UID="+f.uid, "GORACE=atexit_sleep_ms=0")
	out, e := cmd.CombinedOutput()
	if _, ok := errors.AsType[*exec.ExitError](e); !ok {
		t.Fatal("worker did not crash", e, string(out))
	}
	marker, e := os.ReadFile(filepath.Join(f.s.layout.Root, "limit-crashed"))
	if e != nil || string(marker) != "classified" {
		t.Fatal("crash missed classification", e, string(out))
	}
	if r := loaded(t, f.s, f.uid); r.Phase == Held {
		t.Fatal("hold saved before crash")
	}
	for range 2 {
		if e := f.m.Recover(t.Context()); e != nil {
			t.Fatal(e)
		}
		f.m.Wait()
	}
	r := loaded(t, f.s, f.uid)
	if r.Phase != Held || r.Reason != "agent_quota" || f.count("agent.prompt") != 1 {
		t.Fatal("classification did not recover once", r.Phase, r.Reason)
	}
	observations := 0
	for _, op := range r.Ops {
		if op.Kind == "observe" {
			observations++
		}
	}
	if observations != 1 {
		t.Fatalf("settled %d times", observations)
	}
}

func TestAgentLimitCrashWorker(t *testing.T) {
	root := os.Getenv("HK_LIMIT_ROOT")
	if root == "" {
		return
	}
	store := NewStore(state.OSFS{}, state.Layout{Root: root})
	clock := &activityClock{at: time.Date(2026, 9, 24, 16, 0, 0, 0, time.UTC)}
	m := NewManager(config.Config{}, nil, &herdr.Client{Path: os.Getenv("HK_LIMIT_SOCKET")}, store, nil, nil, clock)
	crashpoint.Hook = func(point string) {
		if point != "agent-limit.classified" {
			return
		}
		if e := os.WriteFile(filepath.Join(root, "limit-crashed"), []byte("classified"), 0600); e != nil {
			panic(e)
		}
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	}
	if e := m.execute(t.Context(), os.Getenv("HK_LIMIT_UID")); e != nil {
		t.Fatal(e)
	}
	t.Fatal("classification boundary not reached")
}
