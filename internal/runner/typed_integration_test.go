//go:build kata_replies

package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

type typedFixture struct {
	k    *kata.CLI
	cfg  config.Config
	raw  func(...string) []byte
	argv func() [][]string
}

func typedKata(t *testing.T) typedFixture {
	t.Helper()
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	home := t.TempDir()
	cfg := config.Config{Executor: "typed-test", Kata: config.Kata{Bin: bin, Project: "example-project", Actor: "worker", Env: map[string]string{"KATA_HOME": home}}, Limits: config.Limits{MaxActiveRuns: 2, DefaultTimeout: time.Minute}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}
	var mu sync.Mutex
	var calls [][]string
	runner := func(ctx context.Context, argv, env []string, dir string, stdin []byte) ([]byte, []byte, int, error) {
		mu.Lock()
		calls = append(calls, append([]string(nil), argv...))
		mu.Unlock()
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Env = append(env, "GOMAXPROCS=2")
		cmd.Dir = dir
		cmd.Stdin = strings.NewReader(string(stdin))
		out, e := cmd.Output()
		if ee, ok := errors.AsType[*exec.ExitError](e); ok {
			return out, ee.Stderr, ee.ExitCode(), e
		}
		return out, nil, 0, e
	}
	k := kata.New(cfg.Kata, home, runner, nil)
	raw := startKataDaemon(t, bin, home, k.Env(), cfg.Kata.Project)
	return typedFixture{k: k, cfg: cfg, raw: raw, argv: func() [][]string { mu.Lock(); defer mu.Unlock(); return append([][]string(nil), calls...) }}
}

func TestIntegTypedLoopAndCheckerResumes(t *testing.T) {
	f := typedKata(t)
	l := state.Layout{Root: t.TempDir()}
	s := NewStore(state.OSFS{}, l)
	h := herdrtest.New(t)
	wf := defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "make", Kind: "command", Command: "true"}, {Key: "check", Kind: "prompt", Prompt: "Verify the maker claim", After: []string{"make"}, Options: json.RawMessage(`{"on_fail":{"goto":"make","max_loops":1}}`)}}}
	workflow := createWorkflow(t, f.raw, l.Root, "Typed loop", wf)
	def := defs.JobDef{Version: 1, Kind: "job", Enabled: true, Trigger: defs.Trigger{Kind: "manual"}, Action: defs.Action{Kind: "execute", WorkflowUID: workflow.UID}, Issue: &defs.IssuePolicy{Kind: "per-run", Title: "Verify typed finding chain"}, Overlap: "forbid", Catchup: "skip"}
	job := createJob(t, f.raw, l.Root, "Typed findings", def)
	g := kata.NewRouteGuard(f.cfg, f.k, state.OSFS{}, l)
	if e := g.Initialize(t.Context()); e != nil {
		t.Fatal(e)
	}
	q := NewQueue(f.k, s, g, RealClock{})
	m := NewManager(f.cfg, f.k, &herdr.Client{Path: h.Path}, s, q, g, RealClock{})
	m.environment = cleanLaunch{}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- q.Run(ctx) }()
	defer func() {
		cancel()
		m.Wait()
		if e := <-done; e != nil && !errors.Is(e, context.Canceled) {
			t.Error(e)
		}
	}()
	at := time.Now().UTC()
	uid := ident.RunUID(at, ident.ScheduledSource(job.UID, ident.OccurrenceKey(at)))
	h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method != "agent.prompt" {
			return
		}
		r, e := s.Load(uid)
		if e != nil {
			panic(e)
		}
		step := r.Steps[1]
		result := Result{Version: 1, Run: uid, Step: "check", Attempt: step.Attempt, Status: "failed", Summary: "Verified the maker claim against the focused parser checks."}
		if step.Attempt >= 2 {
			if step.FindingContext == nil || step.FindingContext.MakerUID == "" {
				panic("checker submitted before maker UID")
			}
			result.Target = step.FindingContext.MakerUID
			result.Assert = "confirm"
			if step.Attempt == 2 {
				result.Assert = "refute"
			}
		}
		if step.Attempt == 4 {
			result.Status = "succeeded"
			result.Evidence = []string{"test:focused parser checks"}
			result.CloseReason = "done"
		}
		if e = WriteResult(state.OSFS{}, filepath.Join(l.AttemptDir(uid, "check", step.Attempt), "result.json"), &result); e != nil {
			panic(e)
		}
	}
	decision, e := m.Admit(ctx, sched.Due{Job: job, Def: def, Workflow: &workflow, WFDef: &wf, Occurrence: at})
	if e != nil || decision != sched.Admitted {
		t.Fatal(decision, e)
	}
	drain := func() {
		t.Helper()
		m.Wait()
		r := loaded(t, s, uid)
		if len(r.Ops) > 0 {
			if e := q.wait(ctx, uid, r.Ops[len(r.Ops)-1].ID); e != nil {
				t.Fatal(e)
			}
		}
	}
	drain()
	for visit := 3; visit <= 4; visit++ {
		r := loaded(t, s, uid)
		if r.Phase != Held || r.Reason != "loop_exhausted" || r.Loop.Used != 1 {
			t.Fatalf("before checker %d: %+v", visit, r)
		}
		// Reopening the store and manager models restart between each visit. Recovery
		// drains existing operations and must never submit the previous checker again.
		m = NewManager(f.cfg, f.k, &herdr.Client{Path: h.Path}, NewStore(state.OSFS{}, l), q, g, RealClock{})
		m.environment = cleanLaunch{}
		before := len(h.Requests())
		if e := m.Recover(ctx); e != nil {
			t.Fatal(e)
		}
		m.Wait()
		for _, req := range h.Requests()[before:] {
			if req.Method == "agent.prompt" {
				t.Fatal("recovery resubmitted checker")
			}
		}
		if e := m.Resume(ctx, Request{UID: "checker-resume-" + string(rune(visit+48)), RunUID: uid}); e != nil {
			t.Fatal(e)
		}
		if e := m.Execute(ctx, uid); e != nil {
			t.Fatal(e)
		}
		drain()
	}
	r := loaded(t, s, uid)
	if r.Phase != Settled || r.Outcome != "succeeded" || r.Steps[0].Attempt != 2 || r.Steps[1].Attempt != 4 {
		t.Fatal(r)
	}
	var chain []Op
	for _, op := range r.Ops {
		p, _ := decodePayload(op)
		if p.Chain != nil {
			chain = append(chain, op)
		}
	}
	if len(chain) != 5 {
		t.Fatal(chain)
	}
	issue, e := f.k.ShowIssue(ctx, r.IssueUID)
	if e != nil {
		t.Fatal(e)
	}
	comments := map[string]kata.Comment{}
	for _, c := range issue.Comments {
		comments[c.UID] = c
	}
	wants := []struct{ target, kind string }{{"", ""}, {chain[0].CommentUID, "reply"}, {chain[1].CommentUID, "refute"}, {chain[1].CommentUID, "confirm"}, {chain[3].CommentUID, "reply"}}
	for i, want := range wants {
		c := comments[chain[i].CommentUID]
		if c.UID == "" || c.ReplyToUID != want.target || c.ReplyKind != want.kind {
			t.Fatalf("edge %d: %+v want %+v", i, c, want)
		}
	}
	if !strings.Contains(comments[chain[4].CommentUID].Body, "already published") || !strings.Contains(comments[chain[4].CommentUID].Body, chain[1].CommentUID) {
		t.Fatal(comments[chain[4].CommentUID])
	}
	var thread struct {
		Comments []kata.Comment `json:"comments"`
	}
	if e = json.Unmarshal(f.raw("show", r.IssueUID, "--thread", chain[0].CommentUID), &thread); e != nil || len(thread.Comments) != 5 {
		t.Fatal(thread, e)
	}
	for _, args := range f.argv() {
		for _, arg := range args {
			if arg == "--force" {
				t.Fatal(args)
			}
		}
	}
}

