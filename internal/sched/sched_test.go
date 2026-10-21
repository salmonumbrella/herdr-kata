package sched

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type fakeKata struct {
	job               kata.Job
	workflow          kata.Workflow
	ready             []kata.IssueView
	calls, readyCalls int
	showErr           error
}

func (k *fakeKata) ListJobs(context.Context) ([]kata.Job, error) {
	k.calls++
	return []kata.Job{k.job}, nil
}
func (k *fakeKata) ListWorkflows(context.Context) ([]kata.Workflow, error) {
	k.calls++
	return []kata.Workflow{k.workflow}, nil
}
func (k *fakeKata) ShowJob(_ context.Context, ref string) (kata.Job, error) {
	k.calls++
	return k.job, k.showErr
}
func (k *fakeKata) ShowWorkflow(_ context.Context, ref string) (kata.Workflow, error) {
	k.calls++
	return k.workflow, nil
}
func (k *fakeKata) Ready(context.Context) ([]kata.IssueView, error) {
	k.calls++
	k.readyCalls++
	return k.ready, nil
}

type fakeAdmitter struct {
	gate    bool
	slots   int
	result  AdmitResult
	got     []Due
	onAdmit func()
}

func (a *fakeAdmitter) Admit(_ context.Context, d Due) (AdmitResult, error) {
	a.got = append(a.got, d)
	if a.onAdmit != nil {
		a.onAdmit()
	}
	if a.result == Admitted {
		if d.Def.Overlap == "forbid" {
			a.gate = true
		}
		a.slots--
	}
	return a.result, nil
}
func (a *fakeAdmitter) GateHeld(string) bool { return a.gate }
func (a *fakeAdmitter) FreeSlots() int       { return a.slots }

type reconcilingAdmitter struct {
	*fakeAdmitter
	reconciles int
}

func (a *reconcilingAdmitter) ReconcileHeld(context.Context) error {
	a.reconciles++
	a.slots = 1
	return nil
}

func TestDispatchReconcilesHeldWhenFull(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{false: "scheduled", true: "manual"}[manual], func(t *testing.T) {
			s, _, a, c := setup(t)
			a.slots = 0
			r := &reconcilingAdmitter{fakeAdmitter: a}
			s.admitter = r
			if manual {
				if err := s.RunNow(jobID, "01K00000000000000000000000"); err != nil {
					t.Fatal(err)
				}
				s.drainManual()
			} else {
				c.advance(time.Minute)
			}
			tick(t, s)
			if r.reconciles != 1 || len(a.got) != 1 || len(s.manuals) != 0 || len(pending(t, s)) != 0 {
				t.Fatal("full dispatch failed to reconcile and resume", r.reconciles, a.got)
			}
		})
	}
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers chan *fakeTimer
}
type fakeTimer struct {
	c     chan time.Time
	delay time.Duration
}

