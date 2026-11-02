package defs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSkipExitClassification(t *testing.T) {
	gate := func(opts string) Step {
		return Step{Key: "gate", Kind: "command", Command: "true", Options: json.RawMessage(opts)}
	}
	work := Step{Key: "work", Kind: "prompt", Prompt: "work", After: []string{"gate"}}
	for _, tc := range []struct {
		name      string
		steps     []Step
		supported bool
		reason    string
	}{
		{"gate", []Step{gate(`{"skip_exit":78}`), work}, true, ""},
		{"lowest", []Step{gate(`{"skip_exit":1}`), work}, true, ""},
		{"highest", []Step{gate(`{"skip_exit":255}`), work}, true, ""},
		{"zero", []Step{gate(`{"skip_exit":0}`), work}, false, "skip_exit must be an exit code"},
		{"too large", []Step{gate(`{"skip_exit":256}`), work}, false, "skip_exit must be an exit code"},
		{"fraction", []Step{gate(`{"skip_exit":7.5}`), work}, false, "skip_exit must be an exit code"},
		{"string", []Step{gate(`{"skip_exit":"78"}`), work}, false, "skip_exit must be an exit code"},
		{"null", []Step{gate(`{"skip_exit":null}`), work}, false, "skip_exit must be an exit code"},
		{"prompt step", []Step{{Key: "gate", Kind: "prompt", Prompt: "x", Options: json.RawMessage(`{"skip_exit":78}`)}}, false, "on a command step"},
		{"inside loop span", []Step{
			{Key: "make", Kind: "prompt", Prompt: "make"},
			{Key: "check", Kind: "command", Command: "true", After: []string{"make"}, Options: json.RawMessage(`{"skip_exit":78,"on_fail":{"goto":"make"}}`)},
		}, false, "not supported inside a loop span"},
		{"before loop span", []Step{
			gate(`{"skip_exit":78}`),
			{Key: "make", Kind: "prompt", Prompt: "make", After: []string{"gate"}},
			{Key: "check", Kind: "command", Command: "true", After: []string{"make"}, Options: json.RawMessage(`{"on_fail":{"goto":"make"}}`)},
		}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := fixture(t, "job")
			j.Action.Prompt = ""
			j.Action.WorkflowUID = "wf"
			w := WorkflowDef{Version: 1, Steps: tc.steps}
			_, s := Classify(j, &w, cfg())
			if s.Supported != tc.supported || tc.supported && len(s.Warnings) > 0 {
				t.Fatalf("support: %+v", s)
			}
			if tc.reason != "" && !strings.Contains(strings.Join(s.Reasons, "; "), tc.reason) {
				t.Fatalf("reasons: %v", s.Reasons)
			}
		})
	}
}

func TestSkipExitValue(t *testing.T) {
	for _, tc := range []struct {
		step Step
		want int
	}{
		{Step{Kind: "command", Options: json.RawMessage(`{"skip_exit":78}`)}, 78},
		{Step{Kind: "command"}, 0},
		{Step{Kind: "command", Options: json.RawMessage(`{"skip_exit":0}`)}, 0},
		{Step{Kind: "command", Options: json.RawMessage(`{"skip_exit":300}`)}, 0},
		{Step{Kind: "command", Options: json.RawMessage(`{"Skip_Exit":78}`)}, 0},
		{Step{Kind: "prompt", Options: json.RawMessage(`{"skip_exit":78}`)}, 0},
	} {
		if got := SkipExit(tc.step); got != tc.want {
			t.Fatalf("SkipExit(%s %s) = %d, want %d", tc.step.Kind, tc.step.Options, got, tc.want)
		}
	}
}
