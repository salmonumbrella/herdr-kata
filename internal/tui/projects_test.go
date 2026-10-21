package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

func projectBoard(t *testing.T) (*model, *fakeActions, *fakeActions) {
	t.Helper()
	a, b := &fakeActions{}, &fakeActions{}
	m := newModel(Options{Root: t.TempDir(), Projects: map[string]*Lane{
		"example-project": {Project: "example-project", Config: config.Config{Kata: config.Kata{Project: "example-project"}}, Service: a},
		"team-project":    {Project: "team-project", Config: config.Config{Kata: config.Kata{Project: "team-project"}}, Service: b},
	}})
	m.Update(tea.WindowSizeMsg{Width: 180, Height: 30})
	m.Update(dataMsg{data: Data{
		Projects: map[string]Data{"project-a": {Health: map[string]any{"version": float64(1), "project_uid": "project-a"}, Route: map[string]any{"allowed": true}, Project: "example-project"}, "project-b": {Health: map[string]any{"version": float64(1), "project_uid": "project-b"}, Route: map[string]any{"allowed": true}, Project: "team-project"}},
		Jobs: []defs.JobEntry{
			{UID: "same-job", Name: "Alpha", Supported: true, Owner: defs.Adapter, Definition: json.RawMessage(`{"enabled":true}`), ProjectUID: "project-a", Project: "example-project"},
			{UID: "same-job", Name: "Beta", Supported: true, Owner: defs.Adapter, Definition: json.RawMessage(`{"enabled":true}`), ProjectUID: "project-b", Project: "team-project"},
		},
		Runs: []runner.Run{
			{UID: "run-a", Job: kata.Job{Name: "Alpha"}, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-a"}}, Phase: runner.Running},
			{UID: "run-b", Job: kata.Job{Name: "Beta"}, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-b"}}, Phase: runner.Held},
		},
		Workflows: []kata.Workflow{{UID: "same-flow", Name: "Alpha flow", ProjectUID: "project-a", Project: "example-project"}, {UID: "same-flow", Name: "Beta flow", ProjectUID: "project-b", Project: "team-project"}},
	}})
	return m, a, b
}

// Lists have no project column; a row outside the default project names its
// project instead, and search still finds a project's records.
func TestProjectRowsAndSearch(t *testing.T) {
	for _, tab := range []string{"2", "3", "4"} {
		t.Run(tab, func(t *testing.T) {
			m, _, _ := projectBoard(t)
			send(m, tab)
			view := ansi.Strip(uncachedView(m))
			if strings.Contains(strings.Split(view, "\n")[3], "project") || !strings.Contains(view, "[team-project] Beta") || strings.Contains(view, "[example-project]") {
				t.Fatalf("rows must name only a non-default project:\n%s", view)
			}
			send(m, "/")
			send(m, "TEAM-PROJECT")
			send(m, "enter")
			view = ansi.Strip(uncachedView(m))
			if strings.Contains(view, "Alpha") || !strings.Contains(view, "Beta") {
				t.Fatalf("project search did not select only its records:\n%s", view)
			}
		})
	}
}

func TestProjectCountsInHeader(t *testing.T) {
	m, _, _ := projectBoard(t)
	header := strings.Split(ansi.Strip(uncachedView(m)), "\n")[1]
	for _, want := range []string{"example-project: 1 running", "team-project: 1 parked"} {
		if !strings.Contains(header, want) {
			t.Fatalf("header %q lacks %q", header, want)
		}
	}
}

func TestModerateWidthsPreserveShortFieldsAndCounts(t *testing.T) {
	for _, width := range []int{119, 140, 156, 174} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m, _, _ := projectBoard(t)
			m.opts.Projects["third-project"] = &Lane{Project: "third-project"}
			m.data.Projects["project-c"] = Data{Project: "third-project"}
			m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
			header := strings.Split(ansi.Strip(uncachedView(m)), "\n")[1]
			for _, counts := range [][2]string{{": 1 running", ": 1r"}, {": 1 parked", ": 1p"}} {
				if !strings.Contains(header, counts[0]) && !strings.Contains(header, counts[1]) {
					t.Errorf("project counts cut at %d: %s", width, header)
				}
			}
			if strings.Contains(header, "third-project:") {
				t.Fatal("idle project count shown", header)
			}
			started := time.Date(2026, 10, 16, 3, 4, 0, 0, time.UTC)
			m.now = func() time.Time { return started.Add(2 * time.Hour) }
			m.data.Jobs = m.data.Jobs[:1]
			m.data.Jobs[0].Name = strings.Repeat("Long job name ", 10)
			m.data.Jobs[0].Definition = json.RawMessage(`{"enabled":false,"trigger":{"kind":"manual"}}`)
			m.data.Runs = []runner.Run{{UID: "run", Job: kata.Job{UID: "same-job", Name: m.data.Jobs[0].Name}, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-a"}}, Phase: runner.Settled, Outcome: "succeeded", StartedAt: &started}}
			m.Update(dataMsg{data: m.data})
			for _, tab := range []string{"2", "3"} {
				send(m, tab)
				row := strings.Split(ansi.Strip(uncachedView(m)), "\n")[4]
				if !strings.Contains(row, "succeeded") || !strings.Contains(row, "2h ago") {
					t.Errorf("short fields cut on tab %s at %d: %s", tab, width, row)
				}
			}
		})
	}
}

func TestNarrowJobRowsKeepRecognizableNames(t *testing.T) {
	for _, width := range []int{80, 119, 174} {
		for _, supported := range []bool{true, false} {
			t.Run(fmt.Sprintf("%d/supported=%t", width, supported), func(t *testing.T) {
				m, _ := fixture(t)
				m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
				m.data.Jobs = m.data.Jobs[:2]
				for i, prefix := range []string{"Alpha", "Bravo"} {
					m.data.Jobs[i].Name = prefix + strings.Repeat(" long name", 10)
					m.data.Jobs[i].Supported = supported
					m.data.Jobs[i].Definition = json.RawMessage(`{"enabled":false,"trigger":{"kind":"manual"}}`)
				}
				m.rebuildJobIndex()
				m.rebuild()
				m.frameValid = false
				lines := strings.Split(ansi.Strip(uncachedView(m)), "\n")
				for i, name := range []string{"Alpha", "Bravo"} {
					line := lines[4+i]
					if !strings.Contains(line, name) {
						t.Errorf("job identity or state hidden: %s", line)
					}
				}
			})
		}
	}
}

// Single-project chrome is independent of whether it uses legacy or lane snapshots.
func TestSingleProjectViewUnchanged(t *testing.T) {
	m, _ := fixture(t)
	before := uncachedView(m)
	m.opts.Projects = map[string]*Lane{"example-project": {Project: "example-project"}}
	m.data.Projects = map[string]Data{"project-a": {Project: "example-project"}}
	if got := uncachedView(m); got != before {
		t.Fatalf("single-project view changed:\n%s", got)
	}
}

type projectEditor struct{ text string }

func (e projectEditor) Init() tea.Cmd                  { return nil }
func (e projectEditor) Update(tea.Msg) (form, tea.Cmd) { return e, nil }
func (e projectEditor) View() string                   { return e.text }

func TestNewJobProjectPicker(t *testing.T) {
	m, _, _ := projectBoard(t)
	send(m, "j")
	send(m, "n")
	if text := ansi.Strip(uncachedView(m)); !strings.Contains(text, "new job in project") || !strings.Contains(text, "▶ team-project") {
		t.Fatal(text)
	}
	send(m, "esc")
	if strings.Contains(uncachedView(m), "new job in project") {
		t.Fatal("picker did not cancel")
	}
	send(m, "n")
	send(m, "k")
	send(m, "enter")
	if text := ansi.Strip(uncachedView(m)); !strings.Contains(text, "new job · project example-project") {
		t.Fatal(text)
	}
	form := m.editor.(*jobForm)
	if len(form.workflows) != 1 || form.workflows[0].Project != "example-project" {
		t.Fatal("wrong workflow choices", form.workflows)
	}
}

func TestNewJobSingleProjectSkipsPicker(t *testing.T) {
	m := newModel(Options{Root: t.TempDir(), Projects: map[string]*Lane{"example-project": {Project: "example-project", Config: config.Config{Kata: config.Kata{Project: "example-project"}}, Service: &fakeActions{}}}})
	m.Update(dataMsg{data: Data{Health: map[string]any{"version": float64(1), "project_uid": "project"}}})
	send(m, "n")
	if text := ansi.Strip(uncachedView(m)); !strings.Contains(text, "new job · project example-project") || strings.Contains(text, "new job in project") {
		t.Fatal(text)
	}
}

func TestMutationAcknowledgementsNameProject(t *testing.T) {
	for _, tc := range []struct{ tab, key, call, want string }{{"2", "R", "run:same-job", "run requested · Beta"}, {"2", "p", "pause:same-job", "Beta paused"}, {"3", "u", "resume:run-b", "resume requested"}} {
		t.Run(tc.key, func(t *testing.T) {
			m, a, b := projectBoard(t)
			send(m, tc.tab)
			send(m, "j")
			send(m, tc.key)
			if len(a.calls) != 0 || strings.Join(b.calls, ",") != tc.call {
				t.Fatalf("wrong lane: %v %v", a.calls, b.calls)
			}
			if got := uncachedView(m); !strings.Contains(got, tc.want+" · project team-project") {
				t.Fatalf("project absent from acknowledgement:\n%s", got)
			}
		})
	}
	m, _, _ := projectBoard(t)
	send(m, "D")
	if got := uncachedView(m); !strings.Contains(got, "example-project / Alpha") {
		t.Fatalf("delete confirmation missing project:\n%s", got)
	}
}

func TestRowSanitizesTerminalText(t *testing.T) {
	m, _ := fixture(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 26})
	m.data.Jobs = m.data.Jobs[:1]
	m.data.Jobs[0].Name = "\x1b[31mred\u202e" + string([]byte{0xff})
	m.rebuildJobIndex()
	m.rebuild()
	m.frameValid = false
	view := ansi.Strip(uncachedView(m))
	if !utf8.ValidString(view) || strings.ContainsAny(view, "\x1b\u202e") || strings.Contains(view, "[31m") || !strings.Contains(view, "red") {
		t.Fatalf("unsafe row: %q", view)
	}
	lines := strings.Split(view, "\n")
	if got := ansi.StringWidth(lines[4]); got != 80 {
		t.Fatalf("row width %d, want 80: %q", got, lines[4])
	}
}

