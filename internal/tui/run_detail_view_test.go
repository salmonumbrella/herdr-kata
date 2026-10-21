package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/agentlimit"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

func detailBody(t *testing.T, r runner.Run, width int) string {
	t.Helper()
	m := newModel(Options{Root: t.TempDir(), Now: func() time.Time { return runNow }})
	m.width = width
	m.data.Runs = []runner.Run{r}
	m.rebuild()
	_ = m.pushDetail(viewRun, r.Target.Identity.ProjectUID, r.UID)
	return ansi.Strip(bodyTextForTest(m.renderRunDetail()))
}
func TestU1ResultSelection(t *testing.T) {
	success := runner.StepState{Key: "plan", Kind: "prompt", Status: "succeeded", Result: &runner.ResultRef{Summary: "unrelated success"}}
	failed := runner.StepState{Key: "verify", Kind: "command", Attempt: 2, Status: "failed", Reason: "command verify exited 1", Receipt: &runner.Receipt{ExitCode: 1}, Note: "inspect output"}
	cases := []struct {
		name              string
		r                 runner.Run
		key, want, absent string
	}{
		{"stopping", runner.Run{Phase: runner.Held, ActiveStep: "verify", Steps: []runner.StepState{success, failed}}, "verify", "cmd verify #2: exit 1", "unrelated success"},
		{"earlier failure", runner.Run{Phase: runner.Settled, Steps: []runner.StepState{failed, success}}, "verify", "inspect output", "unrelated success"},
		{"uncertain", runner.Run{Phase: runner.Held, ActiveStep: "verify", Steps: []runner.StepState{success, {Key: "verify", Attempt: 1, Receipt: &runner.Receipt{Uncertain: true}}}}, "verify", "cmd verify #1: exit 0 (uncertain)", "unrelated success"},
		{"handoff", runner.Run{Phase: runner.HandedOff, ActiveStep: "review", Steps: []runner.StepState{{Key: "review", Attempt: 1, Status: "needs-human", Result: &runner.ResultRef{Summary: "approve", Evidence: []string{"observation as text"}}}, success}}, "review", "observation as text", "unrelated success"},
		{"summary", runner.Run{Steps: []runner.StepState{{Key: "review", Status: "failed", Result: &runner.ResultRef{Summary: "failure summary"}}}}, "review", "failure summary", "unrelated success"},
		{"all success", runner.Run{Steps: []runner.StepState{{Key: "first", Status: "succeeded"}, success}}, "plan", "unrelated success", ""},
		{"no result", runner.Run{Phase: runner.Held, ActiveStep: "verify", Steps: []runner.StepState{{Key: "verify"}}}, "verify", "no result recorded", ""},
		{"no steps", runner.Run{}, "", "0/1 steps ok", "no result recorded"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tt.r.UID = "run"
			v := runDetailView(tt.r)
			if (v.ResultStep == nil) != (tt.key == "") || v.ResultStep != nil && v.ResultStep.Key != tt.key {
				t.Fatal(v.ResultStep)
			}
			text := detailBody(t, tt.r, 120)
			if !strings.Contains(text, tt.want) || tt.absent != "" && strings.Contains(text, tt.absent) {
				t.Fatal(text)
			}
			if v.ResultStep != nil && !strings.Contains(text, "from "+tt.key+" #") {
				t.Fatal(text)
			}
		})
	}
	s := runner.StepState{Note: "same note", Result: &runner.ResultRef{Summary: "summary", Note: "same note"}}
	if text := strings.Join(evidence(s, 80), "\n"); strings.Count(text, "same note") != 1 {
		t.Fatal(text)
	}
}
func TestU1DetailFields(t *testing.T) {
	start := runNow.Add(-10 * time.Minute)
	end := start
	r := runner.Run{UID: "run", Job: kata.Job{Name: "Example build"}, Phase: runner.Settled, Outcome: "succeeded", StartedAt: &start, EndedAt: &end, PromptsSent: 3, PromptCap: 12, ProfileName: "default", Profile: config.Profile{Kind: "codex", Args: []string{"--model=example-model"}}, Ops: []runner.Op{{}}, Input: "first line\nsecond line", Cwd: "/srv/projects/example", Occurrence: start.Add(-10 * time.Minute).Format(time.RFC3339)}
	text := detailBody(t, r, 120)
	for _, want := range []string{"Example build", "outcome        succeeded", "reporting", "reporting…", "scheduled", "started 10m after occurrence", "0s", "not", "3/12", "example-model", "pending writes", "first line"} {
		if want == "not" {
			continue
		}
		if !strings.Contains(text, want) {
			t.Fatal(want, text)
		}
	}
	if strings.Contains(text, "second line") || strings.Contains(text, "\n▶ ") {
		t.Fatal(text)
	}
	r.StartedAt = nil
	r.EndedAt = nil
	r.Phase = runner.Held
	r.Reason = "admission_rejected"
	r.UID = "01K00000000000000000000000"
	text = detailBody(t, r, 80)
	for _, want := range []string{"not started", "herdr-kata stop", "herdr-kata status --json", "--local."} {
		if !strings.Contains(text, want) {
			t.Fatal(want, text)
		}
	}
	r.Phase = runner.Settled
	v := false
	r.Admitted = &v
	text = detailBody(t, r, 80)
	if admissionHold(r) || strings.Contains(text, "next action") || strings.Contains(text, "herdr-kata stop") {
		t.Fatal(text)
	}
	r.Phase = runner.Running
	r.StartedAt = &start
	r.ExpiresAt = runNow.Add(time.Hour)
	r.PromptCap = 0
	r.Profile.Args = []string{"--model", "other"}
	text = detailBody(t, r, 120)
	for _, want := range []string{"still running · 10m", "in 1h 0m", "3/unlimited", "other", "server unproven"} {
		if !strings.Contains(text, want) {
			t.Fatal(want, text)
		}
	}
	r.Phase = runner.Settled
	text = detailBody(t, r, 120)
	if strings.Contains(text, "still running") || strings.Contains(text, "deadline") {
		t.Fatal(text)
	}
	r.Child = &runner.ChildRun{}
	r.JobDef.Issue = &defs.IssuePolicy{Kind: "per-run", ParentUID: "parent"}
	text = detailBody(t, r, 120)
	if strings.Contains(text, "run parent") {
		t.Fatal(text)
	}
}
func TestU1DetailKVStyles(t *testing.T) {
	reset := runNow.Add(3 * time.Hour)
	r := runner.Run{UID: "01K00000000000000000000000", Phase: runner.Held, Reason: "agent_quota", ActiveStep: "verify", Steps: []runner.StepState{{Key: "verify", Limit: &agentlimit.Limit{Kind: "agent_quota", Code: "codex:usage_limit_exceeded", ResetsAt: &reset}}}}
	lines := runHoldLines(r, 80, runNow)
	text := ansi.Strip(bodyTextForTest(lines))
	if !strings.HasPrefix(text, "parked         ") {
		t.Fatal("hold metadata is not aligned with the other fields", text)
	}
	if !strings.Contains(text, "resets in 3h") || len(lines) < 2 || r.Steps[0].Limit.ResetsAt == nil {
		t.Fatal(text)
	}
	for _, l := range lines {
		if ansi.StringWidth(l.Text) > 80 {
			t.Fatal(l.Text)
		}
	}
}

