package tui

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

type runLabelCase struct {
	Name                                 string
	Run                                  runner.Run
	Label, LabelRole, Report, ReportRole string
}

func runLabelFixture() []runLabelCase {
	return []runLabelCase{
		{Name: "zero", Label: "never", LabelRole: "Subtle", ReportRole: "Subtle"},
		{Name: "held phase wins", Run: runner.Run{Phase: runner.Held, Outcome: "failed"}, Label: "parked", LabelRole: "Attention", ReportRole: "Subtle"},
		{Name: "blocked", Run: runner.Run{Phase: runner.Blocked}, Label: "blocked", LabelRole: "Attention", ReportRole: "Subtle"},
		{Name: "needs-human", Run: runner.Run{Phase: runner.HandedOff}, Label: "needs-human", LabelRole: "Attention", ReportRole: "Subtle"},
		{Name: "running", Run: runner.Run{Phase: runner.Running, Outcome: "succeeded"}, Label: "running", LabelRole: "Open", ReportRole: "Subtle"},
		{Name: "prepared", Run: runner.Run{Phase: runner.Prepared}, Label: "queued", LabelRole: "Open", ReportRole: "Subtle"},
		{Name: "admitted", Run: runner.Run{Phase: runner.Admitted}, Label: "queued", LabelRole: "Open", ReportRole: "Subtle"},
		{Name: "succeeded", Run: runner.Run{Phase: runner.Settled, Outcome: "succeeded"}, Label: "succeeded", LabelRole: "Closed", ReportRole: "Subtle"},
		{Name: "failed", Run: runner.Run{Phase: runner.Settled, Outcome: "failed"}, Label: "failed", LabelRole: "Error", ReportRole: "Subtle"},
		{Name: "unknown", Run: runner.Run{Phase: runner.Settled, Outcome: "unknown"}, Label: "unknown", LabelRole: "Attention", ReportRole: "Subtle"},
		{Name: "cancelled", Run: runner.Run{Phase: runner.Settled, Outcome: "cancelled"}, Label: "cancelled", LabelRole: "Closed", ReportRole: "Subtle"},
		{Name: "unrecognized", Run: runner.Run{Phase: "future"}, Label: "unknown", LabelRole: "Attention", ReportRole: "Subtle"},
		{Name: "unrecognized outcome", Run: runner.Run{Phase: runner.Settled, Outcome: "future"}, Label: "unknown", LabelRole: "Attention", ReportRole: "Subtle"},
		{Name: "head inspect", Run: runner.Run{Ops: []runner.Op{{Done: true, Inspect: "old"}, {Inspect: "head"}, {Inspect: "later"}}}, Label: "never", LabelRole: "Subtle", Report: "inspect: head", ReportRole: "Attention"},
		{Name: "later inspect ignored", Run: runner.Run{Ops: []runner.Op{{}, {Inspect: "later"}}}, Label: "never", LabelRole: "Subtle", Report: "reporting…", ReportRole: "Subtle"},
		{Name: "pending after done", Run: runner.Run{Ops: []runner.Op{{Done: true}, {}}}, Label: "never", LabelRole: "Subtle", Report: "reporting…", ReportRole: "Subtle"},
		{Name: "all done", Run: runner.Run{Ops: []runner.Op{{Done: true, Inspect: "old"}}}, Label: "never", LabelRole: "Subtle", ReportRole: "Subtle"},
		{Name: "run inspect all done", Run: runner.Run{Inspect: "run", Ops: []runner.Op{{Done: true}}}, Label: "never", LabelRole: "Subtle", Report: "inspect: run", ReportRole: "Attention"},
		{Name: "run inspect precedence", Run: runner.Run{Inspect: "run", Ops: []runner.Op{{Inspect: "op"}}}, Label: "never", LabelRole: "Subtle", Report: "inspect: run", ReportRole: "Attention"},
		{Name: "signing precedence", Run: runner.Run{Inspect: "run", Ops: []runner.Op{{Done: true}, {SigningRefused: true, Inspect: "op"}}}, Label: "never", LabelRole: "Subtle", Report: "signing refused", ReportRole: "Fail"},
	}
}

func TestRunLabelFixtureIsolation(t *testing.T) {
	a, b := runLabelFixture(), runLabelFixture()
	for i := range a {
		if len(a[i].Run.Ops) > 0 {
			a[i].Run.Ops[0].Inspect = "changed"
			if b[i].Run.Ops[0].Inspect == "changed" {
				t.Fatal("ops shared")
			}
		}
	}
	a[0].Label = "changed"
	if b[0].Label == "changed" {
		t.Fatal("cases shared")
	}
}

type fakeActions struct {
	unsupportedService
	calls       []string
	mutationErr error
}

func (f *fakeActions) Pause(j kata.Job) (kata.Job, error) {
	f.calls = append(f.calls, "pause:"+j.UID)
	return savedPauseJob(j), f.mutationErr
}

func (f *fakeActions) Delete(j kata.Job) error {
	f.calls = append(f.calls, "delete:"+j.UID)
	return nil
}

