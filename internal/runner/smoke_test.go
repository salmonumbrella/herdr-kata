package runner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

func TestSmokeRecoverWorkingPrompt(t *testing.T) {
	for _, submission := range []Submission{Sending, Accepted} {
		t.Run(string(submission), func(t *testing.T) {
			f := newRecovery(t)
			f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Working}})
			f.accepted(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			clock := &activityClock{at: time.Now()}
			f.m.clock = clock
			f.s.Transition(f.uid, func(r *Run) error {
				r.ExpiresAt = clock.Now().Add(time.Hour)
				r.Steps[0].Submission = submission
				return nil
			})
			// A fresh manager models the restarted worker, sharing only the journal.
			m := NewManager(f.m.cfg, nil, f.m.h, f.s, nil, nil, clock)
			if err := m.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			defer func() { cancel(); m.Wait() }()
			until := time.Now().Add(10 * time.Second)
			for f.count("agent.wait") == 0 {
				if loaded(t, f.s, f.uid).Phase == Held || time.Now().After(until) {
					t.Fatalf("recovery did not resume the result wait: %+v", loaded(t, f.s, f.uid))
				}
				time.Sleep(time.Millisecond)
			}
			if loaded(t, f.s, f.uid).Steps[0].Submission != Accepted {
				t.Fatal("reconciled activity left submission sending")
			}
			// The original occupant stays working past the startup readiness bound.
			clock.advance(65 * time.Second)
			f.result(t, "succeeded")
			r := loaded(t, f.s, f.uid)
			a, _ := f.m.h.AgentGet(ctx, r.Herdr.AgentName)
			a.Status, a.InteractiveReady = "idle", true
			seq := uint64(1)
			a.CompletionSeq = &seq
			f.h.SetAgent(a)
			m.Wait()
			r = loaded(t, f.s, f.uid)
			if r.Phase != Settled || r.Outcome != "succeeded" || f.count("agent.prompt") != 1 || f.count("agent.start") != 1 {
				t.Fatalf("recovered run: %+v", r)
			}
		})
	}
}

func TestSmokeFailedStartReleasesSlot(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "start error", true: "pending exit"}[pending], func(t *testing.T) {
			f := newAdmission(t)
			h := herdrtest.New(t)
			f.m.h = &herdr.Client{Path: h.Path}
			f.cfg.Limits.MaxActiveRuns = 1
			f.m.cfg = f.cfg
			f.d.Def.Action.WorkflowUID = ""
			f.d.Def.Action.Prompt = "Inspect"
			f.d.Workflow, f.d.WFDef = nil, nil
			if pending {
				h.Script("hk-"+strings.ToLower(f.uid), herdrtest.Script{Start: herdrtest.PendingExit})
			} else {
				h.Fail("agent.start", herdr.Error{Code: "invalid_params", Message: "invalid agent flag"})
			}
			f.k.comment = func(context.Context, string, string, string) error { return nil }
			f.k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) { return 8, nil }
			f.k.notify = func(context.Context, string, string, string) error { return nil }
			f.k.close = func(context.Context, kata.CloseReq) error { return nil }
			stopQueue := f.start(t)
			if decision, err := f.m.Admit(t.Context(), f.d); err != nil || decision != sched.Admitted {
				t.Fatal(decision, err)
			}
			f.m.Wait()
			r := loaded(t, f.s, f.uid)
			if r.Phase != Held || r.Outcome != "unknown" || !r.SlotReleased || !strings.Contains(r.Reason, "agent_start_failed") || !pending && !strings.Contains(r.Reason, "invalid_params") {
				t.Fatalf("failed start: %+v", r)
			}
			stopQueue()
			if f.m.FreeSlots() != 1 || !f.m.GateHeld(f.d.Job.UID) {
				t.Fatal("failed start capacity/gate")
			}
			d := f.d
			d.Job.UID = "other-job"
			d.Occurrence = d.Occurrence.Add(time.Second)
			d.Def.Action.Prompt = ""
			d.Def.Action.WorkflowUID = "wf"
			d.Workflow = &kata.Workflow{UID: "wf", Name: "Verification", DefinitionEventUID: "wf-event"}
			d.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "check", Kind: "command", Command: "true"}}}
			// Reserve the new job while the completed queue is quiescent, then
			// restart delivery once admission reaches its durable queue barrier.
			select {
			case <-f.q.wake:
			default:
			}
			type admissionResult struct {
				decision sched.AdmitResult
				err      error
			}
			admitted := make(chan admissionResult, 1)
			go func() {
				decision, err := f.m.Admit(t.Context(), d)
				admitted <- admissionResult{decision, err}
			}()
			select {
			case <-f.q.wake:
			case result := <-admitted:
				t.Fatal("admission returned before its queue barrier", result)
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			}
			f.start(t)
			if result := <-admitted; result.err != nil || result.decision != sched.Admitted {
				t.Fatal("pending other job", result.decision, result.err)
			}
			f.m.Wait()
		})
	}
}

