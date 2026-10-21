package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
)

const limitSessionID = "00000000-0000-4000-8000-000000000001"

// The fixture owns both the Herdr identity and a main-session log. No real
// agent home is read. Terminal prose is deliberately sensitive-looking so a
// leak into the journal or queued Kata writes is observable.
func limitExecution(t *testing.T, code string) (*executionFixture, *activityClock, string) {
	t.Helper()
	f := newExecution(t)
	clock := &activityClock{at: time.Date(2026, 9, 24, 16, 0, 0, 0, time.UTC)}
	f.m.clock = clock
	_, e := f.s.Transition(f.uid, func(r *Run) error {
		r.ExpiresAt = clock.Now().Add(time.Hour)
		r.IssueRef = "example-project#task"
		r.IssueOwned = true
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	f.accepted(t)
	r := loaded(t, f.s, f.uid)
	a, e := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
	if e != nil {
		t.Fatal(e)
	}
	a.Session = &herdr.AgentSession{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: limitSessionID}
	f.h.SetAgent(a)
	_, e = f.s.Transition(f.uid, func(r *Run) error { r.Herdr.Session = a.Session; return nil })
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	t.Setenv("CODEX_HOME", root)
	path := filepath.Join(root, "sessions", "2026", "09", "24", "rollout-example-"+limitSessionID+".jsonl")
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	writeLimitRecord(t, path, clock.Now().Add(time.Second), code)
	return f, clock, path
}

func resumableLimit(t *testing.T, code string) (*executionFixture, *admissionFixture, *activityClock, string) {
	t.Helper()
	x, clock, path := limitExecution(t, code)
	if e := x.m.execute(t.Context(), x.uid); e != nil {
		t.Fatal(e)
	}
	f := heldResumeFixture(t)
	base := loaded(t, f.s, f.uid)
	r := loaded(t, x.s, x.uid)
	r.UID, r.Target, r.Timeout = f.uid, base.Target, time.Hour
	r.IssueUID, r.IssueRef, r.IssueOwned = "issue", "project#task", false
	r.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "main", Kind: "prompt", Prompt: "Inspect", Retries: 1}}}
	for i := range r.Ops {
		r.Ops[i].Done = true
	}
	if _, e := f.s.Transition(f.uid, func(saved *Run) error { *saved = *r; return nil }); e != nil {
		t.Fatal(e)
	}
	f.m.h, f.m.clock = x.m.h, clock
	x.m, x.s, x.uid = f.m, f.s, f.uid
	return x, f, clock, path
}

func TestAgentLimitResumePreservesRetryAndAgent(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared", true: "closed"}[closed], func(t *testing.T) {
			x, f, clock, _ := resumableLimit(t, "usage_limit_exceeded")
			if closed {
				if e := x.m.h.TabClose(t.Context(), loaded(t, x.s, x.uid).Herdr.TabID); e != nil {
					t.Fatal(e)
				}
			}
			clock.advance(time.Minute)
			f.start(t)
			if e := x.m.Resume(t.Context(), Request{UID: "resume-limit", RunUID: x.uid}); e != nil {
				t.Fatal(e)
			}
			r := loaded(t, x.s, x.uid)
			if r.Steps[0].Attempt != 2 || r.Steps[0].Uncharged != 1 || r.Steps[0].Submission != None || r.Steps[0].Limit != nil || r.Loop.Used != 0 {
				t.Fatalf("resume %+v", r.Steps[0])
			}
			// Submit the new attempt, never the accepted held attempt.
			if e := x.m.launch(t.Context(), x.uid); e != nil {
				t.Fatal(e)
			}
			x.submit(t)
			r = loaded(t, x.s, x.uid)
			b, e := os.ReadFile(filepath.Join(x.s.layout.AttemptDir(x.uid, "main", 2), "prompt.md"))
			if e != nil {
				t.Fatal(e)
			}
			if !strings.Contains(string(b), "Attempt 1 of this step stopped at the agent's usage limit") || !strings.Contains(string(b), "partial work") {
				t.Fatal("resume prompt omitted partial work warning", string(b))
			}
			starts := 1
			if closed {
				starts = 2
			}
			if x.count("agent.prompt") != 2 || x.count("agent.start") != starts || r.Steps[0].Uncharged != 1 {
				t.Fatal("resume replaced shared agent or resent held input", x.count("agent.start"), x.count("agent.prompt"))
			}
		})
	}
}

