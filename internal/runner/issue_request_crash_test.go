//go:build kata && crashpoint

package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/crashpoint"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/inbox"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestIssueRequestCrashBoundaries(t *testing.T) {
	for _, point := range []string{"prepared.save.after", "kata.meta.set.after", "admitted.save.after", "submission-sending.save.after", "settlement.save.after"} {
		t.Run(point, func(t *testing.T) {
			cfg, k, s, _, _, child := integrationRequest(t)
			if err := state.WriteJSON(state.OSFS{}, filepath.Join(s.layout.Root, "config.json"), cfg); err != nil {
				t.Fatal(err)
			}
			if err := state.WriteJSON(state.OSFS{}, filepath.Join(s.layout.Root, "child.json"), child); err != nil {
				t.Fatal(err)
			}
			run := func(boundary string) *exec.Cmd {
				ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
				t.Cleanup(cancel)
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIssueRequestCrashWorker$")
				cmd.Env = append(os.Environ(), "HK_REQUEST_CRASH_ROOT="+s.layout.Root, "HK_REQUEST_CRASH_POINT="+boundary)
				return cmd
			}
			worker := run(point)
			if err := worker.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = worker.Process.Kill() })
			deadline := time.After(30 * time.Second)
			tick := time.NewTicker(20 * time.Millisecond)
			defer tick.Stop()
			for {
				if data, _ := os.ReadFile(filepath.Join(s.layout.Root, "crashed")); string(data) == point {
					break
				}
				select {
				case <-deadline:
					_ = worker.Process.Kill()
					_ = worker.Wait()
					t.Fatal("boundary not reached")
				case <-tick.C:
				}
			}
			if err := worker.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := worker.Wait(); err == nil {
				t.Fatal("worker was not killed")
			}
			for range 2 {
				if output, err := run("").CombinedOutput(); err != nil {
					t.Fatalf("restart: %v\n%s", err, output)
				}
			}
			uid, err := ident.IssueRequestUID(child.UID)
			if err != nil {
				t.Fatal(err)
			}
			r := loaded(t, s, uid)
			count, readErr := os.ReadFile(filepath.Join(s.layout.RunDir(uid), "count"))
			if point == "submission-sending.save.after" {
				if r.Phase != Held || string(count) != "" || !os.IsNotExist(readErr) {
					t.Fatalf("ambiguous submission executed: %+v count=%q err=%v", r, count, readErr)
				}
			} else if r.Phase != Settled || r.Outcome != "succeeded" || string(count) != "once" || readErr != nil {
				t.Fatalf("crash result: %+v count=%q err=%v", r, count, readErr)
			}
			if _, err = k.ShowRun(t.Context(), uid); !kata.NotFound(err) {
				t.Fatal("issue request wrote cron history", err)
			}
		})
	}
}

func TestIssueRequestCrashWorker(t *testing.T) {
	root := os.Getenv("HK_REQUEST_CRASH_ROOT")
	if root == "" {
		return
	}
	l := state.Layout{Root: root}
	s := NewStore(state.OSFS{}, l)
	var cfg config.Config
	var child kata.IssueView
	if err := state.ReadJSON(state.OSFS{}, filepath.Join(root, "config.json"), 64<<10, &cfg); err != nil {
		t.Fatal(err)
	}
	if err := state.ReadJSON(state.OSFS{}, filepath.Join(root, "child.json"), 64<<10, &child); err != nil {
		t.Fatal(err)
	}
	k := kata.New(cfg.Kata, root, nil, nil)
	g := kata.NewRouteGuard(cfg, k, state.OSFS{}, l)
	q := NewQueue(k, s, g, RealClock{})
	m := NewManager(cfg, k, nil, s, q, g, RealClock{})
	crashpoint.Hook = func(point string) {
		if boundary := os.Getenv("HK_REQUEST_CRASH_POINT"); boundary != "" && point == boundary {
			if err := os.WriteFile(filepath.Join(root, "crashed"), []byte(point), 0600); err != nil {
				panic(err)
			}
			select {}
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- q.Run(ctx) }()
	defer func() { cancel(); <-done }()
	if err := m.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	uid, err := ident.IssueRequestUID(child.UID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Load(uid); os.IsNotExist(err) {
		issue, e := k.ShowIssue(ctx, child.UID)
		if e != nil {
			t.Fatal(e)
		}
		request, e := inbox.ValidateRequest(issue, issue.ProjectUID)
		if e != nil {
			t.Fatal(e)
		}
		if _, err = m.Admit(ctx, sched.Due{IssueRequest: &sched.IssueRequestDue{IssueUID: child.UID, IssueRef: child.UID, ParentUID: issue.ParentUID, Request: request}}); err != nil {
			t.Fatal(err)
		}
	}
	m.Wait()
	r := loaded(t, s, uid)
	if len(r.Ops) > 0 {
		if err = q.wait(ctx, uid, r.Ops[len(r.Ops)-1].ID); err != nil {
			t.Fatal(err)
		}
	}
}
