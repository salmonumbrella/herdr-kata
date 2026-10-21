package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// Contract §21.4: opted-in failures stop at the failed step and keep the gate.
func TestWorkflowParkFailure(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "fail", Kind: "command", Command: "exit 7", Retries: 1}, defs.Step{Key: "later", Kind: "command", Command: "touch later"})
	_, e := f.s.Transition(f.uid, func(r *Run) error { r.WFDef.Options = json.RawMessage(`{"on_failure":"park"}`); return nil })
	if e != nil {
		t.Fatal(e)
	}
	r := executeWorkflow(t, f)
	if r.Phase != Held || r.Outcome != "failed" || r.Reason != "step_failed" || r.Steps[0].Attempt != 2 || r.Steps[1].Submission != None {
		t.Fatalf("park: %+v", r)
	}
	if _, e := os.Stat(filepath.Join(r.Cwd, "later")); !os.IsNotExist(e) {
		t.Fatal("later step launched", e)
	}
}

func TestResumeRequestRequiresHeldRun(t *testing.T) {
	f := newAdmission(t)
	r, e := f.m.prepare(f.d, f.uid, "occurrence", Target{})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.Create(r); e != nil {
		t.Fatal(e)
	}
	req := Request{Version: 1, UID: "resume-request", Kind: "resume", RunUID: f.uid}
	if e = state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.RequestsDir(), req.UID+".json"), req); e != nil {
		t.Fatal(e)
	}
	if e = f.m.HandleRequests(context.Background(), nil); e != nil {
		t.Fatal(e)
	}
	var result RequestResult
	if e = state.ReadJSON(f.s.fsys, RequestResultPath(f.s.layout, req.UID), 4096, &result); e != nil || !strings.Contains(result.Error, "held") {
		t.Fatal(result, e)
	}
	if loaded(t, f.s, f.uid).Steps[0].Attempt != 1 {
		t.Fatal("refused request changed attempts")
	}
}

// A stopped parked command can continue; its own forbid gate is not a rival.
func TestResumeStoppedCommandAndReplay(t *testing.T) {
	f := newAdmission(t)
	f.d.WFDef.Options = json.RawMessage(`{"on_failure":"park"}`)
	f.d.WFDef.Steps = []defs.Step{{Key: "main", Kind: "command", Command: `if [ -f once ]; then true; else touch once; exit 7; fi`}}
	// Prepare directly because admission starts execution only after observation.
	target, e := f.m.pin()
	if e != nil {
		t.Fatal(e)
	}
	r, e := f.m.prepare(f.d, f.uid, "occurrence", target)
	if e != nil {
		t.Fatal(e)
	}
	r.Phase = Admitted
	r.Admitted = new(true)
	r.IssueUID = "issue"
	r.IssueRef = "project#task"
	r.IssueOwned = false
	if e = f.s.Create(r); e != nil {
		t.Fatal(e)
	}
	if e = f.m.RestoreGates(); e != nil {
		t.Fatal(e)
	}
	if e = f.m.Execute(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	f.k.comment = func(context.Context, string, string, string) error { return nil }
	f.k.notify = func(_ context.Context, _, to, _ string) error {
		if to != "operator" {
			t.Errorf("wrong recipient %s", to)
		}
		return nil
	}
	f.k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{UID: "issue", ProjectUID: "project", Status: "open", Author: "operator"}, nil
	}
	k := &resumeKata{admissionKata: f.k, ready: []kata.IssueView{{UID: "issue"}}}
	f.m.k = k
	f.q.k = k
	for head(loaded(t, f.s, f.uid)) != nil {
		deliver(t, f.q, f.uid)
	}
	req := Request{Version: 1, UID: "resume-request", Kind: "resume", RunUID: f.uid}
	publishResume(t, f, req)
	f.start(t)
	if e = f.m.HandleRequests(t.Context(), nil); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r = loaded(t, f.s, f.uid)
	if r.Phase != Settled || r.Outcome != "succeeded" || r.Steps[0].Attempt != 2 || r.ExpiresAt.Equal(time.Time{}) {
		t.Fatal(r)
	}
	seen := map[string]bool{}
	for _, op := range r.Ops {
		if seen[op.ID] {
			t.Fatal("duplicate", op.ID)
		}
		seen[op.ID] = true
	}
	b, _ := json.Marshal(r)
	if !strings.Contains(string(b), req.UID) {
		t.Fatal("request consumption absent")
	}
	// Redelivery after a subsequent park must not allocate an attempt.
	_, e = f.s.Transition(f.uid, func(r *Run) error { r.Phase = Held; r.Reason = "step_failed"; return nil })
	if e != nil {
		t.Fatal(e)
	}
	publishResume(t, f, req)
	if e = f.m.HandleRequests(t.Context(), nil); e != nil {
		t.Fatal(e)
	}
	if loaded(t, f.s, f.uid).Steps[0].Attempt != 2 {
		t.Fatal("replayed resume allocated an attempt")
	}
}

type resumeKata struct {
	*admissionKata
	ready []kata.IssueView
}

func (k *resumeKata) Ready(context.Context) ([]kata.IssueView, error) { return k.ready, nil }
func publishResume(t *testing.T, f *admissionFixture, req Request) {
	t.Helper()
	if e := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.RequestsDir(), req.UID+".json"), req); e != nil {
		t.Fatal(e)
	}
}

func heldResumeFixture(t *testing.T) *admissionFixture {
	t.Helper()
	f := newAdmission(t)
	target, e := f.m.pin()
	if e != nil {
		t.Fatal(e)
	}
	r, e := f.m.prepare(f.d, f.uid, "occurrence", target)
	if e != nil {
		t.Fatal(e)
	}
	r.Phase = Held
	r.Admitted = new(true)
	r.Reason = "step_failed"
	r.WFDef.Options = json.RawMessage(`{"on_failure":"park"}`)
	r.Outcome = "failed"
	r.ActiveStep = "main"
	r.IssueUID = "issue"
	r.IssueRef = "project#task"
	r.IssueOwned = false
	r.Ops = nil
	r.SlotReleased = true
	now := f.m.clock.Now()
	r.StartedAt = &now
	r.EndedAt = &now
	r.Steps[0].Status = "failed"
	r.Steps[0].Submission = Accepted
	receipt, e := RunCommand(t.Context(), CommandSpec{Run: f.uid, Step: "main", Attempt: 1, Command: "exit 7", Cwd: t.TempDir(), AttemptDir: t.TempDir(), Deadline: now.Add(time.Minute)})
	if receipt.Version != 1 || receipt.ExitCode != 7 || !GroupGone(receipt.PGID) {
		t.Fatal(receipt, e)
	}
	r.Steps[0].Receipt = &receipt
	r.Ops = []Op{newOp("old-observe", "observe", Payload{Observation: &kata.Observation{Status: "failed", ExpectedRevision: 4}}, "")}
	r.Ops[0].Done = true
	if e = f.s.Create(r); e != nil {
		t.Fatal(e)
	}
	f.k.comment = func(context.Context, string, string, string) error { return nil }
	f.k.notify = func(_ context.Context, _, to, _ string) error {
		if to != "operator" {
			t.Errorf("wrong recipient %s", to)
		}
		return nil
	}
	f.k.show = func(_ context.Context, ref string) (kata.IssueView, error) {
		if ref != "project#task" {
			t.Fatalf("wrong issue %s", ref)
		}
		return kata.IssueView{UID: "issue", ProjectUID: "project", Status: "open", Author: "operator"}, nil
	}
	k := &resumeKata{admissionKata: f.k, ready: []kata.IssueView{{UID: "issue"}}}
	f.m.k = k
	f.q.k = k
	if e = f.m.RestoreGates(); e != nil {
		t.Fatal(e)
	}
	return f
}