func TestInspectorNamesProject(t *testing.T) {
	m, _, _ := projectBoard(t)
	for _, tab := range []string{"2", "4"} {
		send(m, tab)
		if got := strings.Join(strings.Fields(workflowText(m.renderInspector(100))), " "); !strings.Contains(got, "project example-project") {
			t.Fatalf("inspector missing project: %s", got)
		}
	}
}

func TestNewJobOpensOutsideUpdateAndCanCancel(t *testing.T) {
	m, _, _ := projectBoard(t)
	send(m, "n")
	_, cmd := m.Update(press("enter"))
	if cmd == nil || m.editor != nil || !m.openingEditor {
		t.Fatal("form opened inside Update")
	}
	send(m, "esc")
	m.Update(cmd())
	if text := uncachedView(m); m.editor != nil || !strings.Contains(text, "Alpha") || strings.Contains(text, "new job ·") {
		t.Fatal("cancelled open reached the screen", text)
	}
}

func TestNewJobBindsKnownProject(t *testing.T) {
	m, _, _ := projectBoard(t)
	svc := &laneFormService{fakeActions: &fakeActions{}, fakeWriter: &fakeWriter{}}
	m.opts.ForProject = func(uid, name string) Lane {
		view := cfg()
		view.Kata.Project = name
		return Lane{ProjectUID: uid, Project: name, Config: view, Service: svc}
	}
	send(m, "n")
	send(m, "enter")
	form, ok := m.editor.(*jobForm)
	if !ok {
		t.Fatal(m.viewContent())
	}
	form.Set("name", "Draft")
	form.Set("prompt", "Work")
	form.Set("title", "Child")
	if closed := completeSave(t, form); closed.Target.ProjectUID != "project-a" {
		t.Fatal("draft lost its pinned project", closed)
	}
}