func TestU1DetailSanitizesTerminalText(t *testing.T) {
	r := runner.Run{UID: "run", Job: kata.Job{Name: "Example\x1b[31m title"}, Phase: runner.Held, Reason: "reason\x1b]52;c;bad\a", ActiveStep: "main", Steps: []runner.StepState{{Key: "main", Status: "failed", Note: "first\nsecond\rthird", Result: &runner.ResultRef{Summary: "summary\x1b[2J", Evidence: []string{"/srv/projects/example\x1b]0;bad\a"}}}}}
	text := detailBody(t, r, 80)
	for _, bad := range []string{"\x1b", "\r", "\a", "[2J", "]52;"} {
		if strings.Contains(text, bad) {
			t.Fatal("terminal control escaped", text)
		}
	}
	for _, want := range []string{"Example", "summary", "second", "/srv/projects/example"} {
		if !strings.Contains(text, want) {
			t.Fatal(want, text)
		}
	}
	start := runNow.Add(-time.Hour)
	r = runner.Run{UID: "run", Phase: runner.Running, StartedAt: &start}
	m := newModel(Options{Root: t.TempDir(), Now: func() time.Time { return runNow.In(time.FixedZone("example", 2*60*60)) }})
	m.data.Runs = []runner.Run{r}
	_ = m.pushDetail(viewRun, "", "run")
	if text := ansi.Strip(bodyTextForTest(m.renderRunDetail())); !strings.Contains(text, "2026-10-16 13:00:00") {
		t.Fatal(text)
	}
}

