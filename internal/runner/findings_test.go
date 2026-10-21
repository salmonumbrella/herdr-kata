package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

func findingQueue(t *testing.T, f *executionFixture) *fakeKata {
	t.Helper()
	if _, e := f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = time.Now().Add(30 * time.Second); return nil }); e != nil {
		t.Fatal(e)
	}
	var mu sync.Mutex
	comments := []kata.Comment{}
	keys := map[string]kata.Comment{}
	k := &fakeKata{}
	k.commentResult = func(_ context.Context, _, body, key string) (kata.Comment, error) {
		mu.Lock()
		defer mu.Unlock()
		if c, ok := keys[key]; ok {
			return c, nil
		}
		c := kata.Comment{UID: fmt.Sprintf("c%d", len(comments)+1), Body: body}
		keys[key] = c
		comments = append(comments, c)
		return c, nil
	}
	k.show = func(context.Context, string) (kata.IssueView, error) {
		mu.Lock()
		defer mu.Unlock()
		return kata.IssueView{Comments: append([]kata.Comment(nil), comments...)}, nil
	}
	k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) { return kata.Run{}, false, nil }
	q := NewQueue(k, f.s, &testGuard{}, RealClock{})
	f.m.q = q
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- q.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		f.m.Wait()
		if e := <-done; e != nil && e != context.Canceled {
			t.Error(e)
		}
	})
	return k
}
func TestLoopWorkflowSecondVisitAndFindingKeys(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "make", Kind: "command", Command: `echo make >> visits`}, defs.Step{Key: "check", Kind: "command", After: []string{"make"}, Command: `echo check >> visits; n=$(wc -l < visits); [ "$n" -ge 6 ]`, Options: json.RawMessage(`{"on_fail":{"goto":"make","max_loops":3}}`)}, defs.Step{Key: "publish", Kind: "command", After: []string{"check"}, Command: `echo publish >> visits`})
	findingQueue(t, f)
	// This verifies five findings and a plain terminal report. Leave enough
	// execution budget for serialized writes on a busy shared host.
	if _, err := f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = time.Now().Add(2 * time.Minute); return nil }); err != nil {
		t.Fatal(err)
	}
	executeWorkflow(t, f)
	if e := f.m.q.wait(t.Context(), f.uid, "herdr-kata:"+f.uid+":settle:comment"); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	if r.Outcome != "succeeded" || r.Loop.Used != 2 || r.Steps[0].Attempt != 3 || r.Steps[1].Attempt != 3 {
		for _, step := range r.Steps {
			t.Logf("step %s receipt: %+v", step.Key, step.Receipt)
		}
		t.Fatalf("phase %s reason %s loop %+v steps %+v", r.Phase, r.Reason, r.Loop, r.Steps)
	}
	b, e := os.ReadFile(filepath.Join(r.Cwd, "visits"))
	if e != nil || string(b) != "make\ncheck\nmake\ncheck\nmake\ncheck\npublish\n" {
		t.Fatal(string(b), e)
	}
	prev := ""
	count := 0
	keys := map[string]bool{}
	for _, op := range r.Ops {
		p, _ := decodePayload(op)
		if p.Chain == nil {
			continue
		}
		count++
		if keys[op.Key] || p.Chain.Target != prev || op.CommentUID == "" || !op.Done {
			t.Fatal(op, p)
		}
		keys[op.Key] = true
		prev = op.CommentUID
	}
	if count != 5 || r.Findings.RootUID == "" || r.Findings.HeadUID != prev {
		t.Fatal(count, r.Findings)
	}
}
func TestFindingUncertainWriteSerializesPreparation(t *testing.T) {
	f := loopWorkflow(t)
	saveVerdict(t, f, "")
	r, e := f.s.Transition(f.uid, func(r *Run) error { _, e := prepareFinding(r, 1); return e })
	if e != nil {
		t.Fatal(e)
	}
	id := r.Ops[0].ID
	q := NewQueue(&fakeKata{}, f.s, &testGuard{}, RealClock{})
	calls := 0
	q.k.(*fakeKata).commentResult = func(context.Context, string, string, string) (kata.Comment, error) {
		calls++
		if calls == 1 {
			return kata.Comment{}, lostReply
		}
		return kata.Comment{UID: "recovered"}, nil
	}
	if e = q.send(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Transition(f.uid, func(r *Run) error {
		r.Steps[0].Status = "succeeded"
		r.Findings.RootUID = "old-root"
		_, e := prepareFinding(r, 0)
		return e
	}); e != errChainPending {
		t.Fatal("prepared over uncertain write", e)
	}
	if _, e = f.s.Transition(f.uid, func(r *Run) error { r.Ops[0].NextAt = time.Time{}; return nil }); e != nil {
		t.Fatal(e)
	}
	if e = q.send(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	r = loaded(t, f.s, f.uid)
	op, _ := findOp(r, id)
	if !op.Done || op.CommentUID != "recovered" || r.Findings.HeadUID != "recovered" || calls != 2 {
		t.Fatal(op, r.Findings, calls)
	}
	if _, e = f.s.Transition(f.uid, func(r *Run) error { _, e := prepareFinding(r, 0); return e }); e != nil {
		t.Fatal(e)
	}
	r = loaded(t, f.s, f.uid)
	p, _ := decodePayload(r.Ops[1])
	if p.Chain.Target != "recovered" {
		t.Fatal(p)
	}
}
func TestCheckerWaitsForMakerUID(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "make", Kind: "command", Command: "true"}, defs.Step{Key: "check", Kind: "prompt", Prompt: "check", After: []string{"make"}, Options: json.RawMessage(`{"on_fail":{"goto":"make"}}`)})
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	k := findingQueue(t, f)
	original := k.commentResult
	k.commentResult = func(ctx context.Context, ref, body, key string) (kata.Comment, error) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-ctx.Done():
			return kata.Comment{}, ctx.Err()
		}
		return original(ctx, ref, body, key)
	}
	// An already committed rewind has a root and a newly completed maker.
	if _, e := f.s.Transition(f.uid, func(r *Run) error {
		r.Findings = Findings{RootUID: "root", HeadUID: "root"}
		r.Loop.Used = 1
		r.Steps[0].Status = "succeeded"
		_, e := prepareFinding(r, 0)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	f.m.q.Kick(f.uid)
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := f.m.executePrompt(ctx, f.uid, 1); done <- e }()
	// Awaiting the write must leave the checker entirely unlaunched.
	select {
	case e := <-done:
		t.Fatal("checker returned before maker UID", e)
	case <-time.After(30 * time.Millisecond):
	}
	if f.count("agent.prompt") != 0 || f.count("agent.start") != 0 || len(loaded(t, f.s, f.uid).Ops) != 1 {
		t.Fatal("checker passed comment barrier")
	}
	cancel()
	if e := <-done; e != context.Canceled {
		t.Fatal(e)
	}
	close(release)
	f.h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method != "agent.prompt" {
			return
		}
		r, e := f.s.Load(f.uid)
		if e != nil {
			panic(e)
		}
		s := r.Steps[1]
		if s.FindingContext == nil || s.FindingContext.MakerUID == "" {
			panic("checker submitted without maker UID")
		}
		if e = WriteResult(f.s.fsys, filepath.Join(f.s.layout.AttemptDir(f.uid, s.Key, s.Attempt), "result.json"), &Result{Version: 1, Run: f.uid, Step: s.Key, Attempt: s.Attempt, Status: "succeeded"}); e != nil {
			panic(e)
		}
	}
	res, e := f.m.executePrompt(t.Context(), f.uid, 1)
	if e != nil || res == nil || f.count("agent.prompt") != 1 {
		t.Fatal(res, e)
	}
	r := loaded(t, f.s, f.uid)
	if r.Findings.MakerUID == "" || r.Steps[1].FindingContext.MakerUID != r.Findings.MakerUID {
		t.Fatal(r.Findings, r.Steps[1])
	}
}
func TestFindingContinuationTargets(t *testing.T) {
	f := Findings{RootUID: "root", HeadUID: "refutation", MakerUID: "maker", Assertions: map[string][]string{"maker": {"refute"}}}
	for _, tc := range []struct{ kind, target, assertion string }{{"", "refutation", ""}, {"confirm", "maker", "confirm"}, {"refute", "refutation", ""}} {
		target, kind := findingTarget(f, tc.kind)
		if target != tc.target || kind != tc.assertion {
			t.Fatal(target, kind)
		}
	}
}
func TestLoopPromptReferencesAndFreshAgents(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "make", Kind: "prompt", Prompt: "make"}, defs.Step{Key: "check", Kind: "prompt", Prompt: "check", After: []string{"make"}, Options: json.RawMessage(`{"on_fail":{"goto":"make","max_loops":2}}`)})
	findingQueue(t, f)
	f.h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method != "agent.prompt" {
			return
		}
		r, e := f.s.Load(f.uid)
		if e != nil {
			panic(e)
		}
		for _, s := range r.Steps {
			if s.Submission != Sending || s.Status != "" {
				continue
			}
			status, note := "succeeded", ""
			if s.Key == "check" && s.Attempt < 3 {
				status = "failed"
				note = fmt.Sprintf("fix the defect %d", s.Attempt)
			}
			e = WriteResult(f.s.fsys, filepath.Join(f.s.layout.AttemptDir(r.UID, s.Key, s.Attempt), "result.json"), &Result{Version: 1, Run: r.UID, Step: s.Key, Attempt: s.Attempt, Status: status, Summary: "Focused verification of the maker claim.", Note: note})
			if e != nil {
				panic(e)
			}
		}
	}
	r := executeWorkflow(t, f)
	for _, old := range r.PastAttempts {
		if old.FindingContext != nil {
			t.Fatal("archived attempt retained a finding snapshot")
		}
	}
	if r.Outcome != "succeeded" || r.Loop.Used != 2 || f.count("agent.start") != 5 || f.count("agent.prompt") != 6 {
		t.Fatalf("phase %s reason %s outcome %s loop %+v starts %d prompts %d", r.Phase, r.Reason, r.Outcome, r.Loop, f.count("agent.start"), f.count("agent.prompt"))
	}
	for _, key := range []string{"make", "check"} {
		b, e := os.ReadFile(filepath.Join(f.s.layout.AttemptDir(r.UID, key, 2), "prompt.md"))
		if e != nil || !strings.Contains(string(b), "Loop 1/2") || !strings.Contains(string(b), "root c:") || !strings.Contains(string(b), "fix the defect") {
			t.Fatal(string(b), e)
		}
	}
}