func TestNewJobUsesAvailableReplacementProject(t *testing.T) {
	m, _, _ := projectBoard(t)
	m.data.Projects = map[string]Data{
		"old-project": {Route: map[string]any{"allowed": false}, Project: "example-project"},
		"new-project": {Health: map[string]any{"version": float64(1), "project_uid": "new-project"}, Route: map[string]any{"allowed": true}, Project: "example-project"},
	}
	m.data.Workflows = []kata.Workflow{
		{UID: "old-flow", ProjectUID: "old-project", Project: "example-project"},
		{UID: "new-flow", ProjectUID: "new-project", Project: "example-project"},
	}
	m.opts.ForProject = func(uid, name string) Lane {
		return Lane{Project: name, ProjectUID: uid, Service: &fakeActions{}}
	}
	// Reopening must not depend on map iteration order when old pins remain.
	for range 32 {
		send(m, "n")
		send(m, "enter")
		form, ok := m.editor.(*jobForm)
		if !ok || form.projectUID != "new-project" || len(form.workflows) != 1 || form.workflows[0].UID != "new-flow" {
			t.Fatal("draft used stale lane", m.viewContent())
		}
		m.Update(editorClosedMsg{})
	}
}

func TestNewJobWaitsForLocalProjectPin(t *testing.T) {
	m, _, _ := projectBoard(t)
	m.data.Projects = nil
	m.data.Jobs = nil
	m.Update(dataMsg{data: m.data})
	calls := 0
	m.opts.ForProject = func(uid, name string) Lane { calls++; return Lane{} }
	send(m, "n")
	send(m, "enter")
	if calls != 0 || m.editor != nil || len(m.projectChoices) == 0 || m.notice.Text != "project example-project unavailable: project unpinned" {
		t.Fatalf("calls %d status %q", calls, m.notice.Text)
	}
}

