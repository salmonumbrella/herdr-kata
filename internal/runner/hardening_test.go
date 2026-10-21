package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/state"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

// A hidden authority read must not let another journal's evidence authorize work.
func TestAdmissionConflictWithHiddenWinnerDoesNotMutateChild(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(map[bool]string{false: "unowned", true: "owned"}[owned], func(t *testing.T) {
			f := newChildAdmission(t)
			f.k.show = func(context.Context, string) (kata.IssueView, error) {
				owner := ""
				if owned {
					owner = "worker"
				}
				return kata.IssueView{UID: childUID, ProjectUID: "project", QualifiedID: "project#ebrn", Owner: owner, Status: "open", Revision: 7, Metadata: map[string]json.RawMessage{"work.attention": json.RawMessage(`"needs-human"`)}}, nil
			}
			var mutations atomic.Int32
			f.k.claim = func(context.Context, string, bool) (kata.IssueView, error) {
				mutations.Add(1)
				return f.k.show(t.Context(), "project#ebrn")
			}
			f.k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) {
				mutations.Add(1)
				return 8, nil
			}
			var reads atomic.Int32
			f.k.run = func(context.Context, string) (kata.Run, error) {
				if reads.Add(1) > 2 {
					t.Error("admission conflict read more than once")
				}
				return kata.Run{}, &kata.Error{Code: "not_found"}
			}
			var observations atomic.Int32
			f.k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) {
				observations.Add(1)
				return kata.Run{}, false, &kata.Error{Code: "cron_conflict"}
			}
			f.start(t)
			result, err := f.m.Admit(t.Context(), f.d)
			if err == nil || result != sched.NotAdmitted || observations.Load() != 1 {
				t.Fatal(result, err, observations.Load())
			}
			r := loaded(t, f.s, f.uid)
			if r.Phase != Held || r.Reason != "admission_rejected" || !r.SlotReleased || mutations.Load() != 0 {
				t.Fatalf("rejected admission: phase %s reason %s released %v mutations %d", r.Phase, r.Reason, r.SlotReleased, mutations.Load())
			}
		})
	}
}

func TestAdmissionDeadlineFollowsObservation(t *testing.T) {
	f := newAdmission(t)
	offset := int64(90)
	f.d.Def.Issue.DeadlineOffsetSeconds = &offset
	var observed atomic.Bool
	f.k.observe = func(_ context.Context, _ string, o kata.Observation) (kata.Run, bool, error) {
		if o.Summary.Message == "admitted" {
			t.Error("admission lacks journal identity")
		}
		observed.Store(true)
		return kata.Run{}, false, nil
	}
	f.k.deadline = func(context.Context, string, string, kata.Cond) error {
		if !observed.Load() || loaded(t, f.s, f.uid).Phase != Admitted {
			t.Error("deadline before admission saved")
		}
		return nil
	}
	f.start(t)
	f.admit(t)
}