func TestSmokeLateSessionClosesTab(t *testing.T) {
	for _, observation := range []string{"get", "wait"} {
		t.Run(observation, func(t *testing.T) {
			f := newRecovery(t)
			f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = time.Now().Add(time.Minute); return nil })
			f.launch(t)
			r := loaded(t, f.s, f.uid)
			a, _ := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
			a.Session = nil
			f.h.SetAgent(a)
			f.s.Transition(f.uid, func(r *Run) error { r.Herdr.Session = nil; return nil })
			f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Working}})
			f.submit(t)
			f.result(t, "succeeded")
			a.Status, a.InteractiveReady = "idle", true
			a.Session = &herdr.AgentSession{Source: "herdr:codex", Kind: "id", Value: "first-turn-session"}
			seq := uint64(1)
			a.CompletionSeq = &seq
			f.h.SetAgent(a)
			if observation == "wait" {
				if err := f.m.waitAgent(t.Context(), loaded(t, f.s, f.uid), []string{"idle"}); err != nil {
					t.Fatal(err)
				}
				if loaded(t, f.s, f.uid).Herdr.Session == nil {
					t.Fatal("wait observation did not capture session")
				}
			}
			res := f.await(t)
			if err := f.m.finish(t.Context(), f.uid, res); err != nil {
				t.Fatal(err)
			}
			if loaded(t, f.s, f.uid).Outcome != "succeeded" || f.count("tab.close") != 1 {
				t.Fatal("late session prevented cleanup")
			}
		})
	}
}

func TestSmokePromptActivityGate(t *testing.T) {
	f := newRecovery(t)
	f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = time.Now().Add(time.Hour); return nil })
	f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Working}})
	f.accepted(t)
	for _, req := range f.h.Requests() {
		if req.Method != "agent.prompt" {
			continue
		}
		var p struct {
			Wait struct {
				Until     []string
				TimeoutMS int64 `json:"timeout_ms"`
			}
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			t.Fatal(err)
		}
		if strings.Join(p.Wait.Until, ",") != "working,blocked" || p.Wait.TimeoutMS > 15000 {
			t.Fatalf("prompt waits for settlement: %+v", p.Wait)
		}
	}
	if loaded(t, f.s, f.uid).Steps[0].Submission != Accepted {
		t.Fatal("activity not accepted")
	}
}

func TestSmokeActivityGateTimeoutReconciles(t *testing.T) {
	f := newRecovery(t)
	f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = time.Now().Add(time.Hour); return nil })
	f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Timeout}, Wait: []herdrtest.Outcome{herdrtest.Idle}})
	f.accepted(t)
	f.result(t, "succeeded")
	if f.await(t) == nil || f.count("agent.prompt") != 1 {
		t.Fatal("short gate timeout ended execution")
	}
}

func TestSmokeBlockedReasonRestored(t *testing.T) {
	f := newRecovery(t)
	f.s.Transition(f.uid, func(r *Run) error { r.IssueOwned = true; r.IssueRef = "project#issue"; return nil })
	f.m.blocked(f.uid, true)
	f.m.blocked(f.uid, false)
	var messages []string
	for _, op := range loaded(t, f.s, f.uid).Ops {
		var p Payload
		json.Unmarshal(op.Payload, &p)
		if p.Name == "work.attention_msg" {
			messages = append(messages, p.Value)
		}
	}
	if strings.Join(messages, ",") != "herdr-kata: agent blocked,herdr-kata: running" {
		t.Fatal("attention messages", messages)
	}
}

func TestSmokeTabLabel(t *testing.T) {
	f := newRecovery(t)
	f.s.Transition(f.uid, func(r *Run) error { r.Job.Name = "Parser maintenance"; r.IssueRef = "project#abcd"; return nil })
	f.launch(t)
	tab, err := f.m.h.TabGet(t.Context(), loaded(t, f.s, f.uid).Herdr.TabID)
	if err != nil || tab.Label != "Parser maintenance · 01K00000" {
		t.Fatal(tab, err)
	}
}

func TestSmokePromptReturnCapturesSession(t *testing.T) {
	f := newRecovery(t)
	f.launch(t)
	r := loaded(t, f.s, f.uid)
	a, _ := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
	a.Session = nil
	f.h.SetAgent(a)
	f.s.Transition(f.uid, func(r *Run) error { r.Herdr.Session = nil; return nil })
	session := &herdr.AgentSession{Source: "herdr:codex", Kind: "id", Value: "first-turn-session"}
	f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Working}, SessionAfterPrompt: session})
	f.submit(t)
	r = loaded(t, f.s, f.uid)
	if r.Herdr.Session == nil || *r.Herdr.Session != *session || r.Steps[0].Submission != Accepted {
		t.Fatal("prompt session not captured")
	}
}

