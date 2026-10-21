package runner

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
)

// Submission requires settled, interactive-ready AND no pending launch. Even
// inconsistent readiness observations must not feed a prompt to a startup UI.
func TestStartupScreensPreventSubmission(t *testing.T) {
	for _, tc := range []struct {
		name, status   string
		ready, pending bool
	}{
		{"pending idle", "idle", true, true},
		{"pending done", "done", true, true},
		{"not interactive", "idle", false, false},
		{"startup blocked", "blocked", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExecution(t)
			f.launch(t)
			r := loaded(t, f.s, f.uid)
			a, e := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
			if e != nil {
				t.Fatal(e)
			}
			a.Status, a.InteractiveReady, a.LaunchPending = tc.status, tc.ready, tc.pending
			f.h.SetAgent(a)
			f.submit(t)
			if f.count("agent.prompt") != 0 || loaded(t, f.s, f.uid).Steps[0].Submission != None {
				t.Fatal("submitted into a startup screen")
			}
			a.Status, a.InteractiveReady, a.LaunchPending = "idle", true, false
			f.h.SetAgent(a)
			f.submit(t)
			f.submit(t)
			if f.count("agent.prompt") != 1 || loaded(t, f.s, f.uid).Steps[0].Submission != Accepted {
				t.Fatal("ready agent did not receive exactly one prompt")
			}
		})
	}
}

// Herdr can report interactive readiness while Codex's first-run hook review
// owns the input. Sending here loses the task even though agent.prompt accepts it.
func TestHookReviewPreventsSubmissionUntilCleared(t *testing.T) {
	for _, screen := range []string{
		"1 hook needs review",
		"2 hooks need review",
		// Warning and footer from the acceptance capture; no host paths.
		"Hooks\nLifecycle hooks from config and enabled plugins.\n\n  ⚠ 1 hook needs review before it can run.\n\n  t trust all · enter review · esc close",
		// Codex 0.163.0's first screen, captured during evaluation. Herdr
		// reports it idle and interactive-ready; Enter picks "Review hooks".
		"  Hooks need review\n  4 hooks are new or changed.\n  Hooks can run outside the sandbox after you trust them.\n\n› 1. Review hooks\n  2. Trust all and continue\n  3. Continue without trusting (hooks won't run)\n\n  enter confirm · esc skip",
		"  Hooks need review\n  1 hook is new or changed.\n  Hooks can run outside the sandbox after you trust them.",
		// The same modal with the title scrolled off a short pane.
		"› 1. Review hooks\n  2. Trust all and continue\n  3. Continue without trusting (hooks won't run)",
		"  Trusting hooks...",
		// The hooks browser, from the review list capture without host paths.
		// A hook row's status; the text is Codex 0.163.0's, the layout assumed.
		"› SessionStart  startup  User config  ./hook.sh  New hook - review required",
		"  SessionEnd  *  User config  ./hook.sh  Modified since last trusted - review required",
		"  Hooks\n  Lifecycle hooks from config and enabled plugins.\n\n  Event                 Installed   Active      Review      Description\n› SessionStart          4           0           4           When a new session starts\n\n  t trust all · enter review · esc close",
	} {
		t.Run(screen, func(t *testing.T) {
			f := newExecution(t)
			f.launch(t)
			r := loaded(t, f.s, f.uid)
			f.h.SetRead(r.Herdr.AgentName, screen)
			f.submit(t)
			r = loaded(t, f.s, f.uid)
			if f.count("agent.prompt") != 0 || r.Steps[0].Submission != None || r.Phase != Blocked {
				t.Fatalf("hook review swallowed task: prompts=%d submission=%s phase=%s", f.count("agent.prompt"), r.Steps[0].Submission, r.Phase)
			}
			f.h.SetRead(r.Herdr.AgentName, "› Ask Codex to do anything")
			f.submit(t)
			f.submit(t)
			r = loaded(t, f.s, f.uid)
			if f.count("agent.prompt") != 1 || r.Steps[0].Submission != Accepted || r.Phase != Running {
				t.Fatalf("cleared review: prompts=%d submission=%s phase=%s", f.count("agent.prompt"), r.Steps[0].Submission, r.Phase)
			}
		})
	}
}