func TestKataUsesSelectionOrProjectPicker(t *testing.T) {
	m, a, b := projectBoard(t)
	send(m, "j")
	send(m, "K")
	if len(a.calls) != 0 || strings.Join(b.calls, ",") != "kata" {
		t.Fatal("K ignored selected project", a.calls, b.calls)
	}
	a.calls = nil
	b.calls = nil
	m.data.Jobs = nil
	m.rebuildJobIndex()
	m.rebuild()
	m.frameValid = false
	send(m, "K")
	if got := uncachedView(m); !strings.Contains(got, "Project to open Kata") {
		t.Fatal("K did not ask for project", got)
	}
	send(m, "j")
	send(m, "enter")
	if len(a.calls) != 0 || strings.Join(b.calls, ",") != "kata" {
		t.Fatal("picker used wrong project", a.calls, b.calls)
	}
}

func TestOpenKataStartsProjectPicker(t *testing.T) {
	m, _, _ := projectBoard(t)
	m = newModel(Options{Root: t.TempDir(), OpenKata: true, Projects: m.opts.Projects})
	m.Init()
	if got := uncachedView(m); !strings.Contains(got, "Project to open Kata") {
		t.Fatal(got)
	}
}

func TestKataUsesOnlyProjectWithoutSelection(t *testing.T) {
	f := &fakeActions{}
	m := newModel(Options{Root: t.TempDir(), Projects: map[string]*Lane{"example-project": {Project: "example-project", Service: f}}})
	send(m, "K")
	if strings.Join(f.calls, ",") != "kata" {
		t.Fatal(f.calls, uncachedView(m))
	}
}

