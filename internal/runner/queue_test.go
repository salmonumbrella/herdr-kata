package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// Only the external CLI boundary is faked; transitions use real durable files.
type fakeKata struct {
	Kata
	typedComment  func(context.Context, kata.CommentReq) (kata.Comment, error)
	typedReplies  func(context.Context) (bool, string)
	commentResult func(context.Context, string, string, string) (kata.Comment, error)
	comment       func(context.Context, string, string, string) error
	show          func(context.Context, string) (kata.IssueView, error)
	unset         func(context.Context, string, string, kata.Cond) (int64, error)
	meta          func(context.Context, string, string, string, kata.Cond) (int64, error)
	notify        func(context.Context, string, string, string) error
	claim         func(context.Context, string, bool) (kata.IssueView, error)
	observe       func(context.Context, string, kata.Observation) (kata.Run, bool, error)
	create        func(context.Context, kata.CreateReq) (kata.IssueView, error)
	close         func(context.Context, kata.CloseReq) error
}

func (k *fakeKata) CreateIssue(c context.Context, r kata.CreateReq) (kata.IssueView, error) {
	return k.create(c, r)
}
func (k *fakeKata) Close(c context.Context, r kata.CloseReq) error { return k.close(c, r) }

func (k *fakeKata) Observe(c context.Context, uid string, o kata.Observation) (kata.Run, bool, error) {
	return k.observe(c, uid, o)
}

func (k *fakeKata) TypedReplies(ctx context.Context) (bool, string) {
	if k.typedReplies != nil {
		return k.typedReplies(ctx)
	}
	return false, "test baseline"
}
func (k *fakeKata) Comment(c context.Context, req kata.CommentReq) (kata.Comment, error) {
	if k.typedComment != nil {
		return k.typedComment(c, req)
	}
	r, b, key := req.Ref, req.Body, req.IdempotencyKey
	if k.commentResult != nil {
		return k.commentResult(c, r, b, key)
	}
	e := k.comment(c, r, b, key)
	return kata.Comment{UID: "comment-" + key, Body: b}, e
}
func (k *fakeKata) ShowIssue(c context.Context, r string) (kata.IssueView, error) {
	return k.show(c, r)
}
func (k *fakeKata) MetaSet(c context.Context, r, key, v string, cond kata.Cond) (int64, error) {
	return k.meta(c, r, key, v, cond)
}
func (k *fakeKata) MetaUnset(ctx context.Context, ref, key string, cond kata.Cond) (int64, error) {
	if k.unset != nil {
		return k.unset(ctx, ref, key, cond)
	}
	if ref == "" || key != "work.attention_msg" || cond.IfMatch == "" {
		return 0, fmt.Errorf("unexpected metadata cleanup: %s %s %+v", ref, key, cond)
	}
	return 1, nil
}
func (k *fakeKata) Notify(c context.Context, r, to, msg string) error { return k.notify(c, r, to, msg) }
func (k *fakeKata) Claim(c context.Context, r string, u bool) (kata.IssueView, error) {
	return k.claim(c, r, u)
}

type testClock struct{ at time.Time }

func (c *testClock) Now() time.Time                 { return c.at }
func (c *testClock) NewTimer(d time.Duration) Timer { return RealClock{}.NewTimer(d) }

type testGuard struct {
	mu      sync.Mutex
	err     error
	calls   int
	onCheck func()
}

func (g *testGuard) Check(context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	if g.onCheck != nil {
		g.onCheck()
	}
	return g.err
}
func operation(id, kind string, p Payload) Op {
	b, err := json.Marshal(p)
	if err != nil {
		panic(err)
	}
	return Op{ID: id, Kind: kind, Payload: b, Key: "key-" + id, Safety: kata.SafetyOf(kind)}
}
func queueFixture(t *testing.T, ops ...Op) (*Queue, *Store, string, *fakeKata, *testClock, *testGuard) {
	t.Helper()
	s := testStore(t)
	uid := seed(t, s, ops...)
	if _, err := s.Transition(uid, func(r *Run) error { r.Phase = Admitted; return nil }); err != nil {
		t.Fatal(err)
	}
	k := &fakeKata{}
	c := &testClock{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	g := &testGuard{}
	return NewQueue(k, s, g, c), s, uid, k, c, g
}

func TestQueueProjectsCancellationDrainsLaneErrors(t *testing.T) {
	q := NewQueue(nil, testStore(t), nil, RealClock{})
	q.projects = true
	for i := range 10 {
		q.register(fmt.Sprintf("project-%02d", i), nil, nil, q.pinLayout)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan error, 1)
	go func() { done <- q.Run(ctx) }()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queue error = %v, want context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("project queue did not stop after cancellation")
	}
}

type cancelOnErrContext struct {
	context.Context
	cancel context.CancelFunc
	armed  atomic.Bool
}

func (c *cancelOnErrContext) Err() error {
	if c.armed.Load() {
		c.cancel()
	}
	return c.Context.Err()
}

type armCancelClock struct {
	Clock
	ctx *cancelOnErrContext
}

func (c armCancelClock) Now() time.Time {
	c.ctx.armed.Store(true)
	return c.Clock.Now()
}