// An operation prepared before an upgrade must replay as the same plain
// comment, even when the current CLI and daemon both support typed replies.
func TestIntegPlainFindingSurvivesTypedUpgrade(t *testing.T) {
	f := typedKata(t)
	if supported, reason := f.k.TypedReplies(t.Context()); !supported {
		t.Fatal("expected merged Kata typed support", reason)
	}
	issue, err := f.k.CreateIssue(t.Context(), kata.CreateReq{Title: "Verify frozen plain finding", IdempotencyKey: "issue"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := f.k.Comment(t.Context(), kata.CommentReq{Ref: issue.UID, Body: "Initial checker finding", IdempotencyKey: "root"})
	if err != nil {
		t.Fatal(err)
	}
	maker, err := f.k.Comment(t.Context(), kata.CommentReq{Ref: issue.UID, Body: "The maker fixed the parser and verified the focused checks.", IdempotencyKey: "maker"})
	if err != nil {
		t.Fatal(err)
	}
	_, store, uid, _, _, _ := queueFixture(t)
	r, err := store.Transition(uid, func(r *Run) error {
		r.IssueRef = issue.UID
		r.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{
			{Key: "make", Kind: "command", Command: "true"},
			{Key: "check", Kind: "prompt", Prompt: "Verify", After: []string{"make"}, Options: json.RawMessage(`{"on_fail":{"goto":"make","max_loops":1}}`)},
		}}
		r.Steps = []StepState{{Key: "make", Attempt: 2, Status: "succeeded"}, {
			Key: "check", Attempt: 2, Status: "failed", FindingContext: &FindingContext{MakerUID: maker.UID},
			Result: &ResultRef{Summary: "The focused parser checks verify the maker's specific claim.", Assert: "confirm", Target: maker.UID},
		}}
		r.Findings = Findings{RootUID: root.UID, HeadUID: maker.UID, MakerUID: maker.UID}
		_, err := prepareFinding(r, 1, "plain")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	op := r.Ops[0]
	p, err := decodePayload(op)
	if err != nil {
		t.Fatal(err)
	}
	// Save the sending marker, publish, then reopen the queue without saving
	// the response. This models a plain write accepted just before a restart.
	if _, err = store.Transition(uid, func(r *Run) error {
		now := time.Now().UTC()
		r.Ops[0].FirstSent = &now
		r.Ops[0].Attempts = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	published, err := f.k.Comment(t.Context(), kata.CommentReq{Ref: p.Ref, Body: p.Body, IdempotencyKey: op.Key})
	if err != nil {
		t.Fatal(err)
	}
	q := NewQueue(f.k, NewStore(state.OSFS{}, store.layout), &testGuard{}, RealClock{})
	if err = q.send(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	r = loaded(t, store, uid)
	if r.Inspect != "" || !r.Ops[0].Done || r.Ops[0].CommentUID != published.UID || r.Findings.HeadUID != published.UID {
		t.Fatal("plain replay did not recover its original comment", r)
	}
	if kinds := r.Findings.Assertions[maker.UID]; len(kinds) != 1 || kinds[0] != "confirm" {
		t.Fatal("plain assertion was not recorded", kinds)
	}
	remote, err := f.k.ShowIssue(t.Context(), issue.UID)
	if err != nil || len(remote.Comments) != 3 {
		t.Fatal("plain replay created another comment", remote, err)
	}
	found := false
	for _, c := range remote.Comments {
		if c.UID != published.UID {
			continue
		}
		found = true
		if c.ReplyKind != "" || c.ReplyToUID != "" || !strings.Contains(c.Body, "confirms c:"+maker.UID) {
			t.Fatal("plain finding changed mode", c)
		}
	}
	if !found {
		t.Fatal("replayed plain comment is missing from the issue")
	}

}

// Release refuses a settled run whose close committed without its response
// being saved. The frozen close remains available for keyed replay without
// posting a handoff comment to the closed issue.
func TestIntegSettledReleasePreservesLostCloseReply(t *testing.T) {
	f := typedKata(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	issue, err := f.k.CreateIssue(ctx, kata.CreateReq{Title: "Verify release after a lost close response", IdempotencyKey: "issue"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := f.k.Comment(ctx, kata.CommentReq{Ref: issue.UID, Body: "Initial checker finding", IdempotencyKey: "root"})
	if err != nil {
		t.Fatal(err)
	}
	last, err := f.k.Comment(ctx, kata.CommentReq{Ref: issue.UID, Body: "Checker verified the maker claim and its focused parser checks.", ReplyTo: root.UID, Kind: "reply", IdempotencyKey: "head"})
	if err != nil {
		t.Fatal(err)
	}
	r := settleFixture()
	r.UID, r.Outcome = ident.RunUID(time.Now(), "lost-close"), "succeeded"
	r.IssueRef, r.IssueUID = issue.UID, issue.UID
	r.Findings = Findings{RootUID: root.UID, HeadUID: last.UID}
	previous := operation("head", "comment", Payload{Mode: "typed", Chain: &ChainComment{Mode: "typed", Purpose: "checker"}})
	previous.Done, previous.CommentUID = true, last.UID
	r.Ops = append([]Op{previous}, SettleOps(r, resultFixture(), time.Now())...)
	closeOp := &r.Ops[1]
	payload, err := decodePayload(*closeOp)
	if err != nil || payload.Close == nil {
		t.Fatal(payload, err)
	}
	// Publish the keyed close but save only its send marker, modeling the
	// state left behind when the close response is lost.
	now := time.Now().UTC()
	closeOp.FirstSent, closeOp.Attempts = &now, 1
	closeReq := *payload.Close
	closeReq.IdempotencyKey = closeOp.Key
	if err = f.k.Close(ctx, closeReq); err != nil {
		t.Fatal(err)
	}
	store := NewStore(state.OSFS{}, state.Layout{Root: t.TempDir()})
	if err = store.Create(r); err != nil {
		t.Fatal(err)
	}
	q := NewQueue(f.k, store, &testGuard{}, RealClock{})
	m := NewManager(f.cfg, f.k, nil, store, q, &testGuard{}, RealClock{})
	if err = m.Release(ctx, r.UID); !errors.Is(err, errControlRefused) {
		t.Fatal(err)
	}
	// The refused release leaves the frozen close available for reconciliation.
	if err = q.send(ctx, r.UID); err != nil {
		t.Fatal(err)
	}
	saved := loaded(t, store, r.UID)
	if saved.Inspect != "" || saved.Phase != Settled || saved.Findings.HeadUID != last.UID || !saved.Ops[1].Done || len(saved.Ops) != len(r.Ops) || string(saved.Ops[1].Payload) != string(closeOp.Payload) {
		t.Fatal("release changed settled evidence or prevented close reconciliation", saved)
	}
	remote, err := f.k.ShowIssue(ctx, issue.UID)
	if err != nil || remote.Status != "closed" || len(remote.Comments) != 2 {
		t.Fatal(remote, err)
	}
}
