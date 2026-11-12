package defs

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"hegel.dev/go/hegel"
)

func fixture() (kata.CronJob, map[string]kata.CronWorkflow, config.Config) {
	job := kata.CronJob{UID: "01J00000000000000000000010", DefinitionEventUID: "event-job", Name: "example-job", Definition: kata.JobDefinition{Version: 1, Kind: "job", Enabled: true, Trigger: kata.Trigger{Kind: "manual"}, Action: kata.Action{Kind: "execute", Prompt: new("Example {{input}}")}, Issue: &kata.IssuePolicy{Kind: "per-run", Title: new("Example issue")}, Overlap: "forbid", Catchup: "latest", CheckoutKey: new("example-project"), Options: map[string]any{"agent": "default", "input": "context"}}}
	cfg := config.Config{Projects: []config.Project{{Name: "example-project", Commands: true}}, Checkouts: map[string]string{"example-project": "/srv/projects/example"}, Profiles: map[string]config.Profile{"default": {Argv: []string{"agent", "--no-daemon"}}}, Limits: config.Limits{DefaultTimeout: 2 * time.Hour, MaxTimeout: 24 * time.Hour}}
	return job, map[string]kata.CronWorkflow{}, cfg
}
func look(s string) (string, error) {
	if s == "agent" {
		return "/usr/local/bin/agent", nil
	}
	return "", errors.New("binary unavailable")
}

func TestUnsupportedReasons(t *testing.T) {
	for _, tt := range []struct {
		name, reason string
		change       func(*kata.CronJob, map[string]kata.CronWorkflow)
	}{
		{"notify", "notify_unsupported", func(j *kata.CronJob, _ map[string]kata.CronWorkflow) {
			j.Definition.Action.Kind = "notify"
			j.Definition.Issue = &kata.IssuePolicy{Kind: "existing", UID: new("01J00000000000000000000020")}
		}},
		{"loops", "loops_unsupported", func(j *kata.CronJob, _ map[string]kata.CronWorkflow) {
			j.Definition.Options["on_fail"] = map[string]any{"to": "main"}
		}},
		{"findings", "loops_unsupported", func(j *kata.CronJob, _ map[string]kata.CronWorkflow) { j.Definition.Options["findings"] = true }},
		{"park", "park_unsupported", func(j *kata.CronJob, _ map[string]kata.CronWorkflow) { j.Definition.Options["on_failure"] = "park" }},
		{"secrets", "secrets_unsupported", func(j *kata.CronJob, _ map[string]kata.CronWorkflow) {
			j.Definition.SecretRefs = map[string]string{"TOKEN": "secret"}
		}},
		{"mode", "agent_mode_unsupported", func(j *kata.CronJob, _ map[string]kata.CronWorkflow) { j.Definition.Options["agent_mode"] = "resume" }},
		{"scheduled offset", "scheduled_offset_unsupported", func(j *kata.CronJob, _ map[string]kata.CronWorkflow) {
			j.Definition.Issue.ScheduledOffsetSeconds = new(int64(0))
		}},
		{"run parent short ref", "run_parent must be an issue UID", func(j *kata.CronJob, _ map[string]kata.CronWorkflow) {
			j.Definition.Options["run_parent"] = "qspb"
		}},
		{"run parent not a string", "run_parent must be an issue UID", func(j *kata.CronJob, _ map[string]kata.CronWorkflow) {
			j.Definition.Options["run_parent"] = 7
		}},
		{"run parent existing issue", "run_parent requires a per-run issue", func(j *kata.CronJob, _ map[string]kata.CronWorkflow) {
			j.Definition.Options["run_parent"] = "01J00000000000000000000020"
			j.Definition.Issue = &kata.IssuePolicy{Kind: "existing", UID: new("01J00000000000000000000030")}
		}},
		{"run parent on step", "run_parent belongs on the job", func(j *kata.CronJob, w map[string]kata.CronWorkflow) {
			w["01J00000000000000000000040"] = kata.CronWorkflow{UID: "01J00000000000000000000040", Definition: kata.WorkflowDefinition{Steps: []kata.WorkflowStep{{Key: "a", Kind: "prompt", Prompt: new("x"), Options: map[string]any{"run_parent": "01J00000000000000000000020"}}}}}
			j.Definition.Action = kata.Action{Kind: "execute", WorkflowUID: new("01J00000000000000000000040")}
		}},
		{"per-run notify", "per-run notify belongs to the hub", func(j *kata.CronJob, _ map[string]kata.CronWorkflow) { j.Definition.Action.Kind = "notify" }},
		{"adapter", "adapter: other", func(j *kata.CronJob, _ map[string]kata.CronWorkflow) { j.Definition.Options["adapter"] = "other" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			j, w, c := fixture()
			tt.change(&j, w)
			_, reasons := Freeze(j, w, c, "example-project", look)
			if !slices.Contains(reasons, tt.reason) {
				t.Fatalf("reasons: %v, want %s", reasons, tt.reason)
			}
		})
	}
}

