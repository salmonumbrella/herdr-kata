package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

// Switching between Jobs and Runs keeps the leading # column in place.
func TestRunsColumnsAlignWithJobs(t *testing.T) {
	for _, scene := range []string{"one-project", "multi-project"} {
		for _, width := range []int{80, 120, 140} {
			m := jobsFixture(t, scene, width, 24)
			clear(m.favorites)
			m.rebuild()
			m.selectTab(0)
			jobs := ansi.Strip(m.renderJobs()[0].Text)
			m.selectTab(1)
			runs := ansi.Strip(m.renderRuns()[0].Text)
			if !strings.HasPrefix(jobs, "  # ") || !strings.HasPrefix(runs, "  # ") {
				t.Fatalf("%s width %d: # does not lead both tabs:\nJobs %s\nRuns %s", scene, width, jobs, runs)
			}
			if !strings.HasSuffix(strings.TrimRight(jobs, " "), "updated") || !strings.HasSuffix(strings.TrimRight(runs, " "), "started") {
				t.Fatalf("%s width %d: the time column must end both tabs:\nJobs %s\nRuns %s", scene, width, jobs, runs)
			}
		}
	}
}

func TestListStatusHeadings(t *testing.T) {
	m, _ := fixture(t)
	m.width = 120
	for _, tc := range []struct {
		tab  int
		want []string
	}{
		{0, []string{"#", "status", "job", "model", "schedule", "updated"}},
		{1, []string{"#", "status", "job", "model", "detail", "started"}},
		{2, []string{"status", "workflow", "steps", "input", "updated"}},
	} {
		m.selectTab(tc.tab)
		var header string
		switch tc.tab {
		case 0:
			header = m.renderJobs()[0].Text
		case 1:
			header = m.renderRuns()[0].Text
		case 2:
			header = m.renderWorkflows()[0].Text
		}
		if got := strings.Fields(ansi.Strip(header)); !slices.Equal(got, tc.want) {
			t.Fatalf("tab %d header %q, want %q", tc.tab, got, tc.want)
		}
	}
}

func TestListModelAndEffortFitAtEightyColumns(t *testing.T) {
	profile := config.Profile{Kind: "codex", Args: []string{"--model", "gpt-6.1-sol", "-c", `model_reasoning_effort="xhigh"`}}
	for _, multi := range []bool{false, true} {
		m := newModel(Options{Root: t.TempDir(), Project: "example-project", Config: config.Config{Profiles: map[string]config.Profile{"worker": profile}}})
		m.width, m.height = 80, 24
		if multi {
			m.opts.Projects = map[string]*Lane{"example-project": {Config: m.opts.Config}, "other-project": {}}
		}
		m.Update(dataMsg{data: Data{Jobs: []defs.JobEntry{indexJob("p", "job", "Example job")}, Runs: []runner.Run{{UID: "run", Target: runner.Target{Identity: kata.Identity{ProjectUID: "p"}}, Job: kata.Job{UID: "job", Name: "Example job"}, Profile: profile, Phase: runner.Running}}}})
		// Historical Runs use the captured profile, even after configuration changes.
		m.opts.Config.Profiles["worker"] = config.Profile{Kind: "codex", Args: []string{"--model", "other-model"}}
		for _, tab := range []int{0, 1} {
			m.selectTab(tab)
			var row string
			if tab == 0 {
				row = m.renderJobs()[1].Text
			} else {
				row = m.renderRuns()[1].Text
			}
			if !strings.Contains(ansi.Strip(row), "gpt-6.1-sol xhigh") {
				t.Fatalf("tab %d multi=%v model clipped or guessed:\n%s", tab, multi, row)
			}
			if ansi.StringWidth(row) > 80 {
				t.Fatalf("tab %d exceeds pane: %d", tab, ansi.StringWidth(row))
			}
		}
	}
}

func TestRunsWithoutCapturedModelShowUnknown(t *testing.T) {
	m, _ := fixture(t)
	m.opts.Config.Profiles = map[string]config.Profile{"default": {Kind: "codex", Args: []string{"--model", "current-model"}}}
	m.data.Runs[0].ProfileName = "default"
	m.selectTab(1)
	cells := m.runCells(m.rows[0])
	if cellText(t, m.runsColumns(), cells, "model") != "—" {
		t.Fatalf("historical model inferred from current config: %+v", cells)
	}
}

func TestListModelsGolden(t *testing.T) {
	for _, tab := range []int{0, 1} {
		screen := []string{"jobs", "runs"}[tab]
		t.Run(screen, func(t *testing.T) {
			assertFrames(t, screen, "model", [][2]int{{80, 24}, {120, 24}}, func(w, h int) string {
				m := jobsFixture(t, "one-project", w, h)
				profile := config.Profile{Kind: "codex", Args: []string{"--model", "gpt-6.1-sol", "-c", `model_reasoning_effort="xhigh"`}}
				m.opts.Config.Profiles["worker"] = profile
				m.data.Runs[0].Profile = profile
				m.rebuildJobIndex()
				m.selectTab(tab)
				return m.viewContent()
			})
		})
	}
}

func TestRunsStartedExcludesScheduledTime(t *testing.T) {
	m, _ := fixture(t)
	m.selectTab(1)
	r := &m.data.Runs[m.rows[0].run]
	r.StartedAt = nil
	r.Occurrence = "2026-10-16T12:00:00Z"
	m.rebuild()
	cells := m.runCells(m.rows[0])
	if got := cells[len(cells)-1].Text; got != "—" {
		t.Fatalf("unstarted run shows scheduled time as start: %q", got)
	}
}

// With several projects at 80 columns, Workflows keeps its status and updated
// cells, as Jobs and Runs do.
func TestWorkflowsKeepsStatusAndUpdatedAtEightyColumns(t *testing.T) {
	d := workflowSnapshot()
	d.Workflows[0].ProjectUID, d.Workflows[0].Project = "project-a", "example-project"
	other := d.Workflows[0]
	other.ProjectUID, other.Project = "project-b", "another-project"
	d.Workflows = append(d.Workflows, other)
	d.Projects = map[string]Data{"project-a": {Project: "example-project"}, "project-b": {Project: "another-project"}}
	m := workflowModel(t, d)
	m.opts.Projects = map[string]*Lane{"example-project": {Project: "example-project", ProjectUID: "project-a", Config: workflowTestConfig(), Service: &fakeActions{}}, "another-project": {Project: "another-project", ProjectUID: "project-b", Config: workflowTestConfig(), Service: &fakeActions{}}}
	m.Update(dataMsg{data: d})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	body := m.renderWorkflows()
	header := ansi.Strip(body[0].Text)
	if got, want := strings.Fields(header), []string{"status", "workflow", "steps", "input", "updated"}; !slices.Equal(got, want) {
		t.Fatalf("header %q, want %q", got, want)
	}
	rows := ansi.Strip(body[1].Text) + "\n" + ansi.Strip(body[2].Text)
	for _, want := range []string{"never", "running"} {
		if !strings.Contains(rows, want) {
			t.Fatalf("%s clipped at 80 columns:\n%s\n%s", want, header, rows)
		}
	}
	for _, line := range body[:3] {
		if w := ansi.StringWidth(line.Text); w > 80 {
			t.Fatalf("row exceeds pane: %d", w)
		}
	}
}