func TestSmokeSessionChangeHeld(t *testing.T) {
	for _, observation := range []string{"prompt", "wait", "get"} {
		t.Run(observation, func(t *testing.T) {
			f := newRecovery(t)
			f.launch(t)
			r := loaded(t, f.s, f.uid)
			original := *r.Herdr.Session
			changed := original
			changed.Value = "replacement-session"
			if observation == "prompt" {
				f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Working}, SessionAfterPrompt: &changed})
				f.submit(t)
			} else {
				f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Working}})
				f.submit(t)
				a, _ := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
				a.Session = &changed
				a.Status, a.InteractiveReady = "idle", true
				f.h.SetAgent(a)
				if observation == "wait" {
					if err := f.m.waitAgent(t.Context(), loaded(t, f.s, f.uid), []string{"idle"}); err != errUnproven {
						t.Fatal("changed wait session accepted", err)
					}
				}
				f.await(t)
			}
			r = loaded(t, f.s, f.uid)
			if r.Phase != Held || r.SlotReleased || r.Herdr.Session == nil || *r.Herdr.Session != original || f.count("tab.close") != 0 {
				t.Fatal("changed session adopted", r)
			}
		})
	}
}

func TestSmokeDrainLeavesAcceptedAgentWorking(t *testing.T) {
	f := newRecovery(t)
	f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Working}})
	ctx, cancel := context.WithCancel(t.Context())
	defer func() { cancel(); f.m.Wait() }()
	if err := f.m.Execute(ctx, f.uid); err != nil {
		t.Fatal(err)
	}
	// Allow journal fsyncs during launch; this bounds test setup, while the
	// drain assertion below starts only after submission is durably accepted.
	until := time.Now().Add(30 * time.Second)
	for loaded(t, f.s, f.uid).Steps[0].Submission != Accepted {
		if time.Now().After(until) {
			t.Fatal("agent working but submission still sending")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	done := make(chan struct{})
	go func() { f.m.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("drain waited for agent settlement")
	}
	r := loaded(t, f.s, f.uid)
	a, err := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
	if err != nil || a.Status != "working" || r.Steps[0].Submission != Accepted || r.EndedAt != nil {
		t.Fatal("drain lost accepted execution", a, err)
	}
}

func TestSmokeFailedStartProofRejectsChangedOccupant(t *testing.T) {
	for _, change := range []string{"foreign", "server", "terminal", "unreadable"} {
		t.Run(change, func(t *testing.T) {
			f := newRecovery(t)
			f.launch(t)
			r := loaded(t, f.s, f.uid)
			a, _ := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
			f.h.RemoveAgent(a.Name)
			switch change {
			case "foreign":
				a.Name = "foreign-agent"
				f.h.SetAgent(a)
			case "server":
				r.Herdr.ServerID.Ino++
			case "terminal":
				r.Herdr.TerminalID = "old-terminal"
			case "unreadable":
				f.m.h.Path += ".missing"
			}
			if f.m.failedStartStopped(t.Context(), *AgentReference(r)) {
				t.Fatal("unproven occupant released")
			}
		})
	}
}

func TestSmokeLateSessionChangeDuringResultGrace(t *testing.T) {
	f := newRecovery(t)
	f.launch(t)
	r := loaded(t, f.s, f.uid)
	a, _ := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
	a.Session = nil
	f.h.SetAgent(a)
	f.s.Transition(f.uid, func(r *Run) error { r.Herdr.Session = nil; return nil })
	f.submit(t)
	a.Status, a.InteractiveReady = "idle", true
	seq := uint64(1)
	a.CompletionSeq = &seq
	first := herdr.AgentSession{Source: "herdr:codex", Kind: "id", Value: "first-turn-session"}
	a.Session = &first
	f.h.SetAgent(a)
	f.m.clock = &callbackClock{activityClock: &activityClock{at: time.Now()}, first: func() {
		replacement := first
		replacement.Value = "replacement-session"
		a.Session = &replacement
		f.h.SetAgent(a)
	}}
	if f.await(t) != nil {
		t.Fatal("invented result")
	}
	r = loaded(t, f.s, f.uid)
	if r.Phase != Held || r.Reason != "agent_unproven" || r.SlotReleased || r.Herdr.Session == nil || *r.Herdr.Session != first {
		t.Fatalf("late session change released capacity: %+v", r)
	}
}
