package jobform

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"go.kenn.io/kit/secretref"
)

func TestFieldsByShape(t *testing.T) {
	const common = "trigger timezone catchup grace overlap"
	for _, tc := range []struct {
		mode, issue, workflow, want string
		native                      bool
	}{
		{"prompt", "per-run", "", "name description tags mode prompt " + common + " timeout checkout profile issue_kind title body run_parent deadline secret_refs other_options enabled favorite", false},
		{"prompt", "existing", "", "name description tags mode prompt " + common + " timeout checkout profile issue_kind issue secret_refs other_options enabled favorite", false},
		{"workflow", "per-run", "command", "name description tags mode workflow input " + common + " timeout checkout profile issue_kind title body run_parent deadline secret_refs other_options enabled favorite", false},
		{"notify", "existing", "", "name description tags mode recipient message " + common + " issue other_options enabled favorite", false},
		{"recurring", "per-run", "", "name mode parent title body message " + common + " enabled favorite", true},
		{"recurring", "per-run", "", "name mode parent title body message " + common + " enabled favorite", false},
	} {
		t.Run(tc.mode+tc.issue+tc.workflow, func(t *testing.T) {
			d := Draft{Values: map[string]string{"mode": tc.mode, "trigger": "manual", "issue_kind": tc.issue, "workflow": tc.workflow}}
			c := Context{Native: tc.native, Workflows: []kata.Workflow{{UID: "command", Definition: []byte(`{"version":1,"steps":[{"key":"a","kind":"command","command":"true"}]}`)}}}
			fields := Fields(d, c)
			keys := []string{}
			for _, f := range fields {
				keys = append(keys, f.Key)
			}
			if strings.Join(keys, " ") != tc.want {
				t.Fatalf("fields %s", strings.Join(keys, " "))
			}
			for _, f := range fields {
				if tc.native && (f.Key == "mode" || f.Key == "parent") && f.Kind != ReadOnly {
					t.Fatalf("mutable native %s", f.Key)
				}
				if tc.mode == "recurring" && f.Key == "overlap" {
					display, _ := Get(f, d, c)
					if f.Kind != ReadOnly || display != "allow" {
						t.Fatalf("recurring overlap %+v %q", f, display)
					}
				}
				if f.Key == "profile" && tc.workflow == "command" {
					display, _ := Get(f, d, c)
					if f.Kind != ReadOnly || display != "— (not needed: command-only workflow)" {
						t.Fatalf("%+v %q", f, display)
					}
				}
				if tc.mode != "recurring" && f.Key == "trigger" && !reflect.DeepEqual(f.Choices, []string{"manual", "cron", "interval", "once"}) {
					t.Fatal(f.Choices)
				}
			}
		})
	}
	for _, trigger := range []string{"cron", "interval", "once"} {
		fields := Fields(Draft{Values: map[string]string{"mode": "prompt", "trigger": trigger}}, Context{})
		found := false
		for _, f := range fields {
			if f.Key == "schedule" {
				found = true
				if f.Label != map[string]string{"cron": "Cron", "interval": "Interval", "once": "Run at"}[trigger] {
					t.Fatal(f)
				}
			}
		}
		if !found {
			t.Fatal(trigger)
		}
	}
	fields := Fields(Draft{Values: map[string]string{"mode": "workflow", "workflow": "missing"}}, Context{})
	for _, f := range fields {
		if f.Key == "profile" && f.ReadOnlyReason == "not needed: command-only workflow" {
			t.Fatal("missing workflow treated as command-only")
		}
	}
}
func TestFieldSetValidation(t *testing.T) {
	c := Context{Config: config.Config{Limits: config.Limits{MaxTimeout: time.Hour}, Secrets: map[string]secretref.Ref{"example": {Env: "EXAMPLE_SECRET"}}}}
	d := Draft{Values: map[string]string{"mode": "prompt", "trigger": "cron", "timezone": "UTC"}, Dirty: map[string]bool{}}
	set := func(key, s string) error { return Set(Field{Key: key}, &d, c, s) }
	if err := set("tags", " daily, example, daily "); err != nil || d.Values["tags"] != "daily, example" {
		t.Fatalf("%+v %v", d, err)
	}
	if err := set("tags", ""); err != nil || d.Values["tags"] != "" {
		t.Fatalf("%+v %v", d, err)
	}
	for _, tc := range []struct{ key, text string }{{"description", strings.Repeat("x", 201)}, {"tags", strings.Repeat("例", 33)}, {"secret_refs", "BAD-NAME=example"}, {"secret_refs", "KATA_ROUTE=example"}, {"secret_refs", "TOKEN=missing"}, {"secret_refs", "TOKEN=example,TOKEN=example"}, {"schedule", "bad cron"}, {"timeout", "2h"}, {"input", "bad\x00"}, {"prompt", "{{unknown}}"}} {
		if err := set(tc.key, tc.text); err == nil {
			t.Fatalf("accepted %s %q", tc.key, tc.text)
		}
	}
	if err := set("secret_refs", "TOKEN=example"); err != nil {
		t.Fatal(err)
	}
	if err := set("schedule", "0 * * * *"); err != nil {
		t.Fatal(err)
	}
	if err := set("trigger", "interval"); err != nil {
		t.Fatal(err)
	}
	if err := set("schedule", "1h"); err != nil {
		t.Fatal(err)
	}
	if err := set("trigger", "cron"); err != nil || d.Values["schedule"] != "0 * * * *" {
		t.Fatalf("lost cron: %v %+v", err, d.Values)
	}
	d.Dirty = map[string]bool{}
	if err := set("schedule", "0 * * * *"); err != nil || d.Dirty["schedule"] {
		t.Fatalf("unchanged marked dirty: %+v %v", d, err)
	}
}

