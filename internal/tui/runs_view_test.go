package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/agentlimit"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

func TestU1RunsLayout(t *testing.T) {
	for _, tt := range []struct {
		width int
		multi bool
		want  []int
	}{{120, false, []int{1, 7, 6, 5, 87, 7}}, {80, false, []int{1, 7, 6, 5, 47, 7}}, {120, true, []int{1, 7, 6, 5, 87, 7}}, {80, true, []int{1, 7, 6, 5, 47, 7}}} {
		m, _ := fixture(t)
		m.width = tt.width
		if tt.multi {
			m.opts.Projects = map[string]*Lane{"one": {}, "two": {}}
		}
		if got := m.runsWidths(); !reflect.DeepEqual(got, tt.want) {
			t.Fatal(got, tt.want)
		}
	}
	m, _ := fixture(t)
	m.width = 60
	m.selectTab(1)
	if v := ansi.Strip(m.viewContent()); !strings.Contains(v, "M mouse") || !strings.Contains(v, "q quit") {
		t.Fatal(v)
	}
	m.width = 80
	m.data.Runs[0].Job.Name = strings.Repeat("long name", 20)
	m.data.Runs = append(m.data.Runs, m.data.Runs[0])
	m.data.Runs[1].UID = "r2"
	m.rebuild()
	m.frame = 7
	m.Update(press("j"))
	if m.frame != 7 || !m.rendered.Animates || len(m.hits.Rows) != 2 {
		t.Fatal(m.frame, m.rendered)
	}
	m.Update(marqueeTickMsg{generation: m.marqueeGeneration})
	if m.frame != 8 {
		t.Fatal(m.frame)
	}
}
func tallRuns(t *testing.T) *model {
	t.Helper()
	m, _ := fixture(t)
	m.width, m.height = 80, 24
	r := m.data.Runs[0]
	for i := range 60 {
		r.Steps = append(r.Steps, runner.StepState{Key: fmt.Sprintf("step-%02d", i), Kind: "command"})
	}
	m.data.Runs = []runner.Run{r}
	for i := 1; i < 348; i++ {
		copy := r
		copy.UID = fmt.Sprintf("r%03d", i)
		copy.Steps = nil
		m.data.Runs = append(m.data.Runs, copy)
	}
	m.selectTab(1)
	m.expanded["r1"] = true
	m.rebuild()
	m.frameValid = false
	return m
}
func TestU1RunsPaging(t *testing.T) {
	for _, pair := range [][2]string{{"ctrl+d", "ctrl+u"}, {"pgdown", "pgup"}} {
		m := tallRuns(t)
		send(m, pair[0])
		if m.scroll != 21 || m.cursor != 0 || m.follow {
			t.Fatal(m.scroll, m.cursor)
		}
		send(m, pair[1])
		if m.scroll != 0 {
			t.Fatal(m.scroll)
		}
		send(m, "]")
		if m.cursor != m.pageRows() || m.scroll != 0 || !m.follow {
			t.Fatal(m.cursor, m.scroll)
		}
	}
	m := tallRuns(t)
	if len(m.rows) != 348 {
		t.Fatal("steps counted as rows", len(m.rows))
	}
	before := m.pageRows()
	m.notice.Text = "status"
	m.query = "r"
	m.data.Route = map[string]any{"allowed": false, "error": "route refused"}
	if m.pageRows() >= before {
		t.Fatal("footer not measured")
	}
	m.query = ""
	m.rebuild()
	m.cursor = 3
	uid := m.rows[3].uid
	newer := m.data.Runs[0]
	newer.UID = "new"
	m.data.Runs = append([]runner.Run{newer}, m.data.Runs...)
	m.rebuild()
	if m.rows[m.cursor].uid != uid {
		t.Fatal("selection lost")
	}
}
func TestU1RunsOpenWithoutPane(t *testing.T) {
	for _, ref := range []*runner.HerdrRef{nil, {}} {
		for _, k := range []string{"enter", "l", "right"} {
			m, f := fixture(t)
			m.data.Runs[0].Herdr = runner.HerdrRef{}
			m.data.Runs[0].Steps[0].Herdr = ref
			send(m, "3")
			send(m, k)
			if m.currentView() != viewRun || len(f.calls) != 0 {
				t.Fatal(k, f.calls)
			}
		}
	}
}
func TestU1RunsMouse(t *testing.T) {
	m := tallRuns(t)
	m.cursor = 1
	m.scroll = 60
	m.follow = false
	m.frameValid = false
	m.viewContent()
	for y, h := range m.hits.Rows {
		if h.Kind != "run" {
			t.Fatal(h)
		}
		m.mouse(tea.MouseClickMsg{X: 1, Y: y, Button: tea.MouseLeft})
		if m.cursor != h.Index {
			t.Fatal("hit retargeted")
		}
		break
	}
	for y := range m.height {
		if _, ok := m.hits.At(1, y); !ok {
			cursor := m.cursor
			m.mouse(tea.MouseClickMsg{X: 1, Y: y, Button: tea.MouseLeft})
			if m.cursor != cursor {
				t.Fatal("nonrow hit")
			}
		}
	}
}
func TestU1RunsRestore(t *testing.T) {
	m := tallRuns(t)
	m.query = "Job"
	m.rebuild()
	m.scroll = 21
	m.follow = false
	send(m, "enter")
	send(m, "h")
	if m.currentView() != viewList || m.query != "Job" || m.scroll != 21 || m.follow || !m.expanded["r1"] {
		t.Fatal(m.nav, m.scroll)
	}
}
func TestU1RunsGuards(t *testing.T) {
	for _, reason := range []string{"admission_rejected", "false"} {
		m, f := fixture(t)
		r := &m.data.Runs[0]
		r.Phase = runner.Held
		r.Reason = reason
		if reason == "false" {
			v := false
			r.Admitted = &v
		}
		send(m, "3")
		send(m, "u")
		want := "run herdr-kata stop, then herdr-kata status --json to identify and abandon this run --local."
		if m.notice.Text != want || len(f.calls) != 0 {
			t.Fatal(m.notice.Text, f.calls)
		}
	}
	m, f := fixture(t)
	r := &m.data.Runs[0]
	r.IssueUID = ""
	r.Herdr = runner.HerdrRef{}
	send(m, "3")
	send(m, "a")
	if m.notice.Text != "no agent pane for this run · l opens its record" || m.notice.Kind != statusInfo || len(f.calls) != 0 {
		t.Fatal(m.notice.Text, f.calls)
	}
	send(m, "u")
	if m.notice.Text != "Resume requires a parked run" {
		t.Fatal(m.notice.Text)
	}
	r.Phase = runner.Held
	send(m, "u")
	send(m, "i")
	if strings.Join(f.calls, ",") != "resume:r1" || m.notice.Text != "run has no Kata issue" {
		t.Fatal(m.notice.Text, f.calls)
	}
	r = &m.data.Runs[0] // Resume acknowledgement replaces the displayed snapshot.
	r.IssueUID = "issue"
	send(m, "i")
	if strings.Join(f.calls, ",") != "resume:r1,issue:issue" {
		t.Fatal(f.calls)
	}
	r.WFDef = nil
	r.PastAttempts = nil
	m.expanded = map[string]bool{}
	send(m, " ")
	if m.expanded[r.UID] {
		t.Fatal("plain prompt expanded")
	}
	r.PastAttempts = []runner.StepState{{Key: "build", Attempt: 1}}
	send(m, " ")
	if !m.expanded[r.UID] {
		t.Fatal("attempts not expandable")
	}
	r.WFDef = &defs.WorkflowDef{}
	for _, k := range []string{"R", "p", "f", "F", "P", "D", "e", "n", "b"} {
		n := len(f.calls)
		send(m, k)
		if len(f.calls) != n || len(m.confirm) > 0 || m.editor != nil {
			t.Fatal(k)
		}
	}
}