func TestResumeCapacityWaitsAndRechecksExistingReservation(t *testing.T) {
	f := heldResumeFixture(t)
	f.m.cfg.Limits.MaxActiveRuns = 1
	other := *loaded(t, f.s, f.uid)
	other.UID = "other"
	other.Job.UID = "another-job"
	other.SlotReleased = false
	if e := f.s.Create(&other); e != nil {
		t.Fatal(e)
	}
	if e := f.m.RestoreGates(); e != nil {
		t.Fatal(e)
	}
	req := Request{Version: 1, UID: "request", Kind: "resume", RunUID: f.uid}
	publishResume(t, f, req)
	if e := f.m.HandleRequests(t.Context(), nil); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	if r.Steps[0].Attempt != 1 || r.Resume != nil {
		t.Fatal("capacity oversubscribed", r)
	}
	if _, e := os.Stat(filepath.Join(f.s.layout.RequestsDir(), "request.json")); e != nil {
		t.Fatal("capacity wait dropped request", e)
	}
	_, e := f.s.Transition("other", func(r *Run) error { r.Phase = Settled; return nil })
	if e != nil {
		t.Fatal(e)
	}
	f.start(t)
	if e = f.m.HandleRequests(t.Context(), nil); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	if loaded(t, f.s, f.uid).Steps[0].Attempt != 2 {
		t.Fatal("resume did not progress after capacity freed")
	}
}

func TestResumeRefusesChangedIssueAndInspection(t *testing.T) {
	for _, reason := range []string{"closed", "reassigned", "different issue", "not ready", "inspect", "quarantine", "handed off", "settled", "missing timeout"} {
		t.Run(reason, func(t *testing.T) {
			f := heldResumeFixture(t)
			switch reason {
			case "closed", "reassigned", "different issue":
				f.s.Transition(f.uid, func(r *Run) error { r.Child = &ChildRun{}; return nil })
				f.k.show = func(context.Context, string) (kata.IssueView, error) {
					i := kata.IssueView{UID: "issue", ProjectUID: "project", Status: "open", Owner: "worker"}
					switch reason {
					case "closed":
						i.Status = "closed"
					case "reassigned":
						i.Owner = "someone-else"
					default:
						i.UID = "another-issue"
					}
					return i, nil
				}
			case "not ready":
				f.m.k.(*resumeKata).ready = nil
			case "inspect":
				f.s.Transition(f.uid, func(r *Run) error { r.Inspect = "report conflict"; return nil })
			case "handed off":
				f.s.Transition(f.uid, func(r *Run) error { r.Phase = HandedOff; return nil })
			case "settled":
				f.s.Transition(f.uid, func(r *Run) error { r.Phase = Settled; return nil })
			case "missing timeout":
				f.s.Transition(f.uid, func(r *Run) error { r.Timeout = 0; return nil })
			case "quarantine":
				if e := state.QuarantineRun(f.s.fsys, f.s.layout, f.uid, f.s.layout.RunFile(f.uid)); e != nil {
					t.Fatal(e)
				}
			}
			req := Request{Version: 1, UID: "request", Kind: "resume", RunUID: f.uid}
			publishResume(t, f, req)
			if e := f.m.HandleRequests(t.Context(), nil); e != nil {
				t.Fatal(e)
			}
			var result RequestResult
			if e := state.ReadJSON(f.s.fsys, RequestResultPath(f.s.layout, req.UID), 4096, &result); e != nil || result.Error == "" {
				t.Fatal("not refused", result, e)
			}
			if reason != "quarantine" {
				if loaded(t, f.s, f.uid).Steps[0].Attempt != 1 {
					t.Fatal("refusal allocated attempt")
				}
			}
		})
	}
}

func TestResumeRunningObservationBarrierAndRecovery(t *testing.T) {
	f := heldResumeFixture(t)
	clock := &testClock{at: time.Now().UTC()}
	f.m.clock = clock
	// Freeze ten minutes, then change the live config to prove it is not used.
	f.s.Transition(f.uid, func(r *Run) error { r.Timeout = 10 * time.Minute; return nil })
	f.m.cfg.Limits.DefaultTimeout = time.Second
	req := Request{Version: 1, UID: "request", Kind: "resume", RunUID: f.uid}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	// No side of acceptance is written before authorization gets its lock.
	if e := f.m.Resume(ctx, req); e == nil {
		t.Fatal("cancelled acceptance succeeded")
	}
	if loaded(t, f.s, f.uid).Resume != nil {
		t.Fatal("acceptance happened before lock")
	}
	resumeUntilQueued(t, f, req)
	r := loaded(t, f.s, f.uid)
	if r.Phase != Held || r.EndedAt == nil || r.Resume == nil || !r.Resume.Pending || r.SlotReleased || r.Resume.Generation != 1 || r.Resume.Step != "main" || r.Resume.Attempt != 2 || !r.ExpiresAt.Equal(clock.at.Add(10*time.Minute)) {
		t.Fatalf("non-atomic acceptance %+v", r)
	}
	if len(r.ConsumedResumes) != 1 || r.ConsumedResumes[0] != req.UID {
		t.Fatal("consumption", r)
	}
	f.k.observe = func(_ context.Context, uid string, o kata.Observation) (kata.Run, bool, error) {
		current := loaded(t, f.s, uid)
		if o.Status != "running" || o.EndedAt != nil || !sameTime(o.StartedAt, current.StartedAt) || o.ExpectedRevision != 5 || current.Phase != Held || !current.Resume.Pending {
			t.Errorf("activation before observation %+v %+v", o, current)
		}
		return kata.Run{}, false, nil
	}
	// Resume recovery finishes the same attempt even with no control file.
	deliver(t, f.q, f.uid)
	f.k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) { return kata.Run{}, false, nil }
	f.s = NewStore(f.s.fsys, f.s.layout)
	f.m.store = f.s
	f.q.store = f.s
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	if e := f.m.HandleRequests(t.Context(), nil); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r = loaded(t, f.s, f.uid)
	if r.Phase != Settled || r.Steps[0].Attempt != 2 || r.Resume.Pending || r.Resume.Generation != 1 {
		t.Fatal(r)
	}
	for _, op := range r.Ops {
		if strings.Contains(op.ID, ":settle:") && !strings.Contains(op.ID, ":resume:request:settle:") {
			t.Fatal("settlement ID omitted consumed UID", op.ID)
		}
		if op.Kind == "comment" && !strings.Contains(op.Key, ":resume:request:") {
			t.Fatal("key omitted request", op.Key)
		}
	}
}