func TestQueueSigningRefusalRetriesIdenticallyAndKeepsEvidence(t *testing.T) {
	for _, kind := range []string{"create", "comment", "meta.set", "notify"} {
		t.Run(kind, func(t *testing.T) {
			p := Payload{Ref: "issue", Body: "report", To: "worker", Message: "due", Name: "example", Value: "value"}
			if kind == "create" {
				p.Create = &kata.CreateReq{Title: "Task"}
			}
			q, s, uid, k, c, _ := queueFixture(t, operation("write", kind, p))
			var calls int
			refusal := &kata.Error{Code: "root_signing_key_unavailable", Exit: 5}
			effect := func() error {
				calls++
				switch calls {
				case 1:
					return refusal
				case 2:
					return lostReply
				case 3:
					return &kata.Error{Code: "server_error", Exit: 7}
				}
				return nil
			}
			k.create = func(context.Context, kata.CreateReq) (kata.IssueView, error) {
				return kata.IssueView{UID: "issue"}, effect()
			}
			k.comment = func(context.Context, string, string, string) error { return effect() }
			k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) { return 8, effect() }
			k.notify = func(context.Context, string, string, string) error { return effect() }
			k.show = func(context.Context, string) (kata.IssueView, error) {
				if kind == "notify" && calls == 1 {
					t.Error("signing refusal read back")
				}
				if kind == "notify" {
					return kata.IssueView{}, &kata.Error{Code: "not_found"}
				}
				return kata.IssueView{Revision: 7}, nil
			}
			deliver(t, q, uid)
			r := loaded(t, s, uid)
			if !r.Ops[0].SigningRefused || r.Ops[0].Inspect != "" || r.Ops[0].Done {
				t.Fatal("signing refusal not saved as retry", r.Ops[0])
			}
			for range 2 {
				c.at = c.at.Add(time.Minute)
				deliver(t, q, uid)
			}
			// Rebuild both store and queue, so no in-memory flag can satisfy this.
			s = NewStore(s.fsys, s.layout)
			q = NewQueue(k, s, q.guard, c)
			if !loaded(t, s, uid).Ops[0].SigningRefused {
				t.Fatal("restart lost signing evidence")
			}
			c.at = c.at.Add(time.Minute)
			if kind == "notify" {
				// The second send's lost reply needs read-back, while signing evidence persists.
				k.show = func(context.Context, string) (kata.IssueView, error) {
					return kata.IssueView{Metadata: map[string]json.RawMessage{"notify.d29ya2Vy": json.RawMessage(`{"message":"due","from":"","teammate":"herdr-kata"}`)}}, nil
				}
			}
			deliver(t, q, uid)
			r = loaded(t, s, uid)
			if !r.Ops[0].Done || r.Ops[0].SigningRefused {
				t.Fatal("delivery did not clear signing evidence", r.Ops[0])
			}
		})
	}
}

func TestQueueWrite404RemainsPending(t *testing.T) {
	q, s, uid, k, c, _ := queueFixture(t, operation("report", "comment", Payload{Ref: "issue", Body: "report"}))
	var calls int
	k.comment = func(context.Context, string, string, string) error { calls++; return &kata.Error{Code: "not_found"} }
	deliver(t, q, uid)
	c.at = c.at.Add(time.Hour)
	deliver(t, q, uid)
	r := loaded(t, s, uid)
	if calls != 2 || r.Ops[0].Done || r.Ops[0].Inspect != "" {
		t.Fatal("404 settled or inspected", calls, r.Ops[0])
	}
}

func TestReleaseCannotResolvePreAdmissionHold(t *testing.T) {
	f := newAdmission(t)
	no := false
	if err := f.s.Create(&Run{Version: 1, UID: f.uid, Phase: Held, Reason: "admission_rejected", Admitted: &no}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Release(t.Context(), f.uid); err == nil {
		t.Fatal("release changed admission veto")
	}
	if r := loaded(t, f.s, f.uid); r.Reason != "admission_rejected" || len(r.Ops) != 0 {
		t.Fatal(r)
	}
}

func TestRecoveryKeepsAdmissionRejectionLocal(t *testing.T) {
	f := newAdmission(t)
	target, err := f.m.pin()
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.m.prepare(f.d, f.uid, ident.OccurrenceKey(f.d.Occurrence), target)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Create(r); err != nil {
		t.Fatal(err)
	}
	f.k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) {
		return kata.Run{}, false, &kata.Error{Code: "cron_conflict"}
	}
	f.start(t)
	if err = f.m.Recover(t.Context()); err != nil {
		t.Fatal("rejected journal stopped recovery", err)
	}
	if r = loaded(t, f.s, f.uid); r.Reason != "admission_rejected" || !r.SlotReleased {
		t.Fatal(r)
	}
}