func TestU1RunsEmptyExpansionIsInert(t *testing.T) {
	m := tallRuns(t)
	steps := m.data.Runs[0].Steps
	m.data.Runs = m.data.Runs[:2]
	m.data.Runs[0].Steps = nil
	m.data.Runs[1].Steps = steps
	m.expanded = map[string]bool{m.data.Runs[1].UID: true}
	m.rebuild()
	m.cursor = 0
	m.scroll = 21
	m.follow = false
	m.frameValid = false
	before := m.viewContent()
	send(m, " ")
	if m.scroll != 21 || m.follow || m.viewContent() != before {
		t.Fatal("space on an empty step block moved the viewport", m.scroll, m.viewContent())
	}
}

var runNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func TestU1RunProjection(t *testing.T) {
	start := runNow.Add(-time.Minute)
	r := runner.Run{UID: "run", Job: kata.Job{UID: "job"}, Occurrence: "bad", Manual: true}
	v := runView(r)
	if v.Job != "run run" || v.Trigger != "manual" || !v.When.IsZero() {
		t.Fatal(v)
	}
	r.Child = &runner.ChildRun{}
	r.StartedAt = &start
	v = runView(r)
	if v.Trigger != "recurring child" || !v.When.Equal(start) {
		t.Fatal(v)
	}
	r.Child = nil
	r.Manual = false
	r.StartedAt = nil
	r.Occurrence = runNow.Format(time.RFC3339)
	v = runView(r)
	if v.Trigger != "scheduled" || !v.When.IsZero() {
		t.Fatal(v)
	}
}
func TestU1DetailPrecedence(t *testing.T) {
	reset := runNow.Add(3 * time.Hour)
	r := runner.Run{UID: "run", Phase: runner.Held, Reason: "agent_quota", ActiveStep: "verify", WFDef: &defs.WorkflowDef{}, Steps: []runner.StepState{{Key: "build", Status: "succeeded"}, {Key: "verify", Limit: &agentlimit.Limit{ResetsAt: &reset}}}, Ops: []runner.Op{{}}, Inspect: ""}
	cases := []struct {
		name, want string
		change     func(*runner.Run)
	}{
		{"progress wait", "waiting: agent_quota · resets in 3h 0m", func(*runner.Run) {}},
		{"signing", "signing refused", func(r *runner.Run) { r.Ops = []runner.Op{{SigningRefused: true}}; r.Inspect = "diagnosis" }},
		{"inspect", "inspect: diagnosis", func(r *runner.Run) { r.Inspect = "diagnosis" }},
		{"progress", "1/2 · verify", func(r *runner.Run) { r.Phase = runner.Running }},
		{"report", "reporting…", func(r *runner.Run) {
			r.WFDef = nil
			r.Phase = runner.Settled
			r.Steps = []runner.StepState{{Note: "hidden"}}
		}},
		{"summary", "summary", func(r *runner.Run) {
			r.WFDef = nil
			r.Phase = runner.Settled
			r.Ops = nil
			r.Steps = []runner.StepState{{Result: &runner.ResultRef{Summary: "summary"}}}
		}},
		{"blank", "", func(r *runner.Run) { r.WFDef = nil; r.Phase = runner.Settled; r.Ops = nil; r.Steps = nil }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			copy := r
			tt.change(&copy)
			before := runView(copy)
			got, _ := runDetailCell(copy, runNow)
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
			if !reflect.DeepEqual(before, runView(copy)) {
				t.Fatal("mutated snapshot")
			}
		})
	}
}
func TestU1RunSearch(t *testing.T) {
	r := runner.Run{UID: "run", Phase: runner.Settled, Outcome: "succeeded", Steps: []runner.StepState{{Note: "hidden note", Result: &runner.ResultRef{Summary: "hidden summary"}}}, PastAttempts: []runner.StepState{{Note: "archived", Reason: "no_activity"}}, WFDef: &defs.WorkflowDef{}}
	hay := runSearch(runView(r), "example-project", runNow)
	for _, want := range []string{"succeeded", "scheduled", "hidden note", "hidden summary", "archived", "no_activity", "example-project"} {
		if !strings.Contains(hay, want) {
			t.Fatal(want, hay)
		}
	}
	if strings.Contains(hay, "catch-up") {
		t.Fatal(hay)
	}
}

