package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

const workflowF1 = `{"version":1,"about":"Inspect an example","input":"target","options":{"on_failure":"park"},"steps":[{"key":"inspect","kind":"prompt","prompt":"Inspect {{input}}"},{"key":"check","kind":"command","command":"true","after":["inspect"],"options":{"on_fail":{"goto":"inspect","max_loops":2}}},{"key":"summarize","kind":"prompt","prompt":"Summarize {{previous}}","after":["check"],"options":{"agent_mode":"fresh"}}]}`

func workflowTestConfig() config.Config {
	return config.Config{Kata: config.Kata{Project: "example-project"}, Profiles: map[string]config.Profile{"default": {}, "other": {}, "a": {}, "b": {}}}
}

func TestWorkflowProblemsBudget(t *testing.T) {
	got := workflowProblems(json.RawMessage(workflowF1), workflowTestConfig())
	if got.DecodeErr != nil || len(got.Unsupported) != 0 || got.Budget == nil || *got.Budget != (workflowBudget{Total: 4, Prompt: 2, Loop: 2}) {
		t.Fatalf("F1: %+v", got)
	}
	if !slices.Equal(got.Order, []string{"inspect", "check", "summarize"}) || got.Loop == nil || got.Loop.Goto != "inspect" || got.Loop.MaxLoops != 2 {
		t.Fatalf("execution order/loop: %+v", got)
	}
}

func TestWorkflowProblemsProfiles(t *testing.T) {
	for _, steps := range []string{
		`[{"key":"first","kind":"prompt","prompt":"One"},{"key":"second","kind":"prompt","prompt":"Two","after":["first"],"options":{"agent":"other"}}]`,
		`[{"key":"second","kind":"prompt","prompt":"Two","after":["first"],"options":{"agent":"other"}},{"key":"first","kind":"prompt","prompt":"One"}]`,
	} {
		got := workflowProblems(json.RawMessage(`{"version":1,"steps":`+steps+`}`), workflowTestConfig())
		if got.DecodeErr != nil || len(got.Unsupported) != 0 || len(got.DeclaredProfiles) != 0 {
			t.Fatalf("step-only profile: %+v", got)
		}
	}
	got := workflowProblems(json.RawMessage(`{"version":1,"options":{"agent":"b"},"steps":[{"key":"main","kind":"prompt","prompt":"Work","options":{"agent":"a"}}]}`), workflowTestConfig())
	if !slices.Equal(got.DeclaredProfiles, []string{"b"}) || len(got.Unsupported) != 0 {
		t.Fatalf("step profile beside workflow profile: %+v", got)
	}
	got = workflowProblems(json.RawMessage(`{"version":1,"steps":[{"key":"main","kind":"prompt","prompt":"Work","options":{"agent":"missing"}}]}`), workflowTestConfig())
	if !slices.Equal(got.Unsupported, []string{"unmapped profile: steps.main.options.agent missing; configure profiles.missing and include it in the project profiles allowlist"}) {
		t.Fatalf("unmapped step profile: %+v", got)
	}
	for _, declared := range []bool{false, true} {
		cfg := workflowTestConfig()
		cfg.Profiles = nil
		cfg.Limits.MaxRunPrompts = 3
		raw := workflowF1
		if declared {
			raw = strings.Replace(raw, `"on_failure":"park"`, `"on_failure":"park","agent":"other"`, 1)
		}
		got := workflowProblems(json.RawMessage(raw), cfg)
		want := []string{"unmapped profile: options.agent default; configure profiles.default and include it in the project profiles allowlist", "prompt_budget 4 > 3"}
		if declared {
			want = []string{"unmapped profile: options.agent other; configure profiles.other and include it in the project profiles allowlist", "prompt_budget 4 > 3"}
			if !slices.Equal(got.Unsupported, want) || len(got.JobDependent) != 0 {
				t.Fatalf("declared: %+v", got)
			}
		} else if !slices.Equal(got.JobDependent, want) || len(got.Unsupported) != 0 {
			t.Fatalf("job-dependent: %+v", got)
		}
	}
}