func (t *fakeTimer) C() <-chan time.Time { return t.c }
func (t *fakeTimer) Stop()               {}
func (c *fakeClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) NewTimer(delay time.Duration) Timer {
	t := &fakeTimer{make(chan time.Time, 1), delay}
	c.timers <- t
	return t
}
func (c *fakeClock) advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }
func wire(j defs.JobDef) json.RawMessage     { b, _ := json.Marshal(j); return b }
func setup(t *testing.T) (*Scheduler, *fakeKata, *fakeAdmitter, *fakeClock) {
	t.Helper()
	at := instant("2026-01-01T00:00:00Z")
	c := &fakeClock{now: at, timers: make(chan *fakeTimer, 10)}
	j := defs.JobDef{Version: 1, Kind: "job", Enabled: true, Trigger: defs.Trigger{Kind: "interval", IntervalSeconds: 60}, Action: defs.Action{Kind: "execute", Prompt: "Work"}, Issue: &defs.IssuePolicy{Kind: "per-run", Title: "Task"}, Overlap: "forbid", Catchup: "all"}
	k := &fakeKata{job: kata.Job{Author: "worker", UID: jobID, DefinitionEventUID: "event1", CreatedAt: at, Definition: wire(j)}}
	a := &fakeAdmitter{slots: 10, result: Admitted}
	l := state.Layout{Root: t.TempDir()}
	if e := state.WriteJSON(state.OSFS{}, l.StateFile(), map[string]any{"state_created_at": at}); e != nil {
		t.Fatal(e)
	}
	s := New(config.Config{Kata: config.Kata{Actor: "worker"}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}, k, a, state.OSFS{}, l, c)
	if e := s.load(context.Background()); e != nil {
		t.Fatal(e)
	}
	return s, k, a, c
}
func tick(t *testing.T, s *Scheduler) {
	t.Helper()
	if e := s.tick(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func edit(k *fakeKata, fn func(*defs.JobDef)) {
	j, _ := defs.DecodeJob(k.job.Definition)
	fn(&j)
	k.job.Definition = wire(j)
}
func pending(t *testing.T, s *Scheduler) []string {
	t.Helper()
	c, e := LoadCheckpoint(s.fsys, s.layout, jobID, time.Time{})
	if e != nil {
		t.Fatal(e)
	}
	return c.Pending
}
func TestDispatchRevalidates(t *testing.T) {
	s, k, a, c := setup(t)
	edit(k, func(j *defs.JobDef) { j.Enabled = false })
	c.advance(time.Minute)
	tick(t, s)
	if len(a.got) != 0 || len(pending(t, s)) != 0 {
		t.Fatal(a.got)
	}
}
func TestDispatchEditedWorkflow(t *testing.T) {
	s, k, a, c := setup(t)
	edit(k, func(j *defs.JobDef) { j.Action.Prompt = ""; j.Action.WorkflowUID = jobID })
	k.workflow = kata.Workflow{UID: jobID, DefinitionEventUID: "wf2", Definition: json.RawMessage(`{"version":1,"steps":[{"key":"main","kind":"prompt","prompt":"Edited"}]}`)}
	c.advance(time.Minute)
	tick(t, s)
	if len(a.got) != 1 || a.got[0].WFDef.Steps[0].Prompt != "Edited" || a.got[0].Workflow.DefinitionEventUID != "wf2" {
		t.Fatal(a.got)
	}
}
func TestForbidWaits(t *testing.T) {
	s, _, a, c := setup(t)
	a.gate = true
	c.advance(time.Minute)
	tick(t, s)
	if len(a.got) != 0 || len(pending(t, s)) != 1 {
		t.Fatal(a.got)
	}
	a.gate = false
	tick(t, s)
	if len(a.got) != 1 || len(pending(t, s)) != 0 {
		t.Fatal(a.got)
	}
}
func TestAllSerialPerJob(t *testing.T) {
	s, _, a, c := setup(t)
	c.advance(3 * time.Minute)
	tick(t, s)
	if len(a.got) != 1 || len(pending(t, s)) != 2 {
		t.Fatal(a.got)
	}
	a.gate = false
	tick(t, s)
	if len(a.got) != 2 || !a.got[1].Occurrence.Equal(instant("2026-01-01T00:02:00Z")) {
		t.Fatal(a.got)
	}
}
func TestExecuteExistingNotReadyWaits(t *testing.T) {
	s, k, a, c := setup(t)
	edit(k, func(j *defs.JobDef) { j.Issue = &defs.IssuePolicy{Kind: "existing", UID: jobID}; j.Overlap = "allow" })
	c.advance(2 * time.Minute)
	tick(t, s)
	if len(a.got) != 0 || len(pending(t, s)) != 2 || k.readyCalls != 1 {
		t.Fatal(a.got, k.readyCalls)
	}
	k.ready = []kata.IssueView{{UID: jobID, Status: "open"}}
	s.Poke()
	tick(t, s)
	if len(a.got) != 2 || k.readyCalls != 2 {
		t.Fatal(a.got, k.readyCalls)
	}
}
func TestNotifyNotReadinessGated(t *testing.T) {
	s, k, a, c := setup(t)
	edit(k, func(j *defs.JobDef) {
		j.Action = defs.Action{Kind: "notify", Recipient: "current-owner-or-author"}
		j.Issue = &defs.IssuePolicy{Kind: "existing", UID: jobID}
	})
	c.advance(time.Minute)
	tick(t, s)
	if len(a.got) != 1 || k.readyCalls != 0 {
		t.Fatal(a.got, k.readyCalls)
	}
}
func TestDefinitionChangeSupersedes(t *testing.T) {
	s, k, a, c := setup(t)
	a.slots = 0
	c.advance(time.Minute)
	tick(t, s)
	k.job.DefinitionEventUID = "event2"
	c.advance(time.Minute)
	if e := s.load(context.Background()); e != nil {
		t.Fatal(e)
	}
	a.slots = 1
	tick(t, s)
	if len(a.got) != 0 || len(pending(t, s)) != 0 {
		t.Fatal(a.got)
	}
	c.advance(time.Minute)
	tick(t, s)
	if len(a.got) != 1 || !a.got[0].Occurrence.Equal(instant("2026-01-01T00:03:00Z")) {
		t.Fatal(a.got)
	}
}
func TestLogAdmitterKeepsPending(t *testing.T) {
	s, _, _, c := setup(t)
	s.admitter = LogAdmitter{}
	c.advance(time.Minute)
	tick(t, s)
	if len(pending(t, s)) != 1 {
		t.Fatal("pending consumed")
	}
}
func TestClockJumpReplans(t *testing.T) {
	s, _, a, c := setup(t)
	a.slots = 0
	c.advance(time.Hour)
	tick(t, s)
	if len(pending(t, s)) != 60 {
		t.Fatal(len(pending(t, s)))
	}
}
func TestIdleHourNoKataCalls(t *testing.T) {
	s, k, _, c := setup(t)
	edit(k, func(j *defs.JobDef) { j.Enabled = false })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	timer := <-c.timers
	baseline := k.calls
	for range 6 {
		c.advance(10 * time.Minute)
		timer.c <- c.Now()
		timer = <-c.timers
	}
	if k.calls != baseline {
		t.Fatalf("idle calls: %d", k.calls-baseline)
	}
	cancel()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}
func TestManualPreservesCheckpoint(t *testing.T) {
	s, _, a, _ := setup(t)
	if e := s.RunNow(jobID, jobID); e != nil {
		t.Fatal(e)
	}
	s.drainManual()
	tick(t, s)
	if len(a.got) != 1 || !a.got[0].Manual || a.got[0].ManualRunUID != jobID || len(pending(t, s)) != 0 {
		t.Fatal(a.got)
	}
}
func TestDisabledRefreshKeepsCheckpointMonotonic(t *testing.T) {
	s, k, a, c := setup(t)
	a.slots = 0
	c.advance(time.Minute)
	tick(t, s)
	k.job.DefinitionEventUID = "event2"
	edit(k, func(j *defs.JobDef) { j.Enabled = false })
	c.advance(time.Minute)
	if e := s.load(context.Background()); e != nil {
		t.Fatal(e)
	}
	cp, e := LoadCheckpoint(s.fsys, s.layout, jobID, time.Time{})
	if e != nil || cp.DefinitionEventUID != "event2" || !cp.HandledThrough.Equal(c.Now()) || len(cp.Pending) != 0 {
		t.Fatal(cp, e)
	}
}
func TestDeletedManualDoesNotRun(t *testing.T) {
	s, k, a, c := setup(t)
	at := c.Now()
	k.job.DeletedAt = &at
	if e := s.RunNow(jobID, jobID); e != nil {
		t.Fatal(e)
	}
	s.drainManual()
	tick(t, s)
	if len(a.got) != 0 {
		t.Fatal(a.got)
	}
}
func TestManualOccurrenceStableWhilePending(t *testing.T) {
	s, _, a, c := setup(t)
	a.result = NotAdmitted
	if e := s.RunNow(jobID, jobID); e != nil {
		t.Fatal(e)
	}
	s.drainManual()
	tick(t, s)
	c.advance(time.Second)
	s.Poke()
	tick(t, s)
	if len(a.got) != 2 || !a.got[0].Occurrence.Equal(a.got[1].Occurrence) {
		t.Fatal(a.got)
	}
}

func TestAdmissionSupersedeStopsOldSchedule(t *testing.T) {
	s, k, a, c := setup(t)
	a.slots = 0
	c.advance(time.Minute)
	tick(t, s)
	k.job.DefinitionEventUID = "event2"
	edit(k, func(j *defs.JobDef) { j.Trigger = defs.Trigger{Kind: "manual"} })
	a.slots = 10
	tick(t, s)
	c.advance(time.Minute)
	tick(t, s)
	if len(a.got) != 0 || len(pending(t, s)) != 0 {
		t.Fatal(a.got)
	}
}
func TestEachOccurrenceRefreshesDefinition(t *testing.T) {
	s, k, a, c := setup(t)
	edit(k, func(j *defs.JobDef) { j.Overlap = "allow" })
	a.onAdmit = func() { edit(k, func(j *defs.JobDef) { j.Enabled = false }) }
	c.advance(2 * time.Minute)
	tick(t, s)
	if len(a.got) != 1 || len(pending(t, s)) != 0 {
		t.Fatal(a.got)
	}
}
func TestUnreadyNormalFiresAvoidKataReads(t *testing.T) {
	s, k, a, c := setup(t)
	edit(k, func(j *defs.JobDef) { j.Issue = &defs.IssuePolicy{Kind: "existing", UID: jobID} })
	c.advance(time.Minute)
	tick(t, s)
	baseline := k.calls
	c.advance(time.Minute)
	tick(t, s)
	if k.calls != baseline || len(pending(t, s)) != 2 {
		t.Fatal(k.calls - baseline)
	}
	k.ready = []kata.IssueView{{UID: jobID, Status: "open"}}
	s.Poke()
	tick(t, s)
	if len(a.got) != 1 {
		t.Fatal(a.got)
	}
}

func TestMissingJobRetiresScheduleAndManual(t *testing.T) {
	s, k, a, c := setup(t)
	k.showErr = &kata.Error{Code: "not_found"}
	s.k = &accessReader{fakeKata: k}
	c.advance(time.Minute)
	tick(t, s)
	baseline := k.calls
	c.advance(time.Minute)
	tick(t, s)
	if k.calls != baseline || len(a.got) != 0 {
		t.Fatal(k.calls - baseline)
	}
	if e := s.RunNow(jobID, jobID); e != nil {
		t.Fatal(e)
	}
	s.drainManual()
	tick(t, s)
	if len(s.manuals) != 0 {
		t.Fatal("missing manual retained")
	}
}
func TestTransientReadPreservesPending(t *testing.T) {
	s, k, a, c := setup(t)
	k.showErr = &kata.Error{Code: "process_error"}
	c.advance(time.Minute)
	tick(t, s)
	if len(a.got) != 0 || len(pending(t, s)) != 1 {
		t.Fatal(a.got)
	}
	k.showErr = nil
	s.Poke()
	tick(t, s)
	if len(a.got) != 1 {
		t.Fatal(a.got)
	}
}
func TestNormalFireDoesNotUseCatchupGrace(t *testing.T) {
	s, k, a, c := setup(t)
	edit(k, func(j *defs.JobDef) { j.Catchup = "skip" })
	if e := s.load(context.Background()); e != nil {
		t.Fatal(e)
	}
	tick(t, s)
	c.advance(time.Minute + time.Millisecond)
	tick(t, s)
	if len(a.got) != 1 {
		t.Fatal("ordinary timer jitter skipped fire")
	}
}
func TestCheckpointFaultPreventsAdmission(t *testing.T) {
	s, _, a, c := setup(t)
	s.fsys = failSync{}
	c.advance(time.Minute)
	if e := s.tick(context.Background()); e == nil || len(a.got) != 0 {
		t.Fatal(e, a.got)
	}
}
func TestUnreadyManualWaitsForPoke(t *testing.T) {
	s, k, a, c := setup(t)
	edit(k, func(j *defs.JobDef) {
		j.Trigger = defs.Trigger{Kind: "manual"}
		j.Issue = &defs.IssuePolicy{Kind: "existing", UID: jobID}
	})
	if e := s.load(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := s.RunNow(jobID, jobID); e != nil {
		t.Fatal(e)
	}
	s.drainManual()
	tick(t, s)
	baseline := k.calls
	c.advance(time.Second)
	tick(t, s)
	if k.calls != baseline {
		t.Fatal("manual readiness polled")
	}
	k.ready = []kata.IssueView{{UID: jobID, Status: "open"}}
	s.Poke()
	tick(t, s)
	if len(a.got) != 1 {
		t.Fatal(a.got)
	}
}

// A suspend advances wall time without advancing Go's monotonic clock. Adjust
// only the monotonic component of a test timestamp to reproduce that condition.
func wallJump(t time.Time, d time.Duration) time.Time {
	t = t.Add(d)
	v := reflect.ValueOf(&t).Elem().FieldByName("ext")
	*(*int64)(unsafe.Pointer(v.UnsafeAddr())) -= int64(d)
	return t
}
func TestClockJumpWithMonotonicTime(t *testing.T) {
	s, _, a, c := setup(t)
	a.slots = 0
	base := time.Now()
	en := s.entries[jobID]
	en.schedule, _ = Compile(en.def.Trigger, base.Round(0))
	en.cp.HandledThrough = base.Round(0)
	s.reheap()
	c.now = base.Add(time.Minute)
	tick(t, s)
	c.now = wallJump(c.now, 3*time.Minute)
	tick(t, s)
	if len(pending(t, s)) != 4 {
		t.Fatal("wall-clock jump lost occurrences", pending(t, s))
	}
}

func TestNotReadyRecheckedOnSlowWake(t *testing.T) {
	for _, step := range []time.Duration{10 * time.Minute, -50 * time.Minute} {
		t.Run(step.String(), func(t *testing.T) {
			s, k, a, c := setup(t)
			edit(k, func(j *defs.JobDef) {
				j.Issue = &defs.IssuePolicy{Kind: "existing", UID: jobID}
				j.Trigger = defs.Trigger{Kind: "once", At: "2026-01-01T00:01:00Z"}
			})
			c.advance(time.Minute)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- s.Run(ctx) }()
			timer := <-c.timers
			if timer.delay != 10*time.Minute {
				t.Fatal(timer.delay)
			}
			if step < 0 {
				c.advance(-time.Hour)
				s.Poke()
				timer = <-c.timers
				if timer.delay > 10*time.Minute {
					t.Fatal("clock correction delayed reconciliation", timer.delay)
				}
				c.advance(10 * time.Minute)
			} else {
				c.advance(step)
			}
			k.ready = []kata.IssueView{{UID: jobID, Status: "open"}}
			timer.c <- c.Now()
			<-c.timers
			if len(a.got) != 1 {
				t.Fatal("elapsed slow wake did not recheck readiness")
			}
			cancel()
			if e := <-done; e != nil {
				t.Fatal(e)
			}
		})
	}
}

type countWrites struct {
	state.OSFS
	writes int
}

func (f *countWrites) Rename(a, b string) error { f.writes++; return f.OSFS.Rename(a, b) }
func TestRefreshOnlyWritesChangedCheckpoints(t *testing.T) {
	s, k, _, _ := setup(t)
	f := &countWrites{}
	s.fsys = f
	if e := s.load(context.Background()); e != nil {
		t.Fatal(e)
	}
	if f.writes != 0 {
		t.Fatal("unchanged checkpoint rewritten", f.writes)
	}
	k.job.DefinitionEventUID = "event2"
	if e := s.load(context.Background()); e != nil {
		t.Fatal(e)
	}
	cp, e := LoadCheckpoint(s.fsys, s.layout, jobID, time.Time{})
	if e != nil || cp.DefinitionEventUID != "event2" || f.writes != 1 {
		t.Fatal(cp, e, f.writes)
	}
	if e := s.load(context.Background()); e != nil {
		t.Fatal(e)
	}
	if f.writes != 1 {
		t.Fatal("unchanged revision rewritten")
	}
}

func TestManualDroppedRequestReportsCompletion(t *testing.T) {
	for _, reason := range []string{"disabled", "deleted", "authority"} {
		t.Run(reason, func(t *testing.T) {
			s, k, a, _ := setup(t)
			a.slots = 0
			if e := s.RunNow("job", "01K00000000000000000000000"); e != nil {
				t.Fatal(e)
			}
			s.drainManual()
			tick(t, s)
			switch reason {
			case "disabled":
				edit(k, func(d *defs.JobDef) { d.Enabled = false })
			case "deleted":
				k.showErr = &kata.Error{Code: "not_found"}
			case "authority":
				a.result = Dropped
			}
			a.slots = 1
			tick(t, s)
			done, e := s.ManualResult("01K00000000000000000000000")
			if !done || e == nil {
				t.Fatal("dropped manual has no terminal result", done, e)
			}
		})
	}
}

func TestInvalidManualRefusedWhileCapacityFull(t *testing.T) {
	for _, reason := range []string{"paused", "deleted", "unsupported"} {
		t.Run(reason, func(t *testing.T) {
			s, k, a, c := setup(t)
			a.slots = 0
			switch reason {
			case "paused":
				edit(k, func(d *defs.JobDef) { d.Enabled = false })
			case "deleted":
				at := c.Now()
				k.job.DeletedAt = &at
			case "unsupported":
				edit(k, func(d *defs.JobDef) { d.CheckoutKey = "unconfigured" })
			}
			if e := s.RunNow(jobID, "01K00000000000000000000000"); e != nil {
				t.Fatal(e)
			}
			s.drainManual()
			tick(t, s)
			if done, e := s.ManualResult("01K00000000000000000000000"); !done || e == nil || len(a.got) != 0 {
				t.Fatal("invalid request waited for capacity", done, e, a.got)
			}
		})
	}
}

func TestUnchangedRefreshKeepsDueFire(t *testing.T) {
	s, k, a, c := setup(t)
	edit(k, func(j *defs.JobDef) { j.Catchup = "skip"; j.GraceSeconds = 0 })
	k.job.DefinitionEventUID = "event2"
	if e := s.load(context.Background()); e != nil { // pick up skip policy
		t.Fatal(e)
	}
	c.advance(time.Minute + time.Second)             // fire due 1 s ago
	if e := s.load(context.Background()); e != nil { // unrelated refresh lands first
		t.Fatal(e)
	}
	tick(t, s)
	if len(a.got) != 1 {
		t.Fatalf("ordinary fire dropped after refresh; admitted=%d pending=%v", len(a.got), pending(t, s))
	}
}

func TestDueFireWithoutRefresh(t *testing.T) {
	s, k, a, c := setup(t)
	edit(k, func(j *defs.JobDef) { j.Catchup = "skip"; j.GraceSeconds = 0 })
	k.job.DefinitionEventUID = "event2"
	if e := s.load(context.Background()); e != nil {
		t.Fatal(e)
	}
	c.advance(time.Minute + time.Second)
	tick(t, s)
	if len(a.got) != 1 {
		t.Fatalf("control failed: %d", len(a.got))
	}
}

func TestManualInputCarriedAndRefused(t *testing.T) {
	s, _, a, _ := setup(t)
	value := "manual input"
	if e := s.RunNow(jobID, "01K00000000000000000000000", &value); e != nil {
		t.Fatal(e)
	}
	value = "changed"
	s.drainManual()
	tick(t, s)
	if len(a.got) != 1 || a.got[0].ManualInput == nil || *a.got[0].ManualInput != "manual input" {
		t.Fatal(a.got)
	}
	tooLarge := strings.Repeat("x", 4097)
	if e := s.RunNow(jobID, "01K00000000000000000000001", &tooLarge); e == nil {
		t.Fatal("oversized request accepted")
	}
}

func TestReadyReferenceTravelsWithAdmission(t *testing.T) {
	s, k, a, c := setup(t)
	s.cfg.Kata.Project = "example-project"
	edit(k, func(j *defs.JobDef) { j.Issue = &defs.IssuePolicy{Kind: "existing", UID: jobID} })
	k.ready = []kata.IssueView{{UID: jobID, Status: "open", ShortID: "abc45"}, {UID: "unrelated", Status: "open", ShortID: "other5"}}
	c.advance(time.Minute)
	tick(t, s)
	if len(a.got) != 1 || a.got[0].IssueRefs[jobID] != "example-project#abc45" || len(a.got[0].IssueRefs) != 1 || k.readyCalls != 1 {
		t.Fatal(a.got, k.readyCalls)
	}
}
