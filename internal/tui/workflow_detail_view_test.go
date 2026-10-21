package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
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

func openWorkflow(t *testing.T, d Data) *model {
	t.Helper()
	m := workflowModel(t, d)
	send(m, "l")
	return m
}
func TestWorkflowDetailContent(t *testing.T) {
	m := openWorkflow(t, workflowSnapshot())
	m.Update(tea.WindowSizeMsg{Width: 240, Height: 80})
	body := workflowText(m.buildDetail().Body())
	for _, want := range []string{"up to 4 prompts per generation (2 step attempts + 2 extra loop attempts) · cap per job, see used by", "park — exhausted failures hold for u resume", "check ↺ inspect · max 2 · span inspect → check", "1. inspect  prompt shared · retry 0", "2. check  command · retry 0 · ↺ inspect ×2", "3. summarize  prompt fresh", "Used by 1", "Runs 2"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q: %s", want, body)
		}
	}
	if m.detailKey != detailItemKey("step", "project-a", "inspect") {
		t.Fatal("initial scoped step", m.detailKey)
	}
	if strings.Contains(body, "after check") || strings.Contains(body, "after inspect") {
		t.Fatal("redundant dependencies", body)
	}
	for _, width := range []int{80, 120} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		text := ansi.Strip(m.viewContent())
		for _, want := range []string{"back", "move", "step", "open", "run job", "resume", "attach", "edit", "reload", "mouse", "quit"} {
			if !strings.Contains(text, want) {
				t.Fatalf("width %d: missing footer action %s in %s", width, want, text)
			}
		}
	}
}
func TestWorkflowDetailExpansion(t *testing.T) {
	d := workflowSnapshot()
	d.Workflows[0].Definition = json.RawMessage(strings.Replace(workflowF1, "Inspect {{input}}", strings.Repeat("Long example prompt\\n", 100)+"LAST-LINE", 1))
	m := openWorkflow(t, d)
	selected := m.detailKey
	send(m, " ")
	if !m.expanded[selected] {
		t.Fatal("step did not expand")
	}
	for range 20 {
		send(m, "ctrl+d")
	}
	if m.detailKey != selected || m.follow || !strings.Contains(ansi.Strip(m.viewContent()), "LAST-LINE") {
		t.Fatal("expanded text unreachable", m.viewContent())
	}
	send(m, "j")
	if !m.follow || m.detailKey == selected {
		t.Fatal("cursor did not resume following")
	}
	for _, ignored := range []string{"tab", "shift+tab", "1", "2", "3", "4", "5", "/", "[", "]", "ctrl+b", "ctrl+f"} {
		send(m, ignored)
		if m.currentView() != viewWorkflow || m.searching {
			t.Fatal("detail navigation leaked", ignored)
		}
	}
	d.Workflows[0].DefinitionEventUID = "new"
	m.Update(dataMsg{data: d})
	if !m.expanded[selected] {
		t.Fatal("surviving expansion lost")
	}
	send(m, "k")
	send(m, " ")
	if m.expanded[selected] {
		t.Fatal("toggle did not collapse")
	}
}
func TestWorkflowDetailSelectionCap(t *testing.T) {
	d := workflowSnapshot()
	d.Runs = nil
	for i := 40; i > 0; i-- {
		d.Runs = append(d.Runs, runner.Run{UID: fmt.Sprintf("run-%03d", i), Job: kata.Job{UID: "job", Name: "Example job"}, Workflow: &kata.Workflow{UID: "flow"}, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-a"}}, Phase: runner.Held})
	}
	for _, direction := range []string{"k", "j"} {
		for _, restore := range []string{"refresh", "back", "cycle"} {
			t.Run(direction+"/"+restore, func(t *testing.T) {
				m := openWorkflow(t, d)
				items := m.detailItems()
				last := items[len(items)-1]
				m.detailKey, m.detailOrdinal = last.Key, last.Index
				body := m.buildDetail().Body()
				capLine := false
				for _, line := range body {
					if strings.Contains(ansi.Strip(line.Text), "+20 older · Runs lists every retained run") {
						capLine = true
						if line.Hit != nil || len(line.Animations) > 0 {
							t.Fatal("cap became selectable")
						}
					}
				}
				if !capLine {
					t.Fatal("missing cap")
				}
				changed := d
				changed.Runs = append([]runner.Run{{UID: "run-999", Workflow: &kata.Workflow{UID: "flow"}, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-a"}}}}, d.Runs...)
				if restore == "back" {
					m.pushDetail(viewRun, "project-a", "run-040")
				}
				if restore == "cycle" {
					m.pushDetail(viewRun, "project-a", "run-040")
				}
				m.Update(dataMsg{data: changed})
				if restore == "back" {
					m.popDetail()
				}
				if restore == "cycle" {
					m.pushDetail(viewWorkflow, "project-a", "flow")
				}
				if m.detailKey != last.Key {
					t.Fatal("retained capped run lost", m.detailKey, last.Key)
				}
				send(m, direction)
				if m.detailKey != detailItemKey("run", "project-a", "run-022") {
					t.Fatal("cap boundary movement", m.detailKey)
				}
			})
		}
	}
}
func TestWorkflowNavigation(t *testing.T) {
	d := workflowSnapshot()
	m := workflowModel(t, d)
	send(m, "/")
	send(m, "example")
	send(m, "enter")
	floor := m.rows[m.cursor].identity()
	send(m, "l")
	send(m, " ")
	selected := m.detailKey
	send(m, "j")
	send(m, "j")
	send(m, "j")
	send(m, "l")
	if m.currentView() != viewJob {
		t.Fatal("USED BY did not open job")
	}
	m.Update(dataMsg{data: d})
	send(m, "h")
	if m.currentView() != viewWorkflow || !m.expanded[selected] {
		t.Fatal("workflow state not restored")
	}
	send(m, "h")
	if m.currentView() != viewList || m.query != "example" || m.rows[m.cursor].identity() != floor {
		t.Fatal("list floor not restored")
	}
}
func TestWorkflowDetailInvalidAndRemoved(t *testing.T) {
	for _, bad := range []string{"garbage", "unknown:cHJvamVjdC1h:Zmxvdw"} {
		m := openWorkflow(t, workflowSnapshot())
		m.detailKey = bad
		for _, action := range []string{"l", "a", "u", "R"} {
			send(m, action)
			if m.notice.Text != "invalid detail selection" || m.currentView() != viewWorkflow || m.detailKey != bad {
				t.Fatal(action, m.notice.Text, m.detailKey)
			}
		}
	}
	d := workflowSnapshot()
	m := openWorkflow(t, d)
	d.Workflows = nil
	m.Update(dataMsg{data: d})
	if !strings.Contains(ansi.Strip(m.viewContent()), "workflow removed") {
		t.Fatal(m.viewContent())
	}
	send(m, "h")
	if m.currentView() != viewList {
		t.Fatal("back from removed")
	}
}
func TestWorkflowDetailGolden(t *testing.T) {
	for _, name := range []string{"detail", "expanded", "broken", "invalid", "many-runs", "never", "declared", "commands-disabled", "multi", "topology", "f1", "marquee-0", "marquee-7"} {
		t.Run(name, func(t *testing.T) {
			assertFrames(t, "workflow-detail", name, [][2]int{{120, 40}, {80, 24}}, func(w, h int) string {
				d := workflowSnapshot()
				if strings.HasPrefix(name, "marquee-") {
					d.Jobs[0].Name = "ABCDEFGHIJKLMNOPQRSTUVWXYZ job"
					for i := range d.Runs {
						d.Runs[i].Job.Name = "ABCDEFGHIJKLMNOPQRSTUVWXYZ run"
					}
				}
				if name == "f1" {
					d = workflowF1GoldenSnapshot()
				}
				if name == "never" {
					d.Jobs = nil
					d.Runs = nil
				}
				if name == "declared" {
					d.Workflows[0].Definition = json.RawMessage(`{"version":1,"options":{"agent":"other"},"steps":[{"key":"main","kind":"prompt","prompt":"Work","retries":2}]}`)
				}
				if name == "topology" {
					d.Jobs = nil
					d.Runs = nil
					d.Workflows[0].Definition = json.RawMessage(`{"version":1,"steps":[{"key":"same","kind":"prompt","prompt":"First"},{"key":"same","kind":"prompt","prompt":"Second"}]}`)
				}
				if name == "multi" {
					d.Workflows[0].ProjectUID = "project-a"
					d.Workflows[0].Project = "example-project"
					d.Projects = map[string]Data{"project-a": {Project: "example-project"}, "project-b": {Project: "another-project"}}
				}
				if name == "invalid" {
					d.Workflows[0].Definition = json.RawMessage(`{`)
				}
				if name == "broken" {
					d.Workflows[0].Definition = json.RawMessage(strings.Replace(workflowF1, `"park"`, `"retry"`, 1))
				}
				if name == "many-runs" {
					d.Runs = nil
					for i := range 40 {
						d.Runs = append(d.Runs, runner.Run{UID: fmt.Sprintf("retained-%02d", i), Workflow: &kata.Workflow{UID: "flow"}, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-a"}}, Phase: runner.Settled, Outcome: "succeeded"})
					}
				}
				m := workflowModel(t, d)
				if name == "f1" {
					cap := 3
					m.opts.Config.Profiles["cheap"] = config.Profile{MaxRunPrompts: &cap}
				}
				if name == "declared" {
					cap := 4
					m.opts.Config.Profiles["other"] = config.Profile{MaxRunPrompts: &cap}
				}
				if name == "commands-disabled" {
					allowed := false
					m.opts.Config.Commands = &allowed
				}
				if name == "multi" {
					m.opts.Projects = map[string]*Lane{"example-project": {Project: "example-project", ProjectUID: "project-a", Config: workflowTestConfig(), Service: &fakeActions{}}, "another-project": {Project: "another-project", ProjectUID: "project-b", Config: workflowTestConfig(), Service: &fakeActions{}}}
				}
				m.Update(dataMsg{data: d})
				send(m, "l")
				m.Update(tea.WindowSizeMsg{Width: w, Height: h})
				if name == "expanded" {
					send(m, " ")
				}
				if name == "many-runs" {
					for range 25 {
						send(m, "j")
					}
				}
				if strings.HasPrefix(name, "marquee-") {
					m.frame = 0
					if name == "marquee-7" {
						m.frame = 7
					}
					m.scroll, m.follow, m.frameValid = 16, false, false
				}
				return m.viewContent()
			})
		})
	}
}

// Keep fixtures' expected run ordering explicit and independent of timestamps.
func TestWorkflowDetailStepLabels(t *testing.T) {
	c := workflowProblems(json.RawMessage(workflowF1), workflowTestConfig())
	labels := []string{}
	for i := range 3 {
		labels = append(labels, workflowStepLabel(c, i))
	}
	if !slices.Equal(labels, []string{"1. inspect  prompt shared · retry 0", "2. check  command · retry 0 · ↺ inspect ×2", "3. summarize  prompt fresh"}) {
		t.Fatal(labels)
	}
}

func TestWorkflowDetailExpansionAfterRemoval(t *testing.T) {
	d := workflowSnapshot()
	d.Workflows[0].DefinitionEventUID = "one"
	d.Workflows[0].Definition = json.RawMessage(`{"version":1,"steps":[{"key":"main","kind":"prompt","prompt":"Preview\nEXPANDED-ONLY"}]}`)
	m := openWorkflow(t, d)
	send(m, " ")
	if !strings.Contains(ansi.Strip(m.viewContent()), "EXPANDED-ONLY") {
		t.Fatal("not expanded")
	}
	original := d.Workflows[0].Definition
	d.Workflows[0].DefinitionEventUID = "two"
	d.Workflows[0].Definition = json.RawMessage(`{"version":1,"steps":[]}`)
	m.Update(dataMsg{data: d})
	d.Workflows[0].DefinitionEventUID = "three"
	d.Workflows[0].Definition = original
	m.Update(dataMsg{data: d})
	if strings.Contains(ansi.Strip(m.viewContent()), "EXPANDED-ONLY") {
		t.Fatal("removed step retained its expansion", m.viewContent())
	}
}

func TestWorkflowActions(t *testing.T) {
	m, a, b := projectBoard(t)
	d := m.data
	for i := range d.Workflows {
		d.Workflows[i].Definition = json.RawMessage(workflowF1)
	}
	for i := range d.Jobs {
		d.Jobs[i].Definition = json.RawMessage(`{"enabled":true,"action":{"kind":"execute","workflow_uid":"same-flow"}}`)
	}
	d.Runs = []runner.Run{{UID: "new-done", Job: kata.Job{UID: "same-job", Name: "Alpha"}, Workflow: &kata.Workflow{UID: "same-flow"}, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-a"}}, Phase: runner.Settled, Outcome: "succeeded"}, {UID: "held-old", Job: kata.Job{UID: "same-job", Name: "Alpha"}, Workflow: &kata.Workflow{UID: "same-flow"}, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-a"}}, Phase: runner.Held, Herdr: runner.HerdrRef{PaneID: "pane"}}}
	for _, o := range m.opts.Projects {
		o.Config = workflowTestConfig()
	}
	m.Update(dataMsg{data: d})
	send(m, "4")
	send(m, "u")
	if m.notice.Text != "Resume requires a parked run · latest run of Alpha flow is succeeded (Alpha)" || len(a.calls)+len(b.calls) != 0 {
		t.Fatal("latest fell back to held", m.notice.Text, a.calls, b.calls)
	}
	send(m, "l")
	m.detailKey = detailItemKey("run", "project-a", "held-old")
	m.Update(struct{}{})
	send(m, "ctrl+d")
	send(m, "u")
	if !slices.Equal(a.calls, []string{"resume:held-old"}) || len(b.calls) != 0 || !strings.Contains(m.notice.Text, "resume requested · Alpha · project example-project") {
		t.Fatal("selected held target", a.calls, b.calls, m.notice.Text)
	}
	send(m, "a")
	if !slices.Equal(a.calls, []string{"resume:held-old", "focus:held-old"}) {
		t.Fatal(a.calls)
	}
	m.detailKey = detailItemKey("used-by", "project-a", "same-job")
	m.Update(struct{}{})
	_, cmd := m.Update(press("R"))
	if cmd == nil {
		t.Fatal("no RunNow command", m.notice.Text)
	}
	// Refresh and select the other lane before executing the captured command.
	d.Jobs[0].Name = "changed"
	m.Update(dataMsg{data: d})
	m.pushDetail(viewWorkflow, "project-b", "same-flow")
	applyTestCommand(m, cmd)
	if !slices.Equal(a.calls, []string{"resume:held-old", "focus:held-old", "run:same-job"}) || len(b.calls) != 0 || !strings.Contains(m.notice.Text, "run requested · Alpha · project example-project") {
		t.Fatal("action retargeted", a.calls, b.calls, m.notice.Text)
	}
}
func TestWorkflowActionsRefusals(t *testing.T) {
	for _, tc := range []struct {
		name               string
		owner              defs.Owner
		supported, enabled bool
		reasons            []string
		want               string
	}{
		{"native", defs.Native, true, true, nil, "Kata owns native delivery and has no force-fire"},
		{"unsupported", defs.Adapter, false, true, []string{"unsupported input"}, "unsupported input"},
		{"paused", defs.Ignored, true, false, nil, "Job is paused; resume it before running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := workflowSnapshot()
			d.Jobs[0].Owner = tc.owner
			d.Jobs[0].Supported = tc.supported
			d.Jobs[0].Reasons = tc.reasons
			if !tc.enabled {
				d.Jobs[0].Definition = json.RawMessage(`{"enabled":false,"action":{"kind":"execute","workflow_uid":"flow"}}`)
			}
			m := openWorkflow(t, d)
			f := &fakeActions{}
			m.opts.Service = f
			m.opts.Service = f
			m.detailKey = detailItemKey("used-by", "project-a", "job")
			m.Update(struct{}{})
			send(m, "R")
			if m.notice.Text != tc.want || len(f.calls) != 0 {
				t.Fatal(m.notice.Text, f.calls)
			}
		})
	}
	d := workflowSnapshot()
	m := workflowModel(t, d)
	f := &fakeActions{}
	m.opts.Service = f
	m.opts.Service = f
	send(m, "R")
	if m.notice.Text != "R runs a job: l opens this workflow, then R on a used by job" {
		t.Fatal(m.notice.Text)
	}
	send(m, "l")
	send(m, "R")
	if m.notice.Text != "R runs a job: select one under used by" {
		t.Fatal(m.notice.Text)
	}
	send(m, "a")
	if m.notice.Text != "select a run to attach" {
		t.Fatal(m.notice.Text)
	}
	for _, s := range []string{"n", "D", "p", "f", "F", "P"} {
		send(m, s)
	}
	if len(f.calls) != 0 {
		t.Fatal("inert keys wrote", f.calls)
	}
}
func TestWorkflowActionsLegacyAndBusy(t *testing.T) {
	d := workflowSnapshot()
	d.Runs = d.Runs[:1]
	m := workflowModel(t, d)
	f := &fakeActions{}
	m.opts.Service = f
	_, cmd := m.Update(press("u"))
	if cmd == nil {
		t.Fatal(m.notice.Text)
	}
	_, again := m.Update(press("u"))
	if again != nil || m.notice.Text != "Working; wait for the current action to finish" {
		t.Fatal("busy duplicate", m.notice.Text)
	}
	applyTestCommand(m, cmd)
	if !slices.Equal(f.calls, []string{"resume:run-b"}) {
		t.Fatal(f.calls)
	}
	d.Health = nil
	m.Update(dataMsg{data: d})
	send(m, "u")
	if m.notice.Text != "Example has never run" || len(f.calls) != 1 {
		t.Fatal("unproven pin resumed", m.notice.Text, f.calls)
	}
}

func TestWorkflowEditorCancelledWhileOpening(t *testing.T) {
	m := openWorkflow(t, workflowSnapshot())
	m.opts.Service = &fakeActions{}
	_, open := m.Update(press("e"))
	if open == nil {
		t.Fatal("editor was not requested")
	}
	send(m, "esc")
	msg := open().(newEditorMsg)
	form, ok := msg.editor.(*workflowForm)
	if !ok {
		t.Fatal(msg.editor)
	}
	if _, err := os.Stat(form.path); err != nil {
		t.Fatal("draft was not created", err)
	}
	m.Update(msg)
	if _, err := os.Stat(form.path); !os.IsNotExist(err) || m.editor != nil || m.openingEditor || m.currentView() != viewWorkflow {
		t.Fatal("late draft was not cleaned up", err, m.viewContent())
	}
}

func TestWorkflowActionsEditorSize(t *testing.T) {
	m := openWorkflow(t, workflowSnapshot())
	m.opts.Service = &fakeActions{}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	selected := m.detailKey
	send(m, "e")
	form, ok := m.editor.(*workflowForm)
	if !ok {
		t.Fatal(m.viewContent())
	}
	t.Cleanup(func() { _ = form.Close() })
	if text := m.viewContent(); len(strings.Split(text, "\n")) != 40 {
		t.Fatal("initial form height", text)
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	text := m.viewContent()
	if len(strings.Split(text, "\n")) != 24 {
		t.Fatal("resized form height", text)
	}
	for line := range strings.SplitSeq(text, "\n") {
		if ansi.StringWidth(line) > 80 {
			t.Fatal("resized form width", line)
		}
	}
	m.Update(editorClosedMsg{Message: "workflow edit cancelled"})
	if m.currentView() != viewWorkflow || m.detailKey != selected {
		t.Fatal("form lost the caller", m.viewContent())
	}
}

func TestWorkflowActionsCappedTarget(t *testing.T) {
	d := workflowSnapshot()
	d.Runs = nil
	for i := 40; i > 0; i-- {
		d.Runs = append(d.Runs, runner.Run{UID: fmt.Sprintf("run-%03d", i), Workflow: &kata.Workflow{UID: "flow"}, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-a"}}, Phase: runner.Held, Herdr: runner.HerdrRef{PaneID: "pane"}})
	}
	m := openWorkflow(t, d)
	f := &fakeActions{}
	m.opts.Service = f
	m.opts.Service = f
	m.detailKey = detailItemKey("run", "project-a", "run-001")
	m.Update(struct{}{})
	send(m, "ctrl+d")
	send(m, "u")
	send(m, "a")
	if !slices.Equal(f.calls, []string{"resume:run-001", "focus:run-001"}) {
		t.Fatal("capped target changed", f.calls)
	}
	send(m, "l")
	if m.currentView() != viewRun || m.nav[len(m.nav)-1].uid != "run-001" {
		t.Fatal("capped run did not open", m.nav)
	}
}
func TestWorkflowActionsLaneUnavailable(t *testing.T) {
	d := workflowSnapshot()
	d.Runs = d.Runs[:1]
	m := workflowModel(t, d)
	f := &fakeActions{}
	m.opts.Projects = map[string]*Lane{"example-project": {Project: "example-project", ProjectUID: "wrong-project", Service: f}}
	d.Workflows[0].Project = "example-project"
	d.Jobs[0].Project = "example-project"
	m.Update(dataMsg{data: d})
	for _, s := range []string{"u", "e"} {
		send(m, s)
	}
	if len(f.calls) != 0 || m.editor != nil || m.openingEditor || m.notice.Text != "workflow editing is unavailable in this lane" {
		t.Fatal(f.calls, m.notice.Text)
	}
	send(m, "l")
	m.detailKey = detailItemKey("used-by", "project-a", "job")
	m.Update(struct{}{})
	send(m, "R")
	if len(f.calls) != 0 || m.notice.Text != "this board cannot run jobs" {
		t.Fatal(f.calls, m.notice.Text)
	}
}

func TestWorkflowDetailSpaceOnlyExpandsSteps(t *testing.T) {
	for _, key := range []string{detailItemKey("used-by", "project-a", "job"), detailItemKey("run", "project-a", "run-b")} {
		m := openWorkflow(t, workflowSnapshot())
		m.detailKey = key
		m.Update(struct{}{})
		send(m, " ")
		if m.currentView() != viewWorkflow || m.detailKey != key {
			t.Fatal("space activated a linked record", m.nav, m.detailKey)
		}
	}
}
func TestWorkflowNavigationHidesFloorFilter(t *testing.T) {
	m := workflowModel(t, workflowSnapshot())
	send(m, "/")
	send(m, "example")
	send(m, "enter")
	send(m, "l")
	if strings.Contains(ansi.Strip(m.viewContent()), "filter:") {
		t.Fatal("detail shows the list search UI", m.viewContent())
	}
	send(m, "h")
	if !strings.Contains(ansi.Strip(m.viewContent()), "filter: example") {
		t.Fatal("floor filter not restored")
	}
}
func TestWorkflowRowsDisplayedNoteSearch(t *testing.T) {
	d := workflowSnapshot()
	d.Runs = d.Runs[:1]
	d.Runs[0].Reason = "needs review"
	d.Runs[0].WFDef = &defs.WorkflowDef{}
	d.Runs[0].Steps = []runner.StepState{{Key: "not-a-displayed-note", Status: "pending"}}
	m := workflowModel(t, d)
	m.query = "not-a-displayed-note"
	m.rebuild()
	if len(m.rows) != 0 {
		t.Fatal("search includes a hidden progress note")
	}
	m.query = "waiting: needs review"
	m.rebuild()
	if len(m.rows) != 1 {
		t.Fatal("displayed note not searchable")
	}
}

func TestWorkflowCrossScreenAcceptance(t *testing.T) {
	for _, path := range []string{"job-run", "run-job"} {
		t.Run(path, func(t *testing.T) {
			d := workflowSnapshot()
			for i := range d.Runs {
				d.Runs[i].Job = kata.Job{UID: "job", Name: "Uses example"}
			}
			m := workflowModel(t, d)
			f := &fakeActions{}
			m.opts.Service = f
			m.opts.Service = f
			send(m, "/")
			send(m, "example")
			send(m, "enter")
			floor := m.rows[m.cursor].identity()
			send(m, "l")
			send(m, " ")
			step := m.detailKey
			target := detailItemKey("used-by", "project-a", "job")
			if path == "run-job" {
				target = detailItemKey("run", "project-a", "run-c")
			}
			m.detailKey = target
			m.Update(struct{}{})
			send(m, "l")
			m.Update(dataMsg{data: d})
			if path == "job-run" {
				m.detailKey = detailItemKey("run", "project-a", "run-c")
				m.Update(struct{}{})
			}
			send(m, "l")
			m.Update(dataMsg{data: d})
			send(m, "h")
			m.Update(dataMsg{data: d})
			send(m, "h")
			if m.currentView() != viewWorkflow || m.detailKey != target || !m.expanded[step] {
				t.Fatal("round trip lost workflow state", m.nav, m.detailKey)
			}
			if len(f.calls) != 0 {
				t.Fatal("linked local details fetched Kata", f.calls)
			}
			send(m, "h")
			if m.currentView() != viewList || m.query != "example" || m.rows[m.cursor].identity() != floor {
				t.Fatal("floor lost")
			}
		})
	}
}

type workflowCountPoll struct {
	calls int
	data  Data
}

func (p *workflowCountPoll) Poll() (Data, error) { p.calls++; return p.data, nil }
func TestWorkflowNoIOOnRender(t *testing.T) {
	d := workflowSnapshot()
	m := workflowModel(t, d)
	f := &fakeActions{}
	m.opts.Service = f
	m.opts.Service = f
	p := &workflowCountPoll{data: d}
	m.cache = p
	for range 10 {
		m.viewContent()
		m.Update(struct{}{})
		m.Update(marqueeTickMsg{generation: m.marqueeGeneration})
	}
	send(m, "/")
	send(m, "example")
	send(m, "enter")
	send(m, "l")
	send(m, " ")
	for range 10 {
		m.viewContent()
		m.Update(struct{}{})
		send(m, "j")
		send(m, "k")
	}
	if len(f.calls) != 0 || p.calls != 0 {
		t.Fatal("navigation/render did I/O", f.calls, p.calls)
	}
	send(m, "r")
	if len(f.calls) != 0 || p.calls != 1 {
		t.Fatal("reload read Kata or skipped local snapshot", f.calls, p.calls)
	}
}
func TestWorkflowFrameMatrix(t *testing.T) {
	d := workflowSnapshot()
	d.Workflows[0].Name = "界 example \x1b[31mred\x1b[0m\u202e"
	for _, detail := range []bool{false, true} {
		for _, width := range []int{60, 80, 100, 120, 125, 126, 142, 143, 160, 200} {
			for _, height := range []int{1, 2, 8, 24, 40} {
				m := workflowModel(t, d)
				if detail {
					send(m, "l")
				}
				m.Update(tea.WindowSizeMsg{Width: width, Height: height})
				view := m.viewContent()
				lines := strings.Split(view, "\n")
				if len(lines) != height {
					t.Fatal("height", height, len(lines))
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > width {
						t.Fatal("width", width, line)
					}
				}
				if strings.Contains(ansi.Strip(view), "[31m") || strings.ContainsRune(view, '\u202e') {
					t.Fatal("external control escaped")
				}
				for y, hit := range m.hits.Rows {
					if y < 0 || y >= height || hit.X0 < 0 || hit.X1 > width || hit.X0 >= hit.X1 {
						t.Fatal("clipped hit survived", height, width, y, hit)
					}
				}
				if detail && height >= 24 {
					if lines[len(lines)-1] == "" {
						t.Fatal("help unpinned")
					}
				}
			}
		}
	}
}

func TestWorkflowLinkedRunModel(t *testing.T) {
	for _, args := range [][]string{{"--model", "example-model"}, {"-m", "example-model"}, {"-c", "model=example-model"}} {
		d := workflowSnapshot()
		d.Runs[0].Profile = config.Profile{Kind: "codex", Args: args}
		m := openWorkflow(t, d)
		m.detailKey = detailItemKey("run", "project-a", "run-b")
		m.Update(struct{}{})
		send(m, "l")
		text := strings.Join(strings.Fields(workflowText(m.renderRunDetail())), " ")
		if !strings.Contains(text, "model example-model") {
			t.Fatal("linked run lost profile model", args, text)
		}
	}
}

func TestWorkflowDetailAgentAndBudgets(t *testing.T) {
	for _, tc := range []struct{ raw, agent, prompts string }{
		{`{"version":1,"options":{"agent":"other"},"steps":[{"key":"main","kind":"prompt","prompt":"Work","retries":2}]}`, "profile other (declared by the workflow)", "up to 3 prompts per generation (3 step attempts + 0 extra loop attempts) · cap 4"},
		{`{"version":1,"options":{"on_failure":"park"},"steps":[{"key":"make","kind":"prompt","prompt":"Make","options":{"agent_mode":"fresh"}},{"key":"verify","kind":"prompt","prompt":"Verify","after":["make"],"options":{"on_fail":{"goto":"make","max_loops":2}}}]}`, "each job's profile", "up to 6 prompts per generation (2 step attempts + 4 extra loop attempts) · cap per job, see used by"},
	} {
		d := workflowSnapshot()
		d.Workflows[0].Definition = json.RawMessage(tc.raw)
		m := workflowModel(t, d)
		cap := 4
		m.opts.Config.Profiles["other"] = config.Profile{MaxRunPrompts: &cap}
		m.Update(dataMsg{data: d})
		send(m, "l")
		m.Update(tea.WindowSizeMsg{Width: 240, Height: 80})
		text := workflowText(m.buildDetail().Body())
		if !strings.Contains(text, tc.agent) || !strings.Contains(text, tc.prompts) {
			t.Fatal(text)
		}
	}
}
func TestWorkflowDetailPausedUnsupportedInput(t *testing.T) {
	d := workflowSnapshot()
	d.Jobs[0].Name = "manual-inspect"
	d.Jobs[0].Supported = false
	d.Jobs[0].Owner = defs.Ignored
	d.Jobs[0].Reasons = []string{"prompt_budget 4 > 3"}
	d.Jobs[0].Definition = json.RawMessage(`{"enabled":false,"action":{"kind":"execute","workflow_uid":"flow"},"trigger":{"kind":"manual"},"timeout_seconds":1800,"options":{"agent":"cheap"}}`)
	m := workflowModel(t, d)
	cap := 3
	m.opts.Config.Profiles["cheap"] = config.Profile{MaxRunPrompts: &cap}
	m.Update(dataMsg{data: d})
	send(m, "l")
	m.Update(tea.WindowSizeMsg{Width: 240, Height: 80})
	text := strings.Join(strings.Fields(workflowText(m.buildDetail().Body())), " ")
	if !strings.Contains(text, "manual-inspect paused manual cheap 4/3 prompt_budget 4 > 3 30m0s") {
		t.Fatal(text)
	}
}
func TestWorkflowDetailProjectScopedRuns(t *testing.T) {
	m, a, b := projectBoard(t)
	d := m.data
	for i := range d.Workflows {
		d.Workflows[i].Definition = json.RawMessage(workflowF1)
	}
	d.Runs = []runner.Run{{UID: "same-run", Workflow: &kata.Workflow{UID: "same-flow"}, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-a"}}, Phase: runner.Held}, {UID: "same-run", Workflow: &kata.Workflow{UID: "same-flow"}, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-b"}}, Phase: runner.Settled, Outcome: "succeeded"}}
	m.Update(dataMsg{data: d})
	send(m, "4")
	send(m, "l")
	m.detailKey = detailItemKey("run", "project-b", "same-run")
	m.Update(struct{}{})
	send(m, "u")
	if m.notice.Text != "invalid detail selection" || len(a.calls)+len(b.calls) != 0 {
		t.Fatal("cross-project key accepted", m.notice.Text, a.calls, b.calls)
	}
	m.detailKey = detailItemKey("run", "project-a", "same-run")
	m.Update(struct{}{})
	send(m, "u")
	if !slices.Equal(a.calls, []string{"resume:same-run"}) || len(b.calls) != 0 {
		t.Fatal(a.calls, b.calls)
	}
}

func TestWorkflowDetailUnavailableFacts(t *testing.T) {
	d := workflowSnapshot()
	d.Workflows[0].Project = "example-project"
	d.Jobs[0].Project = "example-project"
	d.Jobs[0].Definition = json.RawMessage(`{"enabled":true,"action":{"kind":"execute","workflow_uid":"flow"},"timeout_seconds":1800,"options":{"agent":"other"}}`)
	m := workflowModel(t, d)
	m.opts.Projects = map[string]*Lane{"example-project": {Project: "example-project", ProjectUID: "different-project"}}
	m.Update(dataMsg{data: d})
	send(m, "l")
	m.Update(tea.WindowSizeMsg{Width: 240, Height: 80})
	text := strings.Join(strings.Fields(workflowText(m.buildDetail().Body())), " ")
	for _, fact := range []string{"lane unavailable", "failure —", "agent —", "prompts —", "timeout —", "commands —", "30m0s"} {
		if !strings.Contains(text, fact) {
			t.Fatal("unavailable lane fact", fact, text)
		}
	}
	if strings.Contains(text, "unmapped profile") || strings.Contains(text, "each job's profile") {
		t.Fatal("unavailable lane invented profile facts", text)
	}
}

func TestWorkflowUsedByAgreesWithJobDetail(t *testing.T) {
	for _, tc := range []struct {
		name, options string
		timeout       int
		malformed     bool
	}{
		{name: "defaults", options: `{}`},
		{name: "explicit", options: `{"agent":"other","input":"explicit input"}`, timeout: 1800},
		{name: "malformed agent", options: `{"agent":3,"input":"preserved input"}`, malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := workflowSnapshot()
			d.Jobs[0].Definition = json.RawMessage(fmt.Sprintf(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","workflow_uid":"flow"},"timeout_seconds":%d,"options":%s}`, tc.timeout, tc.options))
			if tc.malformed {
				d.Jobs[0].Supported = false
				d.Jobs[0].Reasons = []string{"options.agent must name a profile"}
			}
			m := workflowModel(t, d)
			m.opts.Config.Limits.DefaultTimeout = 3 * time.Hour
			cap := 4
			m.opts.Config.Profiles["other"] = config.Profile{MaxRunPrompts: &cap}
			m.Update(dataMsg{data: d})
			m.Update(tea.WindowSizeMsg{Width: 240, Height: 80})
			send(m, "l")
			j := m.jobs.byKey[jobKey{"project-a", "job"}]
			v := m.wfIndex[workflowKey{"project-a", "flow"}]
			joined := v.jobs[0]
			cfg := m.jobConfig(j.entry)
			expectedCap, expectedTimeout := defs.EffectiveCap(cfg, j.profile), jobTimeout(j, cfg)
			if joined.profile != j.profile || joined.input != j.input || joined.timeout != expectedTimeout || joined.cap != expectedCap {
				t.Fatal("USED BY diverged from indexed job facts", joined, j)
			}
			workflowBody := strings.Join(strings.Fields(workflowText(m.buildWorkflowDetail().Body())), " ")
			input := "—"
			if j.input != "" {
				input = fmt.Sprintf("%q", j.input)
			}
			if tc.malformed {
				input = strings.Join(j.entry.Reasons, "; ")
			}
			want := fmt.Sprintf("manual %s 4/%d %s %s", j.profile, expectedCap, input, expectedTimeout)
			if !strings.Contains(workflowBody, want) {
				t.Fatal(want, workflowBody)
			}
			m.pushDetail(viewJob, "project-a", "job")
			jobText := strings.Join(strings.Fields(workflowText(m.buildJobDetail().Body())), " ")
			for _, fact := range []string{"profile " + j.profile, "timeout " + expectedTimeout.String(), fmt.Sprintf("prompts 4 planned / %d cap", expectedCap)} {
				if !strings.Contains(jobText, fact) {
					t.Fatal("S4 fact missing", fact, jobText)
				}
			}
			if j.input != "" && !strings.Contains(jobText, "input "+j.input) {
				t.Fatal("S4 input differs", jobText)
			}
		})
	}
}

func TestWorkflowDetailTablesMarquee(t *testing.T) {
	for _, kind := range []string{"used-by", "run"} {
		for _, selected := range []bool{false, true} {
			d := workflowSnapshot()
			d.Jobs[0].Name = "ABCDEFGHIJKLMNOPQRSTUVWXYZ job"
			d.Runs[0].Job.Name = "ABCDEFGHIJKLMNOPQRSTUVWXYZ run"
			d.Runs[1].Job.Name = "ABCDEFGHIJKLMNOPQRSTUVWXYZ run"
			m := openWorkflow(t, d)
			m.Update(tea.WindowSizeMsg{Width: 80, Height: 100})
			if selected {
				for _, item := range m.detailItems() {
					if item.Kind == kind {
						m.detailKey, m.detailOrdinal = item.Key, item.Index
						break
					}
				}
			}
			m.frame = 0
			m.Update(struct{}{})
			if !m.marqueePending || !m.rendered.Animates {
				t.Fatalf("%s selected=%t: no visible timer", kind, selected)
			}
			rowText := func() string {
				t.Helper()
				for _, line := range m.buildDetail().Body() {
					if line.Hit != nil && line.Hit.Kind == kind {
						if len(line.Animations) == 0 {
							t.Fatalf("%s: missing animation span", kind)
						}
						return ansi.Strip(line.Text)
					}
				}
				t.Fatal("missing table row")
				return ""
			}
			before := rowText()
			if strings.Contains(before, "…") {
				t.Fatalf("%s truncates: %q", kind, before)
			}
			for range 6 {
				m.Update(marqueeTickMsg{generation: m.marqueeGeneration})
			}
			if rowText() != before {
				t.Fatal("marquee skipped initial hold")
			}
			m.Update(marqueeTickMsg{generation: m.marqueeGeneration})
			after := rowText()
			if !strings.Contains(before, "ABCDEFGHIJKLMNOPQR") || !strings.Contains(after, "BCDEFGHIJKLMNOPQRS") || before == after {
				t.Fatalf("%s did not advance: %q -> %q", kind, before, after)
			}
			m.follow = false
			m.scroll = 0
			m.Update(tea.WindowSizeMsg{Width: 80, Height: 4})
			if m.marqueePending || m.rendered.Animates {
				t.Fatal("hidden detail table kept timer alive")
			}
		}
	}
}

func TestQuotaRunNoteAcrossViews(t *testing.T) {
	d := workflowSnapshot()
	d.Runs = d.Runs[:1]
	reset := time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)
	r := &d.Runs[0]
	r.Job = kata.Job{UID: "job", Name: "Uses example"}
	r.Reason, r.ActiveStep = "agent_quota", "inspect"
	r.Steps = []runner.StepState{{Key: "inspect", Limit: &agentlimit.Limit{ResetsAt: &reset}}}
	m := workflowModel(t, d)
	m.width, m.height = 240, 80
	v := m.wfIndex[workflowKey{"project-a", "flow"}]
	j := m.jobs.byKey[jobKey{"project-a", "job"}]
	views := map[string]string{
		"workflow inspector": workflowText(m.workflowInspector(v, 100)),
		"job inspector":      workflowText(m.renderJobInspector(j, 100)),
		"workflow search":    v.search,
	}
	m.selectTab(1)
	views["runs list"] = cellText(t, m.runsColumns(), m.runCells(m.rows[0]), "detail")
	m.pushDetail(viewWorkflow, "project-a", "flow")
	views["workflow detail"] = workflowText(m.buildWorkflowDetail().Body())
	m.pushDetail(viewJob, "project-a", "job")
	views["job detail"] = workflowText(m.buildJobDetail().Body())
	for name, text := range views {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(strings.Join(strings.Fields(text), " "), "waiting: agent_quota · resets in 3h 0m") {
				t.Fatal("missing quota reset note:", text)
			}
		})
	}
}

// A stricter step profile cap bounds every job that uses the workflow.
func TestWorkflowDetailStepProfileCap(t *testing.T) {
	d := workflowSnapshot()
	d.Workflows[0].Definition = json.RawMessage(`{"version":1,"options":{"agent":"other"},"steps":[{"key":"make","kind":"prompt","prompt":"Make"},{"key":"check","kind":"prompt","prompt":"Check","after":["make"],"options":{"agent":"checker"}}]}`)
	m := workflowModel(t, d)
	cap := 2
	m.opts.Config.Profiles["checker"] = config.Profile{MaxRunPrompts: &cap}
	m.Update(dataMsg{data: d})
	send(m, "l")
	m.Update(tea.WindowSizeMsg{Width: 240, Height: 80})
	text := strings.Join(strings.Fields(workflowText(m.buildDetail().Body())), " ")
	if !strings.Contains(text, "up to 2 prompts per generation (2 step attempts + 0 extra loop attempts) · cap 2") || !strings.Contains(text, "Uses example enabled manual other 2/2") {
		t.Fatal(text)
	}
	m.pushDetail(viewJob, "project-a", "job")
	if text := strings.Join(strings.Fields(workflowText(m.buildJobDetail().Body())), " "); !strings.Contains(text, "prompts 2 planned / 2 cap") {
		t.Fatal(text)
	}
	v := m.jobs.byKey[jobKey{"project-a", "job"}]
	if text := strings.Join(strings.Fields(workflowText(m.renderJobInspector(v, 64))), " "); !strings.Contains(text, "prompts 2/2") {
		t.Fatal(text)
	}
}