func TestQueueCancellationAfterJournalReadDeadline(t *testing.T) {
	q, s, uid, k, _, _ := queueFixture(t, operation("comment", "comment", Payload{Ref: "project#task", Body: "result"}))
	k.comment = func(context.Context, string, string, string) error {
		t.Error("queue wrote after shutdown")
		return nil
	}
	deadline, stop := context.WithTimeout(t.Context(), 30*time.Second)
	defer stop()
	parent, cancel := context.WithCancel(deadline)
	defer cancel()
	ctx := &cancelOnErrContext{Context: parent, cancel: cancel}
	q.clock = armCancelClock{Clock: q.clock, ctx: ctx}
	// The batch clock arms cancellation for the next parent Err check. A
	// locked journal first expires the read's child context, then that check
	// cancels the parent, reproducing shutdown between the two observations.
	err := func() error {
		journal := s.lock(uid)
		journal.Lock()
		defer journal.Unlock()
		return q.Run(ctx)
	}()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("queue shutdown returned %v, want caller cancellation", err)
	}
	op := loaded(t, s, uid).Ops[0]
	if op.FirstSent != nil || op.Done {
		t.Fatal("shutdown changed the pending write", op)
	}
}

func deliver(t *testing.T, q *Queue, uid string) {
	t.Helper()
	if err := sendOne(q, uid); err != nil {
		t.Fatal(err)
	}
}
func sendOne(q *Queue, uid string) error {
	if err := q.guard.Check(context.Background()); err != nil {
		return err
	}
	return q.send(context.Background(), uid)
}
func loaded(t *testing.T, s *Store, uid string) *Run {
	t.Helper()
	r, err := s.Load(uid)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var lostReply = &kata.Error{Code: "transport", Exit: 7}

func TestQueueResultAppliesByOpIDNotStaleCopy(t *testing.T) {
	q, s, uid, k, _, _ := queueFixture(t, operation("a", "comment", Payload{Ref: "issue", Body: "report"}))
	k.comment = func(context.Context, string, string, string) error {
		_, err := s.Transition(uid, func(r *Run) error {
			r.Phase = Running
			r.Steps[0].Submission = Accepted
			r.Ops = append([]Op{operation("new", "comment", Payload{Ref: "issue", Body: "next"})}, r.Ops...)
			return nil
		})
		return err
	}
	deliver(t, q, uid)
	r := loaded(t, s, uid)
	if r.Phase != Running || r.Steps[0].Submission != Accepted || r.Ops[0].Done || !r.Ops[1].Done {
		t.Fatalf("stale completion: %+v", r)
	}
}
func TestQueueOrderPerRun(t *testing.T) {
	q, s, uid, k, c, _ := queueFixture(t, operation("a", "comment", Payload{Body: "first"}), operation("b", "comment", Payload{Body: "second"}))
	var got []string
	k.comment = func(_ context.Context, _, b, _ string) error {
		got = append(got, b)
		if len(got) == 1 {
			return lostReply
		}
		return nil
	}
	deliver(t, q, uid)
	deliver(t, q, uid)
	if !reflect.DeepEqual(got, []string{"first"}) {
		t.Fatalf("out of order: %v", got)
	}
	c.at = c.at.Add(2 * time.Second)
	deliver(t, q, uid)
	deliver(t, q, uid)
	if !reflect.DeepEqual(got, []string{"first", "first", "second"}) || !loaded(t, s, uid).Ops[1].Done {
		t.Fatalf("order: %v", got)
	}
}
func TestQueueReplaySafeIdenticalPayload(t *testing.T) {
	op := operation("a", "comment", Payload{Ref: "issue", Body: "exact text"})
	q, s, uid, k, c, _ := queueFixture(t, op)
	var calls int
	k.comment = func(_ context.Context, r, b, key string) error {
		calls++
		if r != "issue" || b != "exact text" || key != "key-a" {
			t.Fatal("request regenerated")
		}
		return lostReply
	}
	deliver(t, q, uid)
	first := *loaded(t, s, uid).Ops[0].FirstSent
	c.at = c.at.Add(2 * time.Second)
	deliver(t, q, uid)
	saved := loaded(t, s, uid).Ops[0]
	if calls != 2 || !reflect.DeepEqual(saved.Payload, op.Payload) || !saved.FirstSent.Equal(first) || saved.NextAt.Sub(c.at) != 4*time.Second {
		t.Fatalf("replay changed: %+v", saved)
	}
}
func TestQueueGuardedKeepsSavedGuard(t *testing.T) {
	q, s, uid, k, c, _ := queueFixture(t, operation("a", "meta.set", Payload{Ref: "issue", Name: "custom", Value: "ok"}))
	reads := 0
	k.show = func(context.Context, string) (kata.IssueView, error) {
		reads++
		return kata.IssueView{Revision: 42}, nil
	}
	k.meta = func(_ context.Context, _, _, _ string, cond kata.Cond) (int64, error) {
		if cond.IfMatch != "42" {
			t.Fatalf("guard changed: %+v", cond)
		}
		return 0, lostReply
	}
	deliver(t, q, uid)
	c.at = c.at.Add(2 * time.Second)
	deliver(t, q, uid)
	if reads != 1 || loaded(t, s, uid).Ops[0].Guard.IfMatch != "42" {
		t.Fatalf("guard refrozen: %d", reads)
	}
}
func TestQueueUnguardedReadsBack(t *testing.T) {
	for _, present := range []bool{true, false} {
		t.Run(map[bool]string{true: "present", false: "missing"}[present], func(t *testing.T) {
			q, s, uid, k, _, _ := queueFixture(t, operation("a", "notify", Payload{Ref: "issue", To: "alice/runtime", Message: "help"}))
			if _, err := s.Transition(uid, func(r *Run) error { r.Target.Identity.Actor = "alice"; return nil }); err != nil {
				t.Fatal(err)
			}
			calls := 0
			k.notify = func(context.Context, string, string, string) error { calls++; return lostReply }
			k.show = func(context.Context, string) (kata.IssueView, error) {
				m := map[string]json.RawMessage{}
				if present {
					m["notify.YWxpY2UvcnVudGltZQ"] = json.RawMessage(`{"from":"alice","teammate":"herdr-kata","message":"help"}`)
				}
				return kata.IssueView{Metadata: m}, nil
			}
			deliver(t, q, uid)
			deliver(t, q, uid)
			op := loaded(t, s, uid).Ops[0]
			if calls != 1 || op.Done != present || (!present && op.Inspect == "") {
				t.Fatalf("blind replay: %d %+v", calls, op)
			}
		})
	}
}
func TestQueueClaimReadBack(t *testing.T) {
	q, s, uid, k, _, _ := queueFixture(t, operation("a", "claim", Payload{Ref: "issue", Owner: "alice", IfUnowned: true}))
	calls := 0
	k.claim = func(context.Context, string, bool) (kata.IssueView, error) {
		calls++
		return kata.IssueView{}, lostReply
	}
	k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{UID: "issue", Owner: "alice"}, nil
	}
	deliver(t, q, uid)
	deliver(t, q, uid)
	if calls != 1 || !loaded(t, s, uid).Ops[0].Done {
		t.Fatalf("claim unresolved: %d", calls)
	}
}
func TestQueueFatalInspect(t *testing.T) {
	q, s, uid, k, _, _ := queueFixture(t, operation("a", "comment", Payload{}))
	calls := 0
	k.comment = func(context.Context, string, string, string) error {
		calls++
		return &kata.Error{Code: "validation", Exit: 2}
	}
	deliver(t, q, uid)
	deliver(t, q, uid)
	r := loaded(t, s, uid)
	if calls != 1 || r.Inspect == "" || r.Ops[0].Inspect == "" {
		t.Fatalf("fatal retried: %+v", r)
	}
}
func TestQueueSevenDayHorizon(t *testing.T) {
	q, s, uid, k, c, _ := queueFixture(t, operation("a", "comment", Payload{}))
	calls := 0
	k.comment = func(context.Context, string, string, string) error { calls++; return lostReply }
	deliver(t, q, uid)
	c.at = c.at.Add(7 * 24 * time.Hour)
	deliver(t, q, uid)
	if calls != 1 || loaded(t, s, uid).Ops[0].Inspect == "" {
		t.Fatal("expired key resent")
	}
}
func TestQueueBlockedByRouteGuard(t *testing.T) {
	q, _, _, k, _, g := queueFixture(t, operation("a", "comment", Payload{}))
	if err := g.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	g.err = errors.New("catalog changed")
	calls := 0
	k.comment = func(context.Context, string, string, string) error { calls++; return nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.onCheck = cancel
	if err := q.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("route-blocked queue: %v", err)
	}
	if calls != 0 || g.calls != 2 {
		t.Fatalf("stale gate: mutations=%d checks=%d", calls, g.calls)
	}
}
func TestQueueCoalescesAttention(t *testing.T) {
	p := Payload{Ref: "issue", Name: "work.attention", Value: "ok", Owner: "alice"}
	q, s, uid, k, _, _ := queueFixture(t, operation("a", "meta.set", p), operation("b", "meta.set", p))
	_, err := s.Transition(uid, func(r *Run) error { r.IssueOwned = true; r.Target.Identity.Actor = "alice"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{Status: "open", Owner: "alice", Revision: 1}, nil
	}
	calls := 0
	k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) { calls++; return 2, nil }
	deliver(t, q, uid)
	deliver(t, q, uid)
	if calls != 1 || !loaded(t, s, uid).Ops[1].Done {
		t.Fatalf("unchanged attention sent %d times", calls)
	}
}
func TestQueueRunBoundsConcurrency(t *testing.T) {
	p := Payload{Observation: &kata.Observation{Status: "running"}}
	q, s, uid, k, _, _ := queueFixture(t, operation("a", "observe", p))
	for _, id := range []string{"two", "three"} {
		if err := s.Create(&Run{Version: 1, UID: id, Phase: Settled, Ops: []Op{operation(id, "observe", p)}}); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	active, max, calls := 0, 0, 0
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) {
		mu.Lock()
		active++
		calls++
		if active > max {
			max = active
		}
		mu.Unlock()
		entered <- struct{}{}
		<-release
		mu.Lock()
		active--
		mu.Unlock()
		return kata.Run{}, false, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- q.Run(ctx) }()
	q.Kick(uid)
	for range 2 {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("queue did not start")
		}
	}
	close(release)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("third run lost")
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if max > 2 || calls != 3 {
		t.Fatalf("max=%d calls=%d", max, calls)
	}
}

