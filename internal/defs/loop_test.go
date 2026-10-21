package defs

import (
	"encoding/json"
	"testing"
)

func TestLoopClassification(t *testing.T) {
	for _, tc := range []struct {
		name, edge  string
		retries     int
		branch, two bool
		valid       bool
	}{
		{name: "default", edge: `{"goto":"make"}`, valid: true},
		{name: "eight", edge: `{"goto":"make","max_loops":8}`, valid: true},
		{name: "zero", edge: `{"goto":"make","max_loops":0}`},
		{name: "nine", edge: `{"goto":"make","max_loops":9}`},
		{name: "fraction", edge: `{"goto":"make","max_loops":1.5}`},
		{name: "self", edge: `{"goto":"check"}`},
		{name: "missing", edge: `{"goto":"absent"}`},
		{name: "null", edge: `null`},
		{name: "retry", edge: `{"goto":"make"}`, retries: 1},
		{name: "branch", edge: `{"goto":"make"}`, branch: true},
		{name: "two", edge: `{"goto":"make"}`, two: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := WorkflowDef{Version: 1, Steps: []Step{{Key: "make", Kind: "command", Command: "true", Retries: tc.retries}, {Key: "middle", Kind: "command", Command: "true", After: []string{"make"}}, {Key: "check", Kind: "command", Command: "false", After: []string{"middle"}, Options: json.RawMessage(`{"on_fail":` + tc.edge + `}`)}}}
			if tc.branch {
				w.Steps[2].After = []string{"make"}
			}
			if tc.two {
				w.Steps[1].Options = json.RawMessage(`{"on_fail":{"goto":"make"}}`)
			}
			j := fixture(t, "job")
			j.Action.Prompt = ""
			j.Action.WorkflowUID = "wf"
			_, s := Classify(j, &w, cfg())
			if s.Supported != tc.valid || tc.valid && len(s.Warnings) > 0 {
				t.Fatalf("support: %+v", s)
			}
		})
	}
}