func TestFreezeAbsoluteArgv(t *testing.T) {
	j, w, c := fixture()
	p, reasons := Freeze(j, w, c, "example-project", look)
	if len(reasons) != 0 {
		t.Fatal(reasons)
	}
	if p.Steps[0].Argv[0] != "/usr/local/bin/agent" || p.CheckoutPath != "/srv/projects/example" || p.Input != "context" || p.JobEventUID != "event-job" {
		t.Fatalf("plan: %+v", p)
	}
	c.Profiles["default"].Argv[0] = "changed"
	*j.Definition.Issue.Title = "changed"
	if p.Steps[0].Argv[0] != "/usr/local/bin/agent" || p.Issue.Title != "Example issue" {
		t.Fatal("frozen plan aliases input")
	}
}

func TestTimeoutCappedByMax(t *testing.T) {
	j, w, c := fixture()
	j.Definition.TimeoutSeconds = new(int64(1 << 62))
	p, reasons := Freeze(j, w, c, "example-project", look)
	if len(reasons) != 0 || p.Timeout != 24*time.Hour {
		t.Fatalf("timeout %s reasons %v", p.Timeout, reasons)
	}
	j.Definition.TimeoutSeconds = nil
	p, _ = Freeze(j, w, c, "example-project", look)
	if p.Timeout != 2*time.Hour {
		t.Fatal(p.Timeout)
	}
}

func TestInvalidTriggerReportedUnsupported(t *testing.T) {
	for _, trig := range []kata.Trigger{{Kind: "cron", Cron: new("not a schedule")}, {Kind: "cron", Cron: new("CRON_TZ=America/New_York 0 9 * * *"), Timezone: new("UTC")}, {Kind: "cron", Cron: new("0 9 * * *"), Timezone: new("Invalid/Zone")}} {
		j, w, c := fixture()
		j.Definition.Trigger = trig
		_, reasons := Freeze(j, w, c, "example-project", look)
		if !slices.Contains(reasons, "invalid_trigger") {
			t.Fatalf("invalid trigger disappeared: %v", reasons)
		}
	}
}

func TestWorkflowFrozen(t *testing.T) {
	j, w, c := fixture()
	j.Definition.Action.Prompt = nil
	j.Definition.Action.WorkflowUID = new("01J00000000000000000000011")
	w["01J00000000000000000000011"] = kata.CronWorkflow{UID: "01J00000000000000000000011", DefinitionEventUID: "event-workflow", Name: "example-workflow", Definition: kata.WorkflowDefinition{Version: 1, About: new("About {{input}}"), Input: new("Input description"), Steps: []kata.WorkflowStep{{Key: "gate", Kind: "command", Command: new("exit 7"), Options: map[string]any{"skip_exit": json.Number("7")}}, {Key: "work", Kind: "prompt", Prompt: new("Task"), After: []string{"gate"}, Retries: new(int64(2))}}}}
	p, reasons := Freeze(j, w, c, "example-project", look)
	if len(reasons) != 0 || p.WorkflowEventUID != "event-workflow" || p.WorkflowName != "example-workflow" || p.About != "About {{input}}" || len(p.Steps) != 2 || p.Steps[0].SkipExit != 7 || p.Steps[1].Retries != 2 || p.Steps[1].After[0] != "gate" {
		t.Fatalf("plan %+v reasons %v", p, reasons)
	}
	for _, opt := range []struct {
		key   string
		value any
		want  string
	}{{"on_fail", true, "loops_unsupported"}, {"agent_mode", "resume", "agent_mode_unsupported"}, {"on_failure", "park", "park_unsupported"}} {
		wf := w["01J00000000000000000000011"]
		wf.Definition.Steps[1].Options = map[string]any{opt.key: opt.value}
		w[wf.UID] = wf
		_, got := Freeze(j, w, c, "example-project", look)
		if !slices.Contains(got, opt.want) {
			t.Fatalf("workflow option %s: %v", opt.key, got)
		}
	}
}

func TestFrozenTimeoutBound(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		n := hegel.Draw(ht, hegel.Integers[int64](1, 1<<63-1))
		j, w, c := fixture()
		j.Definition.TimeoutSeconds = &n
		p, reasons := Freeze(j, w, c, "example-project", look)
		if len(reasons) != 0 {
			ht.Fatal(reasons)
		}
		want := 24 * time.Hour
		if n < int64(want/time.Second) {
			want = time.Duration(n) * time.Second
		}
		if p.Timeout != want {
			ht.Fatalf("seconds %d: timeout %s want %s", n, p.Timeout, want)
		}
	})
}