func TestU1StepState(t *testing.T) {
	cases := []struct {
		status        string
		phase         runner.Phase
		submission    runner.Submission
		limit         bool
		mark, outcome string
	}{
		{"succeeded", runner.Held, runner.None, true, "✓", "succeeded"}, {"failed", runner.Running, runner.Sending, false, "✗", "failed (reason)"}, {"needs-human", runner.Running, runner.None, false, "⏸", "needs-human"}, {"unknown", runner.Running, runner.None, false, "?", "unknown (reason)"}, {"skipped", runner.Running, runner.None, false, "·", "skipped"},
		{"", runner.Held, runner.None, false, "⏸", "parked (reason), not sent"}, {"", runner.Blocked, runner.Accepted, false, "⏸", "blocked"}, {"", runner.HandedOff, runner.None, false, "⏸", "needs-human"}, {"", runner.Running, runner.Sending, true, "⏸", "quota (agent_quota)"}, {"", runner.Running, runner.Rejected, false, "⏸", "rejected"}, {"", runner.Running, runner.Sending, false, "▶", "running"}, {"", runner.Admitted, runner.Accepted, false, "▶", "running"}, {"", runner.Prepared, runner.None, false, "·", "not started"},
	}
	for _, tt := range cases {
		s := runner.StepState{Key: "main", Status: tt.status, Reason: "reason", Submission: tt.submission}
		if tt.limit {
			s.Limit = &agentlimit.Limit{Kind: "agent_quota"}
		}
		r := runner.Run{Phase: tt.phase, Reason: "reason", ActiveStep: "main"}
		got := stepDisplay(r, s)
		if got.Mark != tt.mark || got.Outcome != tt.outcome {
			t.Fatalf("%+v: %+v", tt, got)
		}
	}
	if got := stepDisplay(runner.Run{Phase: runner.Running, ActiveStep: "other"}, runner.StepState{Key: "main", Submission: runner.Sending}); got.Outcome != "not started" {
		t.Fatal(got)
	}
}
func TestU1ArchivedAttempt(t *testing.T) {
	for _, tt := range []struct {
		s    runner.StepState
		want string
	}{
		{runner.StepState{Status: "succeeded", Limit: &agentlimit.Limit{}}, "succeeded"}, {runner.StepState{Status: "failed", Reason: "exit"}, "failed (exit)"}, {runner.StepState{Status: "needs-human"}, "needs-human"}, {runner.StepState{Status: "unknown"}, "unknown"}, {runner.StepState{Status: "skipped"}, "skipped"}, {runner.StepState{Limit: &agentlimit.Limit{Kind: "agent_quota"}}, "quota (agent_quota)"}, {runner.StepState{Submission: runner.Rejected, Reason: "other"}, "rejected"}, {runner.StepState{Reason: "no_activity"}, "stopped (no_activity)"}, {runner.StepState{Submission: runner.Accepted}, "sent, outcome unproven"}, {runner.StepState{SubmittedAt: &runNow}, "sent, outcome unproven"}, {runner.StepState{}, "not sent"},
	} {
		if got := attemptDisplay(tt.s); got.Outcome != tt.want {
			t.Fatalf("%+v: %+v", tt, got)
		}
	}
}
func TestU1StepDuration(t *testing.T) {
	sent := runNow.Add(-3 * time.Minute)
	future := runNow.Add(time.Hour)
	before := runNow.Add(-time.Second)
	r := runner.Run{Phase: runner.Running, ActiveStep: "main"}
	for _, tt := range []struct {
		s        runner.StepState
		archived bool
		want     string
	}{
		{runner.StepState{Key: "main", Kind: "prompt", Submission: runner.Sending, SubmittedAt: &sent}, false, "sent 3m"},
		{runner.StepState{Key: "main", Kind: "command", Submission: runner.Sending}, false, ""},
		{runner.StepState{Kind: "command", Receipt: &runner.Receipt{StartedAt: runNow, EndedAt: runNow}}, false, "0s"},
		{runner.StepState{Kind: "command", Receipt: &runner.Receipt{StartedAt: runNow, EndedAt: before}}, false, ""},
		{runner.StepState{Kind: "command", Receipt: &runner.Receipt{}}, false, ""},
		{runner.StepState{Key: "main", Kind: "prompt", Status: "succeeded", SubmittedAt: &sent}, false, "—"},
		{runner.StepState{Key: "main", Kind: "prompt", SubmittedAt: &sent}, true, "—"},
		{runner.StepState{Key: "main", Kind: "prompt", SubmittedAt: &future}, false, "sent under a minute"},
		{runner.StepState{Kind: "prompt"}, false, ""},
	} {
		if got := stepDuration(r, tt.s, tt.archived, runNow); got != tt.want {
			t.Fatalf("%+v got %q", tt, got)
		}
	}
	s := runner.StepState{Key: "main", Kind: "prompt", Status: "succeeded", SubmittedAt: &sent}
	r.Phase = runner.Settled
	for _, now := range []time.Time{runNow, runNow.Add(time.Hour)} {
		if stepDuration(r, s, false, now) != "—" {
			t.Fatal("terminal timer")
		}
	}
	r.Phase = runner.Held
	if stepDuration(r, s, false, runNow) != "—" {
		t.Fatal("held workflow inflated completed prompt")
	}
}
func TestU1StepLines(t *testing.T) {
	r := runner.Run{Phase: runner.Running, ActiveStep: "main", Steps: []runner.StepState{{Key: "main", Kind: "prompt", Attempt: 3, Submission: runner.Sending}}, PastAttempts: []runner.StepState{{Key: "main", Kind: "prompt", Attempt: 2, Reason: "no_activity", Note: "second"}, {Key: "main", Kind: "prompt", Attempt: 1, Limit: &agentlimit.Limit{Kind: "agent_quota"}, Note: "first"}}}
	lines := renderStepLines(r, 120, runNow, 0, false)
	if len(lines) != 3 || !strings.HasPrefix(lines[0].Text, "      ") || !strings.HasPrefix(lines[1].Text, "            ") || !strings.Contains(lines[1].Text, "first") || !strings.Contains(lines[2].Text, "second") || !strings.Contains(lines[0].Text, "agent") || !strings.Contains(lines[0].Text, "running #3") {
		t.Fatal(lines)
	}
	for _, line := range renderStepLines(r, 30, runNow, 0, false) {
		if ansi.StringWidth(line.Text) > 30 || strings.HasPrefix(line.Text, "▶ ") {
			t.Fatal(line)
		}
	}
}

