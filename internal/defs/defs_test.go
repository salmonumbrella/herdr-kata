package defs

import (
	"encoding/json"
	"errors"
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
		{"run parent", "run_parent_unsupported", func(j *kata.CronJob, _ map[string]kata.CronWorkflow) {
			j.Definition.Options["run_parent"] = "01J00000000000000000000020"
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