func TestKataFailureKeepsBoardUsable(t *testing.T) {
	m, f := fixture(t)
	send(m, "4")
	view := ansi.Strip(uncachedView(m))
	if !strings.Contains(strings.Split(view, "\n")[1], "│ kata カタ │ jobs │ runs │ workflows │") {
		t.Fatal("Kata is not first tab", view)
	}
	_, cmd := m.Update(tea.MouseClickMsg{X: 3, Y: 1, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("Kata tab did not attempt a start")
	}
	m.Update(cmd())
	if !strings.Contains(uncachedView(m), "test terminal unavailable") || !strings.Contains(uncachedView(m), "Flow") {
		t.Fatal(m.viewContent())
	}
	for _, tab := range []string{"2", "3", "4"} {
		send(m, tab)
		send(m, "K")
	}
	if strings.Join(f.calls, ",") != "kata,kata,kata,kata" {
		t.Fatal(f.calls)
	}
}

func TestKataTabClickWhileSearchingPreservesFilter(t *testing.T) {
	m, f := fixture(t)
	send(m, "/")
	send(m, "Job")
	_, cmd := m.Update(tea.MouseClickMsg{X: 3, Y: 1, Button: tea.MouseLeft})
	if cmd != nil {
		m.Update(cmd())
	}
	if len(f.calls) != 0 || !m.searching || m.query != "Job" || m.tab != 0 {
		t.Fatal("Kata tab click edited search", f.calls, m.query, m.tab)
	}
}

func TestKataPickerUsesCurrentProjectLane(t *testing.T) {
	m, cached, _ := projectBoard(t)
	m.data.Jobs = nil
	m.rebuildJobIndex()
	m.rebuild()
	m.frameValid = false
	oldLane, newLane := &fakeActions{}, &fakeActions{}
	m.opts.ForProject = func(uid, name string) Lane {
		actions := oldLane
		if uid == "replacement-project" {
			actions = newLane
		}
		return Lane{ProjectUID: uid, Project: name, Service: actions}
	}
	send(m, "K")
	send(m, "enter")
	if strings.Join(oldLane.calls, ",") != "kata" || len(cached.calls) != 0 {
		t.Fatal("picker reused cached project service", oldLane.calls, cached.calls)
	}
	m.data.Projects["project-a"] = Data{Project: "example-project", Route: map[string]any{"allowed": false}}
	m.data.Projects["replacement-project"] = Data{Project: "example-project", Route: map[string]any{"allowed": true}}
	send(m, "K")
	send(m, "enter")
	if strings.Join(newLane.calls, ",") != "kata" || len(oldLane.calls) != 1 {
		t.Fatal("picker used stale project lane", oldLane.calls, newLane.calls)
	}
}

func TestJobMarqueeRevealsStatusSuffixes(t *testing.T) {
	m, _ := fixture(t)
	m.data.Jobs = m.data.Jobs[:2]
	for i := range m.data.Jobs {
		m.data.Jobs[i].Name = "Example " + strings.Repeat("long name ", 12)
		m.data.Jobs[i].Definition = json.RawMessage(`{"enabled":false,"trigger":{"kind":"manual"}}`)
		m.data.Jobs[i].Supported = false
	}
	m.width = 80
	m.rebuildJobIndex()
	m.rebuild()
	found := false
	for frame := 7; frame < 180; frame++ {
		m.frame = frame
		text := ansi.Strip(uncachedView(m))
		if strings.Contains(text, "…") {
			t.Fatal(text)
		}
		if strings.Count(text, "paused") >= 2 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("paused suffix never entered the visible marquee")
	}
}

func TestActionsUseSelectedProject(t *testing.T) {
	m, _ := fixture(t)
	a, b := &fakeActions{}, &fakeActions{}
	m.opts.Projects = map[string]*Lane{"project-a": {Service: a}, "project-b": {Service: b}}
	m.data.Jobs = []defs.JobEntry{{UID: "job-a", Name: "first", Definition: []byte(`{"enabled":true}`), Owner: defs.Adapter, Supported: true, Project: "project-a", ProjectUID: "pa"}, {UID: "job-b", Name: "second", Definition: []byte(`{"enabled":true}`), Owner: defs.Adapter, Supported: true, Project: "project-b", ProjectUID: "pb"}}
	m.rebuildJobIndex()
	m.rebuild()
	m.frameValid = false
	send(m, "j")
	send(m, "R")
	send(m, "p")
	send(m, "K")
	if len(a.calls) != 0 || strings.Join(b.calls, ",") != "run:job-b,pause:job-b,kata" {
		t.Fatal(a.calls, b.calls)
	}
	send(m, "n")
	if !strings.Contains(uncachedView(m), "project") {
		t.Fatal("new job did not refuse project ambiguity", uncachedView(m))
	}
}

func TestSigningRefusalNamesOwningProject(t *testing.T) {
	m, _ := fixture(t)
	m.data.Projects = map[string]Data{"project-b": {Project: "team-project"}}
	m.data.Runs = []runner.Run{{UID: "waiting", Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-b"}}, Ops: []runner.Op{{ID: "report", SigningRefused: true}}}}
	if text := uncachedView(m); !strings.Contains(text, "--project team-project") {
		t.Fatal("missing owning project in signing recovery command", text)
	}
}

func TestActionBindsProjectUIDBeforeReusingEntryName(t *testing.T) {
	m, _ := fixture(t)
	oldLane, newLane := &fakeActions{}, &fakeActions{}
	m.opts.Projects = map[string]*Lane{"example-project": {Service: newLane}}
	var selectedUID string
	m.opts.ForProject = func(uid, name string) Lane {
		selectedUID = uid
		return Lane{ProjectUID: uid, Project: name, Service: oldLane}
	}
	m.data.Jobs = []defs.JobEntry{{UID: "job", Name: "job", Definition: []byte(`{"enabled":true}`), Owner: defs.Adapter, Supported: true, Project: "example-project", ProjectUID: "old-project"}}
	m.rebuildJobIndex()
	m.rebuild()
	m.frameValid = false
	send(m, "R")
	if selectedUID != "old-project" || len(newLane.calls) != 0 || strings.Join(oldLane.calls, ",") != "run:job" {
		t.Fatal("entry name retargeted an old row", selectedUID, newLane.calls, oldLane.calls)
	}
}

func TestRetainedRunKeepsLocalFocusWithoutProjectActions(t *testing.T) {
	m, local := fixture(t)
	m.opts.Projects = map[string]*Lane{"example-project": {}}
	m.opts.ForProject = func(string, string) Lane { return Lane{} }
	m.data.Runs = []runner.Run{{UID: "retained", Herdr: runner.HerdrRef{PaneID: "pane"}, Target: runner.Target{Identity: kata.Identity{ProjectUID: "removed-project"}}}}
	send(m, "3")
	send(m, "a")
	send(m, "enter")
	send(m, "i")
	send(m, "K")
	if strings.Join(local.calls, ",") != "focus:retained" {
		t.Fatal("retained run lost local focus or fell back for Kata actions", local.calls)
	}
}

func TestLegacyDeleteUsesOnlyConfiguredProject(t *testing.T) {
	m, shared := fixture(t)
	pinLegacyActions(m)
	lane := &fakeActions{}
	m.opts.Projects = map[string]*Lane{"example-project": {Service: lane}}
	send(m, "D")
	send(m, "y")
	if len(shared.calls) != 0 || strings.Join(lane.calls, ",") != "delete:j00" {
		t.Fatal("legacy deletion lost its single project binding", shared.calls, lane.calls)
	}
}

func TestDuplicateJobUIDKeepsSecondRowsActions(t *testing.T) {
	m, _ := fixture(t)
	a, b := &fakeActions{}, &fakeActions{}
	m.opts.Projects = map[string]*Lane{"project-a": {Service: a}, "project-b": {Service: b}}
	m.data.Jobs = []defs.JobEntry{{UID: "same", Name: "first", Owner: defs.Adapter, Supported: true, ProjectUID: "project-a", Project: "project-a"}, {UID: "same", Name: "second", Owner: defs.Adapter, Supported: true, ProjectUID: "project-b", Project: "project-b"}}
	m.rebuildJobIndex()
	m.rebuild()
	m.frameValid = false
	send(m, "j")
	send(m, "l")
	send(m, "p")
	if len(a.calls) != 0 || strings.Join(b.calls, ",") != "pause:same" {
		t.Fatal("detail selection crossed projects", a.calls, b.calls)
	}
}

func TestFinishedJobDoesNotHideSameUIDInAnotherProject(t *testing.T) {
	m, _ := fixture(t)
	m.data.Jobs = []defs.JobEntry{{UID: "same", Name: "first", DefinitionEventUID: "event", Definition: json.RawMessage(`{"trigger":{"kind":"once","at":"2026-01-01T00:00:00Z"}}`), ProjectUID: "project-a"}, {UID: "same", Name: "second", DefinitionEventUID: "event", Definition: json.RawMessage(`{"trigger":{"kind":"once","at":"2026-01-01T00:00:00Z"}}`), ProjectUID: "project-b"}}
	m.data.Runs = []runner.Run{{UID: "run-a", Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-a"}}, Job: kata.Job{UID: "same", DefinitionEventUID: "event"}, Phase: runner.Settled, Outcome: "succeeded", Occurrence: "2026-01-01T00:00:00Z"}}
	m.rebuildJobIndex()
	m.rebuild()
	m.frameValid = false
	if len(m.rows) != 1 || m.rows[0].job != 1 {
		t.Fatal("one project's completion hid another project", m.rows)
	}
}

func TestInspectorUsesProjectsOwnCheckpoint(t *testing.T) {
	m, _ := fixture(t)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return at }
	a, b := indexJob("project-a", "same", "first"), indexJob("project-b", "same", "second")
	for _, j := range []*defs.JobEntry{&a, &b} {
		j.CreatedAt = at
		j.Definition = json.RawMessage(`{"version":1,"enabled":true,"trigger":{"kind":"interval","interval_seconds":60},"action":{"kind":"execute","prompt":"work"}}`)
	}
	m.data.Jobs = []defs.JobEntry{a, b}
	m.data.Projects = map[string]Data{}
	for i, uid := range []string{"project-a", "project-b"} {
		m.data.Projects[uid] = Data{Health: map[string]any{"version": float64(1), "project_uid": uid, "state_created_at": at.Format(time.RFC3339)}, Checkpoints: map[string]sched.Checkpoint{"same": {Version: 1, JobUID: "same", HandledThrough: at.Add(time.Duration(1+i*2) * time.Minute)}}}
	}
	m.rebuildJobIndex()
	m.rebuild()
	if text := strings.Join(m.inspector(), "\n"); !strings.Contains(text, "next      00:02 (in 2m)") {
		t.Fatal(text)
	}
	send(m, "j")
	if text := strings.Join(m.inspector(), "\n"); !strings.Contains(text, "next      00:04 (in 4m)") {
		t.Fatal(text)
	}
}

func TestPruneUsesSavedProjectsWithoutSelectedRow(t *testing.T) {
	m, _ := fixture(t)
	a, b := &fakeActions{}, &fakeActions{}
	m.opts.Projects = map[string]*Lane{"project-a": {Service: a}, "project-b": {Service: b}}
	once := json.RawMessage(`{"trigger":{"kind":"once","at":"2026-01-01T00:00:00Z"}}`)
	m.data.Jobs = []defs.JobEntry{{UID: "job-a", Name: "first", DefinitionEventUID: "event", Definition: once, ProjectUID: "project-a", Project: "project-a"}, {UID: "job-b", Name: "second", DefinitionEventUID: "event", Definition: once, ProjectUID: "project-b", Project: "project-b"}}
	m.data.Runs = []runner.Run{{UID: "run-a", Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-a"}}, Job: kata.Job{UID: "job-a", DefinitionEventUID: "event"}, Phase: runner.Settled, Outcome: "succeeded", Occurrence: "2026-01-01T00:00:00Z"}, {UID: "run-b", Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-b"}}, Job: kata.Job{UID: "job-b", DefinitionEventUID: "event"}, Phase: runner.Settled, Outcome: "succeeded", Occurrence: "2026-01-01T00:00:00Z"}}
	m.rebuildJobIndex()
	m.rebuild()
	m.frameValid = false
	if len(m.rows) != 0 {
		t.Fatal("fixture has selected row")
	}
	send(m, "P")
	send(m, "y")
	if strings.Join(a.calls, ",") != "delete:job-a" || strings.Join(b.calls, ",") != "delete:job-b" {
		t.Fatal("prune depended on selection", a.calls, b.calls)
	}
}

func TestPruneNeverFallsBackForRemovedProject(t *testing.T) {
	m, _ := fixture(t)
	live := &fakeActions{}
	m.opts.Projects = map[string]*Lane{"project-a": {Service: live}}
	m.data.Jobs = []defs.JobEntry{{UID: "active", Name: "active", Definition: json.RawMessage(`{"trigger":{"kind":"manual"}}`), ProjectUID: "project-a", Project: "project-a"}, {UID: "removed", Name: "removed", DefinitionEventUID: "event", Definition: json.RawMessage(`{"trigger":{"kind":"once","at":"2026-01-01T00:00:00Z"}}`), ProjectUID: "removed-project", Project: "removed-project"}}
	m.data.Runs = []runner.Run{{UID: "removed-run", Target: runner.Target{Identity: kata.Identity{ProjectUID: "removed-project"}}, Job: kata.Job{UID: "removed", DefinitionEventUID: "event"}, Phase: runner.Settled, Outcome: "succeeded", Occurrence: "2026-01-01T00:00:00Z"}}
	m.rebuildJobIndex()
	m.rebuild()
	m.frameValid = false
	send(m, "P")
	send(m, "y")
	if len(live.calls) != 0 || !strings.Contains(m.notice.Text, "project unavailable") {
		t.Fatal("removed project fell back to selected client", live.calls, m.notice.Text)
	}
}

func TestWorkflowNameUsesOwningProject(t *testing.T) {
	m, _ := fixture(t)
	m.width = 200
	m.data.Jobs = []defs.JobEntry{{UID: "job", Name: "job", Definition: json.RawMessage(`{"action":{"kind":"execute","workflow_uid":"same-workflow"}}`), ProjectUID: "project-b"}}
	m.data.Workflows = []kata.Workflow{{UID: "same-workflow", Name: "Own workflow", ProjectUID: "project-b"}, {UID: "same-workflow", Name: "Other workflow", ProjectUID: "project-a"}}
	m.rebuildJobIndex()
	m.rebuild()
	text := ""
	for _, c := range m.jobCells(m.indexedJob(m.rows[0])) {
		text += c.Text + " "
	}
	if !strings.Contains(text, "Own workflow") || strings.Contains(text, "Other workflow") {
		t.Fatal("workflow label crossed projects", text)
	}
}

func TestResumeUsesSelectedRunProject(t *testing.T) {
	m, _ := fixture(t)
	shared, a, b := &fakeActions{}, &fakeActions{}, &fakeActions{}
	m.opts.Service = shared
	m.opts.Projects = map[string]*Lane{"example-project": {Service: a}, "team-project": {Service: b}}
	m.data.Projects = map[string]Data{"project-b": {Project: "team-project"}}
	m.data.Runs = []runner.Run{{UID: "held-run", Phase: runner.Held, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-b"}}}}
	m.selectTab(1)
	send(m, "u")
	if len(shared.calls) != 0 || len(a.calls) != 0 || strings.Join(b.calls, ",") != "resume:held-run" {
		t.Fatal("resume crossed project lanes", shared.calls, a.calls, b.calls)
	}
	delete(m.opts.Projects, "team-project")
	send(m, "u")
	if len(shared.calls) != 0 || len(a.calls) != 0 || len(b.calls) != 1 {
		t.Fatal("removed lane resumed through another requester", shared.calls, a.calls, b.calls)
	}
}

func TestLegacyRunActionsBindSoleProjectFromPin(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "unavailable modern project"}[modern], func(t *testing.T) {
			m, _ := fixture(t)
			a := &fakeActions{}
			m.opts.Projects = map[string]*Lane{"example-project": {Service: a}}
			m.opts.ForProject = func(uid, name string) Lane {
				if uid == "legacy-project" && name == "example-project" {
					return Lane{ProjectUID: uid, Project: name, Service: a}
				}
				return Lane{}
			}
			m.data.Projects = nil
			m.data.Health = map[string]any{"version": float64(1), "project_uid": "legacy-project"}
			if modern {
				m.data.Health = map[string]any{"version": float64(2)}
			}
			m.data.Runs = []runner.Run{{UID: "run", IssueUID: "issue", Phase: runner.Held, Target: runner.Target{Identity: kata.Identity{ProjectUID: "legacy-project"}}}}
			m.selectTab(1)
			send(m, "K")
			send(m, "i")
			if modern {
				if len(a.calls) != 0 {
					t.Fatal("missing modern lane fell back", a.calls)
				}
			} else if strings.Join(a.calls, ",") != "kata,issue:issue" {
				t.Fatal("legacy run actions unavailable", a.calls)
			}
		})
	}
}

func TestJobPromptBudgetUsesOwningProject(t *testing.T) {
	m, _ := fixture(t)
	m.data.Jobs = []defs.JobEntry{{UID: "job", Name: "job", Definition: json.RawMessage(`{"action":{"kind":"execute","workflow_uid":"same-workflow"}}`), ProjectUID: "project-b"}}
	m.data.Workflows = []kata.Workflow{
		{UID: "same-workflow", Definition: json.RawMessage(`{"version":1,"steps":[{"key":"main","kind":"prompt","prompt":"work"}]}`), ProjectUID: "project-b"},
		{UID: "same-workflow", Definition: json.RawMessage(`{"version":1,"steps":[{"key":"main","kind":"prompt","prompt":"other work","retries":2}]}`), ProjectUID: "project-a"},
	}
	m.rebuildJobIndex()
	m.rebuild()
	m.frameValid = false
	if text := strings.Join(m.inspector(), "\n"); !strings.Contains(text, "prompts   1/12") {
		t.Fatal("prompt budget came from another project's workflow", text)
	}
}

func TestLegacyFavoriteDisplayAndScopedOverride(t *testing.T) {
	m := workflowModel(t, workflowSnapshot())
	m.selectTab(0)
	m.favorites["job"] = true
	m.rebuild()
	j := m.jobs.byKey[jobKey{"project-a", "job"}]
	if got := cellText(t, m.jobsColumns(), m.jobCells(j), " "); got != "★" {
		t.Fatal("legacy favorite missing from job row", got)
	}
	m.favorites["project-a\x00job"] = false
	m.rebuild()
	if got := bodyTextForTest(m.renderJobs()); strings.Contains(got, "★") {
		t.Fatal("scoped false did not override legacy favorite", got)
	}
	delete(m.favorites, "project-a\x00job")
	m.data.Projects = map[string]Data{"project-a": {}, "project-b": {}}
	m.rebuild()
	if got := bodyTextForTest(m.renderJobs()); strings.Contains(got, "★") {
		t.Fatal("legacy favorite leaked onto a multi-project board", got)
	}
}

func TestRetainedProjectsStayLabeledAfterConfigRemoval(t *testing.T) {
	for _, tab := range []string{"2", "3", "4"} {
		t.Run(tab, func(t *testing.T) {
			m, _, _ := projectBoard(t)
			delete(m.opts.Projects, "team-project")
			send(m, tab)
			view := ansi.Strip(uncachedView(m))
			if strings.Contains(strings.Split(view, "\n")[3], "project") || !strings.Contains(view, "[team-project] ") {
				t.Fatalf("retained rows lost their project labels:\n%s", view)
			}
			header := strings.Split(view, "\n")[1]
			for _, want := range []string{"example-project: 1 running", "team-project: 1 parked"} {
				if !strings.Contains(header, want) {
					t.Errorf("header %q lacks %q", header, want)
				}
			}
		})
	}
}

func TestProjectLabelsUseRetainedRowIdentities(t *testing.T) {
	for _, tab := range []string{"2", "3", "4"} {
		t.Run(tab, func(t *testing.T) {
			m, _, _ := projectBoard(t)
			delete(m.opts.Projects, "team-project")
			m.data.Projects = nil
			// Retained records can outlive their installation snapshot.
			switch tab {
			case "2":
				m.data.Runs, m.data.Workflows = nil, nil
			case "3":
				m.data.Jobs, m.data.Workflows = nil, nil
				m.data.Runs[0].ProjectName = "example-project"
				m.data.Runs[1].ProjectName = "team-project"
			case "4":
				m.data.Jobs, m.data.Runs = nil, nil
			}
			m.Update(dataMsg{data: m.data})
			send(m, tab)
			view := ansi.Strip(uncachedView(m))
			want := "team-project"
			if strings.Contains(strings.Split(view, "\n")[3], "project") || !strings.Contains(view, "["+want+"] ") {
				t.Fatalf("retained records lost project identity:\n%s", view)
			}
		})
	}
}

func TestSingleSnapshotProjectKeepsCompactLayout(t *testing.T) {
	m, _, _ := projectBoard(t)
	delete(m.opts.Projects, "team-project")
	delete(m.data.Projects, "project-b")
	m.data.Jobs = m.data.Jobs[:1]
	m.data.Runs = m.data.Runs[:1]
	m.data.Workflows = m.data.Workflows[:1]
	m.Update(dataMsg{data: m.data})
	for _, tab := range []string{"2", "3", "4"} {
		send(m, tab)
		view := ansi.Strip(uncachedView(m))
		if strings.Contains(strings.Split(view, "\n")[3], "project") || strings.Contains(strings.Split(view, "\n")[1], "example-project:") {
			t.Fatalf("single project gained redundant labels:\n%s", view)
		}
	}
}
