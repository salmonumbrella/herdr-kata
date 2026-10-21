package runner

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/inbox"
)

// Admission freezes each differing step profile and the stricter prompt cap.
func TestStepProfileFrozenAtPreparation(t *testing.T) {
	f := newAdmission(t)
	f.m.cfg.Limits.MaxRunPrompts = 6
	f.m.cfg.Profiles["checker"] = config.Profile{Kind: "claude", Args: []string{"--model", "checker-model"}, MaxRunPrompts: new(3)}
	f.d.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{
		{Key: "make", Kind: "prompt", Prompt: "make"},
		{Key: "check", Kind: "prompt", Prompt: "check", After: []string{"make"}, Options: json.RawMessage(`{"agent":"checker"}`)},
	}}
	r, e := f.m.prepare(f.d, f.uid, "occurrence", Target{})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.Create(r); e != nil {
		t.Fatal(e)
	}
	f.m.cfg.Profiles["checker"] = config.Profile{Kind: "codex"}
	r = loaded(t, f.s, f.uid)
	want := map[string]config.Profile{"checker": {Kind: "claude", Args: []string{"--model", "checker-model"}, MaxRunPrompts: new(3)}}
	if r.ProfileName != "default" || r.PromptCap != 3 || !reflect.DeepEqual(r.StepProfiles, want) {
		t.Fatalf("frozen step profiles=%+v cap=%d", r.StepProfiles, r.PromptCap)
	}
}

// A step on another profile starts its own agent with that profile's kind and args.
func TestStepProfileLaunchesFreshAgent(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "make", Kind: "prompt", Prompt: "make"}, defs.Step{Key: "check", Kind: "prompt", Prompt: "check", After: []string{"make"}, Options: json.RawMessage(`{"agent":"checker"}`)})
	if _, e := f.s.Transition(f.uid, func(r *Run) error {
		r.ProfileName = "default"
		r.StepProfiles = map[string]config.Profile{"checker": {Kind: "claude", Args: []string{"--model", "checker-model"}}}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	f.launch(t)
	f.submit(t)
	f.result(t, "succeeded")
	if f.await(t) == nil {
		t.Fatal("no first result")
	}
	if e := f.m.launch(t.Context(), f.uid, 1); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	if r.Steps[1].Herdr == nil || r.Steps[1].Herdr.AgentName == r.Herdr.AgentName {
		t.Fatalf("step profile shared the run agent: %+v", r)
	}
	type start struct {
		Name, Kind string
		Args       []string
	}
	var starts []start
	for _, req := range f.h.Requests() {
		if req.Method == "agent.start" {
			var s start
			if e := json.Unmarshal(req.Params, &s); e != nil {
				t.Fatal(e)
			}
			starts = append(starts, s)
		}
	}
	if len(starts) != 2 || starts[0].Kind != "codex" || starts[1].Name != r.Steps[1].Herdr.AgentName || starts[1].Kind != "claude" || !slices.Equal(starts[1].Args, []string{"--model", "checker-model"}) {
		t.Fatalf("agent starts=%+v", starts)
	}
}

// A child refuses a frozen workflow whose step profile left the configuration.
func TestChildRefusesRemovedStepProfile(t *testing.T) {
	f := newChildAdmission(t)
	f.d.Child.Binding.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "check", Kind: "prompt", Prompt: "check", Options: json.RawMessage(`{"agent":"checker"}`)}}}
	target, err := f.m.pin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.m.childInputs(t.Context(), f.d, target.Identity.ProjectUID); !errors.Is(err, inbox.ErrUnsupported) {
		t.Fatalf("removed step profile admitted: %v", err)
	}
}

// The hook review guard follows the step's own profile: a Codex checker step
// in a job on another agent kind still waits out Codex's hook review.
func TestStepProfileHookReviewFollowsStepKind(t *testing.T) {
	f := workflowExecution(t, defs.Step{Key: "make", Kind: "prompt", Prompt: "make"}, defs.Step{Key: "check", Kind: "prompt", Prompt: "check", After: []string{"make"}, Options: json.RawMessage(`{"agent":"checker"}`)})
	if _, e := f.s.Transition(f.uid, func(r *Run) error {
		r.ProfileName = "default"
		r.Profile = config.Profile{Kind: "claude"}
		r.StepProfiles = map[string]config.Profile{"checker": {Kind: "codex"}}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	f.launch(t)
	f.submit(t)
	f.result(t, "succeeded")
	if f.await(t) == nil {
		t.Fatal("no first result")
	}
	if e := f.m.launch(t.Context(), f.uid, 1); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	f.h.SetRead(r.Steps[1].Herdr.AgentName, "  Hooks need review\n  4 hooks are new or changed.\n\n› 1. Review hooks\n  2. Trust all and continue")
	prompts := f.count("agent.prompt")
	if e := f.m.submitStep(t.Context(), f.uid, 1); e != nil {
		t.Fatal(e)
	}
	if r = loaded(t, f.s, f.uid); f.count("agent.prompt") != prompts || r.Steps[1].Submission != None {
		t.Fatalf("checker task went into the hook review: submission=%s", r.Steps[1].Submission)
	}
}