func TestResumeRefusesWorkingAgentEvenWithReleasedSlot(t *testing.T) {
	f := newExecution(t)
	f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Working}})
	f.accepted(t)
	a := newAdmission(t)
	target, e := a.m.pin()
	if e != nil {
		t.Fatal(e)
	}
	f.s.Transition(f.uid, func(r *Run) error {
		r.Phase = Held
		r.Outcome = "unknown"
		r.Reason = "timeout"
		r.SlotReleased = true
		r.Timeout = time.Hour
		r.Target = target
		r.IssueUID = "issue"
		r.IssueRef = "project#task"
		return nil
	})
	if e = state.WriteJSON(f.s.fsys, f.s.layout.StateFile(), installationForTest(a)); e != nil {
		t.Fatal(e)
	}
	a.k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{UID: "issue", ProjectUID: "project", Status: "open", Author: "operator"}, nil
	}
	k := &resumeKata{admissionKata: a.k, ready: []kata.IssueView{{UID: "issue"}}}
	f.m.cfg = a.cfg
	f.m.k = k
	f.m.guard = a.g
	f.m.q = NewQueue(k, f.s, a.g, RealClock{})
	if e = f.m.RestoreGates(); e != nil {
		t.Fatal(e)
	}
	e = f.m.Resume(t.Context(), Request{UID: "request", RunUID: f.uid})
	if !errors.Is(e, errControlRefused) || !strings.Contains(e.Error(), "stopped") || f.count("agent.prompt") != 1 || loaded(t, f.s, f.uid).Steps[0].Attempt != 1 {
		t.Fatal(e)
	}
}
func installationForTest(f *admissionFixture) any {
	var value map[string]any
	if e := state.ReadJSON(f.s.fsys, f.s.layout.StateFile(), 64<<10, &value); e != nil {
		panic(e)
	}
	return value
}

// Stop a resume after durable acceptance reaches its undelivered queue barrier.
// No elapsed-time assumption decides whether acceptance has happened.
func resumeUntilQueued(t *testing.T, f *admissionFixture, req Request) {
	t.Helper()
	for {
		select {
		case <-f.q.wake:
			continue
		default:
		}
		break
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.m.Resume(ctx, req) }()
	select {
	case <-f.q.wake:
		r := loaded(t, f.s, f.uid)
		if r.Resume == nil || r.Resume.UID != req.UID || !r.Resume.Pending {
			cancel()
			<-done
			t.Fatal("resume wake preceded durable acceptance", r)
		}
	case err := <-done:
		t.Fatal("resume returned before its queue barrier", err)
	case <-t.Context().Done():
		cancel()
		<-done
		t.Fatal(t.Context().Err())
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("resume did not wait for its queue barrier", err)
	}
}

func TestResumeLoopTargets(t *testing.T) {
	for _, tc := range []struct {
		reason               string
		reset                bool
		maker, checker, used int
	}{{"loop_pending", false, 2, 2, 1}, {"loop_exhausted", false, 1, 2, 3}, {"loop_stuck", false, 1, 2, 1}, {"loop_exhausted", true, 2, 2, 0}, {"loop_stuck", true, 2, 2, 0}} {
		t.Run(fmt.Sprint(tc.reason, tc.reset), func(t *testing.T) {
			f := heldResumeFixture(t)
			now := f.m.clock.Now()
			f.s.Transition(f.uid, func(r *Run) error {
				r.WFDef.Steps = []defs.Step{{Key: "before", Kind: "command", Command: "true"}, {Key: "make", Kind: "command", Command: "true", After: []string{"before"}}, {Key: "check", Kind: "command", Command: "true", After: []string{"make"}, Options: json.RawMessage(`{"on_fail":{"goto":"make","max_loops":3}}`)}}
				pgid := r.Steps[0].Receipt.PGID
				r.Steps = nil
				for _, d := range r.WFDef.Steps {
					status := "succeeded"
					exit := 0
					if d.Key == "check" {
						status = "failed"
						exit = 7
					}
					r.Steps = append(r.Steps, StepState{Key: d.Key, Kind: d.Kind, Attempt: 1, Submission: Accepted, Status: status, Receipt: &Receipt{Version: 1, Run: r.UID, Step: d.Key, Attempt: 1, ExitCode: exit, PGID: pgid, StartedAt: now, EndedAt: now}})
				}
				r.ActiveStep = "check"
				r.Reason = tc.reason
				r.Loop = LoopState{Used: tc.used, Rejection: "old rejection", Comparable: true}
				if tc.reason == "loop_pending" {
					r.Loop = LoopState{}
					r.Steps[2].Note = "fix issue"
				}
				return nil
			})
			req := Request{UID: "request", RunUID: f.uid, ResetLoops: tc.reset}
			resumeUntilQueued(t, f, req)
			r := loaded(t, f.s, f.uid)
			if r.Steps[0].Attempt != 1 || r.Steps[0].Status != "succeeded" || r.Steps[1].Attempt != tc.maker || r.Steps[2].Attempt != tc.checker || r.Loop.Used != tc.used {
				t.Fatal(r)
			}
			if tc.reset && (r.Loop.Rejection != "" || r.Loop.Comparable) {
				t.Fatal("reset remembered rejection", r.Loop)
			}
			if tc.reason == "loop_pending" && (r.Resume.Step != "make" || r.Loop.ConsumedAttempt != 1) {
				t.Fatal("did not finish saved edge", r)
			}
			resumeUntilQueued(t, f, req)
			again := loaded(t, f.s, f.uid)
			if again.Loop.Used != tc.used || again.Steps[1].Attempt != tc.maker || again.Steps[2].Attempt != tc.checker {
				t.Fatal("redelivery repeated edge", again)
			}
		})
	}
}