func TestCheckerContinuationUsesLastChecker(t *testing.T) {
	f := loopWorkflow(t)
	saveVerdict(t, f, "")
	findingQueue(t, f)
	if e := f.m.publishFinding(t.Context(), f.uid, 1); e != nil {
		t.Fatal(e)
	}
	first := loaded(t, f.s, f.uid).Findings.HeadUID
	if _, e := f.s.Transition(f.uid, func(r *Run) error { r.Steps[1].Attempt++; r.Steps[1].Status = "succeeded"; return nil }); e != nil {
		t.Fatal(e)
	}
	if e := f.m.publishFinding(t.Context(), f.uid, 1); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	p, _ := decodePayload(r.Ops[1])
	if p.Chain.Target != first || r.Findings.RootUID != first || r.Findings.HeadUID == first {
		t.Fatal(r.Findings, p)
	}
}
func TestFindingContextBoundAndFrozen(t *testing.T) {
	f := loopWorkflow(t)
	saveVerdict(t, f, "")
	k := findingQueue(t, f)
	if e := f.m.publishFinding(t.Context(), f.uid, 1); e != nil {
		t.Fatal(e)
	}
	k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{Comments: []kata.Comment{{UID: "unrelated", Body: "private unrelated thread"}, {UID: "c1", Body: strings.Repeat("x", 17<<10)}}}, nil
	}
	if e := f.m.prepareFindingContext(t.Context(), f.uid, 0); e != nil {
		t.Fatal(e)
	}
	fc := loaded(t, f.s, f.uid).Steps[0].FindingContext
	if !fc.Truncated || len(fc.Text) > 16<<10 || strings.Contains(fc.Text, "unrelated") {
		t.Fatal(fc)
	}
	k.show = func(context.Context, string) (kata.IssueView, error) {
		t.Error("reread frozen context")
		return kata.IssueView{}, nil
	}
	if e := f.m.prepareFindingContext(t.Context(), f.uid, 0); e != nil {
		t.Fatal(e)
	}
}
func TestFindingWaitDeadline(t *testing.T) {
	f := loopWorkflow(t)
	saveVerdict(t, f, "")
	if _, e := f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = time.Now().Add(30 * time.Millisecond); return nil }); e != nil {
		t.Fatal(e)
	}
	k := &fakeKata{}
	f.m.q = NewQueue(k, f.s, &testGuard{}, RealClock{})
	if e := f.m.publishFinding(t.Context(), f.uid, 1); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	if r.Phase != Held || r.Reason != "timeout" || len(r.Ops) != 1 || r.Ops[0].Done {
		t.Fatal(r)
	}
}