func TestRecoveredAdmissionFinishesTailBeforeLaunch(t *testing.T) {
	for _, tail := range []string{"child-claim", "child-tracked", "deadline"} {
		t.Run(tail, func(t *testing.T) {
			f := newAdmission(t)
			if tail != "deadline" {
				f = newChildAdmission(t)
			}
			target, err := f.m.pin()
			if err != nil {
				t.Fatal(err)
			}
			due := f.d
			if tail == "child-claim" {
				f.k.show = func(context.Context, string) (kata.IssueView, error) {
					return kata.IssueView{UID: childUID, ProjectUID: "project", QualifiedID: "project#ebrn", Status: "open", Revision: 7}, nil
				}
			}
			if due.Child != nil {
				due, err = f.m.childInputs(t.Context(), due, "project")
				if err != nil {
					t.Fatal(err)
				}
			} else {
				due.Def.Issue.DeadlineOffsetSeconds = new(int64(90))
			}
			marker := filepath.Join(f.s.layout.Root, "tail-done")
			due.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "main", Kind: "command", Command: "test -f " + marker}}}
			r, err := f.m.prepare(due, f.uid, ident.OccurrenceKey(due.Occurrence), target)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.s.Create(r); err != nil {
				t.Fatal(err)
			}
			if due.Child == nil {
				deliver(t, f.q, f.uid)
				r = loaded(t, f.s, f.uid)
			}
			if err = f.m.append(f.uid, newOp("admission", "observe", Payload{Observation: &r.Admission.Body}, "")); err != nil {
				t.Fatal(err)
			}
			deliver(t, f.q, f.uid)
			r = loaded(t, f.s, f.uid)
			if r.Phase != Admitted || len(r.Ops) < 2 || r.Ops[len(r.Ops)-1].Done {
				t.Fatal("tail not frozen atomically", r)
			}
			f.k.claim = func(context.Context, string, bool) (kata.IssueView, error) {
				return kata.IssueView{UID: childUID, QualifiedID: "project#ebrn", ProjectUID: "project", Owner: "worker", Status: "open", Revision: 9}, nil
			}
			f.k.show = func(context.Context, string) (kata.IssueView, error) {
				return kata.IssueView{UID: childUID, ProjectUID: "project", Owner: "worker", Status: "open", Revision: 9}, nil
			}
			f.k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) {
				return 10, os.WriteFile(marker, []byte("tail complete"), 0600)
			}
			f.k.deadline = func(context.Context, string, string, kata.Cond) error {
				return os.WriteFile(marker, []byte("tail complete"), 0600)
			}
			f.k.close = func(context.Context, kata.CloseReq) error { return nil }
			f.k.comment = func(context.Context, string, string, string) error { return nil }
			f.s = NewStore(f.s.fsys, f.s.layout)
			f.q = NewQueue(f.k, f.s, f.g, RealClock{})
			f.m = NewManager(f.cfg, f.k, nil, f.s, f.q, f.g, RealClock{})
			// Child ack stays behind settlement; this test concerns the launch barrier.
			if due.Child != nil {
				f.q.k = &ackKata{fakeKata: f.k.fakeKata, unset: func(context.Context, string, string, kata.Cond) (int64, error) { return 11, nil }}
			}
			f.start(t)
			if err = f.m.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.m.Wait()
			r = loaded(t, f.s, f.uid)
			if r.Outcome != "succeeded" {
				t.Fatal("command launched before tail or did not recover", r.Outcome, r.Reason)
			}
		})
	}
}

func TestAdmissionLostReplyRetriesSameJournalThroughQueue(t *testing.T) {
	f := newAdmission(t)
	target, err := f.m.pin()
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.m.prepare(f.d, f.uid, ident.OccurrenceKey(f.d.Occurrence), target)
	if err != nil {
		t.Fatal(err)
	}
	r.IssueUID = "issue"
	r.IssueRef = "project#task"
	r.Admission.Body.IssueUID = "issue"
	r.Ops = []Op{newOp("admission", "observe", Payload{Observation: &r.Admission.Body}, "")}
	if err = f.s.Create(r); err != nil {
		t.Fatal(err)
	}
	var original []byte
	calls := 0
	f.k.observe = func(_ context.Context, uid string, o kata.Observation) (kata.Run, bool, error) {
		calls++
		body, _ := json.Marshal(o)
		if calls == 1 {
			original = body
			return kata.Run{}, false, lostReply
		}
		if uid != f.uid || string(body) != string(original) {
			t.Error("admission regenerated after lost reply")
		}
		return kata.Run{}, true, nil
	}
	deliver(t, f.q, f.uid)
	// Crash: only the durable journal and committed boundary effect survive.
	f.s = NewStore(f.s.fsys, f.s.layout)
	clock := &testClock{at: time.Now().Add(time.Minute)}
	f.q = NewQueue(f.k, f.s, f.g, clock)
	deliver(t, f.q, f.uid)
	r = loaded(t, f.s, f.uid)
	if r.Phase != Admitted || r.Admitted == nil || !*r.Admitted || !r.Ops[0].Replayed || calls != 2 {
		t.Fatal("exact replay not saved", r, calls)
	}
}