func FuzzQueueJSONReadBackExactNumbers(f *testing.F) {
	f.Add(uint64(9007199254740992), false)
	f.Add(uint64(9007199254740993), true)
	f.Fuzz(func(t *testing.T, n uint64, match bool) {
		q, s, uid, k, _, _ := queueFixture(t, operation("a", "meta.set", Payload{Ref: "issue", Name: "custom", Value: fmt.Sprintf(`{"n":%d}`, n)}))
		_, err := s.Transition(uid, func(r *Run) error { r.Ops[0].Guard = &kata.Cond{IfMatch: "1", JSONValue: true}; return nil })
		if err != nil {
			t.Fatal(err)
		}
		k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) {
			return 0, &kata.Error{Code: "revision_conflict", Exit: 5}
		}
		k.show = func(context.Context, string) (kata.IssueView, error) {
			observed := n
			if !match {
				observed ^= 1
			}
			return kata.IssueView{Metadata: map[string]json.RawMessage{"custom": json.RawMessage(fmt.Sprintf(`{"n":%d}`, observed))}}, nil
		}
		deliver(t, q, uid)
		op := loaded(t, s, uid).Ops[0]
		if op.Done != match || (match && op.Inspect != "") || (!match && op.Inspect == "") {
			t.Fatalf("incorrect exact-number confirmation: %d match=%t %+v", n, match, op)
		}
	})
}

