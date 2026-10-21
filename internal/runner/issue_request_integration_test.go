//go:build kata

package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/inbox"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"github.com/salmonumbrella/herdr-kata/internal/testenv"
)

func integrationRequest(t *testing.T) (config.Config, *kata.CLI, *Store, *Queue, *Manager, kata.IssueView) {
	t.Helper()
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	home, root := t.TempDir(), t.TempDir()
	cfg := config.Config{Executor: "request-worker", Kata: config.Kata{Bin: bin, Project: "example-project", Actor: "worker", Inbox: "worker/inbox", Env: map[string]string{"KATA_HOME": home}}, Limits: config.Limits{MaxActiveRuns: 2, DefaultTimeout: time.Minute}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}
	k := kata.New(cfg.Kata, root, nil, nil)
	raw := startKataDaemon(t, bin, home, k.Env(), cfg.Kata.Project)
	parent, err := k.CreateIssue(t.Context(), kata.CreateReq{Title: "Request parent", Body: "Explicit command execution binding."})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "workflow.json")
	if err = os.WriteFile(path, []byte(`{"name":"Request command","definition":{"version":1,"steps":[{"key":"check","kind":"command","command":"printf once >> \"$HERDR_KATA_RUN_DIR/count\""}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Workflow kata.Workflow `json:"workflow"`
	}
	if err = json.Unmarshal(raw("cron", "workflow", "create", "--project", cfg.Kata.Project, "--file", path), &wf); err != nil {
		t.Fatal(err)
	}
	if _, err = k.MetaSet(t.Context(), parent.UID, "herdr_kata.workflow", wf.Workflow.UID, kata.Cond{}); err != nil {
		t.Fatal(err)
	}
	child, err := k.CreateIssue(t.Context(), kata.CreateReq{Title: "Explicit request", Body: "Run the bound command exactly once.", Parent: parent.UID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = k.MetaSet(t.Context(), child.UID, "herdr_kata.request", `{"version":1,"kind":"execute","request_uid":"01K00000000000000000000004","requested_at":"2026-01-02T03:04:05Z"}`, kata.Cond{JSONValue: true}); err != nil {
		t.Fatal(err)
	}
	if err = k.Notify(t.Context(), child.UID, cfg.Kata.Inbox, "Execute explicit request"); err != nil {
		t.Fatal(err)
	}
	if err = k.Notify(t.Context(), child.UID, "reviewer", "Preserve this human notification"); err != nil {
		t.Fatal(err)
	}
	l := state.Layout{Root: root}
	guard := kata.NewRouteGuard(cfg, k, state.OSFS{}, l)
	if err = guard.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := NewStore(state.OSFS{}, l)
	q := NewQueue(k, s, guard, RealClock{})
	m := NewManager(cfg, k, nil, s, q, guard, RealClock{})
	return cfg, k, s, q, m, child
}

func TestIntegIssueRequestCompetingRuntimes(t *testing.T) {
	cfg, k, s, q, m, child := integrationRequest(t)
	issue, err := k.ShowIssue(t.Context(), child.UID)
	if err != nil {
		t.Fatal(err)
	}
	request, err := inbox.ValidateRequest(issue, issue.ProjectUID)
	if err != nil {
		t.Fatal(err)
	}
	due := sched.Due{IssueRequest: &sched.IssueRequestDue{IssueUID: child.UID, IssueRef: child.UID, ParentUID: issue.ParentUID, Request: request}}
	uid, err := ident.IssueRequestUID(child.UID)
	if err != nil {
		t.Fatal(err)
	}
	other := cfg
	other.Executor = "other-request-worker"
	store := NewStore(state.OSFS{}, state.Layout{Root: t.TempDir()})
	guard := kata.NewRouteGuard(other, k, state.OSFS{}, store.layout)
	if err = guard.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	queue := NewQueue(k, store, guard, RealClock{})
	manager := NewManager(other, k, nil, store, queue, guard, RealClock{})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan error, 2)
	for _, worker := range []*Manager{m, manager} {
		input, e := worker.requestInputs(ctx, due, issue.ProjectUID)
		if e != nil {
			t.Fatal(e)
		}
		target, e := worker.pin()
		if e != nil {
			t.Fatal(e)
		}
		r, e := worker.prepare(input, uid, "issue-request:"+child.UID, target)
		if e != nil {
			t.Fatal(e)
		}
		if e = worker.store.Create(r); e != nil {
			t.Fatal(e)
		}
	}
	go func() { done <- q.Run(ctx) }()
	go func() { done <- queue.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done; <-done })
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, worker := range []*Manager{m, manager} {
		wg.Go(func() {
			r, e := worker.store.Load(uid)
			if e == nil {
				e = worker.admitPrepared(ctx, r)
			}
			results <- e
		})
	}
	wg.Wait()
	close(results)
	winners := 0
	for e := range results {
		if e == nil {
			winners++
		}
	}
	a, b := loaded(t, s, uid), loaded(t, store, uid)
	if winners != 1 || admitted(a) == admitted(b) {
		t.Fatalf("CAS winners=%d first=%s second=%s", winners, a.Phase, b.Phase)
	}
	remote, err := k.ShowIssue(ctx, child.UID)
	if err != nil {
		t.Fatal(err)
	}
	winner := a
	if admitted(b) {
		winner = b
	}
	if !requestReceiptMatches(winner, remote) {
		t.Fatal("authority does not retain winning receipt")
	}
}

func TestIntegExplicitIssueRequestExecutesWithoutCronHistory(t *testing.T) {
	cfg, k, s, q, m, child := integrationRequest(t)
	l := s.layout
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 2)
	caps, err := k.Capabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	scanner := inbox.New(cfg, k, caps, func(ctx context.Context, d sched.Due) {
		if _, err := m.Admit(ctx, d); err != nil {
			t.Error(err)
		}
	}, nil)
	go func() { done <- q.Run(ctx) }()
	go func() { done <- scanner.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done; <-done; m.Wait() })
	deadline := time.After(30 * time.Second)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	var run *Run
	for run == nil {
		ids, err := s.candidates()
		if err != nil {
			t.Fatal(err)
		}
		for _, uid := range ids {
			r, e := s.Load(uid)
			if e != nil {
				t.Fatal(e)
			}
			if r.Phase == Settled && head(r) == nil {
				run = r
			}
		}
		if run != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("request did not settle")
		case <-tick.C:
		}
	}
	if run.Version != 2 || run.Outcome != "succeeded" || run.IssueRequest == nil || run.Job.UID != "" {
		t.Fatalf("request result: %+v", run)
	}
	count, err := os.ReadFile(filepath.Join(l.RunDir(run.UID), "count"))
	if err != nil || string(count) != "once" {
		t.Fatalf("side effect %q: %v", count, err)
	}
	issue, err := k.ShowIssue(ctx, child.UID)
	if err != nil {
		t.Fatal(err)
	}
	var outcome RequestOutcome
	if err = json.Unmarshal(issue.Metadata["herdr_kata.outcome"], &outcome); err != nil {
		t.Fatal(err)
	}
	if issue.Status != "closed" || outcome.Outcome != "succeeded" || issue.Metadata["notify.cmV2aWV3ZXI"] == nil || issue.Metadata[run.IssueRequest.NotifyKey] != nil {
		t.Fatalf("report or acknowledgement: %+v", issue)
	}
	if _, err = k.ShowRun(ctx, run.UID); !kata.NotFound(err) {
		t.Fatalf("fabricated cron history: %v", err)
	}
	// A retained receipt vetoes admission even after the entire local journal is lost.
	if err = os.RemoveAll(l.RunDir(run.UID)); err != nil {
		t.Fatal(err)
	}
	result, err := m.Admit(ctx, sched.Due{IssueRequest: &sched.IssueRequestDue{IssueUID: child.UID, IssueRef: child.UID}})
	if err != nil || result != sched.Dropped {
		t.Fatalf("lost-state replacement: %v %v", result, err)
	}
}

func TestIntegIssueRequestLostJournalAbandonment(t *testing.T) {
	cfg, k, s, q, m, child := integrationRequest(t)
	issue, err := k.ShowIssue(t.Context(), child.UID)
	if err != nil {
		t.Fatal(err)
	}
	request, err := inbox.ValidateRequest(issue, issue.ProjectUID)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := ident.IssueRequestUID(child.UID)
	if err != nil {
		t.Fatal(err)
	}
	// Admit and track the child, while holding the executor outside this test.
	m.executions.Store(uid, struct{}{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- q.Run(ctx) }()
	if _, err = m.Admit(t.Context(), sched.Due{IssueRequest: &sched.IssueRequestDue{IssueUID: child.UID, IssueRef: child.UID, ParentUID: issue.ParentUID, Request: request}}); err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	cancel()
	<-done
	original := loaded(t, s, uid)
	if err = os.Rename(s.layout.RunDir(uid), filepath.Join(t.TempDir(), "preserved-run")); err != nil {
		t.Fatal(err)
	}
	lost, err := LostIssueRequests(t.Context(), k, s.fsys, s.layout, original.Target, cfg.Kata.Inbox)
	if err != nil || len(lost) != 1 {
		t.Fatal("lost request not detected", lost, err)
	}
	caps, err := k.Capabilities(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	scanCtx, stopScan := context.WithCancel(t.Context())
	scanner := inbox.New(cfg, k, caps, func(context.Context, sched.Due) { t.Error("lost request dispatched") }, nil)
	scanner.CheckRequest = m.CheckIssueRequest
	scanner.SaveSnapshot = func(snapshot inbox.Snapshot) error { return inbox.WriteSnapshot(s.fsys, s.layout.Root, snapshot) }
	scanDone := make(chan error, 1)
	go func() { scanDone <- scanner.Run(scanCtx) }()
	t.Cleanup(func() { stopScan(); <-scanDone })
	deadline := time.After(15 * time.Second)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		snapshot, e := inbox.ReadSnapshot(s.fsys, s.layout.Root)
		if e != nil {
			t.Fatal(e)
		}
		if len(snapshot.Requests) == 1 {
			row := snapshot.Requests[0]
			if row.Status != inbox.IgnoredRequest || !strings.Contains(row.Reason, "local state lost") || !strings.Contains(row.Reason, uid) {
				t.Fatalf("lost request status: %+v", row)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("missing lost request status")
		case <-tick.C:
		}
	}
	for range 2 {
		if err := Abandon(t.Context(), cfg, k, q.guard, s.fsys, s.layout, uid); err != nil {
			t.Fatal(err)
		}
	}
	issue, err = k.ShowIssue(t.Context(), child.UID)
	if err != nil {
		t.Fatal(err)
	}
	var outcome RequestOutcome
	if err = json.Unmarshal(issue.Metadata["herdr_kata.outcome"], &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.RunUID != uid || outcome.JournalID != original.JournalID || outcome.Phase != Settled || outcome.Outcome != "unknown" || outcome.Reason != "local state lost" {
		t.Fatalf("loss report: %+v", outcome)
	}
	if string(issue.Metadata["work.attention"]) != `"needs-human"` || issue.Metadata["notify.d29ya2VyL2luYm94"] != nil || issue.Metadata["notify.cmV2aWV3ZXI"] == nil {
		t.Fatal("loss report damaged notifications or attention", issue.Metadata)
	}
	if _, err = k.ShowRun(t.Context(), uid); !kata.NotFound(err) {
		t.Fatal("loss report wrote cron history", err)
	}
	lost, err = LostIssueRequests(t.Context(), k, s.fsys, s.layout, original.Target, cfg.Kata.Inbox)
	if err != nil || len(lost) != 0 {
		t.Fatal("completed loss still flagged", lost, err)
	}
}