func TestRunParentFrozenOnPerRunIssue(t *testing.T) {
	j, w, c := fixture()
	j.Definition.Options["run_parent"] = "01J00000000000000000000020"
	p, reasons := Freeze(j, w, c, "example-project", look)
	if len(reasons) != 0 {
		t.Fatal(reasons)
	}
	if p.Issue.Parent != "01J00000000000000000000020" {
		t.Fatalf("parent %q", p.Issue.Parent)
	}
}

// A wrong precedence branch must change the actual frozen checkout or agent argv.
func TestProjectDefaultResolution(t *testing.T) {
	for _, tt := range []struct {
		name                          string
		checkout, jobAgent, stepAgent *string
		defaults                      bool
		wantCheckout, wantArg         string
		reasons                       []string
	}{
		{"project defaults", nil, nil, nil, true, "/srv/projects/default", "project", nil},
		{"explicit checkout", new("example-project"), nil, nil, true, "/srv/projects/example", "project", nil},
		{"unknown checkout", new("missing"), nil, nil, true, "", "project", []string{"checkout not configured: missing"}},
		{"empty supplied checkout", new(""), nil, nil, true, "", "project", []string{"checkout not configured: "}},
		{"job overrides project", nil, new("job"), nil, true, "/srv/projects/default", "job", nil},
		{"step overrides job", nil, new("job"), new("step"), true, "/srv/projects/default", "step", nil},
		{"explicit default job", nil, new("default"), nil, true, "/srv/projects/default", "--no-daemon", nil},
		{"explicit default step", nil, new("job"), new("default"), true, "/srv/projects/default", "--no-daemon", nil},
		{"empty agents absent", nil, new(""), new(""), true, "/srv/projects/default", "project", nil},
		{"missing job profile", nil, new("missing"), nil, true, "/srv/projects/default", "", []string{"profile not configured: missing"}},
		{"missing step profile", nil, new("job"), new("missing"), true, "/srv/projects/default", "", []string{"profile not configured: missing"}},
		{"valid step overrides missing job", nil, new("missing"), new("step"), true, "/srv/projects/default", "step", nil},
		{"legacy", new("example-project"), nil, nil, false, "/srv/projects/example", "--no-daemon", nil},
		{"missing checkout", nil, nil, nil, false, "", "--no-daemon", []string{"checkout not configured: set checkout_key or the project's default_checkout"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			j, w, c := fixture()
			c.Checkouts["project"] = "/srv/projects/default"
			for _, profile := range []string{"project", "job", "step"} {
				c.Profiles[profile] = config.Profile{Argv: []string{"agent", profile}}
			}
			if tt.defaults {
				c.Projects[0].DefaultCheckout = "project"
				c.Projects[0].DefaultAgent = "project"
			}
			j.Definition.CheckoutKey = tt.checkout
			delete(j.Definition.Options, "agent")
			if tt.jobAgent != nil {
				j.Definition.Options["agent"] = *tt.jobAgent
			}
			s := kata.WorkflowStep{Key: "work", Kind: "prompt", Prompt: new("work"), Options: map[string]any{}}
			if tt.stepAgent != nil {
				s.Options["agent"] = *tt.stepAgent
			}
			w["wf"] = kata.CronWorkflow{UID: "wf", Definition: kata.WorkflowDefinition{Steps: []kata.WorkflowStep{s}}}
			j.Definition.Action = kata.Action{Kind: "execute", WorkflowUID: new("wf")}
			p, reasons := Freeze(j, w, c, "example-project", look)
			if !slices.Equal(reasons, tt.reasons) || p.CheckoutPath != tt.wantCheckout {
				t.Fatalf("plan %+v reasons %v; want checkout %q reasons %v", p, reasons, tt.wantCheckout, tt.reasons)
			}
			argv := p.Steps[0].Argv
			if tt.wantArg == "" {
				if len(argv) != 0 {
					t.Fatalf("unexpected argv %v", argv)
				}
			} else if !slices.Equal(argv, []string{"/usr/local/bin/agent", tt.wantArg}) {
				t.Fatalf("argv %v", argv)
			}
			// Config edits after preparation must not change frozen execution values.
			c.Checkouts["project"] = "/srv/projects/changed"
			c.Profiles["project"].Argv[1] = "changed"
			if p.CheckoutPath != tt.wantCheckout || (tt.wantArg != "" && p.Steps[0].Argv[1] != tt.wantArg) {
				t.Fatal("plan aliases config")
			}
		})
	}
}

