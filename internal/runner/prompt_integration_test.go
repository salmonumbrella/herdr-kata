//go:build kata

package runner

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"github.com/salmonumbrella/herdr-kata/internal/testenv"
)

func TestIntegPromptRunEndToEndFake(t *testing.T) {
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	home := t.TempDir()
	layout := state.Layout{Root: t.TempDir()}
	h := herdrtest.New(t)
	cfg := config.Config{Executor: "prompt-installation", Kata: config.Kata{Bin: bin, Project: "prompt-test", Actor: "worker", Env: map[string]string{"KATA_HOME": home}}, Limits: config.Limits{MaxActiveRuns: 2, DefaultTimeout: 30 * time.Second}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}
	k := kata.New(cfg.Kata, layout.Root, nil, nil)
	raw := startKataDaemon(t, bin, home, k.Env(), cfg.Kata.Project)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	job := createJob(t, raw, home, "Prompt test", defs.JobDef{Version: 1, Kind: "job", Enabled: true, Trigger: defs.Trigger{Kind: "manual"}, Action: defs.Action{Kind: "execute", Prompt: "Inspect the task and report evidence"}, Issue: &defs.IssuePolicy{Kind: "per-run", Title: "Prompt result task"}, Overlap: "forbid", Catchup: "skip"})
	def, e := defs.DecodeJob(job.Definition)
	if e != nil {
		t.Fatal(e)
	}
	g := kata.NewRouteGuard(cfg, k, state.OSFS{}, layout)
	if e = g.Initialize(ctx); e != nil {
		t.Fatal(e)
	}
	s := NewStore(state.OSFS{}, layout)
	q := NewQueue(k, s, g, RealClock{})
	m := NewManager(cfg, k, &herdr.Client{Path: h.Path}, s, q, g, RealClock{})
	// This suite exercises Kata reporting; real Herdr probe coverage is separate.
	m.environment = cleanLaunch{}
	at := time.Now().UTC()
	due := sched.Due{Job: job, Def: def, Occurrence: at}
	uid := ident.RunUID(at, ident.ScheduledSource(job.UID, ident.OccurrenceKey(at)))
	h.Script("hk-"+strings.ToLower(uid), herdrtest.Script{Start: herdrtest.PendingReady, Prompt: []herdrtest.Outcome{herdrtest.Working}})
	queueCtx, queueCancel := context.WithCancel(ctx)
	queueDone := make(chan error, 1)
	go func() { queueDone <- q.Run(queueCtx) }()
	defer func() {
		queueCancel()
		m.Wait()
		if e := <-queueDone; e != nil && !errors.Is(e, context.Canceled) {
			t.Error(e)
		}
	}()
	start := time.Now()
	decision, e := m.Admit(ctx, due)
	if e != nil || decision != sched.Admitted {
		t.Fatal(decision, e)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("admission waited for execution")
	}
	waitForRun := func(f func(*Run) bool) *Run {
		t.Helper()
		for {
			r, e := s.Load(uid)
			if e != nil {
				t.Fatal(e)
			}
			if f(r) {
				return r
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	r := waitForRun(func(r *Run) bool { return r.Steps[0].Submission == Accepted })
	remote, e := k.ShowRun(ctx, uid)
	if e != nil || remote.Status != "running" || remote.IssueUID != r.IssueUID {
		t.Fatal(remote, e)
	}
	issue, e := k.ShowIssue(ctx, r.IssueUID)
	if e != nil || issue.Status != "open" {
		t.Fatal(issue, e)
	}
	resultPath := filepath.Join(layout.AttemptDir(uid, "main", 1), "result.json")
	if e = WriteResult(state.OSFS{}, resultPath, &Result{Version: 1, Run: uid, Step: "main", Attempt: 1, Status: "succeeded", Summary: "Inspected the prompt task and verified the integration assertions pass.", Evidence: []string{"test:go test -tags kata ./internal/run"}}); e != nil {
		t.Fatal(e)
	}
	a, e := m.h.AgentGet(ctx, r.Herdr.AgentName)
	if e != nil {
		t.Fatal(e)
	}
	a.Status = "idle"
	a.InteractiveReady = true
	seq := uint64(1)
	a.CompletionSeq = &seq
	h.SetAgent(a)
	waitForRun(func(r *Run) bool {
		if r.Phase != Settled {
			return false
		}
		for _, op := range r.Ops {
			if !op.Done {
				return false
			}
		}
		return true
	})
	m.Wait()
	remote, e = k.ShowRun(ctx, uid)
	if e != nil || remote.Status != "succeeded" || remote.EndedAt == nil || !strings.Contains(remote.Summary.Message, "integration assertions") {
		t.Fatal(remote, e)
	}
	issue, e = k.ShowIssue(ctx, r.IssueUID)
	if e != nil || issue.Status != "closed" {
		t.Fatal(issue, e)
	}
	starts, prompts, closes := 0, 0, 0
	for _, req := range h.Requests() {
		switch req.Method {
		case "agent.start":
			starts++
		case "agent.prompt":
			prompts++
		case "tab.close":
			closes++
		}
	}
	if starts != 1 || prompts != 1 || closes != 1 || m.GateHeld(job.UID) || m.FreeSlots() != 2 {
		t.Fatal(starts, prompts, closes)
	}
}