func TestU1DetailPinnedActions(t *testing.T) {
	m, local := fixture(t)
	a, b := &fakeActions{}, &fakeActions{}
	m.opts.Projects = map[string]*Lane{"one": {Service: a}, "two": {Service: b}}
	m.data.Projects = map[string]Data{"pa": {Project: "one"}, "pb": {Project: "two"}}
	r := m.data.Runs[0]
	r.Target.Identity.ProjectUID = "pb"
	r.Phase = runner.Held
	r.IssueUID = "issue"
	m.data.Runs = []runner.Run{r}
	m.selectTab(1)
	send(m, "enter")
	m.query = "hidden"
	copy := r
	copy.UID = "new"
	copy.Target.Identity.ProjectUID = "pa"
	m.data.Runs = append([]runner.Run{copy}, m.data.Runs...)
	m.rebuild()
	for _, s := range []string{"a", "u", "i", "K"} {
		send(m, s)
	}
	if strings.Join(b.calls, ",") != "focus:r1,resume:r1,issue:issue,kata" || len(a.calls) != 0 || len(local.calls) != 0 {
		t.Fatal(a.calls, b.calls, local.calls)
	}
	_, cmd := m.Update(press("a"))
	m.popDetail()
	applyTestCommand(m, cmd)
	if strings.Join(b.calls, ",") != "focus:r1,resume:r1,issue:issue,kata,focus:r1" {
		t.Fatal("late action retargeted", b.calls)
	}
	_ = m.pushDetail(viewRun, "pb", "r1")
	delete(m.data.Projects, "pb")
	m.data.Projects["replacement"] = Data{Project: "two"}
	for _, s := range []string{"a", "u", "i", "K"} {
		send(m, s)
		if !strings.Contains(m.notice.Text, "project pb unavailable") {
			t.Fatal(m.notice.Text)
		}
	}
	if len(b.calls) != 5 {
		t.Fatal("missing lane fell back", b.calls)
	}
}
func TestU1DetailRemoved(t *testing.T) {
	m, f := fixture(t)
	send(m, "3")
	send(m, "enter")
	m.data.Runs = nil
	m.rebuild()
	if text := uncachedView(m); !strings.Contains(text, "run removed") {
		t.Fatal(text)
	}
	for _, s := range []string{"a", "u", "i", "K"} {
		send(m, s)
		if m.notice.Text != "run was removed" {
			t.Fatal(m.notice.Text)
		}
	}
	if len(f.calls) != 0 {
		t.Fatal(f.calls)
	}
	send(m, "h")
	if m.currentView() != viewList {
		t.Fatal(m.nav)
	}
}
func TestU1DetailJobLink(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		m, f := fixture(t)
		m.data.Health = map[string]any{"version": float64(1), "project_uid": "p"}
		m.data.Runs[0].Target.Identity.ProjectUID = "p"
		if !legacy {
			m.data.Jobs[0].ProjectUID = "p"
		}
		m.data.Jobs = m.data.Jobs[:1]
		m.rebuildJobIndex()
		m.rebuild()
		send(m, "l")
		snapshot := "p"
		if m.nav[len(m.nav)-1].projectUID != snapshot {
			t.Fatal(m.nav)
		}
		m.detailKey = detailItemKey("run", "p", "r1")
		send(m, "l")
		if m.currentView() != viewRun {
			t.Fatal(m.nav)
		}
		send(m, "l")
		if len(m.nav) != 2 || m.currentView() != viewJob || len(f.calls) != 0 {
			t.Fatal(m.nav, f.calls)
		}
		send(m, "l")
		m.data.Jobs[0].ProjectUID = "other"
		send(m, "l")
		if m.currentView() != viewRun || !strings.Contains(m.notice.Text, "no longer exists") {
			t.Fatal(m.nav, m.notice.Text)
		}
	}
}
func TestU1DetailKeys(t *testing.T) {
	m := tallRuns(t)
	send(m, "enter")
	for _, pair := range [][2]string{{"ctrl+d", "ctrl+u"}, {"pgdown", "pgup"}} {
		send(m, pair[0])
		if m.scroll != 21 {
			t.Fatal(m.scroll)
		}
		send(m, pair[1])
		if m.scroll != 0 {
			t.Fatal(m.scroll)
		}
	}
	for _, s := range []string{"1", "2", "3", "4", "5", "tab", "shift+tab", "/", " ", "space", "[", "]", "ctrl+b", "ctrl+f", "R", "p", "f", "F", "P", "D", "e", "n", "b"} {
		before := len(m.nav)
		send(m, s)
		if m.currentView() != viewRun || len(m.nav) != before || m.scroll != 0 || m.searching || m.editor != nil || len(m.confirm) > 0 {
			t.Fatal(s, m.nav, m.scroll)
		}
	}
}
func TestU1DetailNavigation(t *testing.T) {
	m := tallRuns(t)
	m.query = "Job"
	m.rebuild()
	m.scroll = 21
	m.follow = false
	send(m, "enter")
	send(m, "l")
	send(m, "h")
	send(m, "h")
	if m.currentView() != viewList || m.scroll != 21 || m.follow || !m.expanded["r1"] || m.query != "Job" {
		t.Fatal(m.nav, m.scroll)
	}
	m.query = ""
	m.selectTab(0)
	send(m, "l")
	send(m, "l")
	m.data.Jobs = nil
	m.rebuildJobIndex()
	m.rebuild()
	send(m, "h")
	if m.currentView() != viewJob || !strings.Contains(uncachedView(m), "deleted in Kata") {
		t.Fatal(m.nav)
	}
	send(m, "h")
	if m.currentView() != viewList {
		t.Fatal(m.nav)
	}
}
func TestU1DetailPinnedFooter(t *testing.T) {
	m := tallRuns(t)
	send(m, "enter")
	m.Update(actionMsg{status: "pinned status"})
	for _, scroll := range []int{0, 100000, 0} {
		m.scroll = scroll
		m.frameValid = false
		text := ansi.Strip(m.viewContent())
		if !strings.Contains(text, "pinned status") || !strings.Contains(text, "M mouse") || !strings.Contains(text, "q quit") || strings.Contains(text, "│ Runs │") || strings.Contains(text, "\n▶ ") {
			t.Fatal(text)
		}
	}
	m.width = 60
	m.frameValid = false
	if text := ansi.Strip(m.viewContent()); !strings.Contains(text, "M mouse") || !strings.Contains(text, "q quit") {
		t.Fatal(text)
	}
}