func TestResumeCheckerOnlyPlainContinuation(t *testing.T) {
	f := heldResumeFixture(t)
	f.s.Transition(f.uid, func(r *Run) error {
		r.Reason = "loop_exhausted"
		r.Loop.Used = 1
		r.WFDef.Steps = []defs.Step{{Key: "make", Kind: "command", Command: "true"}, {Key: "check", Kind: "command", Command: "true", After: []string{"make"}, Options: json.RawMessage(`{"on_fail":{"goto":"make","max_loops":1}}`)}}
		old := r.Steps[0]
		old.Key = "make"
		old.Status = "succeeded"
		old.Receipt.Step = "make"
		old.Receipt.ExitCode = 0
		r.Steps = []StepState{old, {Key: "check", Kind: "command", Attempt: 2, Submission: None, Status: "failed"}}
		r.ActiveStep = "check"
		r.Findings = Findings{RootUID: "finding", HeadUID: "last-checker", MakerUID: "maker-claim"}
		op := newOp(chainID(r, r.Steps[0], "maker"), "comment", Payload{Chain: &ChainComment{Step: "make", Attempt: 1, Purpose: "maker", Target: "finding", Mode: "plain"}}, "old-maker-key")
		op.Done = true
		op.CommentUID = "maker-claim"
		r.Ops = append(r.Ops, op)
		return nil
	})
	f.k.commentResult = func(_ context.Context, ref, body, key string) (kata.Comment, error) {
		if strings.Contains(key, ":step:") && (!strings.Contains(body, "Answers c:last-checker") || strings.Contains(body, "Answers c:maker-claim") || !strings.Contains(key, ":step:check:attempt:3:checker")) {
			t.Errorf("continuation target or key: %s %s", body, key)
		}
		return kata.Comment{UID: "new-checker"}, nil
	}
	f.start(t)
	if e := f.m.Resume(t.Context(), Request{UID: "request", RunUID: f.uid}); e != nil {
		t.Fatal(e)
	}
	if e := f.m.Execute(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r := loaded(t, f.s, f.uid)
	if r.Phase != Settled || r.Outcome != "succeeded" || r.Steps[0].Attempt != 1 || r.Steps[1].Attempt != 3 || r.Findings.HeadUID != "new-checker" {
		t.Fatal(r)
	}
}

func TestResumePendingAcceptanceRetainsSlot(t *testing.T) {
	f := heldResumeFixture(t)
	f.m.cfg.Limits.MaxActiveRuns = 1
	f.m.h = &herdr.Client{Path: herdrtest.New(t).Path}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	done := make(chan error, 1)
	go func() {
		defer close(done)
		done <- f.m.Resume(ctx, Request{UID: "request", RunUID: f.uid})
	}()
	defer func() { cancel(); <-done }()
	// No queue worker is running. Its wake follows durable acceptance, so
	// cancellation cannot race the acceptance this test needs to exercise.
	select {
	case <-f.q.wake:
	case e := <-done:
		t.Fatal("resume returned before acceptance", e)
	case <-ctx.Done():
		t.Fatal("resume was not accepted", ctx.Err())
	}
	r := loaded(t, f.s, f.uid)
	if r.Resume == nil || !r.Resume.Pending || r.Resume.UID != "request" || r.Steps[0].Attempt != 2 {
		t.Fatal("resume acceptance not saved", r)
	}
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if e := f.m.ReconcileHeld(t.Context()); e != nil {
		t.Fatal(e)
	}
	if loaded(t, f.s, f.uid).SlotReleased || f.m.FreeSlots() != 0 {
		t.Fatal("pending resume lost capacity reservation")
	}
}

func TestResumeUncertainCommandWithStoppedGroup(t *testing.T) {
	f := heldResumeFixture(t)
	f.s.Transition(f.uid, func(r *Run) error {
		r.Reason = "command_outcome_unknown"
		r.Steps[0].Status = "unknown"
		r.Steps[0].Receipt.Uncertain = true
		return nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	if e := f.m.Resume(ctx, Request{UID: "request", RunUID: f.uid}); e != context.DeadlineExceeded {
		t.Fatal("stopped uncertain command could not resume", e)
	}
	if loaded(t, f.s, f.uid).Steps[0].Attempt != 2 {
		t.Fatal("uncertainty target not replaced")
	}
}

func TestResumeObservationCASConflictAndExactReplay(t *testing.T) {
	for _, exact := range []bool{false, true} {
		t.Run(fmt.Sprint(exact), func(t *testing.T) {
			f := heldResumeFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
			defer cancel()
			if e := f.m.Resume(ctx, Request{UID: "request", RunUID: f.uid}); e != context.DeadlineExceeded {
				t.Fatal(e)
			}
			f.k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) {
				return kata.Run{}, false, &kata.Error{Code: "conflict"}
			}
			f.k.run = func(context.Context, string) (kata.Run, error) {
				r := loaded(t, f.s, f.uid)
				op, _ := findOp(r, resumePrefix(r)+":observe")
				p, _ := decodePayload(*op)
				o := p.Observation
				revision := o.ExpectedRevision + 1
				if !exact {
					revision++
				}
				return kata.Run{UID: r.UID, JobUID: o.JobUID, DefinitionEventUID: o.DefinitionEventUID, WorkflowUID: o.WorkflowUID, WorkflowDefinitionEventUID: o.WorkflowDefinitionEventUID, OccurrenceKey: o.OccurrenceKey, IssueUID: o.IssueUID, Actor: o.Actor, ExecutorLabel: o.ExecutorLabel, Status: o.Status, Teammate: o.Teammate, Summary: o.Summary, StartedAt: o.StartedAt, EndedAt: o.EndedAt, Revision: revision}, nil
			}
			deliver(t, f.q, f.uid)
			r := loaded(t, f.s, f.uid)
			if exact {
				if r.Inspect != "" || !r.Ops[len(r.Ops)-1].Done {
					t.Fatal("exact replay not confirmed", r)
				}
			} else if r.Inspect == "" || r.Phase != Held || !r.Resume.Pending {
				t.Fatal("another writer authorized activation", r)
			}
		})
	}
}

func TestResumeRefusesRetainedUnknownProvenance(t *testing.T) {
	f := heldResumeFixture(t)
	f.m.h = &herdr.Client{Path: herdrtest.New(t).Path}
	f.s.Transition(f.uid, func(r *Run) error {
		r.PastAttempts = append(r.PastAttempts, StepState{Key: "earlier", Kind: "prompt", Attempt: 1, Herdr: &HerdrRef{AgentName: "unknown"}})
		return nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if e := f.m.Resume(ctx, Request{UID: "request", RunUID: f.uid}); !errors.Is(e, errControlRefused) {
		t.Fatal("unknown provenance accepted", e)
	}
}

func TestResumeWaitsForPriorReports(t *testing.T) {
	f := heldResumeFixture(t)
	_, e := f.s.Transition(f.uid, func(r *Run) error {
		r.Ops = append(r.Ops, newOp("pending-report", "comment", Payload{Ref: r.IssueRef, Body: "frozen report"}, "prior-key"))
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	req := Request{Version: 1, UID: "request", Kind: "resume", RunUID: f.uid}
	publishResume(t, f, req)
	if e = f.m.HandleRequests(t.Context(), nil); e != nil {
		t.Fatal(e)
	}
	if loaded(t, f.s, f.uid).Resume != nil {
		t.Fatal("resume accepted before report drain")
	}
	if _, e = os.Stat(filepath.Join(f.s.layout.RequestsDir(), req.UID+".json")); e != nil {
		t.Fatal("report wait discarded request", e)
	}
	deliver(t, f.q, f.uid)
	f.start(t)
	if e = f.m.HandleRequests(t.Context(), nil); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	if loaded(t, f.s, f.uid).Steps[0].Attempt != 2 {
		t.Fatal("drained report did not permit resume")
	}
}

func TestAdmissionFreezesEffectiveTimeout(t *testing.T) {
	f := newAdmission(t)
	f.d.Def.TimeoutSeconds = 17
	r, e := f.m.prepare(f.d, f.uid, "occurrence", Target{})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.Create(r); e != nil {
		t.Fatal(e)
	}
	f.m.cfg.Limits.DefaultTimeout = time.Hour
	f.d.Def.TimeoutSeconds = 90
	saved := loaded(t, f.s, f.uid)
	if saved.Timeout != 17*time.Second {
		t.Fatal("admitted timeout not frozen", saved.Timeout)
	}
}

func TestReleaseCancelsPendingResumeRecovery(t *testing.T) {
	f := heldResumeFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	if e := f.m.Resume(ctx, Request{UID: "request", RunUID: f.uid}); e != context.DeadlineExceeded {
		t.Fatal(e)
	}
	if e := f.m.Release(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	if r.Phase != HandedOff || !r.SlotReleased || r.Resume.Pending {
		t.Fatal("release left resume activation pending", r)
	}
	for _, op := range r.Ops {
		if strings.HasSuffix(op.ID, ":request:observe") && !op.Done {
			t.Fatal("unsent running observation survived release", op)
		}
	}
	// Recovery must report handoff without attempting resume activation.
	f.m.q = nil
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	if loaded(t, f.s, f.uid).Steps[0].Attempt != 2 {
		t.Fatal("handoff recovery allocated execution")
	}
}

func TestResumeDeadlineBetweenStepsDoesNotReplayCompletedWork(t *testing.T) {
	f := heldResumeFixture(t)
	_, e := f.s.Transition(f.uid, func(r *Run) error {
		r.Phase = Admitted
		r.EndedAt = nil
		r.Outcome = ""
		r.Reason = ""
		r.ExpiresAt = time.Now().Add(-time.Second)
		r.Steps[0].Status = "succeeded"
		r.Steps[0].Receipt.ExitCode = 0
		r.Steps = append(r.Steps, StepState{Key: "next", Kind: "command", Attempt: 1, Submission: None})
		r.WFDef.Steps[0].Command = "echo duplicated >> visits"
		r.WFDef.Steps = append(r.WFDef.Steps, defs.Step{Key: "next", Kind: "command", Command: "echo next >> visits", After: []string{"main"}})
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.m.Execute(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	parked := loaded(t, f.s, f.uid)
	if parked.Phase != Held || parked.Reason != "timeout" || parked.ActiveStep != "next" {
		t.Fatal("wrong parked target", parked)
	}
	for head(loaded(t, f.s, f.uid)) != nil {
		deliver(t, f.q, f.uid)
	}
	// A stale progress marker from an earlier journal must not select completed work.
	if _, e = f.s.Transition(f.uid, func(r *Run) error { r.ActiveStep = "main"; return nil }); e != nil {
		t.Fatal(e)
	}
	f.start(t)
	if e = f.m.Resume(t.Context(), Request{UID: "request", RunUID: f.uid}); e != nil {
		t.Fatal(e)
	}
	if e = f.m.Execute(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r := loaded(t, f.s, f.uid)
	b, e := os.ReadFile(filepath.Join(r.Cwd, "visits"))
	if e != nil || string(b) != "next\n" || r.Steps[0].Attempt != 1 || r.Steps[1].Attempt != 2 {
		t.Fatal("completed work replayed", r, string(b), e)
	}
}

func TestPendingResumeRecoveryDoesNotBlockControls(t *testing.T) {
	f := heldResumeFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	if e := f.m.Resume(ctx, Request{UID: "request", RunUID: f.uid}); e != context.DeadlineExceeded {
		t.Fatal(e)
	}
	ctx, cancel = context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	if e := f.m.Recover(ctx); e != nil {
		t.Fatal("pending resume blocked startup", e)
	}
	if !loaded(t, f.s, f.uid).Resume.Pending {
		t.Fatal("activated before report confirmation")
	}
	other := *loaded(t, f.s, f.uid)
	other.UID = "other"
	other.Resume = nil
	other.ConsumedResumes = nil
	other.Ops = nil
	if e := f.s.Create(&other); e != nil {
		t.Fatal(e)
	}
	publishResume(t, f, Request{Version: 1, UID: "release-request", RunUID: "other", Kind: "release"})
	if e := f.m.HandleRequests(t.Context(), nil); e != nil {
		t.Fatal(e)
	}
	if loaded(t, f.s, "other").Phase != HandedOff {
		t.Fatal("pending resume blocked release")
	}
	deliver(t, f.q, f.uid)
	// No original resume request exists. The saved authorization must still activate.
	if e := f.m.HandleRequests(t.Context(), nil); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r := loaded(t, f.s, f.uid)
	if r.Resume.Pending || r.Steps[0].Attempt != 2 || r.Outcome != "succeeded" {
		t.Fatal("saved request did not finish", r)
	}
}

func TestResumeRouteFailureDoesNotStarveRelease(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprint(retry), func(t *testing.T) {
			f := heldResumeFixture(t)
			cause := error(errors.New("route is not authoritative"))
			if retry {
				cause = lostReply
			}
			f.m.guard = &testGuard{err: cause}
			other := *loaded(t, f.s, f.uid)
			other.UID = "other"
			other.Ops = nil
			if e := f.s.Create(&other); e != nil {
				t.Fatal(e)
			}
			publishResume(t, f, Request{Version: 1, UID: "a-resume", Kind: "resume", RunUID: f.uid})
			publishResume(t, f, Request{Version: 1, UID: "z-release", Kind: "release", RunUID: "other"})
			if e := f.m.HandleRequests(t.Context(), nil); e != nil {
				t.Fatal("route failure stopped control scan", e)
			}
			if loaded(t, f.s, "other").Phase != HandedOff || loaded(t, f.s, f.uid).Steps[0].Attempt != 1 {
				t.Fatal("release starved or failed resume allocated execution")
			}
			_, e := os.Stat(filepath.Join(f.s.layout.RequestsDir(), "a-resume.json"))
			if retry {
				if e != nil {
					t.Fatal("transient request dropped", e)
				}
			} else {
				var result RequestResult
				if e := state.ReadJSON(f.s.fsys, RequestResultPath(f.s.layout, "a-resume"), 4096, &result); e != nil || !strings.Contains(result.Error, "authoritative") {
					t.Fatal(result, e)
				}
			}
		})
	}
}

func TestResumeStoppedUncertainCommandAfterFreshPrompt(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first", Options: json.RawMessage(`{"agent_mode":"fresh"}`)}, defs.Step{Key: "check", Kind: "command", Command: "true", After: []string{"first"}})
	f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = time.Now().Add(30 * time.Second); return nil })
	f.accepted(t)
	f.result(t, "succeeded")
	if f.await(t) == nil {
		t.Fatal("missing first report")
	}
	if e := f.m.runCommandStep(t.Context(), f.uid, 1, loaded(t, f.s, f.uid).WFDef.Steps[1]); e != nil {
		t.Fatal(e)
	}
	a := newAdmission(t)
	target, e := a.m.pin()
	if e != nil {
		t.Fatal(e)
	}
	if e = state.WriteJSON(f.s.fsys, f.s.layout.StateFile(), installationForTest(a)); e != nil {
		t.Fatal(e)
	}
	f.s.Transition(f.uid, func(r *Run) error {
		r.Target = target
		r.Timeout = time.Hour
		r.Phase = Held
		r.Outcome = "unknown"
		r.Reason = "command_outcome_unknown"
		r.IssueUID = "issue"
		r.IssueRef = "project#task"
		r.Steps[1].Status = "unknown"
		r.Steps[1].Receipt.Interrupted = true
		return nil
	})
	a.k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{UID: "issue", ProjectUID: "project", Status: "open"}, nil
	}
	k := &resumeKata{admissionKata: a.k, ready: []kata.IssueView{{UID: "issue"}}}
	f.m.cfg = a.cfg
	f.m.k = k
	f.m.guard = a.g
	f.m.q = NewQueue(k, f.s, a.g, RealClock{})
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	if e = f.m.Resume(ctx, Request{UID: "request", RunUID: f.uid}); e != context.DeadlineExceeded {
		t.Fatal("stopped command refused after fresh prompt", e)
	}
	r := loaded(t, f.s, f.uid)
	if r.Steps[0].Attempt != 1 || r.Steps[1].Attempt != 2 || !r.Resume.Pending {
		t.Fatal(r)
	}
}

func TestQueuedReleasePrecedesConfirmedResume(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprint(restart), func(t *testing.T) {
			f := heldResumeFixture(t)
			f.s.Transition(f.uid, func(r *Run) error { r.WFDef.Steps[0].Command = "echo executed > control-order"; return nil })
			ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
			req := Request{Version: 1, UID: "a-resume", RunUID: f.uid, Kind: "resume"}
			e := f.m.Resume(ctx, req)
			cancel()
			if e != context.DeadlineExceeded {
				t.Fatal(e)
			}
			deliver(t, f.q, f.uid)
			publishResume(t, f, req)
			publishResume(t, f, Request{Version: 1, UID: "z-release", RunUID: f.uid, Kind: "release"})
			if restart {
				if e = f.m.Recover(t.Context()); e != nil {
					t.Fatal(e)
				}
				if !loaded(t, f.s, f.uid).Resume.Pending {
					t.Fatal("startup activated ahead of queued release")
				}
			}
			// An issue write on another run can delay release. That delay must not
			// authorize pending execution, even after its observation is confirmed.
			func() {
				f.q.issueMu.Lock()
				defer f.q.issueMu.Unlock()
				if e = f.m.HandleRequests(t.Context(), nil); e != nil {
					t.Fatal(e)
				}
			}()
			f.m.Wait()
			r := loaded(t, f.s, f.uid)
			if r.Phase != Held || !r.Resume.Pending || r.Steps[0].Submission != None {
				t.Fatal("release wait allowed resume activation", r)
			}
			if e = f.m.HandleRequests(t.Context(), nil); e != nil {
				t.Fatal(e)
			}
			f.m.Wait()
			r = loaded(t, f.s, f.uid)
			if r.Phase != HandedOff || r.Resume.Pending || r.Steps[0].Submission != None {
				t.Fatal(r)
			}
			if _, e = os.Stat(filepath.Join(r.Cwd, "control-order")); !os.IsNotExist(e) {
				t.Fatal("command ran before pending release", e)
			}
		})
	}
}

func TestResumeExitedUncertainSharedPromptContinues(t *testing.T) {
	for _, status := range []string{"", "unknown"} {
		t.Run(status, func(t *testing.T) {
			f := workflowExecution(t, defs.Step{Key: "first", Kind: "prompt", Prompt: "first"}, defs.Step{Key: "next", Kind: "prompt", Prompt: "next", After: []string{"first"}})
			f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = time.Now().Add(30 * time.Second); return nil })
			f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Working}})
			f.accepted(t)
			old := loaded(t, f.s, f.uid).Herdr
			if e := f.m.hold(f.uid, "timeout"); e != nil {
				t.Fatal(e)
			}
			if e := f.m.h.TabClose(t.Context(), old.TabID); e != nil {
				t.Fatal(e)
			}
			a := newAdmission(t)
			target, e := a.m.pin()
			if e != nil {
				t.Fatal(e)
			}
			if e = state.WriteJSON(f.s.fsys, f.s.layout.StateFile(), installationForTest(a)); e != nil {
				t.Fatal(e)
			}
			f.s.Transition(f.uid, func(r *Run) error {
				r.Target = target
				r.Timeout = time.Hour
				r.IssueUID = "issue"
				r.IssueRef = "project#task"
				r.Steps[0].Status = status
				return nil
			})
			a.k.show = func(context.Context, string) (kata.IssueView, error) {
				return kata.IssueView{UID: "issue", ProjectUID: "project", Status: "open"}, nil
			}
			a.k.comment = func(context.Context, string, string, string) error { return nil }
			k := &resumeKata{admissionKata: a.k, ready: []kata.IssueView{{UID: "issue"}}}
			f.m.cfg = a.cfg
			f.m.k = k
			f.m.guard = a.g
			a.q = NewQueue(k, f.s, a.g, RealClock{})
			f.m.q = a.q
			a.start(t)
			reportNextPrompt(f)
			if e = f.m.Resume(t.Context(), Request{UID: "request", RunUID: f.uid}); e != nil {
				t.Fatal(e)
			}
			if e = f.m.Execute(t.Context(), f.uid); e != nil {
				t.Fatal(e)
			}
			f.m.Wait()
			r := loaded(t, f.s, f.uid)
			if r.Phase != Settled || r.Outcome != "succeeded" || r.Steps[0].Attempt != 2 || r.Steps[1].Status != "succeeded" || f.count("agent.start") != 3 || f.count("agent.prompt") != 3 || len(r.PastAttempts) != 1 || r.PastAttempts[0].Herdr.TabID != old.TabID || r.Herdr.TabID == old.TabID {
				t.Fatal("resumed uncertainty could not return to shared agent", r)
			}
		})
	}
}