func TestSigningBlockStopsNewAdmissionButQueueRepairs(t *testing.T) {
	f := newAdmission(t)
	op := operation("report", "comment", Payload{Ref: "issue", Body: "pending report"})
	op.SigningRefused = true
	op.RetryIdentical = true
	if err := f.s.Create(&Run{Version: 1, UID: "blocked", Phase: Settled, Ops: []Op{op}}); err != nil {
		t.Fatal(err)
	}
	result, err := f.m.Admit(t.Context(), f.d)
	if err == nil || result != sched.NotAdmitted {
		t.Fatal("signing evidence allowed new journal", result, err)
	}
	if _, err := f.s.Load(f.uid); !missing(err) {
		t.Fatal("new journal prepared while signing blocked", err)
	}
	f.k.comment = func(context.Context, string, string, string) error { return nil }
	deliver(t, f.q, "blocked")
	if r := loaded(t, f.s, "blocked"); !r.Ops[0].Done || r.Ops[0].SigningRefused {
		t.Fatal("queue blocked its own repair")
	}
	f.start(t)
	f.admit(t)
}

func TestQueueQuarantineVetoesValidLocalJournal(t *testing.T) {
	q, s, uid, k, _, _ := queueFixture(t, operation("report", "comment", Payload{Ref: "issue", Body: "pending"}))
	if err := state.WriteJSON(s.fsys, filepath.Join(s.layout.QuarantineRunDir(uid), "old-run.json"), map[string]string{}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	k.comment = func(context.Context, string, string, string) error { calls++; return nil }
	deliver(t, q, uid)
	if calls != 0 || loaded(t, s, uid).Ops[0].Done {
		t.Fatal("quarantined UID delivered", calls)
	}
}

func TestQueueOldNotificationCanStillConfirmEffect(t *testing.T) {
	op := operation("notify", "notify", Payload{Ref: "issue", To: "worker", Message: "due"})
	sent := time.Date(2025, 12, 20, 0, 0, 0, 0, time.UTC)
	op.FirstSent = &sent
	op.Attempts = 1
	q, s, uid, k, _, _ := queueFixture(t, op)
	k.notify = func(context.Context, string, string, string) error { t.Error("notification resent"); return nil }
	k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{Metadata: map[string]json.RawMessage{"notify.d29ya2Vy": json.RawMessage(`{"message":"due","from":"","teammate":"herdr-kata"}`)}}, nil
	}
	deliver(t, q, uid)
	if r := loaded(t, s, uid); !r.Ops[0].Done || r.Ops[0].Inspect != "" {
		t.Fatal("expiry preempted authoritative read-back", r.Ops[0])
	}
}

// Later semantic refusals cannot clear signing evidence. A force-new create is
// a different operation, so whole-run local resolution remains the escape.
func TestRetiredSigningRefusalRequiresWholeRunResolution(t *testing.T) {
	f := newAdmission(t)
	target, err := f.m.pin()
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.m.prepare(f.d, f.uid, ident.OccurrenceKey(f.d.Occurrence), target)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Create(r); err != nil {
		t.Fatal(err)
	}
	clock := &testClock{at: time.Now()}
	f.q.clock = clock
	calls := 0
	f.k.create = func(_ context.Context, req kata.CreateReq) (kata.IssueView, error) {
		calls++
		if calls == 1 {
			return kata.IssueView{}, &kata.Error{Code: "root_signing_key_unavailable", Exit: 5}
		}
		if calls == 2 {
			return kata.IssueView{}, &kata.Error{Code: "duplicate_candidates", CandidateUIDs: []string{"other"}}
		}
		if !req.ForceNew {
			t.Error("replacement lost force-new intent")
		}
		return kata.IssueView{UID: "fresh"}, nil
	}
	f.k.show = func(context.Context, string) (kata.IssueView, error) { return kata.IssueView{UID: "other"}, nil }
	deliver(t, f.q, f.uid)
	clock.at = clock.at.Add(time.Minute)
	deliver(t, f.q, f.uid)
	deliver(t, f.q, f.uid)
	saved := loaded(t, f.s, f.uid)
	if !saved.Ops[0].Done || !saved.Ops[0].SigningRefused || saved.IssueUID != "fresh" {
		t.Fatal("later refusal cleared signing evidence", saved)
	}
	another := f.d
	another.Manual = true
	another.ManualRunUID = "other-run"
	if result, err := f.m.Admit(t.Context(), another); err == nil || result != sched.NotAdmitted {
		t.Fatal("retired refusal allowed new admission", result, err)
	}
	if err = AbandonLocal(t.Context(), f.s.fsys, f.s.layout, f.uid, nil); err != nil {
		t.Fatal(err)
	}
	if blocked, err := f.s.SigningBlocked(); err != nil || blocked {
		t.Fatal("whole-run resolution did not unblock", blocked, err)
	}
}

