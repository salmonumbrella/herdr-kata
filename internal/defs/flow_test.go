package defs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFlowClassification(t *testing.T) {
	for _, tc := range []struct {
		name, job, workflow, step, prompt, input string
		supported                                bool
	}{
		{name: "required at admission", input: "description", prompt: "{{input}}", supported: true},
		{name: "input", job: `{"input":"text"}`, supported: true},
		{name: "input type", job: `{"input":42}`},
		{name: "input null", job: `{"input":null}`},
		{name: "input too large", job: `{"input":"` + strings.Repeat("x", 4097) + `"}`},
		{name: "input nul", job: `{"input":"\u0000"}`},
		{name: "fresh", step: `{"agent_mode":"fresh"}`, supported: true},
		{name: "mode unknown", step: `{"agent_mode":"reuse"}`},
		{name: "on fail", step: `{"on_fail":{"goto":"first"}}`},
		{name: "on failure", workflow: `{"on_failure":"park"}`, supported: true},
		{name: "unknown failure policy", workflow: `{"on_failure":"retry"}`},
		{name: "job on failure", job: `{"on_failure":"park"}`},
		{name: "first previous", prompt: "{{previous}}"},
		{name: "unknown placeholder", prompt: "{{secret}}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := fixture(t, "job")
			j.Action.Prompt = ""
			j.Action.WorkflowUID = "wf"
			j.Options = json.RawMessage(tc.job)
			p := tc.prompt
			if p == "" {
				p = "work"
			}
			w := WorkflowDef{Version: 1, Input: tc.input, Options: json.RawMessage(tc.workflow), Steps: []Step{{Key: "first", Kind: "prompt", Prompt: p, Options: json.RawMessage(tc.step)}}}
			_, s := Classify(j, &w, cfg())
			if s.Supported != tc.supported || tc.supported && len(s.Warnings) > 0 {
				t.Fatalf("support: %+v", s)
			}
		})
	}
}

func TestWorkflowInputType(t *testing.T) {
	for _, value := range []string{"null", "42", "{}"} {
		if _, e := DecodeWorkflow([]byte(`{"version":1,"input":` + value + `,"steps":[{"key":"main","kind":"command","command":"true"}]}`)); e == nil {
			t.Fatal("accepted input", value)
		}
	}
}

// Runtime execution modes must agree with Classify's exact option keys.
func TestFlowExactKeys(t *testing.T) {
	for _, tc := range []struct {
		name, step, workflow string
		fresh, park          bool
		warnings             int
	}{
		{"variants only", `{"Agent_Mode":"fresh"}`, `{"ON_FAILURE":"park"}`, false, false, 2},
		{"canonical then variants", `{"agent_mode":"fresh","Agent_Mode":"reuse"}`, `{"on_failure":"park","ON_FAILURE":"ignore"}`, true, true, 2},
		{"variants then canonical", `{"Agent_Mode":"reuse","agent_mode":"fresh"}`, `{"ON_FAILURE":"ignore","on_failure":"park"}`, true, true, 2},
		{"duplicate canonical", `{"agent_mode":"reuse","agent_mode":"fresh"}`, `{"on_failure":"ignore","on_failure":"park"}`, true, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := fixture(t, "job")
			j.Action = Action{Kind: "execute", WorkflowUID: "wf"}
			w := &WorkflowDef{Version: 1, Options: json.RawMessage(tc.workflow), Steps: []Step{{Key: "main", Kind: "prompt", Prompt: "work", Options: json.RawMessage(tc.step)}}}
			_, s := Classify(j, w, cfg())
			if !s.Supported || len(s.Warnings) != tc.warnings {
				t.Fatalf("support: %+v", s)
			}
			if got := FreshAgent(w.Steps[0]); got != tc.fresh {
				t.Errorf("fresh = %v, want %v", got, tc.fresh)
			}
			if got := ParkFailure(w); got != tc.park {
				t.Errorf("park = %v, want %v", got, tc.park)
			}
		})
	}
}