func TestSentOperationImmutable(t *testing.T) {
	q, s, uid, k, _, _ := queueFixture(t, operation("a", "comment", Payload{Body: "original"}))
	k.comment = func(context.Context, string, string, string) error { return lostReply }
	deliver(t, q, uid)
	_, err := s.Transition(uid, func(r *Run) error { r.Ops[0].Payload = []byte(`{"Body":"changed"}`); return nil })
	if err == nil {
		t.Fatal("sent payload changed")
	}
}

func TestQueueReadBackUsesOperationTarget(t *testing.T) {
	q, s, uid, k, _, _ := queueFixture(t, operation("a", "notify", Payload{Ref: "actual", To: "alice", Message: "help", Close: &kata.CloseReq{Ref: "other"}}))
	k.notify = func(context.Context, string, string, string) error { return lostReply }
	k.show = func(_ context.Context, ref string) (kata.IssueView, error) {
		if ref != "actual" {
			t.Errorf("read back wrong issue: %s", ref)
		}
		return kata.IssueView{}, nil
	}
	deliver(t, q, uid)
	if loaded(t, s, uid).Ops[0].Done {
		t.Fatal("unconfirmed effect accepted")
	}
}

type failFS struct {
	state.FS
	fail bool
}

func (f *failFS) SyncDir(p string) error {
	if f.fail {
		return errors.New("injected sync failure")
	}
	return f.FS.SyncDir(p)
}
func TestQueueFsyncFailureSendsNothing(t *testing.T) {
	disk := &failFS{FS: state.OSFS{}}
	s := NewStore(disk, state.Layout{Root: t.TempDir()})
	uid := seed(t, s, operation("a", "comment", Payload{}))
	if _, err := s.Transition(uid, func(r *Run) error { r.Phase = Admitted; return nil }); err != nil {
		t.Fatal(err)
	}
	calls := 0
	k := &fakeKata{comment: func(context.Context, string, string, string) error { calls++; return nil }}
	q := NewQueue(k, s, &testGuard{}, RealClock{})
	disk.fail = true
	if err := sendOne(q, uid); err == nil {
		t.Fatal("fsync failure ignored")
	}
	if calls != 0 {
		t.Fatal("mutation sent after failed durable marker")
	}
}

func TestQueueRestartNeverResendsUnguarded(t *testing.T) {
	q, s, uid, k, c, g := queueFixture(t, operation("a", "notify", Payload{Ref: "issue", To: "alice", Message: "help"}))
	calls := 0
	k.notify = func(context.Context, string, string, string) error { calls++; return lostReply }
	k.show = func(context.Context, string) (kata.IssueView, error) { return kata.IssueView{}, lostReply }
	deliver(t, q, uid)
	q = NewQueue(k, s, g, c)
	c.at = c.at.Add(2 * time.Second)
	deliver(t, q, uid)
	c.at = c.at.Add(2 * time.Second)
	deliver(t, q, uid)
	if calls != 1 || loaded(t, s, uid).Ops[0].Done {
		t.Fatal("uncertain notify blindly resent")
	}
}

func FuzzStoreIdentityRoundTrip(f *testing.F) {
	f.Add("run")
	f.Add("\xff")
	f.Add("../escape")
	f.Fuzz(func(t *testing.T, uid string) {
		s := testStore(t)
		err := s.Create(&Run{Version: 1, UID: uid, Phase: Prepared})
		if err != nil {
			return
		}
		r, err := s.Load(uid)
		if err != nil || r.UID != uid {
			t.Fatalf("accepted UID lost: %q %+v %v", uid, r, err)
		}
	})
}

func TestQueueOwnedUnownedIssue(t *testing.T) {
	p := Payload{Ref: "issue", Name: "work.attention", Value: "ok", Owner: ""}
	q, s, uid, k, _, _ := queueFixture(t, operation("a", "meta.set", p))
	if _, err := s.Transition(uid, func(r *Run) error { r.IssueOwned = true; r.Target.Identity.Actor = "alice"; return nil }); err != nil {
		t.Fatal(err)
	}
	k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{Status: "open", Revision: 1}, nil
	}
	k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) { return 2, nil }
	deliver(t, q, uid)
	if !loaded(t, s, uid).Ops[0].Done {
		t.Fatal("created unowned issue refused")
	}
}