// Review 20: remaining eligibility uses <=; exhaustion uses >. A resolved
// failure with no retries must settle even after its server proof is lost.
func TestAgentLimitRetryParityAcrossRestart(t *testing.T) {
	for _, recoverRun := range []bool{false, true} {
		for _, retries := range []int{0, 1} {
			t.Run(fmt.Sprintf("restart=%t/retries=%d", recoverRun, retries), func(t *testing.T) {
				x, f, clock, _ := resumableLimit(t, "usage_limit_exceeded")
				clock.advance(time.Minute)
				f.start(t)
				if e := x.m.Resume(t.Context(), Request{UID: "resume-limit", RunUID: x.uid}); e != nil {
					t.Fatal(e)
				}
				if e := x.m.launch(t.Context(), x.uid); e != nil {
					t.Fatal(e)
				}
				x.submit(t)
				x.result(t, "failed")
				if res := x.await(t); res == nil || res.Status != "failed" {
					t.Fatal(res)
				}
				_, e := x.s.Transition(x.uid, func(r *Run) error {
					r.WFDef.Steps[0].Retries = retries
					if retries == 0 {
						r.Herdr.TerminalID = "unproven"
					}
					return nil
				})
				if e != nil {
					t.Fatal(e)
				}
				x.h.BeforeRequest = func(req herdrtest.Request) {
					if req.Method == "agent.prompt" {
						x.result(t, "succeeded")
					}
				}
				if recoverRun {
					m := NewManager(x.m.cfg, x.m.k, x.m.h, x.s, x.m.q, x.m.guard, clock)
					m.environment = cleanLaunch{}
					if e := m.Recover(t.Context()); e != nil {
						t.Fatal(e)
					}
					m.Wait()
				} else {
					if e := x.m.execute(t.Context(), x.uid); e != nil {
						t.Fatal(e)
					}
				}
				r := loaded(t, x.s, x.uid)
				want, attempt, prompts := "failed", 2, 2
				if retries == 1 {
					want, attempt, prompts = "succeeded", 3, 3
				}
				if r.Phase != Settled || r.Outcome != want || r.Steps[0].Attempt != attempt || x.count("agent.prompt") != prompts {
					t.Fatalf("parity: phase=%s outcome=%s reason=%s attempt=%d prompts=%d", r.Phase, r.Outcome, r.Reason, r.Steps[0].Attempt, x.count("agent.prompt"))
				}
			})
		}
	}
}

func TestAgentLimitResumeDoesNotCarryOldQuota(t *testing.T) {
	x, f, clock, path := resumableLimit(t, "usage_limit_exceeded")
	// An old attempt writes its last record before fresh stopped proof permits
	// the next submission. That record must not classify the resumed attempt.
	clock.advance(time.Minute)
	writeLimitRecord(t, path, clock.Now(), "usage_limit_exceeded")
	clock.advance(time.Second)
	f.start(t)
	if e := x.m.Resume(t.Context(), Request{UID: "resume-limit", RunUID: x.uid}); e != nil {
		t.Fatal(e)
	}
	if e := x.m.execute(t.Context(), x.uid); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, x.s, x.uid)
	if r.Phase != Held || r.Reason != "no_result" || r.Steps[0].Attempt != 2 || x.count("agent.prompt") != 2 {
		t.Fatal("old error leaked into new attempt", r.Phase, r.Reason)
	}
}

func TestAgentLimitReportingUsesCodesOnly(t *testing.T) {
	f, _, _ := limitExecution(t, "unauthorized")
	if e := f.m.execute(context.Background(), f.uid); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	for _, op := range r.Ops {
		var p Payload
		if e := json.Unmarshal(op.Payload, &p); e != nil {
			t.Fatal(e)
		}
		if op.Kind == "notify" && (!strings.Contains(p.Message, "codex login failed") || !strings.Contains(p.Message, "herdr-kata resume ")) {
			t.Fatal("unhelpful login notification", p.Message)
		}
	}
}

