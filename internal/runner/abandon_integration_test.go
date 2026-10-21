//go:build kata

package runner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"github.com/salmonumbrella/herdr-kata/internal/testenv"
)

func TestIntegAbandonReportsLostStateOnIssue(t *testing.T) {
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	home := t.TempDir()
	l := state.Layout{Root: t.TempDir()}
	cfg := config.Config{Executor: "installation", Kata: config.Kata{Bin: bin, Project: "example-project", Actor: "worker", Env: map[string]string{"KATA_HOME": home}}}
	k := kata.New(cfg.Kata, l.Root, nil, nil)
	raw := startKataDaemon(t, bin, home, k.Env(), cfg.Kata.Project)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	job := createJob(t, raw, home, "Lost-state record", defs.JobDef{Version: 1, Kind: "job", Enabled: true, Trigger: defs.Trigger{Kind: "manual"}, Action: defs.Action{Kind: "execute", Prompt: "Inspect the issue."}, Issue: &defs.IssuePolicy{Kind: "per-run", Title: "Lost-state issue"}, Overlap: "forbid", Catchup: "skip"})
	guard := kata.NewRouteGuard(cfg, k, state.OSFS{}, l)
	if err := guard.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	uid := ident.RunUID(time.Now(), "abandon-fixture")
	issue, err := k.CreateIssue(ctx, kata.CreateReq{Title: "Lost-state issue", IdempotencyKey: "herdr-kata:" + uid, Meta: map[string]string{"herdr_kata.run": uid, "work.attention": "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = k.Observe(ctx, uid, kata.Observation{JobUID: job.UID, DefinitionEventUID: job.DefinitionEventUID, OccurrenceKey: "manual:" + uid, IssueUID: issue.UID, Actor: "worker", ExecutorLabel: cfg.Executor, Status: "running", Summary: kata.Summary{Version: 1, Message: "admitted example-journal"}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Abandon(ctx, cfg, k, guard, state.OSFS{}, l, uid); err != nil {
			t.Fatal(err)
		}
	}
	remote, err := k.ShowRun(ctx, uid)
	if err != nil || remote.Status != "unknown" || remote.Summary.Message != "local state lost" {
		t.Fatal(remote, err)
	}
	issue, err = k.ShowIssue(ctx, issue.UID)
	if err != nil {
		t.Fatal(err)
	}
	var attention string
	if err := json.Unmarshal(issue.Metadata["work.attention"], &attention); err != nil || attention != "needs-human" || issue.Status != "open" {
		t.Fatal(issue, err)
	}
	comments := 0
	for _, comment := range issue.Comments {
		if strings.Contains(comment.Body, "local state lost") {
			comments++
		}
	}
	if comments != 1 {
		t.Fatal("loss comments", comments, issue.Comments)
	}
}