func TestQueueUnsentIntentChangesDuringGuardRead(t *testing.T) {
	q, s, uid, k, _, _ := queueFixture(t, operation("a", "meta.set", Payload{Ref: "issue", Name: "custom", Value: "old"}))
	k.show = func(context.Context, string) (kata.IssueView, error) {
		_, err := s.Transition(uid, func(r *Run) error {
			r.Ops[0].Payload = operation("a", "meta.set", Payload{Ref: "issue", Name: "custom", Value: "new"}).Payload
			return nil
		})
		return kata.IssueView{Revision: 1}, err
	}
	calls := 0
	k.meta = func(_ context.Context, _, _, v string, _ kata.Cond) (int64, error) { calls++; return 2, nil }
	deliver(t, q, uid)
	if calls != 0 {
		t.Fatal("stale payload sent with a different journaled intent")
	}
}

func TestQueueCancellationKeepsPending(t *testing.T) {
	q, s, uid, k, _, _ := queueFixture(t, operation("a", "comment", Payload{}))
	k.comment = func(context.Context, string, string, string) error { return context.Canceled }
	err := sendOne(q, uid)
	r := loaded(t, s, uid)
	if !errors.Is(err, context.Canceled) || r.Ops[0].Inspect != "" || r.Ops[0].Done {
		t.Fatalf("shutdown corrupted pending operation: %+v %v", r.Ops[0], err)
	}
}

func TestQueueGuardReadBackoffDoubles(t *testing.T) {
	q, s, uid, k, c, _ := queueFixture(t, operation("a", "meta.set", Payload{Ref: "issue", Name: "custom", Value: "ok"}))
	k.show = func(context.Context, string) (kata.IssueView, error) { return kata.IssueView{}, lostReply }
	for _, want := range []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second} {
		deliver(t, q, uid)
		op := loaded(t, s, uid).Ops[0]
		if op.NextAt.Sub(c.at) != want {
			t.Fatalf("delay=%s want=%s", op.NextAt.Sub(c.at), want)
		}
		c.at = op.NextAt
	}
}

func TestQueueRunnerCanHoldOrReorderBeforeSend(t *testing.T) {
	for _, mode := range []string{"inspect", "delay", "reorder"} {
		t.Run(mode, func(t *testing.T) {
			q, s, uid, k, c, _ := queueFixture(t, operation("a", "meta.set", Payload{Ref: "issue", Name: "custom", Value: "ok"}))
			k.show = func(context.Context, string) (kata.IssueView, error) {
				_, err := s.Transition(uid, func(r *Run) error {
					switch mode {
					case "inspect":
						r.Ops[0].Inspect = "held"
					case "delay":
						r.Ops[0].NextAt = c.at.Add(time.Hour)
					case "reorder":
						r.Ops = append([]Op{operation("earlier", "comment", Payload{})}, r.Ops...)
					}
					return nil
				})
				return kata.IssueView{Revision: 1}, err
			}
			calls := 0
			k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) { calls++; return 2, nil }
			deliver(t, q, uid)
			if calls != 0 {
				t.Fatal("ineligible operation sent")
			}
		})
	}
}

func TestQueueCreateConflictReadsBackRunMarker(t *testing.T) {
	for _, match := range []bool{false, true} {
		t.Run(fmt.Sprint(match), func(t *testing.T) {
			// Admission supplies the marker in its frozen create request.
			marker := map[string]string{"herdr_kata.run": "01K00000000000000000000000", "work.attention": "ok"}
			q, s, uid, k, _, _ := queueFixture(t, operation("a", "create", Payload{Create: &kata.CreateReq{Title: "Task", Body: "Perform task", Meta: marker}}))
			k.create = func(_ context.Context, req kata.CreateReq) (kata.IssueView, error) {
				if req.Meta["herdr_kata.run"] != uid || req.Meta["work.attention"] != "ok" || req.IdempotencyKey != "key-a" {
					t.Fatalf("frozen create metadata changed: %+v", req)
				}
				return kata.IssueView{}, &kata.Error{Code: "duplicate_candidates", Exit: 5, CandidateUIDs: []string{"candidate"}}
			}
			k.show = func(context.Context, string) (kata.IssueView, error) {
				marker := "other-run"
				if match {
					marker = uid
				}
				b, _ := json.Marshal(marker)
				return kata.IssueView{UID: "candidate", QualifiedID: "project#abc", Metadata: map[string]json.RawMessage{"herdr_kata.run": b}}, nil
			}
			deliver(t, q, uid)
			r := loaded(t, s, uid)
			if r.Ops[0].Done != match || (!match && r.Ops[0].Inspect == "") || (match && r.IssueUID != "candidate") {
				t.Fatalf("wrong candidate decision: %+v", r)
			}
		})
	}
}
func TestQueueCloseConflictReadsBackStatus(t *testing.T) {
	for _, status := range []string{"open", "closed"} {
		t.Run(status, func(t *testing.T) {
			q, s, uid, k, _, _ := queueFixture(t, operation("a", "close", Payload{Close: &kata.CloseReq{Ref: "issue", IfMatch: "1", Reason: "done"}}))
			k.close = func(context.Context, kata.CloseReq) error { return &kata.Error{Code: "revision_conflict", Exit: 5} }
			k.show = func(context.Context, string) (kata.IssueView, error) { return kata.IssueView{Status: status}, nil }
			deliver(t, q, uid)
			op := loaded(t, s, uid).Ops[0]
			if op.Done != (status == "closed") || (status == "open" && op.Inspect == "") {
				t.Fatalf("wrong close decision: %+v", op)
			}
		})
	}
}