func TestAgentLimitSharedRetryAndNextStepIgnoreOldError(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprint(retry), func(t *testing.T) {
			f, clock, _ := limitExecution(t, "usage_limit_exceeded")
			_, e := f.s.Transition(f.uid, func(r *Run) error {
				r.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "main", Kind: "prompt", Prompt: "Inspect", Retries: 1}}}
				if !retry {
					r.WFDef.Steps = append(r.WFDef.Steps, defs.Step{Key: "next", Kind: "prompt", Prompt: "Check", After: []string{"main"}})
					r.Steps = append(r.Steps, StepState{Key: "next", Kind: "prompt", Attempt: 1, Submission: None})
				}
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			status := "succeeded"
			if retry {
				status = "failed"
			}
			f.result(t, status)
			if res := f.await(t); res == nil || res.Status != status {
				t.Fatal(res)
			}
			clock.advance(time.Minute)
			if e := f.m.execute(t.Context(), f.uid); e != nil {
				t.Fatal(e)
			}
			r := loaded(t, f.s, f.uid)
			if r.Reason != "no_result" || r.Phase != Held || f.count("agent.prompt") != 2 || f.count("agent.start") != 1 {
				t.Fatal("shared attempt inherited old quota", r.Reason, r.ActiveStep)
			}
		})
	}
}

func TestAgentLimitNoActivityCanResume(t *testing.T) {
	x, f, clock, _ := resumableLimit(t, "usage_limit_exceeded")
	r := loaded(t, x.s, x.uid)
	a, e := x.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
	if e != nil {
		t.Fatal(e)
	}
	// An idle no-activity hold need not have an advancing completion sequence.
	a.CompletionSeq = r.Herdr.PreSubmitSeq
	x.h.SetAgent(a)
	_, e = x.s.Transition(x.uid, func(r *Run) error { r.Steps[0].Reason = "no_activity"; r.Steps[0].SawActivity = false; return nil })
	if e != nil {
		t.Fatal(e)
	}
	clock.advance(time.Minute)
	f.start(t)
	if e := x.m.Resume(t.Context(), Request{UID: "resume-limit", RunUID: x.uid}); e != nil {
		t.Fatal(e)
	}
	if r := loaded(t, x.s, x.uid); r.Steps[0].Attempt != 2 || r.Steps[0].Uncharged != 1 {
		t.Fatal(r.Steps[0])
	}
}

func TestAgentLimitLoginResumePrompt(t *testing.T) {
	x, f, clock, _ := resumableLimit(t, "unauthorized")
	clock.advance(time.Minute)
	f.start(t)
	if e := x.m.Resume(t.Context(), Request{UID: "resume-login", RunUID: x.uid}); e != nil {
		t.Fatal(e)
	}
	if e := x.m.launch(t.Context(), x.uid); e != nil {
		t.Fatal(e)
	}
	x.submit(t)
	b, e := os.ReadFile(filepath.Join(x.s.layout.AttemptDir(x.uid, "main", 2), "prompt.md"))
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(b), "login failure") || !strings.Contains(string(b), "partial work") {
		t.Fatal(string(b))
	}
}

func TestAgentLimitDoesNotTakeLoop(t *testing.T) {
	for _, checker := range []bool{false, true} {
		t.Run(fmt.Sprint(checker), func(t *testing.T) {
			f, _, _ := limitExecution(t, "usage_limit_exceeded")
			_, e := f.s.Transition(f.uid, func(r *Run) error {
				r.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "main", Kind: "prompt", Prompt: "Make"}, {Key: "check", Kind: "prompt", Prompt: "Check", After: []string{"main"}, Options: json.RawMessage(`{"on_fail":{"goto":"main","max_loops":2}}`)}}}
				if checker {
					old := r.Steps[0]
					old.Key = "check"
					r.Steps = []StepState{{Key: "main", Kind: "prompt", Attempt: 1, Submission: Accepted, Status: "succeeded"}, old}
					r.ActiveStep = "check"
				}
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			index := 0
			if checker {
				index = 1
			}
			if _, e := f.m.awaitStepResult(t.Context(), f.uid, index); e != nil {
				t.Fatal(e)
			}
			if e := f.m.execute(t.Context(), f.uid); e != nil {
				t.Fatal(e)
			}
			r := loaded(t, f.s, f.uid)
			if r.Reason != "agent_quota" || r.Loop.Used != 0 || r.Loop.ConsumedAttempt != 0 || r.Steps[index].Attempt != 1 {
				t.Fatal("quota took a verdict edge", r.Reason, r.Loop)
			}
		})
	}
}