func TestRunOutcomeAligned(t *testing.T) {
	r := runner.Run{UID: "run", Phase: runner.Settled, Outcome: "succeeded"}
	text := detailBody(t, r, 120)
	if !strings.Contains(text, "\noutcome        succeeded\n") || strings.Contains(text, "(succeeded)") {
		t.Fatal(text)
	}
}

func TestParkedRunPresentation(t *testing.T) {
	r := runner.Run{UID: "run", Phase: runner.Held, Reason: "no_result", ActiveStep: "main", Steps: []runner.StepState{{Key: "main"}}}
	text := detailBody(t, r, 120)
	for _, want := range []string{"outcome        parked", "phase          parked", "parked         no_result"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	m, _ := fixture(t)
	m.data.Runs = []runner.Run{r}
	m.selectTab(1)
	m.query = "parked"
	m.rebuild()
	if text := ansi.Strip(uncachedView(m)); !strings.Contains(text, "parked") || len(m.rows) != 1 {
		t.Fatal(text)
	}
}

func TestQuotaHoldShowsReset(t *testing.T) {
	m, _ := fixture(t)
	reset := time.Date(2026, 9, 24, 17, 23, 0, 0, time.UTC)
	m.data.Runs = []runner.Run{{UID: "01K00000000000000000000000", Phase: runner.Held, Outcome: "unknown", Reason: "agent_quota", ActiveStep: "build", WFDef: &defs.WorkflowDef{Steps: []defs.Step{{Key: "build", Kind: "prompt"}}}, Steps: []runner.StepState{{Key: "build", Attempt: 1, Limit: &agentlimit.Limit{Kind: "agent_quota", Code: "codex:usage_limit_exceeded", ResetsAt: &reset}}}}}
	m.width = 120 // The issue and status columns leave detail room for the reset at 120.
	send(m, "3")
	view := uncachedView(m)
	if !strings.Contains(view, "agent_quota") || !strings.Contains(view, "resets in") || !strings.Contains(view, "parked") {
		t.Fatal(view)
	}
}

func TestHeldDetailLeadsWithDiagnosis(t *testing.T) {
	m := newModel(Options{Root: t.TempDir(), Service: &fakeActions{}})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.data.Runs = []runner.Run{{UID: "held", Phase: runner.Held, Reason: "step_failed", ActiveStep: "review", Steps: []runner.StepState{{Key: "review", Status: "failed", Note: "Inspect digest.md and approve it."}}}}
	send(m, "3")
	send(m, "l")
	text := runDetailText(t, m)
	for _, want := range []string{"failed step", "review #0", "Inspect digest.md and approve it.", "then u resume."} {
		if !strings.Contains(text, want) {
			t.Fatal(want, text)
		}
	}

}

func TestHeldDetailAdmissionRequiresAbandon(t *testing.T) {
	for _, reason := range []string{"admission_rejected"} {
		t.Run(reason, func(t *testing.T) {
			m := newModel(Options{Root: t.TempDir(), Service: &fakeActions{}})
			m.data.Runs = []runner.Run{{UID: "held", Phase: runner.Held, Reason: reason}}
			send(m, "3")
			send(m, "l")
			text := runDetailText(t, m)
			summary, _, _ := strings.Cut(text, "Raw journal:")
			if !strings.Contains(summary, "abandon this run --local") || strings.Contains(summary, "u resume") {
				t.Fatal(summary)
			}
		})
	}
}

// Diagnosis and recovery instructions must survive the default terminal width.
func TestHeldDetailReadableAt80Columns(t *testing.T) {
	for _, reason := range []string{"step_failed", "admission_rejected"} {
		t.Run(reason, func(t *testing.T) {
			m := newModel(Options{Root: t.TempDir(), Service: &fakeActions{}})
			m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			uid := "01K00000000000000000000001"
			note := strings.Repeat("Review ", 18) + "Approval must precede resume."
			m.data.Runs = []runner.Run{{UID: uid, Phase: runner.Held, Reason: reason, ActiveStep: "review", Steps: []runner.StepState{{Key: "review", Status: "failed", Note: note}}}}
			if reason == "admission_rejected" {
				m.data.Runs[0].Steps = nil
			}
			m.data.Running, m.data.WorkerRunning = true, true
			send(m, "3")
			send(m, "l")
			view := uncachedView(m)
			diagnosis := strings.Join(strings.Fields(strings.Split(view, "Raw journal:")[0]), " ")
			if reason == "step_failed" && !strings.Contains(diagnosis, "Approval must precede resume.") {
				t.Fatal("note ending is hidden", view)
			}
			if reason == "admission_rejected" && (strings.Contains(diagnosis, uid) || !strings.Contains(diagnosis, "herdr-kata status --json") || !strings.Contains(diagnosis, "--local") || !strings.Contains(diagnosis, "herdr-kata stop")) {
				t.Fatal("recovery command is incomplete", view)
			}
		})
	}
}