func TestNonStringAgentUnsupported(t *testing.T) {
	for _, scope := range []string{"job", "step"} {
		for _, value := range []any{nil, 7, true, []any{"default"}} {
			t.Run(scope+"/"+fmt.Sprint(value), func(t *testing.T) {
				j, w, c := fixture()
				if scope == "job" {
					j.Definition.Options["agent"] = value
				} else {
					j.Definition.Action = kata.Action{Kind: "execute", WorkflowUID: new("wf")}
					w["wf"] = kata.CronWorkflow{UID: "wf", Definition: kata.WorkflowDefinition{Steps: []kata.WorkflowStep{{Key: "a", Kind: "prompt", Prompt: new("x"), Options: map[string]any{"agent": value}}}}}
				}
				_, reasons := Freeze(j, w, c, "example-project", look)
				if !slices.Equal(reasons, []string{"agent must be a string"}) {
					t.Fatalf("reasons %v", reasons)
				}
			})
		}
	}
}

func TestOnceBeforeCreationUnsupported(t *testing.T) {
	for _, tt := range []struct {
		at   string
		want []string
	}{
		{"2026-01-01T00:00:00Z", []string{"once_in_past"}},
		{"2025-12-31T23:59:59Z", []string{"once_in_past"}},
		{"2026-01-01T00:00:01Z", nil},
		{"invalid", []string{"invalid_trigger"}},
	} {
		j, w, c := fixture()
		j.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		j.Definition.Trigger = kata.Trigger{Kind: "once", At: &tt.at}
		_, reasons := Freeze(j, w, c, "example-project", look)
		if !slices.Equal(reasons, tt.want) {
			t.Fatalf("at %s: %v", tt.at, reasons)
		}
	}
}

// A mapping with an empty name must not become an implicit project default.
func TestCheckoutRequiresSelection(t *testing.T) {
	j, w, c := fixture()
	c.Checkouts[""] = "/srv/projects/example"
	j.Definition.CheckoutKey = nil
	p, reasons := Freeze(j, w, c, "example-project", look)
	if p.CheckoutPath != "" || !slices.Equal(reasons, []string{"checkout not configured: set checkout_key or the project's default_checkout"}) {
		t.Fatalf("unselected checkout resolved: %q, reasons %v", p.CheckoutPath, reasons)
	}
	// Preserve explicit selection of a configured key; no new key restriction.
	j.Definition.CheckoutKey = new("")
	p, reasons = Freeze(j, w, c, "example-project", look)
	if p.CheckoutPath != "/srv/projects/example" || len(reasons) != 0 {
		t.Fatalf("explicit checkout: %q, reasons %v", p.CheckoutPath, reasons)
	}
}

func TestPersistentAgentFreeze(t *testing.T) {
	j, w, c := fixture()
	delete(j.Definition.Options, "agent")
	j.Definition.Options["persistent_agent"] = "worker"
	j.Definition.Issue = &kata.IssuePolicy{Kind: "existing", UID: new("01J00000000000000000000020")}
	p, reasons := Freeze(j, w, c, "example-project", func(string) (string, error) { t.Fatal("resolved a profile"); return "", nil })
	if len(reasons) != 0 || p.Agent != "worker" || len(p.Steps[0].Argv) != 0 {
		t.Fatalf("plan: %+v reasons: %v", p, reasons)
	}
}

func TestPersistentAgentRejections(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		change       func(*kata.CronJob)
	}{
		{"invalid", "persistent_agent must be a name of 1–64 letters, digits, dots, underscores or hyphens", func(j *kata.CronJob) { j.Definition.Options["persistent_agent"] = "bad name" }},
		{"workflow", "persistent_agent requires a prompt action", func(j *kata.CronJob) {
			j.Definition.Action.Prompt = nil
			j.Definition.Action.WorkflowUID = new("wf")
		}},
		{"overlap", "persistent_agent requires overlap: forbid", func(j *kata.CronJob) { j.Definition.Overlap = "allow" }},
		{"catchup", "persistent_agent does not support catchup: all", func(j *kata.CronJob) { j.Definition.Catchup = "all" }},
		{"issue", "persistent_agent requires an existing issue", func(j *kata.CronJob) { j.Definition.Issue.Kind = "per-run" }},
		{"agent", "persistent_agent cannot be combined with agent", func(j *kata.CronJob) { j.Definition.Options["agent"] = "default" }},
		{"model", "persistent_agent cannot be combined with model", func(j *kata.CronJob) { j.Definition.Options["model"] = "example" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, w, c := fixture()
			delete(j.Definition.Options, "agent")
			j.Definition.Options["persistent_agent"] = "worker"
			j.Definition.Issue = &kata.IssuePolicy{Kind: "existing"}
			tc.change(&j)
			_, reasons := Freeze(j, w, c, "example-project", look)
			if !slices.Contains(reasons, tc.reason) {
				t.Fatalf("reasons: %v", reasons)
			}
		})
	}
}