func TestReleaseRefusesInspectedAdmissionTail(t *testing.T) {
	f := newChildAdmission(t)
	f.k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) {
		return 0, &kata.Error{Code: "conflict"}
	}
	f.start(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := f.m.Admit(ctx, f.d); err == nil {
		t.Fatal("tracking conflict not reproduced")
	}
	before := loaded(t, f.s, f.uid)
	if before.Phase != Admitted || before.Inspect == "" {
		t.Fatal("missing inspected admission tail")
	}
	if err := f.m.Release(t.Context(), f.uid); !errors.Is(err, errControlRefused) {
		t.Fatal("release accepted a permanently fenced handoff", err)
	}
	after := loaded(t, f.s, f.uid)
	if after.Phase != Admitted || after.EndedAt != nil || len(after.Ops) != len(before.Ops) {
		t.Fatal("refused release changed journal", after)
	}
}
func TestRetentionKeepsRetiredSigningEvidence(t *testing.T) {
	f := newAdmission(t)
	old := time.Now().Add(-15 * 24 * time.Hour)
	op := operation("create", "create", Payload{Create: &kata.CreateReq{Title: "Task"}})
	op.Done = true
	op.SigningRefused = true
	if err := f.s.Create(&Run{Version: 1, UID: f.uid, Phase: Settled, Outcome: "succeeded", EndedAt: &old, Ops: []Op{op}}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Retain(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Load(f.uid); err != nil {
		t.Fatal("retention erased signing evidence without quarantine", err)
	}
	if blocked, err := f.s.SigningBlocked(); err != nil || !blocked {
		t.Fatal("retention silently unblocked admission", blocked, err)
	}
	if err := AbandonLocal(t.Context(), f.s.fsys, f.s.layout, f.uid, nil); err != nil {
		t.Fatal(err)
	}
	if blocked, err := f.s.SigningBlocked(); err != nil || blocked {
		t.Fatal("whole-run resolution did not clear block", blocked, err)
	}
}

type cancellationProbe struct{ entered, release chan struct{} }

func (g *cancellationProbe) Check(context.Context) error { return nil }
func (g *cancellationProbe) ProbeAccess(ctx context.Context) error {
	close(g.entered)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.release:
		return nil
	}
}
func TestQueueStopCancelsAccessProbeAfter404(t *testing.T) {
	q, s, uid, k, _, _ := queueFixture(t, operation("meta", "meta.set", Payload{Ref: "issue", Name: "example", Value: "value"}))
	g := &cancellationProbe{entered: make(chan struct{}), release: make(chan struct{})}
	q.guard = g
	k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{}, &kata.Error{Code: "not_found"}
	}
	k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) {
		t.Error("write sent after hidden read")
		return 1, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- q.Run(ctx) }()
	select {
	case <-g.entered:
	case <-time.After(5 * time.Second):
		close(g.release)
		cancel()
		<-done
		t.Fatal("access probe never started")
	}
	cancel()
	select {
	case err := <-done:
		close(g.release)
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		close(g.release)
		<-done
		t.Fatal("worker stop could not cancel the access probe")
	}
	r := loaded(t, s, uid)
	if r.Ops[0].Done || r.Ops[0].FirstSent != nil || r.Ops[0].Inspect != "" {
		t.Fatal("cancelled read changed operation evidence", r.Ops[0])
	}
}
