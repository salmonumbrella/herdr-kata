package runner

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
)

// Scripted waits advance logical time while using the real socket decoder.
type activityClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *activityClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}
func (c *activityClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}
func (c *activityClock) NewTimer(d time.Duration) Timer {
	c.advance(d)
	return RealClock{}.NewTimer(0)
}

func expireWaits(t *testing.T, h *herdrtest.Server, c *activityClock, check func([]string, uint64)) {
	t.Helper()
	h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method != "agent.wait" {
			return
		}
		var p struct {
			Until     []string
			TimeoutMS uint64 `json:"timeout_ms"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			t.Error(err)
			return
		}
		if check != nil {
			check(p.Until, p.TimeoutMS)
		}
		c.advance(time.Duration(p.TimeoutMS) * time.Millisecond)
		h.Fail("agent.wait", herdr.Error{Code: "timeout"})
	}
}

func TestLateActivityWithoutCompletionSeq(t *testing.T) {
	for _, report := range []bool{true, false} {
		t.Run(map[bool]string{true: "result", false: "no-result"}[report], func(t *testing.T) {
			f := newExecution(t)
			f.h.OmitCompletionSeq = true
			start := time.Now()
			clock := &activityClock{at: start}
			f.m.clock = clock
			if _, e := f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = start.Add(3 * time.Minute); return nil }); e != nil {
				t.Fatal(e)
			}
			f.launch(t)
			r := loaded(t, f.s, f.uid)
			a, e := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
			if e != nil {
				t.Fatal(e)
			}
			// The prompt was delivered, but the short activity gate saw stale idle.
			f.h.Fail("agent.prompt", herdr.Error{Code: "timeout"})
			f.h.BeforeRequest = func(req herdrtest.Request) {
				if req.Method == "agent.prompt" {
					clock.advance(15 * time.Second)
				}
				if req.Method != "agent.wait" {
					return
				}
				var p struct {
					Until     []string
					TimeoutMS uint64 `json:"timeout_ms"`
				}
				if e := json.Unmarshal(req.Params, &p); e != nil {
					t.Error(e)
					return
				}
				if a.Status == "idle" {
					if !slices.Contains(p.Until, "working") || !slices.Contains(p.Until, "blocked") || p.TimeoutMS > 30000 {
						t.Errorf("stale idle wait: %+v", p)
					}
					clock.advance(start.Add(20 * time.Second).Sub(clock.Now()))
					a.Status = "working"
					a.InteractiveReady = false
				} else {
					clock.advance(60 * time.Second)
					a.Status = "idle"
					a.InteractiveReady = true
					if report {
						f.result(t, "succeeded")
					}
				}
				f.h.SetAgent(a)
			}
			if e := f.m.execute(t.Context(), f.uid); e != nil {
				t.Fatal(e)
			}
			r = loaded(t, f.s, f.uid)
			if clock.Now().Before(start.Add(80 * time.Second)) {
				t.Fatalf("settled before the real idle: phase=%s reason=%s elapsed=%s", r.Phase, r.Reason, clock.Now().Sub(start))
			}
			if report && (r.Phase != Settled || r.Outcome != "succeeded") || !report && (r.Phase != Held || r.Reason != "no_result") {
				t.Fatalf("phase=%s outcome=%s reason=%s", r.Phase, r.Outcome, r.Reason)
			}
			if f.count("agent.prompt") != 1 {
				t.Fatal("prompt resent")
			}
		})
	}
}

func TestFastIdleResultWithoutActivity(t *testing.T) {
	f := newExecution(t)
	f.h.OmitCompletionSeq = true
	f.accepted(t)
	f.result(t, "succeeded")
	if e := f.m.execute(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	if r.Phase != Settled || r.Outcome != "succeeded" || f.count("agent.prompt") != 1 || f.count("agent.wait") != 0 {
		t.Fatalf("fast result lost: phase=%s outcome=%s", r.Phase, r.Outcome)
	}
}

func TestNoActivityWaitsToDeadline(t *testing.T) {
	f := newExecution(t)
	f.h.OmitCompletionSeq = true
	clock := &activityClock{at: time.Now()}
	f.m.clock = clock
	deadline := clock.Now().Add(95 * time.Second)
	if _, e := f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = deadline; return nil }); e != nil {
		t.Fatal(e)
	}
	expireWaits(t, f.h, clock, func(until []string, timeoutMS uint64) {
		if !slices.Equal(until, []string{"working", "blocked"}) || timeoutMS > 30000 {
			t.Errorf("activity slice: until=%v timeout_ms=%d", until, timeoutMS)
		}
	})
	f.accepted(t)
	if e := f.m.execute(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	if r.Phase != Held || r.Reason != "timeout" || r.SlotReleased || clock.Now().Before(deadline) || f.count("agent.prompt") != 1 || f.count("agent.wait") != 4 {
		t.Fatalf("unproven idle: phase=%s reason=%s released=%v waits=%d", r.Phase, r.Reason, r.SlotReleased, f.count("agent.wait"))
	}
}

func TestActivitySurvivesRecovery(t *testing.T) {
	for _, source := range []string{"prompt", "wait", "get"} {
		t.Run(source, func(t *testing.T) {
			f := newExecution(t)
			f.h.OmitCompletionSeq = true
			clock := &activityClock{at: time.Now()}
			f.m.clock = clock
			if _, e := f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = clock.Now().Add(time.Minute); return nil }); e != nil {
				t.Fatal(e)
			}
			if source == "prompt" {
				f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Working}})
			}
			f.accepted(t)
			r := loaded(t, f.s, f.uid)
			a, e := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
			if e != nil {
				t.Fatal(e)
			}
			if source != "prompt" {
				if source == "get" {
					a.Status = "working"
					f.h.SetAgent(a)
				}
				// Cancel only after the accepted attempt has observed working.
				ctx, cancel := context.WithCancel(t.Context())
				f.h.BeforeRequest = func(req herdrtest.Request) {
					if req.Method != "agent.wait" {
						return
					}
					if source == "wait" {
						a.Status = "working"
						f.h.SetAgent(a)
						source = "observed"
					} else {
						cancel()
					}
				}
				_, _ = f.m.awaitResult(ctx, f.uid)
				cancel()
			}
			if !loaded(t, f.s, f.uid).Steps[0].SawActivity {
				t.Fatal("activity not saved in journal")
			}
			a.Status = "idle"
			f.h.SetAgent(a)
			f.h.BeforeRequest = nil
			// A new store and manager must resolve idle as no_result without
			// having to observe working again or resubmit the prompt.
			store := NewStore(f.s.fsys, f.s.layout)
			m := NewManager(f.m.cfg, nil, f.m.h, store, nil, nil, clock)
			if e := m.Recover(t.Context()); e != nil {
				t.Fatal(e)
			}
			m.Wait()
			r = loaded(t, store, f.uid)
			if r.Phase != Held || r.Reason != "no_result" || f.count("agent.prompt") != 1 || f.count("agent.start") != 1 {
				t.Fatalf("recovered activity lost: phase=%s reason=%s", r.Phase, r.Reason)
			}
		})
	}
}

func TestHeldWithoutActivityKeepsSlot(t *testing.T) {
	f := newExecution(t)
	f.h.OmitCompletionSeq = true
	f.accepted(t)
	if e := f.m.hold(f.uid, "timeout"); e != nil {
		t.Fatal(e)
	}
	if e := f.m.ReconcileHeld(t.Context()); e != nil {
		t.Fatal(e)
	}
	if loaded(t, f.s, f.uid).SlotReleased {
		t.Fatal("stale idle released an unproven attempt's slot")
	}
}

func TestResultRecheckSavesActivity(t *testing.T) {
	f := newExecution(t)
	f.h.OmitCompletionSeq = true
	clock := &activityClock{at: time.Now()}
	f.m.clock = clock
	if _, e := f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = clock.Now().Add(time.Minute); return nil }); e != nil {
		t.Fatal(e)
	}
	f.accepted(t)
	f.result(t, "succeeded")
	r := loaded(t, f.s, f.uid)
	a, e := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
	if e != nil {
		t.Fatal(e)
	}
	_, path, e := f.m.attemptPaths(r)
	if e != nil {
		t.Fatal(e)
	}
	reads := 0
	f.h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method == "agent.get" {
			reads++
			a.Status = "idle"
			if reads == 2 {
				// The result was read, then retracted while work continued.
				a.Status = "working"
				if e := os.Remove(path); e != nil {
					t.Error(e)
				}
			}
			f.h.SetAgent(a)
		}
		if req.Method == "agent.wait" {
			clock.advance(30 * time.Second)
			f.h.Fail("agent.wait", herdr.Error{Code: "timeout"})
		}
	}
	f.await(t)
	r = loaded(t, f.s, f.uid)
	if r.Reason != "no_result" || !r.Steps[0].SawActivity {
		t.Fatalf("result recheck lost activity: reason=%s saw=%v", r.Reason, r.Steps[0].SawActivity)
	}
}

func TestRecoveryObservationSavesActivity(t *testing.T) {
	f := newExecution(t)
	f.h.OmitCompletionSeq = true
	clock := &activityClock{at: time.Now()}
	f.m.clock = clock
	if _, e := f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = clock.Now().Add(time.Minute); return nil }); e != nil {
		t.Fatal(e)
	}
	f.accepted(t)
	f.script(herdrtest.Script{Get: []herdr.Agent{{Status: "working"}, {Status: "idle"}}})
	f.h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method == "agent.wait" {
			clock.advance(30 * time.Second)
			f.h.Fail("agent.wait", herdr.Error{Code: "timeout"})
		}
	}
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r := loaded(t, f.s, f.uid)
	if r.Reason != "no_result" || !r.Steps[0].SawActivity || !r.SlotReleased || f.count("agent.prompt") != 1 {
		t.Fatalf("recovery read lost activity: reason=%s saw=%v released=%v", r.Reason, r.Steps[0].SawActivity, r.SlotReleased)
	}
}

func TestActivityGraceHoldsUnknownAndReleasesStoppedSlot(t *testing.T) {
	for _, omitSeq := range []bool{true, false} {
		t.Run(map[bool]string{true: "no-sequence", false: "stale-sequence"}[omitSeq], func(t *testing.T) {
			f := newExecution(t)
			f.h.OmitCompletionSeq = omitSeq
			clock := &activityClock{at: time.Now()}
			start := clock.Now()
			f.m.clock = clock
			f.m.cfg.Limits.MaxActiveRuns = 1
			if _, e := f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = start.Add(time.Hour); return nil }); e != nil {
				t.Fatal(e)
			}
			f.h.Fail("agent.prompt", herdr.Error{Code: "timeout"})
			expireWaits(t, f.h, clock, nil)
			f.accepted(t)
			if e := f.m.RestoreGates(); e != nil {
				t.Fatal(e)
			}
			f.await(t)
			r := loaded(t, f.s, f.uid)
			if r.Phase != Held || r.Outcome != "unknown" || r.Reason != "no_activity" || clock.Now().Sub(start) != 5*time.Minute || f.count("agent.prompt") != 1 {
				t.Fatalf("activity grace: phase=%s outcome=%s reason=%s elapsed=%s", r.Phase, r.Outcome, r.Reason, clock.Now().Sub(start))
			}
			if e := f.m.ReconcileHeld(t.Context()); e != nil {
				t.Fatal(e)
			}
			if f.m.FreeSlots() != 1 || !f.m.GateHeld(r.Job.UID) || !loaded(t, f.s, f.uid).SlotReleased {
				t.Fatal("proven stopped no-activity attempt retained its slot or lost its gate")
			}
		})
	}
}

func TestActivityGraceUsesOriginalSubmissionAcrossRecovery(t *testing.T) {
	f := newExecution(t)
	f.h.OmitCompletionSeq = true
	clock := &activityClock{at: time.Now()}
	start := clock.Now()
	f.m.clock = clock
	if _, e := f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = start.Add(time.Hour); return nil }); e != nil {
		t.Fatal(e)
	}
	f.h.Fail("agent.prompt", herdr.Error{Code: "timeout"})
	expireWaits(t, f.h, clock, nil)
	f.accepted(t)
	clock.advance(2 * time.Minute)
	store := NewStore(f.s.fsys, f.s.layout)
	m := NewManager(f.m.cfg, nil, f.m.h, store, nil, nil, clock)
	if e := m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	m.Wait()
	r := loaded(t, store, f.uid)
	if r.Reason != "no_activity" || clock.Now().Sub(start) != 5*time.Minute || f.count("agent.prompt") != 1 {
		t.Fatalf("grace restarted on recovery: reason=%s elapsed=%s", r.Reason, clock.Now().Sub(start))
	}
}

func TestActivityGraceValidResultWins(t *testing.T) {
	for _, working := range []bool{false, true} {
		t.Run(map[bool]string{false: "fast-result-after-grace", true: "activity-continues"}[working], func(t *testing.T) {
			f := newExecution(t)
			f.h.OmitCompletionSeq = true
			clock := &activityClock{at: time.Now()}
			f.m.clock = clock
			if _, e := f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = clock.Now().Add(time.Hour); return nil }); e != nil {
				t.Fatal(e)
			}
			if working {
				f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Working}, Wait: []herdrtest.Outcome{herdrtest.Idle}})
			}
			f.accepted(t)
			clock.advance(6 * time.Minute)
			f.result(t, "succeeded")
			if e := f.m.execute(t.Context(), f.uid); e != nil {
				t.Fatal(e)
			}
			r := loaded(t, f.s, f.uid)
			if r.Phase != Settled || r.Outcome != "succeeded" || f.count("agent.prompt") != 1 {
				t.Fatalf("valid result lost to activity grace: phase=%s outcome=%s reason=%s", r.Phase, r.Outcome, r.Reason)
			}
		})
	}
}

func TestHeldReconciliationUsesUnresolvedPromptActivity(t *testing.T) {
	f := newExecution(t)
	f.h.OmitCompletionSeq = true
	f.accepted(t)
	if _, e := f.s.Transition(f.uid, func(r *Run) error {
		// Definition order need not be execution order. An earlier completed
		// dependency must not prove activity for the current prompt.
		r.Steps = append(r.Steps, StepState{Key: "dependency", Kind: "prompt", Attempt: 1, Submission: Accepted, SawActivity: true, Status: "succeeded"})
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if e := f.m.hold(f.uid, "timeout"); e != nil {
		t.Fatal(e)
	}
	if e := f.m.ReconcileHeld(t.Context()); e != nil {
		t.Fatal(e)
	}
	if loaded(t, f.s, f.uid).SlotReleased {
		t.Fatal("completed dependency supplied activity for an unresolved prompt")
	}
}
