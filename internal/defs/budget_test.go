package defs

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

// §21.8 counts prompt attempts (including retries and loop visits), never commands.
func TestClassifyPromptBudget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		steps  []Step
		cap    int
		reason string
	}{
		{name: "single at cap", cap: 1},
		{name: "retries at cap", steps: []Step{{Key: "a", Kind: "prompt", Prompt: "work", Retries: 2}, {Key: "b", Kind: "prompt", Prompt: "check", Retries: 1}}, cap: 5},
		{name: "retries over cap", steps: []Step{{Key: "a", Kind: "prompt", Prompt: "work", Retries: 2}, {Key: "b", Kind: "prompt", Prompt: "check", Retries: 1}}, cap: 4, reason: "prompt_budget 5 > 4"},
		{name: "mixed loop at cap", steps: []Step{{Key: "make", Kind: "prompt", Prompt: "make"}, {Key: "test", Kind: "command", Command: "true", After: []string{"make"}}, {Key: "check", Kind: "prompt", Prompt: "check", After: []string{"test"}, Options: json.RawMessage(`{"on_fail":{"goto":"make","max_loops":3}}`)}}, cap: 8},
		{name: "mixed loop over cap", steps: []Step{{Key: "make", Kind: "prompt", Prompt: "make"}, {Key: "test", Kind: "command", Command: "true", After: []string{"make"}}, {Key: "check", Kind: "prompt", Prompt: "check", After: []string{"test"}, Options: json.RawMessage(`{"on_fail":{"goto":"make","max_loops":3}}`)}}, cap: 7, reason: "prompt_budget 8 > 7"},
		{name: "command only", steps: []Step{{Key: "test", Kind: "command", Command: "true", Retries: 100}}, cap: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := fixture(t, "job")
			var w *WorkflowDef
			if tc.steps != nil {
				j.Action = Action{Kind: "execute", WorkflowUID: "wf"}
				w = &WorkflowDef{Version: 1, Steps: tc.steps}
			}
			c := cfg()
			c.Limits.MaxRunPrompts = tc.cap
			_, s := Classify(j, w, c)
			if tc.reason == "" {
				if !s.Supported {
					t.Fatal(s)
				}
			} else if s.Supported || !slices.Contains(s.Reasons, tc.reason) {
				t.Fatalf("support=%+v", s)
			}
		})
	}
}
func TestClassifyProfileBudget(t *testing.T) {
	j := fixture(t, "job")
	j.Action = Action{Kind: "execute", WorkflowUID: "wf"}
	j.Options = json.RawMessage(`{"agent":"other"}`)
	w := &WorkflowDef{Version: 1, Steps: []Step{{Key: "main", Kind: "prompt", Prompt: "work", Retries: 4}}}
	c := cfg()
	c.Limits.MaxRunPrompts = 6
	p := c.Profiles["other"]
	p.MaxRunPrompts = new(4)
	c.Profiles["other"] = p
	_, s := Classify(j, w, c)
	if s.Supported || !slices.Contains(s.Reasons, "prompt_budget 5 > 4") {
		t.Fatal(s)
	}
	p.MaxRunPrompts = new(100)
	c.Profiles["other"] = p
	c.Limits.MaxRunPrompts = 4
	_, s = Classify(j, w, c)
	if s.Supported || !slices.Contains(s.Reasons, "prompt_budget 5 > 4") {
		t.Fatal("profile raised installation cap", s)
	}
}
func TestClassifyTimeoutLimit(t *testing.T) {
	c := cfg()
	c.Kata.Env = map[string]string{"KATA_HOME": t.TempDir()}
	c.Limits.MaxTimeout = 8 * time.Hour
	for _, tc := range []struct {
		seconds   int64
		supported bool
	}{{0, true}, {28800, true}, {28801, false}} {
		j := fixture(t, "job")
		j.TimeoutSeconds = tc.seconds
		_, s := Classify(j, nil, c)
		if s.Supported != tc.supported || !tc.supported && !slices.Contains(s.Reasons, "timeout_seconds 28801 exceeds limits.max_timeout 8h0m0s") {
			t.Fatalf("seconds=%d support=%+v", tc.seconds, s)
		}
	}
}

func TestPromptBudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		j    JobDef
		w    *WorkflowDef
		want int
	}{
		{name: "single", j: JobDef{Action: Action{Kind: "execute", Prompt: "work"}}, want: 1},
		{name: "retries", j: JobDef{Action: Action{Kind: "execute", WorkflowUID: "wf"}}, w: &WorkflowDef{Steps: []Step{{Key: "a", Kind: "prompt", Retries: 2}, {Key: "b", Kind: "prompt", Retries: 1}, {Key: "c", Kind: "command", Retries: 100}}}, want: 5},
		{name: "loop", j: JobDef{Action: Action{Kind: "execute", WorkflowUID: "wf"}}, w: &WorkflowDef{Steps: []Step{{Key: "a", Kind: "prompt"}, {Key: "b", Kind: "command", After: []string{"a"}}, {Key: "c", Kind: "prompt", After: []string{"b"}, Options: json.RawMessage(`{"on_fail":{"goto":"a","max_loops":3}}`)}}}, want: 8},
		{name: "commands", j: JobDef{Action: Action{Kind: "execute", WorkflowUID: "wf"}}, w: &WorkflowDef{Steps: []Step{{Key: "a", Kind: "command", Retries: 100}}}, want: 0},
		{name: "notify", j: JobDef{Action: Action{Kind: "notify"}}, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PromptBudget(tc.j, tc.w); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}