func TestParkedPromptKeepsFailureDiagnosis(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "check", Kind: "prompt", Prompt: "check"}, defs.Step{Key: "later", Kind: "command", Command: "true"})
	f.s.Transition(f.uid, func(r *Run) error {
		r.WFDef.Options = json.RawMessage(`{"on_failure":"park"}`)
		r.ExpiresAt = time.Now().Add(30 * time.Second)
		return nil
	})
	f.h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method != "agent.prompt" {
			return
		}
		r, e := f.s.Load(f.uid)
		if e != nil {
			panic(e)
		}
		step := r.Steps[0]
		e = WriteResult(f.s.fsys, filepath.Join(f.s.layout.AttemptDir(f.uid, step.Key, step.Attempt), "result.json"), &Result{Version: 1, Run: f.uid, Step: step.Key, Attempt: step.Attempt, Status: "failed", Summary: "Parser rejects escaped commas in generated inputs."})
		if e != nil {
			panic(e)
		}
	}
	r := executeWorkflow(t, f)
	if r.Phase != Held || r.Outcome != "failed" || r.Reason != "step_failed" || r.Steps[1].Submission != None {
		t.Fatal(r)
	}
	_, comment := settlePayload(t, r.Ops, "comment")
	_, observe := settlePayload(t, r.Ops, "observe")
	if comment.Body != "Reason: step_failed\nParser rejects escaped commas in generated inputs." || observe.Observation.Status != "failed" || observe.Observation.Summary.Message != "Reason: step_failed\nParser rejects escaped commas in generated inputs." {
		t.Fatal("park discarded the diagnosis", comment, observe)
	}
}

