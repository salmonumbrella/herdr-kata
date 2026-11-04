package defs

import (
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"hegel.dev/go/hegel"
	"slices"
	"testing"
)

// Removing the append, splitting the model or mutating profile argv must fail.
func TestModelIsOneUnchangedArg(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		model := hegel.Draw(ht, hegel.Text())
		j, w, c := fixture()
		c.Profiles["default"] = config.Profile{Argv: []string{"agent", "--no-daemon"}, ModelFlag: "--model"}
		j.Definition.Options["model"] = model
		p, reasons := Freeze(j, w, c, "example-project", look)
		want := []string{"/usr/local/bin/agent", "--no-daemon"}
		if model != "" {
			want = append(want, "--model", model)
		}
		if len(reasons) != 0 || !slices.Equal(p.Steps[0].Argv, want) || !slices.Equal(c.Profiles["default"].Argv, []string{"agent", "--no-daemon"}) {
			ht.Fatalf("model %q: argv %q reasons %v profile %q", model, p.Steps[0].Argv, reasons, c.Profiles["default"].Argv)
		}
	})
}

// Literal expected argv pins precedence independently of the resolver.
func TestModelResolution(t *testing.T) {
	for _, tt := range []struct {
		name      string
		job, step map[string]any
		profile   string
		flag      string
		want      []string
		reasons   []string
	}{
		{name: "shrunk nonempty model", job: map[string]any{"model": "0"}, flag: "--model", want: []string{"/usr/local/bin/agent", "--model", "0"}},
		{name: "job", job: map[string]any{"model": "small"}, flag: "--model", want: []string{"/usr/local/bin/agent", "--model", "small"}},
		{name: "step", job: map[string]any{"model": "small"}, step: map[string]any{"model": "large"}, flag: "--model", want: []string{"/usr/local/bin/agent", "--model", "large"}},
		{name: "empty step inherits", job: map[string]any{"model": "small"}, step: map[string]any{"model": ""}, flag: "--model", want: []string{"/usr/local/bin/agent", "--model", "small"}},
		{name: "step profile", job: map[string]any{"model": "small"}, step: map[string]any{"agent": "custom"}, profile: "custom", flag: "-m", want: []string{"/usr/local/bin/agent", "-m", "small"}},
		{name: "no model", flag: "--model", want: []string{"/usr/local/bin/agent"}},
		{name: "empty model", job: map[string]any{"model": ""}, flag: "--model", want: []string{"/usr/local/bin/agent"}},
		{name: "flagless", job: map[string]any{"model": "small"}, want: []string{"/usr/local/bin/agent"}, reasons: []string{"model unsupported by profile: default"}},
		{name: "missing", job: map[string]any{"model": "small", "agent": "missing"}, reasons: []string{"profile not configured: missing"}},
		{name: "spaces and unicode", job: map[string]any{"model": "a b; '模型' --other"}, flag: "--model", want: []string{"/usr/local/bin/agent", "--model", "a b; '模型' --other"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			j, w, c := fixture()
			j.Definition.Options = tt.job
			c.Profiles["default"] = config.Profile{Argv: []string{"agent"}, ModelFlag: tt.flag}
			if tt.profile != "" {
				c.Profiles[tt.profile] = c.Profiles["default"]
			}
			j.Definition.Action = kata.Action{Kind: "execute", WorkflowUID: new("wf")}
			w["wf"] = kata.CronWorkflow{UID: "wf", Definition: kata.WorkflowDefinition{Options: map[string]any{"model": false}, Steps: []kata.WorkflowStep{{Key: "work", Kind: "prompt", Prompt: new("x"), Options: tt.step}}}}
			p, r := Freeze(j, w, c, "example-project", look)
			if !slices.Equal(r, tt.reasons) || !slices.Equal(p.Steps[0].Argv, tt.want) {
				t.Fatalf("argv %q reasons %v; want %q %v", p.Steps[0].Argv, r, tt.want, tt.reasons)
			}
		})
	}
}