func TestAgentLimitReadCannotOverrideChangedAttempt(t *testing.T) {
	f, _, _ := limitExecution(t, "usage_limit_exceeded")
	old := loaded(t, f.s, f.uid)
	_, e := f.s.Transition(f.uid, func(r *Run) error { r.Steps[0].Attempt = 2; r.Steps[0].Submission = None; return nil })
	if e != nil {
		t.Fatal(e)
	}
	if e := f.m.holdNoResult(old, 0, "no_result", true); e != nil {
		t.Fatal(e)
	}
	if r := loaded(t, f.s, f.uid); r.Phase == Held || r.Steps[0].Limit != nil || r.Steps[0].Attempt != 2 {
		t.Fatal("stale read changed new attempt", r.Phase, r.Steps[0])
	}
}

// Roborev 18712: an identity learned at the grace-period recheck must be used
// by the hold; silently rejecting a stale snapshot leaves a run without a runner.
func TestAgentLimitLateSessionDuringGrace(t *testing.T) {
	f, clock, _ := limitExecution(t, "usage_limit_exceeded")
	r := loaded(t, f.s, f.uid)
	a, e := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
	if e != nil {
		t.Fatal(e)
	}
	session := a.Session
	a.Session = nil
	f.h.SetAgent(a)
	_, e = f.s.Transition(f.uid, func(r *Run) error { r.Herdr.Session = nil; return nil })
	if e != nil {
		t.Fatal(e)
	}
	start := clock.Now()
	f.h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method == "agent.get" && !clock.Now().Before(start.Add(10*time.Second)) {
			a.Session = session
			f.h.SetAgent(a)
		}
	}
	if e := f.m.execute(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	r = loaded(t, f.s, f.uid)
	if r.Phase != Held || r.Reason != "agent_quota" || r.EndedAt == nil || f.count("agent.prompt") != 1 {
		t.Fatal("late identity stranded the run", r.Phase, r.Reason, r.EndedAt)
	}
}

// A missing agent with the saved shell in foreground is stopped proof, just
// like an exited pane. Resume must keep its evidence but create a new occupant.
func TestAgentLimitResumeAfterAgentExitsToShell(t *testing.T) {
	x, f, clock, _ := resumableLimit(t, "usage_limit_exceeded")
	old := loaded(t, x.s, x.uid).Herdr
	x.h.RemoveAgent(old.AgentName)
	clock.advance(time.Minute)
	f.start(t)
	if e := x.m.Resume(t.Context(), Request{UID: "resume-limit", RunUID: x.uid}); e != nil {
		t.Fatal(e)
	}
	if e := x.m.launch(t.Context(), x.uid); e != nil {
		t.Fatal(e)
	}
	x.submit(t)
	r := loaded(t, x.s, x.uid)
	if r.Phase != Running || r.Steps[0].Submission != Accepted || r.Herdr.PaneID == old.PaneID || x.count("agent.start") != 2 || x.count("agent.prompt") != 2 {
		t.Fatal("shell exit could not continue", r.Phase, r.Reason, r.Herdr.PaneID)
	}
	if len(r.PastAttempts) == 0 || r.PastAttempts[0].Herdr.PaneID != old.PaneID || r.PastAttempts[0].Limit == nil {
		t.Fatal("old occupant evidence lost", r.PastAttempts)
	}
}