func TestQueueSettlementFreezesCloseRevision(t *testing.T) {
	q, s, uid, k, _, _ := queueFixture(t, operation("a", "close", Payload{Close: &kata.CloseReq{Ref: "issue", Reason: "done"}}))
	s.Transition(uid, func(r *Run) error { r.IssueOwned = true; return nil })
	k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{UID: "issue", Revision: 42, Status: "open"}, nil
	}
	k.close = func(_ context.Context, req kata.CloseReq) error {
		if req.IfMatch != "42" {
			t.Errorf("unguarded close %+v", req)
		}
		r := loaded(t, s, uid)
		if r.Ops[0].Guard == nil || r.Ops[0].Guard.IfMatch != "42" || r.Ops[0].FirstSent == nil {
			t.Error("guard not durable")
		}
		return nil
	}
	deliver(t, q, uid)
}
func TestQueueResolvesRecipientOnce(t *testing.T) {
	q, s, uid, k, _, _ := queueFixture(t, operation("resolve", "resolve-recipient", Payload{Ref: "issue"}), operation("notify", "notify", Payload{Ref: "issue", Message: "help"}))
	reads := 0
	k.show = func(context.Context, string) (kata.IssueView, error) {
		reads++
		return kata.IssueView{Owner: "owner", Author: "author"}, nil
	}
	k.notify = func(_ context.Context, ref, to, msg string) error {
		if ref != "issue" || to != "owner" || msg != "help" {
			t.Error(ref, to, msg)
		}
		r := loaded(t, s, uid)
		var p Payload
		json.Unmarshal(r.Ops[1].Payload, &p)
		if p.To != "owner" || !r.Ops[0].Done {
			t.Error("recipient not saved")
		}
		return nil
	}
	deliver(t, q, uid)
	deliver(t, q, uid)
	if reads != 1 || !loaded(t, s, uid).Ops[1].Done {
		t.Fatal(reads)
	}
}

func TestCloseValidationRejectionFallsBackToReview(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		t.Run(fmt.Sprint(resumed), func(t *testing.T) {
			q, s, uid, k, _, _ := queueFixture(t)
			s.Transition(uid, func(r *Run) error {
				r.Phase = Settled
				r.Outcome = "succeeded"
				r.IssueOwned = true
				r.IssueRef = "issue"
				r.IssueUID = "issue"
				res := &Result{Status: "succeeded", Summary: "Verified the implementation with focused regression tests.", Evidence: []string{"test:go test ./internal/run"}}
				if resumed {
					r.Resume = &ResumeState{UID: "reviewed", Generation: 1}
				}
				r.Ops = SettleOps(r, res, q.clock.Now())
				return nil
			})
			k.show = func(context.Context, string) (kata.IssueView, error) {
				return kata.IssueView{UID: "issue", Status: "open", Revision: 4}, nil
			}
			k.close = func(context.Context, kata.CloseReq) error {
				return &kata.Error{Code: "validation", Exit: 2, Message: "evidence rejected"}
			}
			comments := map[string]string{}
			k.comment = func(_ context.Context, _ string, body, key string) error {
				if previous, ok := comments[key]; ok && previous != body {
					return fmt.Errorf("idempotency conflict: %s", key)
				}
				comments[key] = body
				return nil
			}
			k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) { return 5, nil }
			k.Kata = &settlementLabels{}
			observed := false
			k.observe = func(_ context.Context, _ string, o kata.Observation) (kata.Run, bool, error) {
				observed = o.Status == "succeeded"
				return kata.Run{}, false, nil
			}
			for range 10 {
				deliver(t, q, uid)
			}
			r := loaded(t, s, uid)
			if r.Phase != Settled || r.Outcome != "succeeded" || r.Inspect != "" || !observed {
				t.Fatal("validation lost terminal outcome", r, observed)
			}
			found := false
			for _, op := range r.Ops {
				var p Payload
				json.Unmarshal(op.Payload, &p)
				if p.Name == "needs-review" && op.Done {
					found = true
				}
			}
			if !found {
				t.Fatal("no review fallback")
			}

		})
	}
}