func TestHookReviewWaitsWithoutSpinning(t *testing.T) {
	f := newExecution(t)
	clock := &activityClock{at: time.Now()}
	f.m.clock = clock
	deadline := clock.Now().Add(95 * time.Second)
	if _, e := f.s.Transition(f.uid, func(r *Run) error { r.ExpiresAt = deadline; return nil }); e != nil {
		t.Fatal(e)
	}
	f.launch(t)
	r := loaded(t, f.s, f.uid)
	f.h.SetRead(r.Herdr.AgentName, "1 hook needs review")
	f.h.Fail("agent.prompt", herdr.Error{Code: "timeout"})
	expireWaits(t, f.h, clock, nil)
	if _, e := f.m.executePrompt(t.Context(), f.uid, 0); e != nil {
		t.Fatal(e)
	}
	r = loaded(t, f.s, f.uid)
	if f.count("agent.prompt") != 0 || r.Steps[0].Submission != None || r.Phase != Held || r.Reason != "timeout" {
		t.Fatalf("review wait: prompts=%d submission=%s phase=%s reason=%s", f.count("agent.prompt"), r.Steps[0].Submission, r.Phase, r.Reason)
	}
	if reads := f.count("agent.read"); reads < 3 || reads > 6 || clock.Now().Before(deadline) {
		t.Fatalf("unpaced review wait: reads=%d elapsed=%s", reads, clock.Now().Sub(deadline))
	}
}

func TestHookReviewReadFailureDoesNotSubmit(t *testing.T) {
	f := newExecution(t)
	f.launch(t)
	f.h.Fail("agent.read", herdr.Error{Code: "read_failed"})
	f.submit(t)
	r := loaded(t, f.s, f.uid)
	if f.count("agent.prompt") != 0 || r.Steps[0].Submission != None || r.Phase != Held || r.Reason != "submission_unproven" {
		t.Fatalf("unproven screen: prompts=%d submission=%s phase=%s reason=%s", f.count("agent.prompt"), r.Steps[0].Submission, r.Phase, r.Reason)
	}
}

func TestHookReviewClearedWhileWorkerWaits(t *testing.T) {
	f := newExecution(t)
	f.launch(t)
	r := loaded(t, f.s, f.uid)
	f.h.SetRead(r.Herdr.AgentName, "1 hook needs review")
	start := time.Now()
	clock := &activityClock{at: start}
	f.m.clock = &callbackClock{activityClock: clock, first: func() {
		f.h.SetRead(r.Herdr.AgentName, "› Ask Codex to do anything")
	}}
	f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Idle}})
	f.h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method == "agent.prompt" {
			f.result(t, "succeeded")
		}
	}
	res, e := f.m.executePrompt(t.Context(), f.uid, 0)
	if e != nil {
		t.Fatal(e)
	}
	if res == nil || res.Status != "succeeded" || f.count("agent.prompt") != 1 || clock.Now().Sub(start) != 30*time.Second {
		t.Fatalf("cleared review lost task: result=%+v prompts=%d elapsed=%s", res, f.count("agent.prompt"), clock.Now().Sub(start))
	}
}

func TestHookReviewMentionInConversationDoesNotBlock(t *testing.T) {
	for _, mention := range []string{
		"Explain why 1 hook needs review can swallow the task.",
		"The modal says Hooks need review and 2 hooks are new or changed; choose Trust all and continue.",
		"A row reading New hook - review required means the hook is untrusted.",
		"The row says New hook - review required",
	} {
		t.Run(mention, func(t *testing.T) {
			f := newExecution(t)
			f.launch(t)
			r := loaded(t, f.s, f.uid)
			f.h.SetRead(r.Herdr.AgentName, mention+"\n› Ask Codex to do anything")
			f.submit(t)
			if f.count("agent.prompt") != 1 || loaded(t, f.s, f.uid).Steps[0].Submission != Accepted {
				t.Fatal("conversation text was mistaken for hook review")
			}
		})
	}
}