func TestAgentLimitArchivedShellAllowsSecondResumeAndLoop(t *testing.T) {
	for _, loop := range []bool{false, true} {
		t.Run(fmt.Sprint(loop), func(t *testing.T) {
			x, f, clock, path := resumableLimit(t, "usage_limit_exceeded")
			old := loaded(t, x.s, x.uid).Herdr
			x.h.RemoveAgent(old.AgentName)
			clock.advance(time.Minute)
			f.start(t)
			if e := x.m.Resume(t.Context(), Request{UID: "resume-limit", RunUID: x.uid}); e != nil {
				t.Fatal(e)
			}
			if e := x.m.launch(t.Context(), x.uid); e != nil {
				t.Fatal(e)
			}
			x.submit(t)
			if loop {
				x.result(t, "failed")
				if res := x.await(t); res == nil {
					t.Fatal("missing verdict")
				}
				_, e := x.s.Transition(x.uid, func(r *Run) error {
					r.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "make", Kind: "command", Command: "true"}, {Key: "main", Kind: "prompt", Prompt: "Check", After: []string{"make"}, Options: json.RawMessage(`{"on_fail":{"goto":"make","max_loops":2}}`)}}}
					r.Steps = append([]StepState{{Key: "make", Kind: "command", Attempt: 1, Submission: Accepted, Status: "succeeded"}}, r.Steps...)
					return nil
				})
				if e != nil {
					t.Fatal(e)
				}
				if e := x.m.takeLoop(t.Context(), x.uid, 1); e != nil {
					t.Fatal(e)
				}
				r := loaded(t, x.s, x.uid)
				if r.Phase == Held || r.Loop.Used != 1 || r.Steps[1].Attempt != 3 || r.Steps[1].Uncharged != 1 {
					t.Fatal("archived shell blocked loop or charged quota", r.Phase, r.Reason, r.Loop, r.Steps[1])
				}
				return
			}
			r := loaded(t, x.s, x.uid)
			a, e := x.m.h.AgentGet(t.Context(), AgentReference(r).AgentName)
			if e != nil {
				t.Fatal(e)
			}
			a.Session = &herdr.AgentSession{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "00000000-0000-4000-8000-000000000002"}
			x.h.SetAgent(a)
			_, e = x.s.Transition(x.uid, func(r *Run) error { AgentReference(r).Session = a.Session; return nil })
			if e != nil {
				t.Fatal(e)
			}
			path = strings.Replace(path, limitSessionID, a.Session.Value, 1)
			writeLimitRecord(t, path, clock.Now().Add(time.Second), "usage_limit_exceeded")
			if e := x.m.execute(t.Context(), x.uid); e != nil {
				t.Fatal(e)
			}
			r = loaded(t, x.s, x.uid)
			if r.Reason != "agent_quota" {
				t.Fatal(r.Reason)
			}
			if e := x.m.q.wait(t.Context(), x.uid, r.Ops[len(r.Ops)-1].ID); e != nil {
				t.Fatal(e)
			}
			clock.advance(time.Minute)
			if e := x.m.Resume(t.Context(), Request{UID: "resume-limit-2", RunUID: x.uid}); e != nil {
				t.Fatal("archived shell blocked second resume", e)
			}
			r = loaded(t, x.s, x.uid)
			if r.Steps[0].Attempt != 3 || r.Steps[0].Uncharged != 2 || r.Phase != Admitted {
				t.Fatal("second resume", r.Phase, r.Steps[0])
			}
			if _, e := x.m.h.PaneGet(t.Context(), old.PaneID); e != nil {
				t.Fatal("old pane should remain open", e)
			}
		})
	}
}

func writeLimitRecord(t *testing.T, path string, at time.Time, code string) {
	t.Helper()
	b := `{"timestamp":"` + at.Format(time.RFC3339Nano) + `","type":"event_msg","payload":{"type":"task_complete","error":{"message":"PRIVATE TRANSCRIPT","codex_error_info":"` + code + `"}}}` + "\n"
	if e := os.WriteFile(path, []byte(b), 0600); e != nil {
		t.Fatal(e)
	}
}