// A definitive close refusal must report successful command work for review,
// even when its receipts independently supply valid closure evidence.
func TestCloseValidationCommandRejectionDeliversReview(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, resumed := range []bool{false, true} {
			t.Run(fmt.Sprintf("mixed=%t/resumed=%t", mixed, resumed), func(t *testing.T) {
				q, s, uid, k, _, _ := queueFixture(t)
				if _, err := s.Transition(uid, func(r *Run) error {
					r.Phase, r.Outcome = Settled, "succeeded"
					r.IssueOwned, r.IssueRef, r.IssueUID = true, "issue", "issue"
					r.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "check", Kind: "command", Command: "make check"}}}
					r.Steps = []StepState{{Key: "check", Kind: "command", Attempt: 1, Status: "succeeded", Receipt: &Receipt{Version: 1, Run: uid, Step: "check", Attempt: 1, ExitCode: 0, PGID: 1}}}
					if mixed {
						r.WFDef.Steps = append(r.WFDef.Steps, defs.Step{Key: "main", Kind: "prompt"})
						r.Steps = append(r.Steps, StepState{Key: "main", Kind: "prompt", Attempt: 1, Status: "succeeded"})
					}
					if resumed {
						r.Resume = &ResumeState{UID: "reviewed", Generation: 1}
					}
					r.Ops = SettleOps(r, &Result{Status: "succeeded", Summary: "Verified the implementation with focused regression tests.", CloseReason: "done", Evidence: []string{"test:make check"}}, q.clock.Now())
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				k.show = func(context.Context, string) (kata.IssueView, error) {
					return kata.IssueView{UID: "issue", Status: "open", Revision: 4}, nil
				}
				closes := 0
				k.close = func(_ context.Context, req kata.CloseReq) error {
					closes++
					if req.Ref != "issue" || req.Reason != "done" || !reflect.DeepEqual(req.Evidence, []string{"test:make check"}) {
						t.Fatalf("unexpected refused close: %+v", req)
					}
					return &kata.Error{Code: "validation", Exit: 2, Message: "evidence rejected"}
				}
				comments := map[string]string{}
				k.comment = func(_ context.Context, ref, body, key string) error {
					if ref != "issue" || body == "" {
						t.Fatalf("invalid review comment: %s %q", ref, body)
					}
					comments[key] = body
					return nil
				}
				metadata := map[string]string{}
				k.meta = func(_ context.Context, ref, name, value string, cond kata.Cond) (int64, error) {
					if ref != "issue" || cond.IfMatch == "" {
						t.Fatalf("unguarded review attention: %s %s %+v", ref, name, cond)
					}
					metadata[name] = value
					return 5, nil
				}
				labels := &reviewLabels{}
				k.Kata = labels
				observed := 0
				k.observe = func(_ context.Context, _ string, o kata.Observation) (kata.Run, bool, error) {
					if o.Status != "succeeded" {
						t.Fatalf("lost successful observation: %+v", o)
					}
					observed++
					return kata.Run{}, false, nil
				}
				for range 12 {
					deliver(t, q, uid)
				}
				r := loaded(t, s, uid)
				prefix := "herdr-kata:" + uid
				if resumed {
					prefix += ":resume:reviewed"
				}
				if comments[prefix+":comment:succeeded"] == "" || !reflect.DeepEqual(labels.calls, []string{"issue:needs-review"}) || metadata["work.attention"] != "needs-human" || metadata["work.attention_msg"] != "herdr-kata: review run "+uid {
					t.Fatalf("review not delivered: comments=%v labels=%v metadata=%v", comments, labels.calls, metadata)
				}
				if closes != 1 || observed != 1 || r.Phase != Settled || r.Outcome != "succeeded" || r.Inspect != "" {
					t.Fatalf("changed settlement or retried close: closes=%d observed=%d run=%+v", closes, observed, r)
				}
				for _, op := range r.Ops {
					if !op.Done || op.Inspect != "" {
						t.Fatalf("unfinished review operation: %+v", op)
					}
				}
			})
		}
	}
}

type reviewLabels struct {
	Kata
	calls []string
}

func (k *reviewLabels) LabelAdd(_ context.Context, ref, name string) error {
	k.calls = append(k.calls, ref+":"+name)
	return nil
}

type settlementLabels struct{ Kata }

func (*settlementLabels) LabelAdd(context.Context, string, string) error { return nil }

func TestOwnedCloseLostReplyReadBackCompletes(t *testing.T) {
	q, s, uid, k, c, _ := queueFixture(t, operation("close", "close", Payload{Close: &kata.CloseReq{Ref: "issue", Reason: "done"}}), operation("observe", "observe", Payload{Observation: &kata.Observation{Status: "succeeded"}}))
	s.Transition(uid, func(r *Run) error { r.Phase = Settled; r.Outcome = "succeeded"; r.IssueOwned = true; return nil })
	closed := false
	k.show = func(context.Context, string) (kata.IssueView, error) {
		status := "open"
		if closed {
			status = "closed"
		}
		return kata.IssueView{UID: "issue", Status: status, Revision: 1}, nil
	}
	sends := 0
	k.close = func(context.Context, kata.CloseReq) error {
		sends++
		closed = true
		return &kata.Error{Code: "transport", Exit: 7}
	}
	observed := false
	k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) {
		observed = true
		return kata.Run{}, false, nil
	}
	deliver(t, q, uid)
	c.at = c.at.Add(2 * time.Second)
	deliver(t, q, uid)
	deliver(t, q, uid)
	r := loaded(t, s, uid)
	if r.Inspect != "" || !r.Ops[0].Done || !observed || sends != 1 {
		t.Fatal("committed close stranded report", r, sends, observed)
	}
}

func TestQueueReleaseCancelsRecipient(t *testing.T) {
	q, s, uid, k, _, _ := queueFixture(t,
		operation("herdr-kata:x:settle:recipient", "resolve-recipient", Payload{Ref: "issue"}),
		operation("herdr-kata:x:settle:notify", "notify", Payload{Ref: "issue", Message: "m"}))
	k.show = func(context.Context, string) (kata.IssueView, error) {
		// Simulates Release's transition landing between send's Load and its Transition.
		_, err := s.Transition(uid, func(r *Run) error {
			for i := range r.Ops {
				r.Ops[i].Done, r.Ops[i].Inspect = true, "cancelled by release"
			}
			r.Ops = append(r.Ops, operation("herdr-kata:x:release:observe", "observe", Payload{Observation: &kata.Observation{}}))
			return nil
		})
		return kata.IssueView{Owner: "someone"}, err
	}
	if err := sendOne(q, uid); err != nil {
		t.Fatalf("stale recipient delivery is fatal to Queue.Run: %v", err)
	}
}