func TestModelTypesAndCommandScope(t *testing.T) {
	for _, bad := range []any{nil, false, 7, []any{"small"}, map[string]any{"name": "small"}} {
		for _, scope := range []string{"job", "step", "both", "overridden job"} {
			j, w, c := fixture()
			c.Profiles["default"] = config.Profile{Argv: []string{"agent"}, ModelFlag: "--model"}
			step := kata.WorkflowStep{Key: "work", Kind: "prompt", Prompt: new("x"), Options: map[string]any{}}
			if scope != "step" {
				j.Definition.Options["model"] = bad
			}
			if scope == "step" || scope == "both" {
				step.Options["model"] = bad
			}
			if scope == "overridden job" {
				step.Options["model"] = "small"
			}
			j.Definition.Action = kata.Action{Kind: "execute", WorkflowUID: new("wf")}
			w["wf"] = kata.CronWorkflow{UID: "wf", Definition: kata.WorkflowDefinition{Steps: []kata.WorkflowStep{step}}}
			_, r := Freeze(j, w, c, "example-project", look)
			if !slices.Equal(r, []string{"model must be a string"}) {
				t.Fatalf("scope %s bad %v: %v", scope, bad, r)
			}
			step.Kind = "command"
			step.Command = new("true")
			w["wf"] = kata.CronWorkflow{UID: "wf", Definition: kata.WorkflowDefinition{Steps: []kata.WorkflowStep{step}}}
			_, r = Freeze(j, w, c, "example-project", look)
			if len(r) != 0 {
				t.Fatalf("command scope %s: %v", scope, r)
			}
		}
	}
}

func TestModelMixedProfiles(t *testing.T) {
	j, w, c := fixture()
	c.Profiles["capable"] = config.Profile{Argv: []string{"agent"}, ModelFlag: "--model"}
	j.Definition.Action = kata.Action{Kind: "execute", WorkflowUID: new("wf")}
	w["wf"] = kata.CronWorkflow{UID: "wf", Definition: kata.WorkflowDefinition{Steps: []kata.WorkflowStep{
		{Key: "choose", Kind: "prompt", Prompt: new("x"), Options: map[string]any{"agent": "capable", "model": "small"}},
		{Key: "fixed", Kind: "prompt", Prompt: new("y")},
	}}}
	p, r := Freeze(j, w, c, "example-project", look)
	if len(r) != 0 || !slices.Equal(p.Steps[0].Argv, []string{"/usr/local/bin/agent", "--model", "small"}) || !slices.Equal(p.Steps[1].Argv, []string{"/usr/local/bin/agent", "--no-daemon"}) {
		t.Fatalf("plan %+v reasons %v", p, r)
	}
	j.Definition.Options["model"] = "inherited"
	_, r = Freeze(j, w, c, "example-project", look)
	if !slices.Equal(r, []string{"model unsupported by profile: default"}) {
		t.Fatal(r)
	}
}

// Different steps and later config edits must not corrupt an already frozen argv.
func TestModelFrozenArgvIsolation(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		first := hegel.Draw(ht, hegel.Text())
		second := hegel.Draw(ht, hegel.Text())
		j, w, c := fixture()
		backing := make([]string, 1, 16)
		backing[0] = "agent"
		c.Profiles["default"] = config.Profile{Argv: backing, ModelFlag: "--model"}
		j.Definition.Action = kata.Action{Kind: "execute", WorkflowUID: new("wf")}
		w["wf"] = kata.CronWorkflow{UID: "wf", Definition: kata.WorkflowDefinition{Steps: []kata.WorkflowStep{
			{Key: "a", Kind: "prompt", Prompt: new("x"), Options: map[string]any{"model": first}},
			{Key: "b", Kind: "prompt", Prompt: new("x"), Options: map[string]any{"model": second}},
		}}}
		p, r := Freeze(j, w, c, "example-project", look)
		if len(r) != 0 {
			ht.Fatal(r)
		}
		want := []string{"/usr/local/bin/agent"}
		if first != "" {
			want = append(want, "--model", first)
		}
		sibling := slices.Clone(p.Steps[1].Argv)
		c.Profiles["default"].Argv[0] = "changed"
		p.Steps[1].Argv[0] = "other"
		if !slices.Equal(p.Steps[0].Argv, want) || sibling[0] != "/usr/local/bin/agent" {
			ht.Fatalf("first %q want %q", p.Steps[0].Argv, want)
		}
	})
}

func TestModelWarningMatchesExplicitSelection(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		value := hegel.Draw(ht, hegel.Text())
		flag := "-" + hegel.Draw(ht, hegel.Text())
		// Exercise the diagnostic's exact string matching independently of config grammar.
		j, w, c := fixture()
		for _, argv := range [][]string{{"agent", flag, value}, {"agent", flag + "=" + value}} {
			c.Profiles["default"] = config.Profile{Argv: argv, ModelFlag: flag}
			got := DefaultModelSteps(j, w, c, "example-project")
			var want []string
			if value == "" {
				want = []string{"warning: job 01J00000000000000000000010 step main: profile default has no explicit model in job options or profile argv; set options.model"}
			}
			if !slices.Equal(got, want) {
				ht.Fatalf("flag %q value %q: %q want %q", flag, value, got, want)
			}
		}
	})
}
