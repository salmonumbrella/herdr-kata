package runner

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

func TestFatalCreateReleasesCapacityAndCanBeReleased(t *testing.T) {
	f := newAdmission(t)
	calls := 0
	f.k.create = func(context.Context, kata.CreateReq) (kata.IssueView, error) {
		calls++
		return kata.IssueView{}, &kata.Error{Code: "validation", Message: "title contains NUL"}
	}
	f.start(t)
	_, err := f.m.Admit(t.Context(), f.d)
	if err == nil {
		t.Fatal("fatal create accepted")
	}
	r := loaded(t, f.s, f.uid)
	if r.Phase != Held || r.Reason != "create_rejected" || !r.SlotReleased || !strings.Contains(r.Inspect, "title contains NUL") || calls != 1 {
		t.Fatalf("fatal create wedged: %+v calls=%d", r, calls)
	}
	if f.m.FreeSlots() != 2 {
		t.Fatal("create refusal kept execution capacity")
	}
	if err := f.m.Release(t.Context(), f.uid); err != nil {
		t.Fatal("cannot release fatal create", err)
	}
	r = loaded(t, f.s, f.uid)
	if r.Phase != HandedOff || !r.SlotReleased || calls != 1 {
		t.Fatalf("release did not retire rejected create: %+v", r)
	}
}

func TestAdmissionRejectionDoesNotKeepOverlapGate(t *testing.T) {
	f := newAdmission(t)
	f.k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) {
		return kata.Run{}, false, &kata.Error{Code: "conflict"}
	}
	f.start(t)
	_, _ = f.m.Admit(t.Context(), f.d)
	r := loaded(t, f.s, f.uid)
	if r.Reason != "admission_rejected" {
		t.Fatal(r)
	}
	if f.m.GateHeld("job") || f.m.FreeSlots() != 2 {
		t.Fatal("rejected admission retained overlap/capacity")
	}
	if err := f.m.RestoreGates(); err != nil {
		t.Fatal(err)
	}
	if f.m.GateHeld("job") {
		t.Fatal("restart restored rejected admission gate")
	}
}

func TestRejectedOccurrenceIsDroppedWithoutRetryingAdmission(t *testing.T) {
	f := newAdmission(t)
	observes := 0
	f.k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) {
		observes++
		return kata.Run{}, false, &kata.Error{Code: "conflict"}
	}
	f.start(t)
	_, _ = f.m.Admit(t.Context(), f.d)
	result, err := f.m.Admit(t.Context(), f.d)
	if result != sched.Dropped || err != nil || observes != 1 {
		t.Fatalf("rejected occurrence wedges later fires: result=%v err=%v writes=%d", result, err, observes)
	}
}

// Kata refuses an admission that another installation already made. The run
// is held without executing and names the winner. The lane lease, not this
// conflict, decides which installation serves the lane, so nothing is
// written and the next occurrence is admitted as usual.
func TestForeignAdmissionHoldsOnlyThatRun(t *testing.T) {
	f := newAdmission(t)
	var observed atomic.Bool
	f.k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) {
		if observed.Swap(true) {
			return kata.Run{}, false, nil
		}
		return kata.Run{}, false, &kata.Error{Code: "cron_conflict"}
	}
	f.k.run = func(_ context.Context, uid string) (kata.Run, error) {
		if !observed.Load() || uid != f.uid {
			return kata.Run{}, &kata.Error{Code: "not_found"}
		}
		return kata.Run{UID: f.uid, JobUID: "job", ExecutorLabel: "elsewhere", IssueUID: "issue"}, nil
	}
	f.k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) {
		t.Error("a refused admission wrote issue metadata")
		return 0, nil
	}
	f.start(t)
	_, _ = f.m.Admit(t.Context(), f.d)
	r := loaded(t, f.s, f.uid)
	if r.Phase != Held || r.Reason != "admission_rejected" || r.Inspect != "another installation (elsewhere) admitted this occurrence" || f.m.GateHeld("job") {
		t.Fatalf("foreign admission: %+v", r)
	}
	f.d.Occurrence = f.d.Occurrence.Add(time.Hour)
	if result, err := f.m.Admit(t.Context(), f.d); result == sched.NotAdmitted && err != nil {
		t.Fatalf("the next occurrence was refused: %v", err)
	}
}