func TestProfileModelChoicePresentation(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		args       []string
		want       string
	}{
		{"codex", "codex", []string{"--model", "example-model", "-c", `model_reasoning_effort="high"`}, "worker · example-model high"},
		{"codex config", "codex", []string{"--config", `model="example-model"`, "--config=model_reasoning_effort='medium'"}, "worker · example-model medium"},
		{"claude", "claude", []string{"--model=example-model", "--effort", "low"}, "worker · example-model low"},
		{"claude equals", "claude", []string{"--model", "example-model", "--effort=high"}, "worker · example-model high"},
		{"model only", "codex", []string{"-m", "example-model"}, "worker · example-model"},
		{"defaults", "codex", nil, "worker · agent default"},
		{"effort only", "codex", []string{"-c", "model_reasoning_effort=high"}, "worker · agent default high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Context{Config: config.Config{Profiles: map[string]config.Profile{"worker": {Kind: tc.kind, Args: tc.args}}}}
			d := Draft{Values: map[string]string{"profile": "worker"}}
			display, edit := Get(Field{Key: "profile", Kind: Choice}, d, c)
			if display != tc.want || edit != "worker" {
				t.Fatalf("display %q, edit %q; want %q, worker", display, edit, tc.want)
			}
		})
	}
}

func TestWorkflowHelpUsesName(t *testing.T) {
	const uid = "01HZNQ7VFPK1XGD8R5MABCD4EX"
	c := Context{Workflows: []kata.Workflow{{UID: uid, Name: "Example workflow", Definition: []byte(`{"version":1,"steps":[{"key":"check","kind":"command","command":"true"}]}`)}}}
	d := Draft{Values: map[string]string{"mode": "workflow", "workflow": uid}}
	for _, f := range Fields(d, c) {
		if f.Key == "workflow" {
			if strings.Contains(f.Help, uid) || !strings.Contains(f.Help, "Example workflow") {
				t.Fatal(f.Help)
			}
		}
	}
}

func TestDraftIssueReferenceDisplayUsesProject(t *testing.T) {
	for _, input := range []string{"abcd5", "#abcd5", "example-project#abcd5"} {
		d := Draft{Values: map[string]string{"issue": input}}
		display, edit := Get(Field{Key: "issue"}, d, Context{Project: "example-project", IssueReference: func(string) string { return "" }})
		if display != "example-project#abcd5" || edit != input {
			t.Fatal(input, display, edit)
		}
	}
}