func (f *fakeActions) Focus(r runner.Run) error {
	f.calls = append(f.calls, "focus:"+r.UID)
	return nil
}

func (f *fakeActions) KataCommand(ref string) (*exec.Cmd, error) {
	call := "kata"
	if ref != "" {
		call = "issue:" + ref
	}
	f.calls = append(f.calls, call)
	return nil, fmt.Errorf("test terminal unavailable")
}

func (f *fakeActions) RunNow(uid string) error { f.calls = append(f.calls, "run:"+uid); return nil }

// press builds the key Bubble Tea reports for a key name such as "q",
// "ctrl+s" or "shift+tab", or for several typed runes.
func press(s string) tea.KeyPressMsg {
	named := map[string]rune{"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab, "space": tea.KeySpace, "backspace": tea.KeyBackspace, "delete": tea.KeyDelete, "up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight, "home": tea.KeyHome, "end": tea.KeyEnd, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown}
	var mod tea.KeyMod
	name := s
	for {
		prefix, rest, ok := strings.Cut(name, "+")
		if !ok || rest == "" || (prefix != "ctrl" && prefix != "shift" && prefix != "alt") {
			break
		}
		mod |= map[string]tea.KeyMod{"ctrl": tea.ModCtrl, "shift": tea.ModShift, "alt": tea.ModAlt}[prefix]
		name = rest
	}
	if code, ok := named[name]; ok {
		k := tea.KeyPressMsg{Code: code, Mod: mod}
		if code == tea.KeySpace && mod == 0 {
			k.Text = " "
		}
		return k
	}
	r := []rune(name)
	if len(r) == 1 && mod != 0 {
		return tea.KeyPressMsg{Code: r[0], Mod: mod}
	}
	if len(r) == 1 {
		return tea.KeyPressMsg{Code: r[0], Text: name}
	}
	return tea.KeyPressMsg{Code: tea.KeyExtended, Text: s}
}
func applyTestCommand(m *model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			applyTestCommand(m, child)
		}
		return
	}
	m.Update(msg)
}

func send(m *model, s string) { _, cmd := m.Update(press(s)); applyTestCommand(m, cmd) }

func fixture(t testing.TB) (*model, *fakeActions) {
	t.Helper()
	f := &fakeActions{}
	m := newModel(Options{Root: t.TempDir(), Service: f})
	m.width, m.height = 110, 26
	for i := range 40 {
		m.data.Jobs = append(m.data.Jobs, defs.JobEntry{UID: fmt.Sprintf("j%02d", i), Name: fmt.Sprintf("Job %02d", i), Supported: true, Owner: defs.Adapter, Definition: json.RawMessage(`{"enabled":true,"trigger":{"kind":"manual"}}`)})
	}
	m.data.Runs = []runner.Run{{UID: "r1", IssueUID: "issue", Job: kata.Job{UID: "j00", Name: "Job 00"}, Phase: runner.Running, Herdr: runner.HerdrRef{PaneID: "w1:p2"}, WFDef: &defs.WorkflowDef{}, Steps: []runner.StepState{{Key: "build"}}}}
	m.data.Workflows = []kata.Workflow{{UID: "w1", Name: "Flow", Definition: json.RawMessage(`{"version":1,"steps":[]}`)}}
	m.Update(dataMsg{data: m.data})
	return m, f
}

func (f *fakeActions) Resume(uid string) error { f.calls = append(f.calls, "resume:"+uid); return nil }

func uncachedView(m *model) string { m.frameValid = false; return m.viewContent() }

// runDetailText returns the open run detail's body lines.
func runDetailText(t *testing.T, m *model) string {
	t.Helper()
	if m.currentView() != viewRun {
		t.Fatal("run detail not open", m.currentView())
	}
	var lines []string
	for _, l := range m.renderRunDetail() {
		lines = append(lines, l.Text)
	}
	return strings.Join(lines, "\n")
}

// Action fixtures carry a verified synthetic installation pin. Unpinned joins
// remain useful for rendering, but cannot authorize a mutation lane.
func pinLegacyActions(m *model) {
	m.data.Health = map[string]any{"version": float64(1), "project_uid": "p"}
	m.opts.ProjectUID = "p"
	for i := range m.data.Jobs {
		if m.data.Jobs[i].ProjectUID == "" {
			m.data.Jobs[i].ProjectUID = "p"
		}
	}
	for i := range m.data.Runs {
		if m.data.Runs[i].Target.Identity.ProjectUID == "" {
			m.data.Runs[i].Target.Identity.ProjectUID = "p"
		}
	}
	m.rebuildJobIndex()
	m.rebuild()
}

func savedPauseJob(j kata.Job) kata.Job {
	d := map[string]json.RawMessage{}
	_ = json.Unmarshal(j.Definition, &d)
	var enabled bool
	_ = json.Unmarshal(d["enabled"], &enabled)
	d["enabled"], _ = json.Marshal(!enabled)
	j.Definition, _ = json.Marshal(d)
	j.DefinitionEventUID = "saved-event"
	return j
}