func TestFindingContextExactBudget(t *testing.T) {
	f := loopWorkflow(t)
	k := findingQueue(t, f)
	if _, e := f.s.Transition(f.uid, func(r *Run) error {
		r.Findings = Findings{RootUID: "a", HeadUID: "b"}
		for _, uid := range []string{"a", "b"} {
			op := operation(uid, "comment", Payload{Chain: &ChainComment{}})
			op.Done = true
			op.CommentUID = uid
			r.Ops = append(r.Ops, op)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{Comments: []kata.Comment{{UID: "a", Body: strings.Repeat("x", 8187)}, {UID: "b", Body: strings.Repeat("y", 8187)}}}, nil
	}
	if e := f.m.prepareFindingContext(t.Context(), f.uid, 0); e != nil {
		t.Fatal(e)
	}
	fc := loaded(t, f.s, f.uid).Steps[0].FindingContext
	if len(fc.Text) > 16<<10 || !fc.Truncated {
		t.Fatal("context exceeded its declared bound", len(fc.Text), fc.Truncated)
	}
}
func TestFindingReadRetryAndDeadline(t *testing.T) {
	for _, outage := range []bool{false, true} {
		t.Run(fmt.Sprint(outage), func(t *testing.T) {
			f := loopWorkflow(t)
			k := findingQueue(t, f)
			if _, e := f.s.Transition(f.uid, func(r *Run) error { r.Findings = Findings{RootUID: "root", HeadUID: "root"}; return nil }); e != nil {
				t.Fatal(e)
			}
			f.m.clock = &activityClock{at: time.Now()}
			calls := 0
			k.show = func(context.Context, string) (kata.IssueView, error) {
				calls++
				if outage || calls == 1 {
					return kata.IssueView{}, lostReply
				}
				return kata.IssueView{}, nil
			}
			if e := f.m.prepareFindingContext(t.Context(), f.uid, 0); e != nil {
				t.Fatal(e)
			}
			r := loaded(t, f.s, f.uid)
			if outage {
				if r.Reason != "timeout" || r.Phase != Held {
					t.Fatal(r)
				}
			} else if calls != 2 || r.Phase == Held || r.Steps[0].FindingContext == nil {
				t.Fatal(r, calls)
			}
		})
	}
}

func TestLoopReturnsToExitedSharedAgent(t *testing.T) {
	f := workflowExecution(t,
		defs.Step{Key: "make", Kind: "prompt", Prompt: "make"},
		defs.Step{Key: "check", Kind: "command", After: []string{"make"}, Command: `if [ -f checked ]; then exit 0; fi; touch checked; exit 1`, Options: json.RawMessage(`{"on_fail":{"goto":"make"}}`)},
		defs.Step{Key: "finish", Kind: "prompt", Prompt: "finish", After: []string{"check"}},
	)
	findingQueue(t, f)
	f.h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method != "agent.prompt" {
			return
		}
		r, e := f.s.Load(f.uid)
		if e != nil {
			panic(e)
		}
		for _, s := range r.Steps {
			if s.Submission != Sending || s.Status != "" {
				continue
			}
			if s.Key == "make" && s.Attempt == 2 {
				if e = f.m.h.TabClose(t.Context(), r.Herdr.TabID); e != nil {
					panic(e)
				}
			}
			e = WriteResult(f.s.fsys, filepath.Join(f.s.layout.AttemptDir(r.UID, s.Key, s.Attempt), "result.json"), &Result{Version: 1, Run: r.UID, Step: s.Key, Attempt: s.Attempt, Status: "succeeded", Summary: "Verified the current workflow step."})
			if e != nil {
				panic(e)
			}
		}
	}
	r := executeWorkflow(t, f)
	if r.Outcome != "succeeded" || f.count("agent.start") != 3 || f.count("agent.prompt") != 3 || r.Steps[2].Status != "succeeded" {
		t.Fatalf("phase=%s reason=%s starts=%d prompts=%d", r.Phase, r.Reason, f.count("agent.start"), f.count("agent.prompt"))
	}
}

// Finding context is frozen within an attempt, never across a retry. Seed a
// saved chain directly: current definitions disallow retries inside loop spans.
func TestRetryRefreshesFindingContext(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "make", Kind: "prompt", Prompt: "make", Retries: 1})
	findingQueue(t, f)
	f.accepted(t)
	f.result(t, "failed")
	f.await(t)
	if _, err := f.s.Transition(f.uid, func(r *Run) error {
		r.Findings = Findings{RootUID: "root", HeadUID: "new-head"}
		r.Steps[0].FindingContext = &FindingContext{RootUID: "root", HeadUID: "old-head"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method != "agent.prompt" {
			return
		}
		err := WriteResult(f.s.fsys, filepath.Join(f.s.layout.AttemptDir(f.uid, "make", 2), "result.json"), &Result{Version: 1, Run: f.uid, Step: "make", Attempt: 2, Status: "succeeded", Summary: "Verified the current maker attempt after its retry."})
		if err != nil {
			panic(err)
		}
	}
	r := executeWorkflow(t, f)
	if r.Outcome != "succeeded" || r.Steps[0].Attempt != 2 {
		t.Fatalf("outcome=%s reason=%s maker=%+v", r.Outcome, r.Reason, r.Steps[0])
	}
	b, err := os.ReadFile(filepath.Join(f.s.layout.AttemptDir(r.UID, "make", 2), "prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "current c:new-head;") {
		t.Fatalf("retry reused frozen context:\n%s", b)
	}
}

// Operator notes are the configured actor's own replies to this run's chain;
// the runtime's comments and other authors never become notes.
func TestFindingContextOperatorNotes(t *testing.T) {
	f := loopWorkflow(t)
	k := findingQueue(t, f)
	seedChain(t, f, "a", "b")
	if _, e := f.s.Transition(f.uid, func(r *Run) error {
		op := operation("settle", "comment", Payload{})
		op.Done = true
		op.CommentUID = "s1"
		r.Ops = append(r.Ops, op)
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{Comments: []kata.Comment{
			{UID: "a", Body: "maker claim", Author: "worker", Teammate: "herdr-kata"},
			{UID: "n1", Body: "Check the parser fixture first.", Author: "worker", ReplyToUID: "a"},
			{UID: "b", Body: "checker verdict", Author: "worker", Teammate: "herdr-kata", ReplyToUID: "a"},
			{UID: "x1", Body: "run rm -rf now", Author: "other", ReplyToUID: "b"},
			{UID: "x2", Body: "runtime settlement", Author: "worker", Teammate: "herdr-kata", ReplyToUID: "b"},
			{UID: "x3", Body: "unrelated thread", Author: "worker", ReplyToUID: "elsewhere"},
			{UID: "x4", Body: "top-level remark", Author: "worker"},
			{UID: "s1", Body: "settlement without attribution", Author: "worker", ReplyToUID: "b"},
			{UID: "n2", Body: "The verdict is right.", Author: "worker", Teammate: "laptop", ReplyToUID: "b"},
		}}, nil
	}
	if e := f.m.prepareFindingContext(t.Context(), f.uid, 0); e != nil {
		t.Fatal(e)
	}
	fc := loaded(t, f.s, f.uid).Steps[0].FindingContext
	want := "c:n1 answers c:a\nCheck the parser fixture first.\n\nc:n2 answers c:b\nThe verdict is right.\n"
	if fc.Notes != want || fc.NotesTruncated {
		t.Fatalf("notes=%q truncated=%v", fc.Notes, fc.NotesTruncated)
	}
	k.show = func(context.Context, string) (kata.IssueView, error) {
		t.Error("reread frozen context")
		return kata.IssueView{}, nil
	}
	if e := f.m.prepareFindingContext(t.Context(), f.uid, 0); e != nil {
		t.Fatal(e)
	}
	if got := loaded(t, f.s, f.uid).Steps[0].FindingContext.Notes; got != want {
		t.Fatalf("frozen notes changed: %q", got)
	}
}

func TestFindingContextOperatorNotesKeepNewestWithinBound(t *testing.T) {
	f := loopWorkflow(t)
	k := findingQueue(t, f)
	seedChain(t, f, "a")
	comments := []kata.Comment{{UID: "a", Body: "claim", Author: "worker", Teammate: "herdr-kata"}}
	for i := 1; i <= 12; i++ {
		comments = append(comments, kata.Comment{UID: fmt.Sprintf("n%02d", i), Body: fmt.Sprintf("note %d", i), Author: "worker", ReplyToUID: "a"})
	}
	k.show = func(context.Context, string) (kata.IssueView, error) { return kata.IssueView{Comments: comments}, nil }
	if e := f.m.prepareFindingContext(t.Context(), f.uid, 0); e != nil {
		t.Fatal(e)
	}
	fc := loaded(t, f.s, f.uid).Steps[0].FindingContext
	if !fc.NotesTruncated || strings.Contains(fc.Notes, "note 2\n") || !strings.HasPrefix(fc.Notes, "c:n03 answers c:a\nnote 3\n") || !strings.HasSuffix(fc.Notes, "c:n12 answers c:a\nnote 12\n") {
		t.Fatalf("notes=%q truncated=%v", fc.Notes, fc.NotesTruncated)
	}

	f = loopWorkflow(t)
	k = findingQueue(t, f)
	seedChain(t, f, "a")
	k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{Comments: []kata.Comment{
			{UID: "a", Body: "claim", Author: "worker", Teammate: "herdr-kata"},
			{UID: "n1", Body: "short older note", Author: "worker", ReplyToUID: "a"},
			{UID: "n2", Body: strings.Repeat("y", 4<<10), Author: "worker", ReplyToUID: "a"},
		}}, nil
	}
	if e := f.m.prepareFindingContext(t.Context(), f.uid, 0); e != nil {
		t.Fatal(e)
	}
	fc = loaded(t, f.s, f.uid).Steps[0].FindingContext
	if fc.Notes != "c:n1 answers c:a\nshort older note\n" || !fc.NotesTruncated {
		t.Fatalf("notes=%q truncated=%v", fc.Notes, fc.NotesTruncated)
	}
}

func seedChain(t *testing.T, f *executionFixture, uids ...string) {
	t.Helper()
	if _, e := f.s.Transition(f.uid, func(r *Run) error {
		r.Target.Identity.Actor = "worker"
		r.Findings = Findings{RootUID: uids[0], HeadUID: uids[len(uids)-1]}
		for _, uid := range uids {
			op := operation(uid, "comment", Payload{Chain: &ChainComment{}})
			op.Done = true
			op.CommentUID = uid
			r.Ops = append(r.Ops, op)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