func TestAgentLimitHoldsAndRecovery(t *testing.T) {
	for _, route := range []string{"settled", "no_activity", "gone", "recovery_gone"} {
		t.Run(route, func(t *testing.T) {
			f, clock, _ := limitExecution(t, "usage_limit_exceeded")
			if route == "no_activity" {
				f.h.OmitCompletionSeq = true
				_, e := f.s.Transition(f.uid, func(r *Run) error { r.Steps[0].SawActivity = false; r.Herdr.PreSubmitSeq = nil; return nil })
				if e != nil {
					t.Fatal(e)
				}
				clock.advance(6 * time.Minute)
			}
			if strings.Contains(route, "gone") {
				r := loaded(t, f.s, f.uid)
				if e := f.m.h.TabClose(t.Context(), r.Herdr.TabID); e != nil {
					t.Fatal(e)
				}
			}
			if route == "recovery_gone" {
				if e := f.m.Recover(t.Context()); e != nil {
					t.Fatal(e)
				}
				f.m.Wait()
			} else {
				if e := f.m.execute(t.Context(), f.uid); e != nil {
					t.Fatal(e)
				}
			}
			r := loaded(t, f.s, f.uid)
			if r.Phase != Held || r.Outcome != "unknown" || r.Reason != "agent_quota" || f.count("agent.prompt") != 1 {
				t.Fatalf("hold: phase=%s reason=%s prompts=%d", r.Phase, r.Reason, f.count("agent.prompt"))
			}
			b, e := os.ReadFile(f.s.layout.RunFile(f.uid))
			if e != nil {
				t.Fatal(e)
			}
			if strings.Contains(string(b), "PRIVATE TRANSCRIPT") || !strings.Contains(string(b), "codex:usage_limit_exceeded") {
				t.Fatal("missing code or transcript leaked", string(b))
			}
			// Saved holds are final, and restarting never re-sends the attempt.
			if e := f.m.Recover(t.Context()); e != nil {
				t.Fatal(e)
			}
			f.m.Wait()
			if r := loaded(t, f.s, f.uid); r.Reason != "agent_quota" || f.count("agent.prompt") != 1 {
				t.Fatal("saved hold changed", r.Reason)
			}
		})
	}
}

func TestAgentLimitResultWins(t *testing.T) {
	f, _, _ := limitExecution(t, "usage_limit_exceeded")
	f.result(t, "failed")
	if e := f.m.execute(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	if r.Phase != Settled || r.Outcome != "failed" || r.Reason == "agent_quota" {
		t.Fatal("quota overrode verdict", r.Phase, r.Outcome, r.Reason)
	}
}

func TestAgentLimitDoesNotReclassifyUnprovenResult(t *testing.T) {
	f, _, _ := limitExecution(t, "usage_limit_exceeded")
	f.result(t, "failed")
	f.h.RemoveAgent(loaded(t, f.s, f.uid).Herdr.AgentName)
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r := loaded(t, f.s, f.uid)
	// The existing recovery contract requires pane-exit proof to accept this
	// artifact. Missing that proof must not turn a report into a quota verdict.
	if r.Reason != "agent_lost" || r.Steps[0].Limit != nil {
		t.Fatal("reported result reclassified", r.Reason, r.Steps[0].Limit)
	}
}

func TestAgentLimitBlockedPassDoesNotSaveVerdict(t *testing.T) {
	f, _, _ := limitExecution(t, "unauthorized")
	if e := f.m.blockedStep(f.uid, 0, true); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	if r.Phase != Blocked || r.Outcome != "" || r.Reason != "" {
		t.Fatal("blocked classified as held")
	}
	found := false
	for _, op := range r.Ops {
		var p Payload
		if e := json.Unmarshal(op.Payload, &p); e != nil {
			t.Fatal(e)
		}
		if op.Kind == "notify" && strings.Contains(p.Message, "agent_login") {
			if strings.Contains(p.Message, "resume") {
				t.Fatal("blocked run cannot resume", p.Message)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("blocked notification missing login diagnosis")
	}
	f.result(t, "succeeded")
	if e := f.m.blockedStep(f.uid, 0, false); e != nil {
		t.Fatal(e)
	}
	if e := f.m.execute(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	if r := loaded(t, f.s, f.uid); r.Phase != Settled || r.Outcome != "succeeded" {
		t.Fatal("blocked read consumed attempt", r.Phase, r.Reason)
	}
}

func TestAgentLimitTimeoutIsFinal(t *testing.T) {
	f, clock, path := limitExecution(t, "usage_limit_exceeded")
	f.script(herdrtest.Script{Get: []herdr.Agent{{Status: "working"}}})
	clock.advance(2 * time.Hour)
	if e := f.m.execute(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	writeLimitRecord(t, path, clock.Now(), "usage_limit_exceeded")
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	if r := loaded(t, f.s, f.uid); r.Reason != "timeout" || r.SlotReleased {
		t.Fatal("working timeout reclassified", r.Reason, r.SlotReleased)
	}
}