func TestU1StepLabelsRetainAttemptEvidence(t *testing.T) {
	r := runner.Run{Phase: runner.Held, Reason: "no_result", ActiveStep: "verify", Steps: []runner.StepState{{Key: "verify", Kind: "prompt", Attempt: 3, Submission: runner.None}}, PastAttempts: []runner.StepState{{Key: "verify", Kind: "prompt", Attempt: 2, Reason: "no_activity"}}}
	lines := renderStepLines(r, 120, runNow, 0, false)
	if len(lines) != 2 || !strings.Contains(ansi.Strip(lines[0].Text), "parked (no_result), not sent #3") || !strings.Contains(ansi.Strip(lines[1].Text), "stopped (no_activity) #2") {
		t.Fatal("step labels lost saved attempt evidence", lines)
	}
	sent := runNow.Add(-3*time.Hour - 20*time.Minute)
	r.Phase = runner.Running
	r.Steps[0].Submission = runner.Sending
	r.Steps[0].SubmittedAt = &sent
	if line := ansi.Strip(renderStepLines(r, 120, runNow, 0, false)[0].Text); !strings.Contains(line, "sent 3h 20m") {
		t.Fatal("submission elapsed text clipped", line)
	}
}

func TestParkedRunDetailColumn(t *testing.T) {
	r := runner.Run{UID: "run", Phase: runner.Held, Reason: "no_result", ActiveStep: "check", WFDef: &defs.WorkflowDef{}, Steps: []runner.StepState{{Key: "check"}, {Key: "finish"}}}
	if got, _ := runDetailCell(r, runNow); got != "waiting: no_result" {
		t.Fatal(got)
	}
}