func TestWorkflowProblemsSemantic(t *testing.T) {
	for _, tt := range []struct{ name, raw, reason string }{
		{"failure", `{"on_failure":"retry"}`, "options.on_failure supports only park workflows"},
		{"input", `{"input":"x"}`, "options.input must be a job string"},
		{"parent", `{"run_parent":"x"}`, "options.run_parent must be a full issue UID on a job"},
		{"on_fail", `{"on_fail":{"goto":"main"}}`, "options.on_fail unsupported"},
		{"agent-number", `{"agent":3}`, "options.agent must name a profile"},
		{"agent-empty", `{"agent":""}`, "options.agent must name a profile"},
		{"options-array", `[]`, "options must be an object"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"version":1,"options":` + tt.raw + `,"steps":[{"key":"main","kind":"prompt","prompt":"Work"}]}`
			got := workflowProblems(json.RawMessage(raw), workflowTestConfig())
			if got.DecodeErr != nil || !slices.Equal(got.Unsupported, []string{tt.reason}) {
				t.Fatalf("want only %q, got %+v", tt.reason, got)
			}
		})
	}
	for _, tt := range []struct{ name, raw, reason string }{
		{"shared", `{"version":1,"steps":[{"key":"main","kind":"prompt","prompt":"Work","options":{"agent_mode":"shared"}}]}`, "options.agent_mode supports only fresh prompt steps"},
		{"command-mode", `{"version":1,"steps":[{"key":"main","kind":"command","command":"true","options":{"agent_mode":"fresh"}}]}`, "options.agent_mode supports only fresh prompt steps"},
		{"about", `{"version":1,"about":"{{previous}}","steps":[{"key":"main","kind":"prompt","prompt":"Work"}]}`, "{{previous}} unsupported in first step"},
		{"topological-first", `{"version":1,"steps":[{"key":"second","kind":"prompt","prompt":"Work","after":["first"]},{"key":"first","kind":"prompt","prompt":"{{previous}}"}]}`, "{{previous}} unsupported in first step"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := workflowProblems(json.RawMessage(tt.raw), workflowTestConfig())
			if got.DecodeErr != nil || !slices.Equal(got.Unsupported, []string{tt.reason}) {
				t.Fatalf("want only %q, got %+v", tt.reason, got)
			}
		})
	}
	allowed := false
	cfg := workflowTestConfig()
	cfg.Commands = &allowed
	got := workflowProblems(json.RawMessage(workflowF1), cfg)
	if !slices.Equal(got.Unsupported, []string{`commands are disabled for project "example-project" (commands=false)`}) {
		t.Fatalf("command policy: %+v", got)
	}
	got = workflowProblems(json.RawMessage(strings.Replace(workflowF1, `"on_failure":"park"`, `"on_failure":"park","future":true`, 1)), workflowTestConfig())
	if len(got.Unsupported) != 0 || !slices.Equal(got.Warnings, []string{"ignored options key: future"}) {
		t.Fatalf("warning-only: %+v", got)
	}
}

func TestWorkflowProblemsUntrustedBudget(t *testing.T) {
	for _, raw := range []string{
		strings.Replace(workflowF1, `"key":"summarize"`, `"retries":101,"key":"summarize"`, 1),
		`{"version":1,"steps":[{"key":"a","kind":"prompt","prompt":"Work","after":["b"]},{"key":"b","kind":"prompt","prompt":"Work","after":["a"]}]}`,
		`{"version":1,"steps":[{"key":"a","kind":"prompt","prompt":"Work"},{"key":"a","kind":"prompt","prompt":"Work"}]}`,
		`{"version":1,"steps":[{"key":"a","kind":"prompt","prompt":"Work","retries":-1}]}`,
		`{"version":1,"steps":[{"key":"a","kind":"prompt","prompt":"Work","retries":101}]}`,
		`{"version":1,"steps":[{"key":"a","kind":"prompt","prompt":"Work","options":{"on_fail":{"goto":"a","max_loops":0}}}]}`,
	} {
		d := workflowSnapshot()
		d.Workflows[0].Definition = json.RawMessage(raw)
		var checks []workflowCheck
		for _, available := range []bool{true, false} {
			index := buildWorkflowIndex(d, jobIndex{}, func(string, string) (Lane, bool) {
				return Lane{Config: workflowTestConfig()}, available
			}, runNow)
			checks = append(checks, index[workflowKey{"project-a", "flow"}].check)
		}
		a, b := checks[0], checks[1]
		if !reflect.DeepEqual(a.Definition, b.Definition) || !reflect.DeepEqual(a.Order, b.Order) || !reflect.DeepEqual(a.Loop, b.Loop) || !reflect.DeepEqual(a.Budget, b.Budget) {
			t.Fatalf("lane availability changed execution facts: available=%+v unavailable=%+v", a, b)
		}
		got := workflowProblems(json.RawMessage(raw), workflowTestConfig())
		if got.DecodeErr != nil || len(got.Unsupported) == 0 || got.Budget != nil || got.Loop != nil {
			t.Fatalf("untrusted execution: %+v", got)
		}
	}
	for _, raw := range []string{`{`, `[]`, `{"input":null}`, `{"steps":"x"}`} {
		got := workflowProblems(json.RawMessage(raw), workflowTestConfig())
		if got.DecodeErr == nil || got.Definition != nil || got.Budget != nil || len(got.Unsupported)+len(got.JobDependent) != 0 {
			t.Fatalf("decode failure must not classify a zero definition: %+v", got)
		}
	}
}

func workflowSnapshot() Data {
	return Data{Health: map[string]any{"version": float64(1), "project_uid": "project-a"}, Workflows: []kata.Workflow{{UID: "flow", Name: "Example", Definition: json.RawMessage(workflowF1)}}, Jobs: []defs.JobEntry{{UID: "job", Name: "Uses example", Owner: defs.Adapter, Supported: true, Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":true,"action":{"kind":"execute","workflow_uid":"flow"},"trigger":{"kind":"manual"},"options":{"agent":"other","input":"example input"}}`)}}, Runs: []runner.Run{{UID: "run-b", Workflow: &kata.Workflow{UID: "flow", DefinitionEventUID: "old"}, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-a"}}, Phase: runner.Held}, {UID: "run-c", Workflow: &kata.Workflow{UID: "flow"}, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-a"}}, Phase: runner.Running}, {UID: "run-a", Workflow: nil, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-a"}}}}}
}

