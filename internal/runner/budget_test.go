package runner

import (
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
)

func TestPromptBudgetExhausted(t *testing.T) {
	for _, launched := range []bool{false, true} {
		t.Run(map[bool]string{false: "before launch", true: "before submit"}[launched], func(t *testing.T) {
			f := newExecution(t)
			if launched {
				f.launch(t)
			}
			if _, e := f.s.Transition(f.uid, func(r *Run) error { r.PromptCap = 1; r.PromptsSent = 1; return nil }); e != nil {
				t.Fatal(e)
			}
			var e error
			if launched {
				e = f.m.submit(t.Context(), f.uid)
			} else {
				_, e = f.m.executePrompt(t.Context(), f.uid, 0)
			}
			r := loaded(t, f.s, f.uid)
			if e != nil || r.Phase != Held || r.Reason != "prompt_budget" || r.PromptsSent != 1 || f.count("agent.prompt") != 0 {
				t.Fatalf("run=%+v error=%v prompts=%d", r, e, f.count("agent.prompt"))
			}
			if !launched && (f.count("tab.create") != 0 || f.count("agent.start") != 0) {
				t.Fatal("exhausted budget launched an agent")
			}
		})
	}
}

func TestPromptBudgetRejectedCountsOnce(t *testing.T) {
	for _, code := range []string{"agent_blocked", "agent_not_ready"} {
		t.Run(code, func(t *testing.T) {
			f := newExecution(t)
			if _, e := f.s.Transition(f.uid, func(r *Run) error { r.PromptCap = 1; return nil }); e != nil {
				t.Fatal(e)
			}
			f.launch(t)
			f.h.Fail("agent.prompt", herdr.Error{Code: code})
			f.submit(t)
			r := loaded(t, f.s, f.uid)
			if r.Steps[0].Submission != Rejected || r.PromptsSent != 1 {
				t.Fatalf("rejection=%+v", r)
			}
			f.submit(t)
			f.submit(t)
			r = loaded(t, f.s, f.uid)
			if r.Steps[0].Submission != Accepted || r.PromptsSent != 1 || f.count("agent.prompt") != 2 {
				t.Fatalf("resend=%+v prompts=%d", r, f.count("agent.prompt"))
			}
		})
	}
}

func TestPromptBudgetLostBeforeSubmission(t *testing.T) {
	f := newExecution(t)
	if _, e := f.s.Transition(f.uid, func(r *Run) error { r.PromptCap = 1; return nil }); e != nil {
		t.Fatal(e)
	}
	f.launch(t)
	r := loaded(t, f.s, f.uid)
	f.h.RemoveAgent(r.Herdr.AgentName)
	f.submit(t)
	r = loaded(t, f.s, f.uid)
	if r.Phase != Held || r.Reason != "submission_unproven" || r.PromptsSent != 0 || f.count("agent.start") != 1 || f.count("agent.prompt") != 0 {
		t.Fatalf("run=%+v", r)
	}
}

func TestPromptBudgetLegacyRun(t *testing.T) {
	f := newExecution(t)
	f.m.cfg.Limits.MaxRunPrompts = 1
	if _, e := f.s.Transition(f.uid, func(r *Run) error { r.PromptsSent = 12; return nil }); e != nil {
		t.Fatal(e)
	}
	f.accepted(t)
	r := loaded(t, f.s, f.uid)
	if r.Steps[0].Submission != Accepted || r.PromptsSent != 13 || f.count("agent.prompt") != 1 {
		t.Fatalf("legacy run gained a cap: %+v", r)
	}
}

func TestPromptCapFrozenAtPreparation(t *testing.T) {
	f := newAdmission(t)
	f.d.Def.Action = defs.Action{Kind: "execute", Prompt: "work"}
	f.d.WFDef = nil
	f.d.Workflow = nil
	f.m.cfg.Limits.MaxRunPrompts = 6
	f.m.cfg.Profiles["default"] = config.Profile{Kind: "codex", MaxRunPrompts: new(4)}
	r, e := f.m.prepare(f.d, f.uid, "occurrence", Target{})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.Create(r); e != nil {
		t.Fatal(e)
	}
	f.m.cfg.Limits.MaxRunPrompts = 1
	r = loaded(t, f.s, f.uid)
	if r.PromptCap != 4 || r.Timeout != time.Hour {
		t.Fatalf("frozen limits=%+v", r)
	}
	if _, e = f.s.Transition(f.uid, func(r *Run) error { r.Phase = Admitted; r.PromptsSent = 1; return nil }); e != nil {
		t.Fatal(e)
	}
	h := herdrtest.New(t)
	f.m.h = &herdr.Client{Path: h.Path}
	if e = f.m.launch(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	if e = f.m.submit(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	r = loaded(t, f.s, f.uid)
	if r.PromptsSent != 2 || r.Steps[0].Submission != Accepted {
		t.Fatalf("live config shortened frozen budget: %+v", r)
	}
}

func TestResumePromptBudget(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "reset loops"}[reset], func(t *testing.T) {
			f := heldResumeFixture(t)
			if _, e := f.s.Transition(f.uid, func(r *Run) error {
				r.PromptCap = 1
				r.PromptsSent = 1
				r.Reason = "prompt_budget"
				r.Loop.Used = 2
				r.Loop.Rejection = "saved rejection"
				// Resume starts the held step; the failed attempt and retries stay spent.
				r.WFDef.Steps[0].Kind = "prompt"
				r.WFDef.Steps[0].Command = ""
				r.WFDef.Steps[0].Prompt = "Continue"
				r.WFDef.Steps[0].Retries = 1
				r.Steps[0].Kind = "prompt"
				r.Steps[0].Attempt = 2
				r.Steps[0].Submission = None
				r.Steps[0].Receipt = nil
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			req := Request{Version: 1, UID: "request", Kind: "resume", RunUID: f.uid, ResetLoops: reset}
			if e := f.m.acceptResume(t.Context(), req); e != nil {
				t.Fatal(e)
			}
			r := loaded(t, f.s, f.uid)
			if r.PromptsSent != 0 || r.PromptCap != 1 || r.Steps[0].Attempt != 3 {
				t.Fatalf("acceptance=%+v", r)
			}
			if reset {
				if r.Loop.Used != 0 || r.Loop.Rejection != "" {
					t.Fatal(r.Loop)
				}
			} else if r.Loop.Used != 2 || r.Loop.Rejection != "saved rejection" {
				t.Fatal(r.Loop)
			}
			for head(loaded(t, f.s, f.uid)) != nil {
				deliver(t, f.q, f.uid)
			}
			if e := f.m.activateResume(t.Context(), f.uid); e != nil {
				t.Fatal(e)
			}
			h := herdrtest.New(t)
			f.m.h = &herdr.Client{Path: h.Path}
			if e := f.m.launch(t.Context(), f.uid); e != nil {
				t.Fatal(e)
			}
			if e := f.m.submit(t.Context(), f.uid); e != nil {
				t.Fatal(e)
			}
			prompts := 0
			for _, request := range h.Requests() {
				if request.Method == "agent.prompt" {
					prompts++
				}
			}
			r = loaded(t, f.s, f.uid)
			if r.PromptsSent != 1 || r.Steps[0].Submission != Accepted || prompts != 1 {
				t.Fatalf("resumed submission=%+v", r)
			}
			// Replaying acceptance must not grant another generation's budget.
			if e := f.m.Resume(t.Context(), req); e != nil {
				t.Fatal(e)
			}
			if loaded(t, f.s, f.uid).PromptsSent != 1 {
				t.Fatal("replay reset budget")
			}
		})
	}
}
