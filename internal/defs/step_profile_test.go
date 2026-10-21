package defs

import (
	"encoding/json"
	"slices"
	"testing"
)

func stepProfileJob(t *testing.T) JobDef {
	j := fixture(t, "job")
	j.Action = Action{Kind: "execute", WorkflowUID: "wf"}
	return j
}

// A prompt step may run on another configured profile than its job.
func TestClassifyStepProfile(t *testing.T) {
	for _, tc := range []struct {
		name, kind, agent, reason string
	}{
		{"mapped prompt", "prompt", "other", ""},
		{"run profile", "prompt", "default", ""},
		{"unmapped prompt", "prompt", "missing", "unmapped profile: steps.check.options.agent missing; configure profiles.missing and include it in the project profiles allowlist"},
		{"command", "command", "other", "step profile differs from run profile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := Step{Key: "check", Kind: tc.kind, After: []string{"make"}, Options: json.RawMessage(`{"agent":"` + tc.agent + `"}`)}
			if tc.kind == "prompt" {
				check.Prompt = "check"
			} else {
				check.Command = "true"
			}
			w := &WorkflowDef{Version: 1, Steps: []Step{{Key: "make", Kind: "prompt", Prompt: "make"}, check}}
			_, s := Classify(stepProfileJob(t), w, cfg())
			if tc.reason == "" {
				if !s.Supported {
					t.Fatalf("support=%+v", s)
				}
			} else if s.Supported || !slices.Contains(s.Reasons, tc.reason) {
				t.Fatalf("support=%+v", s)
			}
		})
	}
}

// The stricter of the run's and a prompt step's profile cap bounds the run.
func TestClassifyStepProfileCap(t *testing.T) {
	c := cfg()
	c.Limits.MaxRunPrompts = 6
	p := c.Profiles["other"]
	p.MaxRunPrompts = new(2)
	c.Profiles["other"] = p
	w := &WorkflowDef{Version: 1, Steps: []Step{{Key: "make", Kind: "prompt", Prompt: "make", Retries: 1}, {Key: "check", Kind: "prompt", Prompt: "check", After: []string{"make"}, Options: json.RawMessage(`{"agent":"other"}`)}}}
	if got := RunCap(c, "default", w); got != 2 {
		t.Fatalf("cap=%d", got)
	}
	_, s := Classify(stepProfileJob(t), w, c)
	if s.Supported || !slices.Contains(s.Reasons, "prompt_budget 3 > 2") {
		t.Fatalf("support=%+v", s)
	}
	w.Steps[1].Kind, w.Steps[1].Prompt, w.Steps[1].Command, w.Steps[1].Options = "command", "", "true", nil
	if got := RunCap(c, "default", w); got != 6 {
		t.Fatalf("cap without step profile=%d", got)
	}
}

// A step whose profile differs from the run's always gets a fresh agent.
func TestStepProfile(t *testing.T) {
	for _, tc := range []struct {
		options, run, want string
		fresh              bool
	}{
		{``, "default", "default", false},
		{`{"agent":"default"}`, "default", "default", false},
		{`{"agent":"other"}`, "default", "other", true},
		{`{"agent":"other","agent_mode":"fresh"}`, "default", "other", true},
		{`{"agent_mode":"fresh"}`, "other", "other", true},
	} {
		s := Step{Key: "check", Kind: "prompt", Options: json.RawMessage(tc.options)}
		if got := StepProfile(s, tc.run); got != tc.want {
			t.Errorf("%s: profile=%q", tc.options, got)
		}
		if got := FreshStep(s, tc.run); got != tc.fresh {
			t.Errorf("%s: fresh=%v", tc.options, got)
		}
	}
}