func TestHookReviewReadFromForeignPaneDoesNotSubmit(t *testing.T) {
	f := newExecution(t)
	f.launch(t)
	r := loaded(t, f.s, f.uid)
	a, e := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
	if e != nil {
		t.Fatal(e)
	}
	f.h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method == "agent.read" {
			a.PaneID = "foreign-pane"
			f.h.SetAgent(a)
		}
	}
	f.submit(t)
	r = loaded(t, f.s, f.uid)
	if f.count("agent.prompt") != 0 || r.Steps[0].Submission != None || r.Reason != "submission_unproven" {
		t.Fatalf("foreign screen: prompts=%d submission=%s reason=%s", f.count("agent.prompt"), r.Steps[0].Submission, r.Reason)
	}
}

func TestHookReviewCancellationDoesNotHold(t *testing.T) {
	for _, method := range []string{"submit", "execute"} {
		t.Run(method, func(t *testing.T) {
			f := newExecution(t)
			f.launch(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f.h.BeforeRequest = func(req herdrtest.Request) {
				if req.Method == "agent.read" {
					cancel()
				}
			}
			var e error
			if method == "submit" {
				e = f.m.submit(ctx, f.uid)
			} else {
				_, e = f.m.executePrompt(ctx, f.uid, 0)
			}
			r := loaded(t, f.s, f.uid)
			if !errors.Is(e, context.Canceled) || r.Phase == Held || r.Steps[0].Submission != None || f.count("agent.prompt") != 0 {
				t.Fatalf("canceled screen read: err=%v phase=%s submission=%s", e, r.Phase, r.Steps[0].Submission)
			}
		})
	}
}

// A startup screen can become blocked after launch readiness was observed.
// The pre-submission wait must wake on blocked, before the run expires.
func TestStartupBecomesBlockedWhileWaiting(t *testing.T) {
	f := newExecution(t)
	if _, e := f.s.Transition(f.uid, func(r *Run) error {
		r.IssueRef, r.IssueOwned = "example-project#task", true
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	f.launch(t)
	r := loaded(t, f.s, f.uid)
	a, e := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
	if e != nil {
		t.Fatal(e)
	}
	f.script(herdrtest.Script{Get: []herdr.Agent{a, {Status: "unknown", LaunchPending: true}}})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	waits := 0
	f.h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method != "agent.wait" {
			return
		}
		waits++
		if waits > 1 {
			cancel()
			return
		}
		a.Status, a.LaunchPending, a.InteractiveReady = "blocked", true, false
		f.h.SetAgent(a)
		var p struct{ Until []string }
		if e := json.Unmarshal(req.Params, &p); e != nil {
			t.Error(e)
		}
		// Model expiry of a wait that excludes the newly blocked status.
		if !slices.Contains(p.Until, "blocked") {
			f.h.Fail("agent.wait", herdr.Error{Code: "timeout"})
		}
	}
	_, e = f.m.executePrompt(ctx, f.uid, 0)
	if !errors.Is(e, context.Canceled) {
		r = loaded(t, f.s, f.uid)
		t.Fatalf("did not keep waiting for the operator: %v phase=%s reason=%s waits=%d", e, r.Phase, r.Reason, waits)
	}
	r = loaded(t, f.s, f.uid)
	if r.Phase != Blocked || r.Steps[0].Submission != None || f.count("agent.prompt") != 0 {
		t.Fatalf("startup block: phase=%s submission=%s", r.Phase, r.Steps[0].Submission)
	}
	assertStartupAttention(t, r)
}

func assertStartupAttention(t *testing.T, r *Run) {
	t.Helper()
	notify, attention := false, false
	for _, op := range r.Ops {
		var p Payload
		if e := json.Unmarshal(op.Payload, &p); e != nil {
			t.Fatal(e)
		}
		if op.Kind == "notify" && p.Ref == "example-project#task" && p.Message == "herdr-kata: agent blocked" {
			notify = true
		}
		if op.Kind == "meta.set" && p.Name == "work.attention" && p.Value == "needs-human" {
			attention = true
		}
	}
	if !notify || !attention {
		t.Fatal("startup block did not request human attention and notification")
	}
}