// An explicitly empty workflow UID still describes a prompt job.
func TestDraftEmptyWorkflowUIDUsesPromptFields(t *testing.T) {
	d := NewDraft(map[string]any{"action": map[string]any{"kind": "execute", "workflow_uid": "", "prompt": "work"}}, "Example")
	display, _ := Get(Field{Key: "mode"}, d, Context{})
	if display != "prompt" {
		t.Fatalf("action mode = %q, want prompt", display)
	}
	found := false
	for _, f := range Fields(d, Context{}) {
		found = found || f.Key == "prompt"
	}
	if !found {
		t.Fatal("empty workflow UID hid the prompt field")
	}
}

func TestNewDraftHydratesEditValues(t *testing.T) {
	for _, mode := range []struct{ action, issue, want string }{
		{`{"kind":"execute","prompt":"work"}`, "per-run", "prompt"},
		{`{"kind":"execute","prompt":"work","workflow_uid":""}`, "per-run", "prompt"},
		{`{"kind":"execute","workflow_uid":"flow"}`, "per-run", "workflow"},
		{`{"kind":"notify","recipient":"worker"}`, "existing", "notify"},
		{`{"kind":"notify","recipient":"worker/inbox"}`, "per-run", "recurring"},
	} {
		for _, trigger := range []struct{ raw, want string }{
			{`{"kind":"manual"}`, ""},
			{`{"kind":"cron","cron":"0 * * * *"}`, "0 * * * *"},
			{`{"kind":"interval","interval_seconds":3600}`, "1h0m0s"},
			{`{"kind":"once","at":"2030-01-01T00:00:00Z"}`, "2030-01-01T00:00:00Z"},
		} {
			t.Run(mode.want+mode.action+trigger.raw, func(t *testing.T) {
				var raw map[string]any
				body := `{"enabled":false,"action":` + mode.action + `,"trigger":` + trigger.raw + `,"issue":{"kind":"` + mode.issue + `","deadline_offset_seconds":-60},"options":{"tags":["daily","example"],"input":"input","run_parent":"parent","description":"description","opaque":9007199254740993},"secret_refs":{"Z_TOKEN":"last","A_TOKEN":"first"}}`
				decoder := json.NewDecoder(strings.NewReader(body))
				decoder.UseNumber()
				if err := decoder.Decode(&raw); err != nil {
					t.Fatal(err)
				}
				d := NewDraft(raw, "Example")
				for key, want := range map[string]string{"name": "Example", "mode": mode.want, "schedule": trigger.want, "enabled": "false", "deadline": "-1m0s", "tags": "daily, example", "secret_refs": "A_TOKEN=first, Z_TOKEN=last", "input": "input", "description": "description", "run_parent": "parent"} {
					if got := d.Values[key]; got != want {
						t.Fatalf("%s = %q, want %q", key, got, want)
					}
				}
				if len(d.Dirty) != 0 {
					t.Fatal("hydration dirtied fields", d.Dirty)
				}
				if raw["options"].(map[string]any)["opaque"] != json.Number("9007199254740993") {
					t.Fatal("opaque option changed", raw)
				}
			})
		}
	}
}

// The profile help caps a workflow job by its stricter step profile.
func TestProfileHelpUsesStepProfileCap(t *testing.T) {
	cap := 2
	c := Context{Config: config.Config{Profiles: map[string]config.Profile{"default": {Kind: "codex"}, "checker": {Kind: "claude", MaxRunPrompts: &cap}}}, Workflows: []kata.Workflow{{UID: "flow", Definition: []byte(`{"version":1,"steps":[{"key":"make","kind":"prompt","prompt":"Make"},{"key":"check","kind":"prompt","prompt":"Check","after":["make"],"options":{"agent":"checker"}}]}`)}}}
	for mode, want := range map[string]string{"workflow": "2 planned / 2 cap", "prompt": "1 planned / 12 cap"} {
		d := Draft{Values: map[string]string{"mode": mode, "workflow": "flow", "trigger": "manual", "issue_kind": "per-run", "profile": "default"}}
		found := false
		for _, f := range Fields(d, c) {
			if f.Key == "profile" {
				found = true
				if !strings.Contains(f.Help, want) {
					t.Fatal(mode, f.Help)
				}
			}
		}
		if !found {
			t.Fatal("no profile field", mode)
		}
	}
}