// M1 pre-admission holds never gain execution authority through F3 resume.
func TestResumeRefusesPreAdmissionHolds(t *testing.T) {
	for _, reason := range []string{"admission_rejected"} {
		t.Run(reason, func(t *testing.T) {
			f := heldResumeFixture(t)
			_, err := f.s.Transition(f.uid, func(r *Run) error {
				r.Reason = reason
				r.Admitted = new(false)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
			defer cancel()
			err = f.m.Resume(ctx, Request{Version: 1, UID: "resume-request", Kind: "resume", RunUID: f.uid})
			if !errors.Is(err, errControlRefused) || !strings.Contains(err.Error(), "abandon --local") {
				t.Fatalf("resume rejection: %v", err)
			}
			r := loaded(t, f.s, f.uid)
			if r.Resume != nil || len(r.ConsumedResumes) != 0 || r.Steps[0].Attempt != 1 {
				t.Fatal("refusal changed run", r)
			}
		})
	}
}

// A reporting timeout after the final successful checker must settle saved
// results when resumed, without allocating or executing another attempt.
func TestResumeAfterFinalSuccessfulFindingTimeout(t *testing.T) {
	for _, priorFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "all-succeeded", true: "earlier-exhausted-failure"}[priorFailure], func(t *testing.T) {
			f := heldResumeFixture(t)
			r := loaded(t, f.s, f.uid)
			steps := []StepState{}
			keys := []string{"make", "check"}
			if priorFailure {
				keys = append([]string{"fail"}, keys...)
			}
			for _, key := range keys {
				command, status, exitCode := "echo "+key+" >> visits", "succeeded", 0
				if key == "fail" {
					command += "; exit 7"
					status, exitCode = "failed", 7
				}
				receipt, err := RunCommand(t.Context(), CommandSpec{Run: f.uid, Step: key, Attempt: 1, Command: command, Cwd: r.Cwd, AttemptDir: f.s.layout.AttemptDir(f.uid, key, 1), Deadline: time.Now().Add(time.Minute)})
				if (err == nil) != (exitCode == 0) || receipt.ExitCode != exitCode || receipt.Interrupted || receipt.Uncertain || receipt.GroupAlive || !GroupGone(receipt.PGID) {
					t.Fatal(receipt, err)
				}
				steps = append(steps, StepState{Key: key, Kind: "command", Attempt: 1, Submission: Accepted, Status: status, Reason: status, Receipt: &receipt})
			}
			_, err := f.s.Transition(f.uid, func(r *Run) error {
				r.WFDef.Options = nil
				r.WFDef.Steps = []defs.Step{{Key: "make", Kind: "command", Command: "echo make >> visits"}, {Key: "check", Kind: "command", After: []string{"make"}, Command: "echo check >> visits", Options: json.RawMessage(`{"on_fail":{"goto":"make","max_loops":2}}`)}}
				if priorFailure {
					r.WFDef.Steps = append([]defs.Step{{Key: "fail", Kind: "command", Command: "echo fail >> visits; exit 7"}}, r.WFDef.Steps...)
				}
				r.Steps = steps
				r.ActiveStep = "check"
				r.Phase = Running
				r.EndedAt = nil
				r.Outcome = ""
				r.Reason = ""
				r.Findings = Findings{RootUID: "finding", HeadUID: "maker-claim", MakerUID: "maker-claim"}
				maker := newOp(chainID(r, steps[len(steps)-2], "maker"), "comment", Payload{Chain: &ChainComment{Step: "make", Attempt: 1, Purpose: "maker", Target: "finding", Mode: "plain"}}, "maker-key")
				maker.Done = true
				maker.CommentUID = "maker-claim"
				r.Ops = append(r.Ops, maker)
				r.ExpiresAt = f.m.clock.Now().Add(-time.Second)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = f.m.publishFinding(t.Context(), f.uid, len(steps)-1); err != nil {
				t.Fatal(err)
			}
			r = loaded(t, f.s, f.uid)
			if r.Phase != Held || r.Reason != "timeout" || pendingChain(r) == nil {
				t.Fatal("finding did not time out", r)
			}
			if err = f.m.finish(t.Context(), f.uid, nil); err != nil {
				t.Fatal(err)
			}
			findingCalls := 0
			f.k.commentResult = func(_ context.Context, ref, body, key string) (kata.Comment, error) {
				if strings.Contains(key, ":checker") {
					findingCalls++
					if ref != "project#task" || !strings.Contains(body, "step check · attempt 1 · succeeded") || !strings.Contains(body, "Answers c:maker-claim") {
						t.Errorf("wrong checker publication: %s %s", ref, body)
					}
					return kata.Comment{UID: "final-checker"}, nil
				}
				return kata.Comment{UID: "settlement"}, nil
			}
			for head(loaded(t, f.s, f.uid)) != nil {
				deliver(t, f.q, f.uid)
			}
			f.start(t)
			if err = f.m.Resume(t.Context(), Request{UID: "request", RunUID: f.uid}); err != nil {
				t.Fatal(err)
			}
			if err = f.m.Execute(t.Context(), f.uid); err != nil {
				t.Fatal(err)
			}
			f.m.Wait()
			r = loaded(t, f.s, f.uid)
			visits, err := os.ReadFile(filepath.Join(r.Cwd, "visits"))
			expectedVisits, outcome := "make\ncheck\n", "succeeded"
			if priorFailure {
				expectedVisits, outcome = "fail\nmake\ncheck\n", "failed"
			}
			for _, step := range r.Steps {
				if step.Attempt != 1 {
					t.Fatal("completed attempt replaced", step)
				}
			}
			if err != nil || string(visits) != expectedVisits || r.Phase != Settled || r.Outcome != outcome || len(r.PastAttempts) != 0 || findingCalls != 1 || r.Findings.HeadUID != "final-checker" {
				t.Fatal("resume replayed completed work or failed to settle", r, string(visits), findingCalls, err)
			}

		})
	}
}

func TestResumeDelayedStopProofKeepsRequestForRetry(t *testing.T) {
	f := heldResumeFixture(t)
	h := herdrtest.New(t)
	unblock := make(chan struct{})
	h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method == "agent.get" {
			<-unblock
		}
	}
	// The saved occupant has exited; delay the absence proof past the scan's
	// deadline, then let a later scan read the stopped state successfully.
	id, err := herdr.ReadServerID(h.Path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Transition(f.uid, func(r *Run) error {
		r.Herdr = HerdrRef{ServerID: id, AgentName: "worker", PaneID: "pane", TerminalID: "terminal", TabID: "tab", WorkspaceID: "workspace"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.m.h = &herdr.Client{Path: h.Path}
	publishResume(t, f, Request{Version: 1, UID: "request", Kind: "resume", RunUID: f.uid})
	err = f.m.HandleRequests(t.Context(), nil)
	close(unblock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(f.s.layout.RequestsDir(), "request.json")); err != nil {
		t.Fatal("delayed proof discarded request", err)
	}
	if r := loaded(t, f.s, f.uid); r.Resume != nil || r.Steps[0].Attempt != 1 {
		t.Fatal("timed out proof changed run", r)
	}
	f.start(t)
	retryCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for {
		if err = f.m.HandleRequests(retryCtx, nil); err != nil {
			t.Fatal(err)
		}
		f.m.Wait()
		if loaded(t, f.s, f.uid).Phase == Settled {
			break
		}
		select {
		case <-retryCtx.Done():
			t.Fatal("later scans did not finish the saved resume", retryCtx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if r := loaded(t, f.s, f.uid); r.Phase != Settled || r.Outcome != "succeeded" || r.Steps[0].Attempt != 2 || len(r.ConsumedResumes) != 1 {
		t.Fatal("later proof did not resume once", r)
	}
	if _, err = os.Stat(filepath.Join(f.s.layout.RequestsDir(), "request.json")); !os.IsNotExist(err) {
		t.Fatal("completed request retained", err)
	}
}

func TestReleaseRefusesInspectedPendingResume(t *testing.T) {
	f := heldResumeFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	if err := f.m.Resume(ctx, Request{UID: "request", RunUID: f.uid}); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	f.k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) {
		return kata.Run{}, false, &kata.Error{Code: "revision_conflict"}
	}
	f.k.run = func(context.Context, string) (kata.Run, error) {
		return kata.Run{UID: f.uid, Status: "failed", Revision: 6}, nil
	}
	deliver(t, f.q, f.uid)
	before := loaded(t, f.s, f.uid)
	if before.Inspect == "" || !before.Resume.Pending {
		t.Fatal("resume conflict did not require inspection", before)
	}
	if err := f.m.Release(t.Context(), f.uid); !errors.Is(err, errControlRefused) {
		t.Fatal("release accepted blocked reporting tail", err)
	}
	after := loaded(t, f.s, f.uid)
	if after.Phase != Held || after.SlotReleased || !after.Resume.Pending || len(after.Ops) != len(before.Ops) {
		t.Fatal("refused release changed resume", after)
	}
}

func TestResumeNoActivityThenCheckerLoop(t *testing.T) {
	for _, park := range []bool{false, true} {
		t.Run(map[bool]string{false: "loop", true: "park-and-resume"}[park], func(t *testing.T) {
			f := workflowExecution(t, defs.Step{Key: "make", Kind: "prompt", Prompt: "make"}, defs.Step{Key: "check", Kind: "command", Command: "if [ -f once ]; then true; else touch once; exit 7; fi", After: []string{"make"}, Options: json.RawMessage(`{"on_fail":{"goto":"make","max_loops":1}}`)})
			if park {
				if _, err := f.s.Transition(f.uid, func(r *Run) error {
					r.WFDef.Options = json.RawMessage(`{"on_failure":"park"}`)
					r.WFDef.Steps[1].Options = nil
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			clock := &activityClock{at: time.Now()}
			f.m.clock = clock
			f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = clock.Now().Add(time.Hour); return nil })
			f.h.Fail("agent.prompt", herdr.Error{Code: "timeout"})
			expireWaits(t, f.h, clock, nil)
			f.accepted(t)
			f.await(t)
			r := loaded(t, f.s, f.uid)
			old := r.Herdr
			if r.Reason != "no_activity" || old.PreSubmitSeq == nil {
				t.Fatal("no activity hold absent", r)
			}
			f.m.clock = RealClock{}
			a := newAdmission(t)
			target, err := a.m.pin()
			if err != nil {
				t.Fatal(err)
			}
			if err = state.WriteJSON(f.s.fsys, f.s.layout.StateFile(), installationForTest(a)); err != nil {
				t.Fatal(err)
			}
			_, err = f.s.Transition(f.uid, func(r *Run) error {
				r.Target = target
				r.Timeout = time.Hour
				r.IssueUID = "issue"
				r.IssueRef = "project#task"
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			fk := findingQueue(t, f)
			originalShow := fk.show
			fk.show = func(ctx context.Context, ref string) (kata.IssueView, error) {
				i, err := originalShow(ctx, ref)
				i.UID, i.ProjectUID, i.Status, i.Author = "issue", "project", "open", "operator"
				return i, err
			}
			fk.notify = func(context.Context, string, string, string) error { return nil }
			a.k.fakeKata = fk
			k := &resumeKata{admissionKata: a.k, ready: []kata.IssueView{{UID: "issue"}}}
			f.m.cfg, f.m.k, f.m.guard = a.cfg, k, a.g
			f.m.q.k = k
			reportNextPrompt(f)
			if err = f.m.Resume(t.Context(), Request{UID: "request", RunUID: f.uid}); err != nil {
				t.Fatal(err)
			}
			if err = f.m.Execute(t.Context(), f.uid); err != nil {
				t.Fatal(err)
			}
			f.m.Wait()
			r = loaded(t, f.s, f.uid)
			maker, used, prompts := 3, 1, 3
			if park {
				if r.Phase != Held || r.Reason != "step_failed" {
					t.Fatal("checker did not park", r)
				}
				if err = f.m.q.wait(t.Context(), f.uid, r.Ops[len(r.Ops)-1].ID); err != nil {
					t.Fatal(err)
				}
				if err = f.m.Resume(t.Context(), Request{UID: "request-again", RunUID: f.uid}); err != nil {
					t.Fatal("second resume refused stopped occupants", err)
				}
				if err = f.m.Execute(t.Context(), f.uid); err != nil {
					t.Fatal(err)
				}
				f.m.Wait()
				r = loaded(t, f.s, f.uid)
				maker, used, prompts = 2, 0, 2
			}
			if r.Phase != Settled || r.Outcome != "succeeded" || r.Loop.Used != used || r.Steps[0].Attempt != maker || r.Steps[1].Attempt != 2 || f.count("agent.prompt") != prompts {
				t.Fatal("retained no activity occupant blocked loop", r)
			}
			if len(r.PastAttempts) == 0 || r.PastAttempts[0].Herdr.PaneID != old.PaneID || r.PastAttempts[0].Herdr.PreSubmitSeq == nil || *r.PastAttempts[0].Herdr.PreSubmitSeq != *old.PreSubmitSeq {
				t.Fatal("original provenance lost", r.PastAttempts)
			}

		})
	}
}

// Reproduce the deadline window observed by the socket test on macOS:
// the deadline has fired while cancellation is not yet observable via Err.
type laggingDeadlineContext struct {
	context.Context
	at time.Time
}

func (c laggingDeadlineContext) Deadline() (time.Time, bool) { return c.at, true }

func TestResumeExpiredProofDeadlineBeforeCancellation(t *testing.T) {
	f := heldResumeFixture(t)
	h := herdrtest.New(t)
	id, err := herdr.ReadServerID(h.Path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Transition(f.uid, func(r *Run) error {
		r.Herdr = HerdrRef{ServerID: id, AgentName: "worker", PaneID: "pane", TerminalID: "terminal"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.m.h = &herdr.Client{Path: h.Path}
	ctx := laggingDeadlineContext{Context: t.Context(), at: time.Now().Add(-time.Second)}
	if ctx.Err() != nil {
		t.Fatal("cancellation already observed", ctx.Err())
	}
	err = f.m.Resume(ctx, Request{UID: "request", RunUID: f.uid})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("expired proof became permanent refusal", err)
	}
	if r := loaded(t, f.s, f.uid); r.Resume != nil || r.Steps[0].Attempt != 1 {
		t.Fatal("expired proof changed run", r)
	}
}