func TestQueueShutdownWhileRouteCheckCompletes(t *testing.T) {
	q, s, uid, k, _, guard := queueFixture(t, operation("report", "comment", Payload{Ref: "issue", Body: "saved report"}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	guard.onCheck = cancel
	k.comment = func(context.Context, string, string, string) error {
		t.Error("write started after shutdown")
		return nil
	}
	if e := q.Run(ctx); e != context.Canceled {
		t.Fatalf("shutdown lost its cancellation result: %T %v", e, e)
	}
	op := loaded(t, s, uid).Ops[0]
	if op.Done || op.FirstSent != nil {
		t.Fatal("shutdown changed the saved operation", op)
	}
}

func TestQueueRechecksRouteAfterScanAndWriteSlotWait(t *testing.T) {
	for _, wait := range []string{"journal scan", "write slot"} {
		t.Run(wait, func(t *testing.T) {
			q, s, uid, k, _, _ := queueFixture(t, operation("report", "comment", Payload{Ref: "issue", Body: "saved report"}))
			var changed atomic.Bool
			checked, refused := make(chan struct{}), make(chan struct{})
			var checkOnce, refusalOnce sync.Once
			q.guard = laneCheckFunc(func(context.Context) error {
				if changed.Load() {
					refusalOnce.Do(func() { close(refused) })
					return errors.New("configured route changed")
				}
				checkOnce.Do(func() { close(checked) })
				return nil
			})
			mutations := make(chan struct{}, 4)
			k.comment = func(context.Context, string, string, string) error { mutations <- struct{}{}; return nil }
			var gate *blockedRecoveryPin
			var releaseOnce sync.Once
			unblock := func() {
				if gate != nil {
					releaseOnce.Do(func() { close(gate.release) })
				}
			}
			if wait == "journal scan" {
				if err := s.Create(&Run{Version: 1, UID: "zzz", Phase: Settled}); err != nil {
					t.Fatal(err)
				}
				gate = &blockedRecoveryPin{FS: s.fsys, path: s.layout.RunFile("zzz"), entered: make(chan struct{}), release: make(chan struct{})}
				s.fsys = gate
			} else {
				q.writeSlots <- struct{}{}
				q.writeSlots <- struct{}{}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			done := make(chan error, 1)
			go func() { done <- q.Run(ctx) }()
			joined := false
			defer func() {
				unblock()
				cancel()
				if !joined {
					<-done
				}
			}()
			select {
			case <-checked:
			case <-ctx.Done():
				t.Fatal("initial route check did not complete")
			}
			if gate != nil {
				select {
				case <-gate.entered:
				case <-ctx.Done():
					t.Fatal("journal scan did not block after route check")
				}
			} else {
				for q.issueMu.TryLock() {
					q.issueMu.Unlock()
					if ctx.Err() != nil {
						t.Fatal("delivery did not wait for its write slot")
					}
					time.Sleep(time.Millisecond)
				}
			}
			changed.Store(true)
			if gate != nil {
				unblock()
			} else {
				<-q.writeSlots
			}
			select {
			case <-refused:
			case <-mutations:
				t.Error("delivery used a route check from before its wait")
			case <-ctx.Done():
				t.Error("changed route was not rechecked")
			}
			cancel()
			<-done
			joined = true
			op := loaded(t, s, uid).Ops[0]
			if len(mutations) != 0 || op.FirstSent != nil || op.Done {
				t.Fatal("changed route received or marked a delivery", op.FirstSent, op.Done)
			}
		})
	}
}

// Every lane's sends share two write slots. A freed slot goes to one waiter,
// which rechecks its route once; the other waiters keep waiting without
// probing Kata. Handing the slot around made each waiter probe per release.
func TestQueueSlotWaitersRecheckOncePerSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := Payload{Observation: &kata.Observation{Status: "running"}}
		s := testStore(t)
		q := NewQueue(nil, s, nil, RealClock{})
		q.projects = true
		const lanes = 8
		entered := make(chan struct{}, lanes)
		release := make(chan struct{})
		k := &fakeKata{observe: func(context.Context, string, kata.Observation) (kata.Run, bool, error) {
			entered <- struct{}{}
			<-release
			return kata.Run{}, false, nil
		}}
		ctx, cancel := context.WithCancel(t.Context())
		// A slot handed between waiters never blocks; stop it so the test fails
		// instead of spinning.
		g := &testGuard{}
		g.onCheck = func() {
			if g.calls > 1000 {
				cancel()
			}
		}
		for i := range lanes {
			project := fmt.Sprintf("project-%02d", i)
			uid := fmt.Sprintf("01K000000000000000000000%02d", i)
			r := &Run{Version: 1, UID: uid, Phase: Settled, Ops: []Op{operation(uid, "observe", p)}}
			r.Target.Identity.ProjectUID = project
			if err := s.Create(r); err != nil {
				t.Fatal(err)
			}
			q.register(project, k, g, q.pinLayout)
		}
		done := make(chan error, 1)
		go func() { done <- q.Run(ctx) }()
		defer func() { cancel(); close(release); <-done }()
		checks := func() int { g.mu.Lock(); defer g.mu.Unlock(); return g.calls }
		<-entered
		<-entered
		// The other lanes now wait for a slot.
		synctest.Wait()
		for range lanes - 2 {
			before := checks()
			release <- struct{}{}
			synctest.Wait()
			if probes := checks() - before; probes > 1 || len(entered) != 1 {
				t.Fatalf("one freed slot cost %d route checks and started %d sends", probes, len(entered))
			}
			<-entered
		}
	})
}