func workflowF1GoldenSnapshot() Data {
	d := workflowSnapshot()
	w := &d.Workflows[0]
	w.Name, w.DefinitionEventUID = "inspect-and-check", "definition-current"
	w.UpdatedAt = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	d.Jobs = []defs.JobEntry{
		{UID: "nightly", Name: "nightly-inspect", Owner: defs.Adapter, Supported: true, Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"interval","interval_seconds":86400},"overlap":"allow","catchup":"skip","issue":{"kind":"per-run","title":"Example run"},"action":{"kind":"execute","workflow_uid":"flow"},"options":{"agent":"default","input":"Review the current diff."}}`)},
		{UID: "manual", Name: "manual-inspect", Owner: defs.Ignored, Reasons: []string{"prompt_budget 4 > 3"}, Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":false,"trigger":{"kind":"manual"},"overlap":"allow","catchup":"skip","issue":{"kind":"per-run","title":"Example run"},"action":{"kind":"execute","workflow_uid":"flow"},"timeout_seconds":1800,"options":{"agent":"cheap"}}`)},
	}
	d.Runs = nil
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	for i, tc := range []struct {
		uid, event, phase, outcome, job, reason string
		age                                     time.Duration
		manual                                  bool
	}{
		{"run-3", "definition-current", string(runner.Settled), "succeeded", "nightly-inspect", "", 10 * time.Minute, false},
		{"run-2", "definition-old", string(runner.Held), "", "manual-inspect", "loop_exhausted", 3 * time.Hour, true},
		{"run-1", "definition-current", string(runner.Settled), "failed", "nightly-inspect", "", 24 * time.Hour, false},
	} {
		at := now.Add(-tc.age)
		r := runner.Run{UID: tc.uid, Phase: runner.Phase(tc.phase), Outcome: tc.outcome, Reason: tc.reason, StartedAt: &at, Manual: tc.manual, Job: kata.Job{UID: "nightly", Name: tc.job}, Workflow: &kata.Workflow{UID: "flow", DefinitionEventUID: tc.event}, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-a"}}}
		if tc.manual {
			r.Job.UID = "manual"
		}
		if i == 0 {
			r.Steps = []runner.StepState{{Note: "Diff whitespace check passed."}}
		}
		if i == 2 {
			r.Steps = []runner.StepState{{Note: "check: exit 1"}}
		}
		d.Runs = append(d.Runs, r)
	}
	return d
}
func workflowModel(t testing.TB, d Data) *model {
	t.Helper()
	m := newModel(Options{Root: t.TempDir(), Now: func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) }, Config: workflowTestConfig(), Service: &fakeActions{}})
	m.Update(dataMsg{data: d})
	m.selectTab(2)
	return m
}
func TestWorkflowIndex(t *testing.T) {
	d := workflowSnapshot()
	cfg := workflowTestConfig()
	cap := 4
	cfg.Profiles["other"] = config.Profile{MaxRunPrompts: &cap}
	cfg.Limits.DefaultTimeout = 3 * time.Hour
	m := newModel(Options{Root: t.TempDir(), Config: cfg, Service: &fakeActions{}})
	m.Update(dataMsg{data: d})
	v := m.wfIndex[workflowKey{"project-a", "flow"}]
	if v == nil || !v.laneAvailable || len(v.jobs) != 1 || len(v.runs) != 2 {
		t.Fatalf("missing legacy joins: %+v", v)
	}
	if d.Runs[v.runs[0]].UID != "run-c" || d.Runs[v.runs[1]].UID != "run-b" {
		t.Fatal("UID ordering", v.runs)
	}
	j := v.jobs[0]
	if j.profile != "other" || j.input != "example input" || j.cap != 4 || j.timeout != 3*time.Hour {
		t.Fatalf("job facts: %+v", j)
	}
	for _, health := range []map[string]any{nil, {"version": float64(2), "project_uid": "project-a"}, {"version": float64(1), "project_uid": 12}} {
		d.Health = health
		m.Update(dataMsg{data: d})
		v = m.wfIndex[workflowKey{"", "flow"}]
		if v == nil || len(v.runs) != 0 {
			t.Fatal("unverified pin joined a nonempty project", v)
		}
	}
	d = workflowSnapshot()
	d.Projects = map[string]Data{"project-a": {Project: "example-project"}, "project-b": {Project: "another-project"}}
	d.Workflows[0].ProjectUID = "project-a"
	d.Workflows[0].Project = "example-project"
	other := d.Workflows[0]
	other.ProjectUID = "project-b"
	other.Project = "another-project"
	d.Workflows = append(d.Workflows, other)
	d.Jobs[0].ProjectUID = "project-a"
	d.Jobs[0].Project = "example-project"
	otherJob := d.Jobs[0]
	otherJob.ProjectUID = "project-b"
	otherJob.Project = "another-project"
	d.Jobs = append(d.Jobs, otherJob)
	d.Runs = append(d.Runs, runner.Run{UID: "run-d", Workflow: &kata.Workflow{UID: "flow"}, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-b"}}})
	m = workflowModel(t, d)
	a, b := m.wfIndex[workflowKey{"project-a", "flow"}], m.wfIndex[workflowKey{"project-b", "flow"}]
	if len(a.jobs) != 1 || len(b.jobs) != 1 || len(a.runs) != 2 || len(b.runs) != 1 || d.Runs[b.runs[0]].UID != "run-d" {
		t.Fatal("cross-lane joins", a, b)
	}
}
func TestWorkflowIndexUnavailableLane(t *testing.T) {
	d := workflowSnapshot()
	d.Workflows[0].Definition = json.RawMessage(strings.Replace(workflowF1, `"on_failure":"park"`, `"on_failure":"park","agent":"other"`, 1))
	jobs := workflowModel(t, d).jobs
	index := buildWorkflowIndex(d, *jobs, func(string, string) (Lane, bool) { return Lane{}, false }, runNow)
	v := index[workflowKey{"project-a", "flow"}]
	if v.laneAvailable || v.check.Definition == nil || len(v.check.Unsupported) != 0 || len(v.check.JobDependent) != 0 || v.jobs[0].cap != 0 || v.jobs[0].timeout != 0 {
		t.Fatalf("unavailable lane invented facts: %+v", v)
	}
	// A configured single lane resolves its snapshot pin.
	m := workflowModel(t, d)
	if _, ok := m.workflowOptions("project-a", ""); !ok {
		t.Fatal("minimal single lane unavailable")
	}
	m.opts.Projects = map[string]*Lane{"example-project": {Project: "example-project", ProjectUID: "project-b", Config: workflowTestConfig(), Service: &fakeActions{}}}
	if _, ok := m.workflowOptions("project-a", "example-project"); ok {
		t.Fatal("mismatched lane accepted")
	}
	m.opts.ForProject = func(uid, name string) Lane {
		return Lane{ProjectUID: "project-b", Project: name, Config: workflowTestConfig()}
	}
	if _, ok := m.workflowOptions("project-a", "example-project"); ok {
		t.Fatal("mismatched resolver accepted")
	}
}
func TestWorkflowRows(t *testing.T) {
	d := workflowSnapshot()
	d.Workflows = append(d.Workflows, kata.Workflow{UID: "broken", Name: "Z broken", Definition: json.RawMessage(`{`)}, kata.Workflow{UID: "normal", Name: "alpha", Definition: json.RawMessage(strings.Replace(workflowF1, `"on_failure":"park"`, `"on_failure":"park","hidden-key":true`, 1))})
	m := workflowModel(t, d)
	if len(m.rows) != 3 || m.rows[0].uid != "broken" || m.rows[1].uid != "normal" {
		t.Fatal("broken-first/name sort", m.rows)
	}
	send(m, "j")
	selected := m.rows[m.cursor].identity()
	d.Workflows[2].Name = "zz normal"
	m.Update(dataMsg{data: d})
	if m.rows[m.cursor].identity() != selected {
		t.Fatal("selection retargeted after sorting")
	}
	for query, want := range map[string]int{"Inspect an example": 2, "inspect": 2, "workflow does not exist": 0, "true": 0, "unsupported": 0} {
		m.query = query
		m.rebuild()
		if len(m.rows) != want {
			t.Fatalf("query %q: %d rows, want %d", query, len(m.rows), want)
		}
	}
	// An ignored option is displayed as a warning and is therefore searchable;
	// arbitrary top-level JSON fields and prompt bodies are not search fields.
	d.Workflows[0].Definition = json.RawMessage(strings.Replace(workflowF1, `"version":1`, `"version":1,"private-option":"invisible-token"`, 1))
	m.Update(dataMsg{data: d})
	m.query = "invisible-token"
	m.rebuild()
	if len(m.rows) != 0 {
		t.Fatal("raw JSON entered search")
	}
}

func TestWorkflowListFilteredPager(t *testing.T) {
	d := workflowSnapshot()
	d.Workflows = nil
	for i, name := range []string{"Match first", "Match second", "Other third", "Other fourth", "Other fifth"} {
		d.Workflows = append(d.Workflows, kata.Workflow{UID: fmt.Sprint(i), Name: name, Definition: json.RawMessage(workflowF1)})
	}
	m := workflowModel(t, d)
	send(m, "/")
	send(m, "Match")
	send(m, "enter")
	if text := ansi.Strip(m.viewContent()); !strings.Contains(text, "page 1/1 · 2 of 5 match") || strings.Contains(text, "Other third") {
		t.Fatal(text)
	}
}

func TestWorkflowIndexDeletedAndMalformed(t *testing.T) {
	d := workflowSnapshot()
	now := time.Now()
	deleted := d.Jobs[0]
	deleted.UID = "deleted"
	deleted.DeletedAt = &now
	malformed := d.Jobs[0]
	malformed.UID = "bad"
	malformed.Definition = json.RawMessage(`{"action":{"workflow_uid":"flow"},"timeout_seconds":"bad"}`)
	d.Jobs = append(d.Jobs, deleted, malformed)
	d.Jobs[0].Definition = json.RawMessage(`{"enabled":true,"action":{"kind":"execute","workflow_uid":"flow"},"timeout_seconds":1800,"options":{"agent":3,"input":"explicit"}}`)
	m := workflowModel(t, d)
	v := m.wfIndex[workflowKey{"project-a", "flow"}]
	if len(v.jobs) != 1 || v.jobs[0].timeout != 30*time.Minute || v.jobs[0].profile != m.jobs.byKey[jobKey{"project-a", "job"}].profile {
		t.Fatal("job projection", v.jobs)
	}
	d.Workflows[0].DeletedAt = &now
	m.Update(dataMsg{data: d})
	if len(m.wfIndex) != 0 {
		t.Fatal("deleted workflow retained")
	}
}
func BenchmarkWorkflowIndex(b *testing.B) {
	d := workflowSnapshot()
	d.Jobs = nil
	d.Runs = nil
	for i := range 500 {
		d.Jobs = append(d.Jobs, defs.JobEntry{UID: fmt.Sprintf("job-%04d", i), Name: "Example job", Definition: json.RawMessage(`{"action":{"kind":"execute","workflow_uid":"flow"}}`)})
	}
	for i := range 2000 {
		d.Runs = append(d.Runs, runner.Run{UID: fmt.Sprintf("run-%04d", i), Workflow: &kata.Workflow{UID: "flow"}, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-a"}}})
	}
	m := workflowModel(b, d)
	b.ResetTimer()
	for b.Loop() {
		buildWorkflowIndex(d, *m.jobs, m.workflowOptions, m.now())
	}
}

func workflowText(b bodyLines) string {
	var lines []string
	for _, l := range b {
		lines = append(lines, ansi.Strip(l.Text))
	}
	return strings.Join(lines, "\n")
}
func TestWorkflowListWidths(t *testing.T) {
	m := workflowModel(t, workflowSnapshot())
	for _, width := range []int{60, 80, 100, 120, 125, 126, 160, 200} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
		for line := range strings.SplitSeq(m.viewContent(), "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatalf("%d: %s", width, line)
			}
		}
		text := ansi.Strip(m.viewContent())
		for _, header := range []string{"status", "workflow", "steps", "input", "updated"} {
			if width >= 80 && !strings.Contains(text, header) {
				t.Fatalf("missing %s at %d", header, width)
			}
		}
	}
	// At 200 columns the list sits in the split pane; workflow takes its rest.
	if got := m.workflowWidths(); !slices.Equal(got, []int{7, 77, 5, 6, 7}) {
		t.Fatal(got)
	}
	for _, width := range []int{80, 120} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		text := ansi.Strip(m.viewContent())
		for _, want := range []string{"tabs", "search", "page", "move", "open", "resume", "edit", "reload", "layout", "mouse", "quit"} {
			if !strings.Contains(text, want) {
				t.Fatalf("width %d: missing footer action %s in %s", width, want, text)
			}
		}
	}
}
func TestWorkflowInspectorThreshold(t *testing.T) {
	for _, multi := range []bool{false, true} {
		m := workflowModel(t, workflowSnapshot())
		base := 139
		if multi {
			m.opts.Projects = map[string]*Lane{"example-project": {Config: workflowTestConfig()}, "another-project": {Config: workflowTestConfig()}}
		}
		for _, w := range []int{base, base + 1} {
			m.Update(tea.WindowSizeMsg{Width: w, Height: 40})
			if got := m.workflowInspectorWidth(); (got > 0) != (w == base+1) {
				t.Fatal("threshold", w, got)
			}
		}
	}
	m := workflowModel(t, workflowSnapshot())
	v := m.wfIndex[workflowKey{"project-a", "flow"}]
	text := workflowText(m.workflowInspector(v, 64))
	for _, want := range []string{"input   target", "failure park", "1. inspect", "2. check", "↺inspect", "Last run"} {
		if !strings.Contains(text, want) {
			t.Fatal(want, text)
		}
	}
	for _, raw := range []string{`{"version":1,"options":{"on_failure":"retry"},"steps":[{"key":"main","kind":"prompt","prompt":"Work"}]}`, `{`} {
		d := workflowSnapshot()
		d.Workflows[0].Definition = json.RawMessage(raw)
		m.Update(dataMsg{data: d})
		text = workflowText(m.workflowInspector(m.wfIndex[workflowKey{"project-a", "flow"}], 64))
		if raw == "{" {
			if !strings.Contains(text, "does not parse") {
				t.Fatal(text)
			}
		} else if !strings.Contains(text, "unsupported workflow") || !strings.Contains(text, "failure —") {
			t.Fatal(text)
		}
	}
}
func TestWorkflowMarquee(t *testing.T) {
	d := workflowSnapshot()
	d.Jobs = d.Jobs[:1]
	d.Jobs[0].Name = strings.Repeat("Example job ", 12)
	d.Runs = nil
	m := workflowModel(t, d)
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	if !m.rendered.Animates {
		t.Fatal("inspector-only overflow did not animate")
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.rendered.Animates {
		t.Fatal("hidden inspector kept animation alive")
	}
	d.Workflows[0].Name = "Short"
	other := d.Workflows[0]
	other.UID = "other"
	other.Name = strings.Repeat("界abc", 20)
	d.Workflows = append(d.Workflows, other)
	m.Update(dataMsg{data: d})
	if !m.rendered.Animates {
		t.Fatal("unselected overflow did not animate")
	}
	m.frame = 6
	m.Update(struct{}{})
	six := m.viewContent()
	m.Update(marqueeTickMsg{generation: m.marqueeGeneration})
	seven := m.viewContent()
	if six == seven || strings.Contains(seven, "…") {
		t.Fatal("hold/advance or cell ellipsis", seven)
	}
	frame := m.frame
	send(m, "j")
	send(m, "/")
	send(m, "enter")
	if m.frame != frame {
		t.Fatal("counter reset")
	}
}
func TestWorkflowListGolden(t *testing.T) {
	for _, name := range []string{"list", "broken", "empty", "filtered", "states", "multi", "multi-broken", "f1", "inactive"} {
		t.Run(name, func(t *testing.T) {
			assertFrames(t, "workflows", name, [][2]int{{120, 40}, {80, 24}, {160, 40}}, func(w, h int) string {
				d := workflowSnapshot()
				if name == "f1" {
					d = workflowF1GoldenSnapshot()
				}
				if name == "inactive" {
					for _, name := range []string{"Paused workflow", "Unused workflow"} {
						w := d.Workflows[0]
						w.UID, w.Name = strings.ToLower(strings.Fields(name)[0]), name
						d.Workflows = append(d.Workflows, w)
					}
					j := d.Jobs[0]
					j.UID, j.Name = "paused-job", "Paused job"
					j.Definition = json.RawMessage(strings.Replace(strings.Replace(string(j.Definition), `"enabled":true`, `"enabled":false`, 1), `"workflow_uid":"flow"`, `"workflow_uid":"paused"`, 1))
					d.Jobs = append(d.Jobs, j)
				}
				d.Running = true
				d.WorkerRunning = true
				if name == "states" {
					d.Workflows = nil
					d.Jobs = nil
					d.Runs = nil
					for _, state := range []struct {
						name    string
						phase   runner.Phase
						outcome string
					}{{"Done", runner.Settled, "succeeded"}, {"Failed", runner.Settled, "failed"}, {"Held", runner.Held, ""}, {"Queued", runner.Prepared, ""}, {"Running", runner.Running, ""}, {"Never", "", ""}} {
						uid := strings.ToLower(state.name)
						d.Workflows = append(d.Workflows, kata.Workflow{UID: uid, Name: state.name, Definition: json.RawMessage(workflowF1)})
						if state.name != "Never" {
							r := runner.Run{UID: "run-" + uid, Phase: state.phase, Outcome: state.outcome, Workflow: &kata.Workflow{UID: uid}, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-a"}}}
							if state.name != "Queued" {
								at := time.Date(2026, 10, 16, 11, 50, 0, 0, time.UTC)
								r.StartedAt = &at
							}
							d.Runs = append(d.Runs, r)
						}
					}
					d.Workflows = append(d.Workflows, kata.Workflow{UID: "decode", Name: "Decode failure", Definition: json.RawMessage(`{`)}, kata.Workflow{UID: "warning", Name: "Warning only", Definition: json.RawMessage(strings.Replace(workflowF1, `"on_failure":"park"`, `"on_failure":"park","future":true`, 1))})
				}
				if strings.HasPrefix(name, "multi") {
					d.Workflows[0].ProjectUID = "project-a"
					d.Workflows[0].Project = "example-project"
					other := d.Workflows[0]
					other.ProjectUID = "project-b"
					other.Project = "another-project"
					if name == "multi-broken" {
						other.Definition = json.RawMessage(`{`)
					}
					d.Workflows = append(d.Workflows, other)
					d.Projects = map[string]Data{"project-a": {Project: "example-project"}, "project-b": {Project: "another-project"}}
				}
				if name == "broken" {
					d.Workflows[0].Definition = json.RawMessage(`{"version":1,"options":{"on_failure":"retry"},"steps":[{"key":"main","kind":"prompt","prompt":"Work"}]}`)
				}
				if name == "empty" {
					d.Workflows = nil
				}
				m := workflowModel(t, d)
				if strings.HasPrefix(name, "multi") {
					m.opts.Projects = map[string]*Lane{"example-project": {Project: "example-project", ProjectUID: "project-a", Config: workflowTestConfig(), Service: &fakeActions{}}, "another-project": {Project: "another-project", ProjectUID: "project-b", Config: workflowTestConfig(), Service: &fakeActions{}}}
					m.Update(dataMsg{data: d})
				}
				m.Update(tea.WindowSizeMsg{Width: w, Height: h})
				if name == "filtered" {
					send(m, "/")
					send(m, "absent")
					send(m, "enter")
				}
				return m.viewContent()
			})
		})
	}
}

func TestWorkflowListRunLabels(t *testing.T) {
	for _, tc := range runLabelFixture() {
		t.Run(tc.Name, func(t *testing.T) {
			d := workflowSnapshot()
			r := tc.Run
			r.UID = "retained"
			r.Workflow = &kata.Workflow{UID: "flow"}
			r.Target.Identity.ProjectUID = "project-a"
			d.Runs = []runner.Run{r}
			m := workflowModel(t, d)
			v := m.wfIndex[workflowKey{"project-a", "flow"}]
			cells := m.workflowCells(v)
			if cells[0].Text != tc.Label || cells[4].Text != "—" {
				t.Fatal(cells)
			}
			note, _ := runNote(r, m.now())
			if tc.Report != "" && note != tc.Report {
				t.Fatal("report precedence", note, tc.Report)
			}
		})
	}
	d := workflowSnapshot()
	d.Runs = nil
	m := workflowModel(t, d)
	if cells := m.workflowCells(m.wfIndex[workflowKey{"project-a", "flow"}]); cells[0].Text != "never" || cells[4].Text != "—" {
		t.Fatal(cells)
	}
}

func TestWorkflowReload(t *testing.T) {
	m := workflowModel(t, workflowSnapshot())
	p := &controlledPoll{entered: make(chan struct{}, 2), release: make(chan struct{}, 2), data: workflowSnapshot()}
	m.cache = p
	_, first := m.Update(press("r"))
	result := make(chan tea.Msg, 1)
	go func() { result <- first() }()
	<-p.entered
	for range 5 {
		if _, cmd := m.Update(press("r")); cmd != nil {
			t.Fatal("overlapping workflow poll")
		}
	}
	if p.calls.Load() != 1 || !m.pollAgain {
		t.Fatal("reloads did not coalesce")
	}
	p.release <- struct{}{}
	_, follow := m.Update(<-result)
	if follow == nil {
		t.Fatal("pending poll lost")
	}
	go func() { result <- follow() }()
	<-p.entered
	p.release <- struct{}{}
	m.Update(<-result)
	if p.calls.Load() != 2 || p.peak.Load() != 1 || m.polling || m.notice.Text != "reloaded" || m.notice.Kind != statusInfo {
		t.Fatal("workflow reload result", p.calls.Load(), p.peak.Load(), m.notice.Text, m.notice.Kind)
	}
	m.refreshGeneration = m.pollGeneration + 1
	m.polling = true
	m.pollGeneration++
	m.Update(dataMsg{data: workflowSnapshot(), err: errors.New("snapshot failed")})
	if !strings.Contains(ansi.Strip(m.viewContent()), "snapshot failed") {
		t.Fatal("reload failure hidden", m.viewContent())
	}
}

// The flows inspector contract follows the pinned Bermuda layout: section
// boundaries stay visible even when the table has only one row.
func TestWorkflowInspectorSections(t *testing.T) {
	m := workflowModel(t, workflowSnapshot())
	body := workflowText(m.workflowInspector(m.selectedWorkflow(), 64))
	lines := strings.Split(body, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	body = strings.Join(lines, "\n")
	for _, want := range []string{"Example\n\nInspect an example\n\ninput", "\n\nSteps 3\n", "\n\nLast run\n"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing section spacing %q:\n%s", want, body)
		}
	}
}

func TestWorkflowInspectorGolden(t *testing.T) {
	assertFrames(t, "workflows", "inspector-summary", [][2]int{{64, 32}}, func(w, h int) string {
		m := workflowModel(t, workflowSnapshot())
		var lines []string
		for _, line := range m.workflowInspector(m.selectedWorkflow(), w) {
			lines = append(lines, line.Text)
		}
		return strings.Join(lines, "\n")
	})
}

func TestInactiveWorkflowNamesAreDim(t *testing.T) {
	preserveTheme(t)
	applyColorMode(colorDark, true)
	for _, tc := range []struct {
		name  string
		setup func(*Data)
		want  string
	}{
		{"unused", func(d *Data) { d.Jobs = nil }, "\x1b[38;5;246mExample workflow"},
		{"paused", func(d *Data) {
			d.Jobs[0].Definition = json.RawMessage(strings.Replace(string(d.Jobs[0].Definition), `"enabled":true`, `"enabled":false`, 1))
		}, "\x1b[38;5;246mExample workflow"},
		{"enabled", func(d *Data) {}, "\x1b[m Example workflow "},
		{"other project", func(d *Data) { d.Jobs[0].ProjectUID = "project-b" }, "\x1b[38;5;246mExample workflow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := workflowSnapshot()
			d.Workflows[0].Name = "Example workflow"
			tc.setup(&d)
			m := workflowModel(t, d)
			text := tableRow(m.workflowCells(m.selectedWorkflow()), m.workflowWidths(), false, 0, 0).Text
			if !strings.Contains(text, tc.want) {
				t.Fatalf("workflow name style: got %q, want %q", text, tc.want)
			}
		})
	}
}

func TestWorkflowInspectorProfilePromptCap(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode colorMode
	}{{"light", colorLight}, {"dark", colorDark}, {"none", colorNone}} {
		t.Run(tc.name, func(t *testing.T) {
			preserveTheme(t)
			applyColorMode(tc.mode, true)
			m, _ := fixture(t)
			m.opts.Config = config.Config{Limits: config.Limits{MaxRunPrompts: 12}, Profiles: map[string]config.Profile{"other": {Kind: "codex", MaxRunPrompts: new(4)}}}
			m.data.Workflows = []kata.Workflow{{UID: "wf", Name: "flow", Definition: json.RawMessage(`{"version":1,"options":{"agent":"other"},"steps":[{"key":"main","kind":"prompt","prompt":"work","retries":2}]}`)}}
			m.Update(dataMsg{data: m.data})
			send(m, "4")
			m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
			v := m.wfIndex[workflowKey{"", "wf"}]
			if v == nil || v.check.Budget == nil || v.check.Budget.Total != 3 || !slices.Equal(v.check.DeclaredProfiles, []string{"other"}) || len(v.check.Unsupported) != 0 || defs.EffectiveCap(m.opts.Config, "other") != 4 {
				t.Fatal(v)
			}
			// Step numbers and names have separate styles. Check their visible text.
			view := ansi.Strip(uncachedView(m))
			if !strings.Contains(view, "1. main") || strings.Contains(view, "unsupported workflow") {
				t.Fatal(view)
			}
		})
	}
}

func TestWorkflowProjectionAfterPause(t *testing.T) {
	m := workflowModel(t, workflowSnapshot())
	saved := m.jobs.byKey[jobKey{"project-a", "job"}].job
	saved.Definition = json.RawMessage(strings.Replace(string(saved.Definition), `"enabled":true`, `"enabled":false`, 1))
	saved.DefinitionEventUID = "paused-event"
	m.Update(jobPausedMsg{saved: saved})
	if v := m.wfIndex[workflowKey{"project-a", "flow"}]; v.active {
		t.Fatal("paused job still makes workflow active before the next snapshot")
	}
}

func TestWorkflowProjectionAfterSavedBinding(t *testing.T) {
	d := workflowSnapshot()
	other := d.Workflows[0]
	other.UID, other.Name = "other-flow", "Other workflow"
	d.Workflows = append(d.Workflows, other)
	m := workflowModel(t, d)
	m.pushDetail(viewWorkflow, "project-a", "flow")
	m.detailKey = detailItemKey("used-by", "project-a", "job")
	saved := m.jobs.byKey[jobKey{"project-a", "job"}].job
	saved.Definition = json.RawMessage(strings.Replace(string(saved.Definition), `"workflow_uid":"flow"`, `"workflow_uid":"other-flow"`, 1))
	saved.DefinitionEventUID = "rebound-event"
	m.Update(editorClosedMsg{Target: editorTarget{ProjectUID: "project-a", UID: "job"}, Saved: &saved})
	if a, b := m.wfIndex[workflowKey{"project-a", "flow"}], m.wfIndex[workflowKey{"project-a", "other-flow"}]; len(a.jobs) != 0 || len(b.jobs) != 1 {
		t.Fatalf("saved binding not projected: old=%d new=%d", len(a.jobs), len(b.jobs))
	}
	if m.detailKey == detailItemKey("used-by", "project-a", "job") {
		t.Fatal("removed used-by selection retained after save")
	}
}

func TestWorkflowProjectionSavedNameSurvivesStaleSnapshot(t *testing.T) {
	d := workflowSnapshot()
	other := d.Jobs[0]
	other.UID, other.Name = "other-job", "Middle job"
	d.Jobs = append(d.Jobs, other)
	m := workflowModel(t, d)
	saved := m.jobs.byKey[jobKey{"project-a", "job"}].job
	saved.Name, saved.DefinitionEventUID = "Alpha renamed", "renamed-event"
	m.Update(editorClosedMsg{Target: editorTarget{ProjectUID: "project-a", UID: "job"}, Saved: &saved})
	m.Update(dataMsg{data: d})
	v := m.wfIndex[workflowKey{"project-a", "flow"}]
	text := workflowText(m.workflowInspector(v, 100))
	if !strings.Contains(text, "2 Alpha renamed, Middle job") || strings.Contains(text, "Uses example") {
		t.Fatal("workflow inspector bypassed the saved name or sorted by the stale name:", text)
	}
	m.width, m.height = 240, 80
	m.pushDetail(viewWorkflow, "project-a", "flow")
	text = workflowText(m.buildWorkflowDetail().Body())
	if !strings.Contains(text, "Alpha renamed") || strings.Contains(text, "Uses example") {
		t.Fatal("workflow detail bypassed the saved name:", text)
	}
}

// A step on its own profile is labelled fresh with that profile's model.
func TestWorkflowStepLabelShowsStepModel(t *testing.T) {
	raw := json.RawMessage(`{"version":1,"steps":[{"key":"make","kind":"prompt","prompt":"Make"},{"key":"check","kind":"prompt","prompt":"Check","after":["make"],"options":{"agent":"checker"}},{"key":"note","kind":"prompt","prompt":"Note","after":["check"],"options":{"agent":"other"}}]}`)
	cfg := workflowTestConfig()
	cfg.Profiles["checker"] = config.Profile{Kind: "claude", Args: []string{"--model", "checker-model", "--effort", "high"}}
	c := workflowProblems(raw, cfg)
	got := []string{workflowStepLabel(c, 0), workflowStepLabel(c, 1), workflowStepLabel(c, 2)}
	// Without a workflow profile, sharing depends on each job's profile.
	want := []string{"1. make  prompt shared", "2. check  prompt · checker-model high", "3. note  prompt · profile other"}
	if !slices.Equal(got, want) {
		t.Fatalf("labels=%q", got)
	}
	if got := workflowStepLabel(workflowShape(raw), 1); got != "2. check  prompt · profile checker" {
		t.Fatalf("lane-free label=%q", got)
	}
	declared := json.RawMessage(strings.Replace(string(raw), `{"version":1,`, `{"version":1,"options":{"agent":"default"},`, 1))
	if got := workflowStepLabel(workflowProblems(declared, cfg), 1); got != "2. check  prompt fresh · checker-model high" {
		t.Fatalf("declared label=%q", got)
	}
}

// A step profile's cap bounds every using job, so exceeding it is not per job.
func TestWorkflowProblemsStepProfileCap(t *testing.T) {
	cfg := workflowTestConfig()
	cap := 2
	cfg.Profiles["checker"] = config.Profile{MaxRunPrompts: &cap}
	got := workflowProblems(json.RawMessage(`{"version":1,"steps":[{"key":"make","kind":"prompt","prompt":"Make","retries":1},{"key":"check","kind":"prompt","prompt":"Check","after":["make"],"options":{"agent":"checker"}}]}`), cfg)
	if !slices.Equal(got.Unsupported, []string{"prompt_budget 3 > 2"}) || len(got.JobDependent) != 0 {
		t.Fatalf("step cap: %+v", got)
	}
}