// A run records the lease generation it was prepared under. If the lane's
// lease passes to another installation before the run's admission writes go
// out, the run is held without creating its issue or observing its
// admission. Without the lease, nothing is prepared at all.
func TestLostLeaseFencesPreAdmissionWrites(t *testing.T) {
	f := newAdmission(t)
	var generation atomic.Int64
	generation.Store(2)
	held := atomic.Bool{}
	held.Store(true)
	f.m.SetLease(func() (int64, bool) { return generation.Load(), held.Load() })
	f.k.create = func(context.Context, kata.CreateReq) (kata.IssueView, error) {
		t.Error("created an issue after the lease passed")
		return kata.IssueView{}, nil
	}
	f.k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) {
		t.Error("observed an admission after the lease passed")
		return kata.Run{}, false, nil
	}
	// The takeover lands between preparation and the first admission write:
	// the queue starts only after the run is prepared and the lease is gone.
	admitted := make(chan struct{})
	go func() {
		defer close(admitted)
		_, _ = f.m.Admit(t.Context(), f.d)
	}()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for _, err := f.s.Load(f.uid); err != nil; _, err = f.s.Load(f.uid) {
		<-tick.C
	}
	held.Store(false)
	f.start(t)
	<-admitted
	r := loaded(t, f.s, f.uid)
	if r.LeaseGeneration != 2 || r.Phase != Held || r.Reason != "admission_rejected" || !strings.Contains(r.Inspect, "lease passed to another installation") {
		t.Fatalf("run after the lease passed: generation %d phase %s reason %s inspect %q", r.LeaseGeneration, r.Phase, r.Reason, r.Inspect)
	}
	f.d.Occurrence = f.d.Occurrence.Add(time.Hour)
	if result, err := f.m.Admit(t.Context(), f.d); result != sched.NotAdmitted || err == nil || !strings.Contains(err.Error(), "lane lease not held") {
		t.Fatalf("admission without the lease: %v %v", result, err)
	}
}

// On a restart the lane queue can reach a prepared run before the lane has
// claimed its lease in this process. Until then the run waits, prepared and
// unsent; once the lane holds the generation the run was prepared under, its
// admission proceeds. The outcome does not depend on which comes first.
func TestUnclaimedLeaseDefersPreAdmissionWrites(t *testing.T) {
	f := newAdmission(t)
	var generation atomic.Int64
	held := atomic.Bool{}
	generation.Store(2)
	held.Store(true)
	f.m.SetLease(func() (int64, bool) { return generation.Load(), held.Load() })
	var created atomic.Int32
	f.k.create = func(context.Context, kata.CreateReq) (kata.IssueView, error) {
		created.Add(1)
		return kata.IssueView{UID: "issue", QualifiedID: "project#task", Revision: 7}, nil
	}
	admitted := make(chan struct{})
	go func() {
		defer close(admitted)
		_, _ = f.m.Admit(t.Context(), f.d)
	}()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for _, err := f.s.Load(f.uid); err != nil; _, err = f.s.Load(f.uid) {
		<-tick.C
	}
	// The restarted process has not claimed the lane yet.
	generation.Store(0)
	held.Store(false)
	f.start(t)
	// The queue defers the admission instead of holding the run.
	for r := loaded(t, f.s, f.uid); r.Ops[0].NextAt.IsZero(); r = loaded(t, f.s, f.uid) {
		<-tick.C
	}
	if r := loaded(t, f.s, f.uid); r.Phase != Prepared || created.Load() != 0 {
		t.Fatalf("unclaimed lane: phase %s reason %s, %d creates", r.Phase, r.Reason, created.Load())
	}
	// The lane claims the lease at the generation the run was prepared under.
	generation.Store(2)
	held.Store(true)
	f.q.Kick(f.uid)
	<-admitted
	if r := loaded(t, f.s, f.uid); r.Phase == Prepared || r.Phase == Held || created.Load() != 1 {
		t.Fatalf("claimed lane: phase %s reason %s, %d creates", r.Phase, r.Reason, created.Load())
	}
}

// An unclaimed lane's prepared run polls slowly once the process has been up
// a while: the wait grows with the queue's age, up to five minutes.
func TestUnclaimedLeaseDeferralSlowsDown(t *testing.T) {
	clock := &testClock{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	q := NewQueue(nil, testStore(t), nil, clock)
	for _, tc := range []struct{ age, want time.Duration }{
		{0, 2 * time.Second},
		{time.Minute, 15 * time.Second},
		{time.Hour, 5 * time.Minute},
	} {
		clock.at = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(tc.age)
		if got := q.deferDelay(); got != tc.want {
			t.Errorf("after %s: deferral %s, want %s", tc.age, got, tc.want)
		}
	}
}
