//go:build kata_replies && crashpoint

package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/crashpoint"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// Kill after Kata accepted C3 but before its UID is saved. A fresh queue must
// recover C3 by the frozen key, even though a fresh confirm would be a duplicate.
func TestIntegTypedChainCrash(t *testing.T) {
	f := typedKata(t)
	issue, e := f.k.CreateIssue(t.Context(), kata.CreateReq{Title: "Verify typed crash replay", IdempotencyKey: "issue"})
	if e != nil {
		t.Fatal(e)
	}
	previous := ""
	var root, maker string
	for _, kind := range []string{"", "reply", "refute"} {
		c, e := f.k.Comment(t.Context(), kata.CommentReq{Ref: issue.UID, Body: strings.Repeat("界", 40), IdempotencyKey: "before-" + kind, ReplyTo: previous, Kind: kind})
		if e != nil {
			t.Fatal(e)
		}
		previous = c.UID
		if kind == "" {
			root = c.UID
		}
		if kind == "reply" {
			maker = c.UID
		}
	}
	p := Payload{Ref: issue.UID, Body: "confirms c:" + maker + "\n\n" + strings.Repeat("界", 40), Mode: "typed", ReplyTo: maker, ReplyKind: "confirm", Chain: &ChainComment{Purpose: "checker", Target: maker, Assertion: "confirm"}}
	_, s, uid, _, _, _ := queueFixture(t, operation("confirm", "comment", p))
	if _, e = s.Transition(uid, func(r *Run) error {
		r.Findings = Findings{RootUID: root, HeadUID: previous, MakerUID: maker, Assertions: map[string][]string{maker: {"refute"}}}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if e = state.WriteJSON(state.OSFS{}, filepath.Join(s.layout.Root, "kata.json"), f.cfg.Kata); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTypedReplyCrashWorker$")
	cmd.Env = append(os.Environ(), "HK_TYPED_CRASH_ROOT="+s.layout.Root, "HK_TYPED_CRASH_UID="+uid, "GORACE=atexit_sleep_ms=0")
	out, e := cmd.CombinedOutput()
	var exit *exec.ExitError
	marker, readErr := os.ReadFile(filepath.Join(s.layout.Root, "typed-crash-point"))
	if !errors.As(e, &exit) || !strings.Contains(exit.Error(), "killed") || readErr != nil || string(marker) != "kata.comment.after" {
		t.Fatalf("did not reach crash window: %v %v %s", e, readErr, out)
	}
	r := loaded(t, s, uid)
	if r.Ops[0].FirstSent == nil || r.Ops[0].Done || r.Ops[0].CommentUID != "" {
		t.Fatal(r.Ops)
	}
	before, e := f.k.ShowIssue(ctx, issue.UID)
	if e != nil || len(before.Comments) != 4 {
		t.Fatal(before, e)
	}
	var confirmed string
	for _, c := range before.Comments {
		if c.ReplyKind == "confirm" {
			confirmed = c.UID
		}
	}
	q := NewQueue(f.k, NewStore(state.OSFS{}, s.layout), &testGuard{}, RealClock{})
	if e = q.send(ctx, uid); e != nil {
		t.Fatal(e)
	}
	r = loaded(t, s, uid)
	after, e := f.k.ShowIssue(ctx, issue.UID)
	if e != nil || len(after.Comments) != 4 || confirmed == "" || !r.Ops[0].Done || r.Ops[0].CommentUID != confirmed || r.Findings.HeadUID != confirmed || r.Inspect != "" {
		t.Fatal(r, after, e)
	}
	if kinds := r.Findings.Assertions[maker]; len(kinds) != 2 || kinds[0] != "refute" || kinds[1] != "confirm" {
		t.Fatal(kinds)
	}
}

func TestTypedReplyCrashWorker(t *testing.T) {
	root := os.Getenv("HK_TYPED_CRASH_ROOT")
	if root == "" {
		return
	}
	var cfg config.Kata
	if e := state.ReadJSON(state.OSFS{}, filepath.Join(root, "kata.json"), 1<<20, &cfg); e != nil {
		t.Fatal(e)
	}
	crashpoint.Hook = func(point string) {
		if point == "kata.comment.after" {
			if e := os.WriteFile(filepath.Join(root, "typed-crash-point"), []byte(point), 0600); e != nil {
				panic(e)
			}
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		}
	}
	q := NewQueue(kata.New(cfg, root, nil, nil), NewStore(state.OSFS{}, state.Layout{Root: root}), &testGuard{}, RealClock{})
	if e := q.send(t.Context(), os.Getenv("HK_TYPED_CRASH_UID")); e != nil {
		t.Fatal(e)
	}
	t.Fatal("crash hook did not run")
}
