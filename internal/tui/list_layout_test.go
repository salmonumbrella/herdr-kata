package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

// kataGutter is Kata's list gutter: every cell renders with PaddingRight(1)
// (go.kenn.io/kata internal/tui/list_render.go renderBody), so adjacent
// columns sit exactly one cell apart.
const kataGutter = 1

// listTabs names the three list tabs, their flex column, and their renderer.
var listTabs = []struct {
	tab  int
	name string
	flex string
}{{0, "jobs", "job"}, {1, "runs", "detail"}, {2, "workflows", "workflow"}}

func renderTab(m *model, tab int) bodyLines {
	m.selectTab(tab)
	switch tab {
	case 0:
		return m.renderJobs()
	case 1:
		return m.renderRuns()
	default:
		return m.renderWorkflows()
	}
}

// tabLayout returns a tab's column specs, widths and the cells of every
// visible row.
func tabLayout(m *model, tab int) ([]column, []int, [][]cell) {
	m.selectTab(tab)
	var rows [][]cell
	start, end, _, _ := pager(len(m.rows), m.cursor, m.pageRows())
	switch tab {
	case 0:
		for _, r := range m.rows[start:end] {
			rows = append(rows, m.jobCells(m.indexedJob(r)))
		}
		return m.jobsColumns(), m.jobsWidths(), rows
	case 1:
		for _, r := range m.rows[start:end] {
			rows = append(rows, m.runCells(r))
		}
		return m.runsColumns(), m.runsWidths(), rows
	default:
		for _, r := range m.rows[start:end] {
			rows = append(rows, m.workflowCells(m.wfIndex[workflowKey{r.projectUID, r.uid}]))
		}
		return m.workflowColumns(), m.workflowWidths(), rows
	}
}

// multiProjectLists is the two-project fixture with a workflow in each
// project and a run whose issue lives in the other project.
func multiProjectLists(t *testing.T, width int) *model {
	t.Helper()
	m := jobsFixture(t, "multi-project", width, 24)
	m.data.Runs[1].Target.Identity.ProjectUID = "q"
	m.data.Runs[1].IssueRef = "other-project#a12b"
	lonely := indexJob("q", "lonely", "Example lonely job")
	lonely.Project = "other-project"
	m.data.Jobs = append(m.data.Jobs, lonely)
	m.data.Workflows = []kata.Workflow{
		{UID: "wf-a", Name: "example-flow", ProjectUID: "p", Project: "example-project"},
		{UID: "wf-b", Name: "other-flow", ProjectUID: "q", Project: "other-project"},
	}
	m.reproject()
	m.rebuild()
	return m
}

func TestListHasNoProjectColumn(t *testing.T) {
	for _, width := range []int{80, 140} {
		m := multiProjectLists(t, width)
		if !m.showProjects() {
			t.Fatal("fixture must span two projects")
		}
		for _, tc := range listTabs {
			header := strings.Fields(ansi.Strip(renderTab(m, tc.tab)[0].Text))
			if slices.Contains(header, "project") {
				t.Fatalf("%s at %d renders a project column: %q", tc.name, width, header)
			}
			cols, _, _ := tabLayout(m, tc.tab)
			for _, c := range cols {
				if c.Title == "project" {
					t.Fatalf("%s keeps a project column spec", tc.name)
				}
			}
		}
	}
}

func TestListIssueHeaderIsHash(t *testing.T) {
	for _, scene := range []string{"one-project", "multi-project"} {
		m := jobsFixture(t, scene, 120, 24)
		m.rebuild()
		for _, tab := range []int{0, 1} {
			header := strings.Fields(ansi.Strip(renderTab(m, tab)[0].Text))
			if len(header) == 0 || header[0] != "#" {
				t.Fatalf("%s tab %d header %q, want # first", scene, tab, header)
			}
			if slices.Contains(header, "issue") {
				t.Fatalf("%s tab %d still says issue: %q", scene, tab, header)
			}
		}
	}
}

func TestListIssueCellQualifiesOtherProject(t *testing.T) {
	m := multiProjectLists(t, 140)
	_, _, runs := tabLayout(m, 1)
	var issues []string
	for _, cells := range runs {
		issues = append(issues, cells[0].Text)
	}
	if !slices.Contains(issues, "other-project#a12b") {
		t.Fatalf("other-project run lost its project: %q", issues)
	}
	if !slices.Contains(issues, "#a02b") || slices.Contains(issues, "example-project#a02b") {
		t.Fatalf("default-project run must show a bare #: %q", issues)
	}
	// A row outside the default project with no issue names its project in
	// the name cell, as Kata's all-projects title prefix does.
	_, _, jobs := tabLayout(m, 0)
	found := false
	for _, cells := range jobs {
		for _, c := range cells {
			if c.Text == "[other-project] Example lonely job" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("other-project job without an issue is indistinguishable: %+v", jobs)
	}
	_, _, flows := tabLayout(m, 2)
	var names []string
	for _, cells := range flows {
		names = append(names, cells[1].Text)
	}
	if !slices.Contains(names, "[other-project] other-flow") || !slices.Contains(names, "example-flow") {
		t.Fatalf("workflow rows must name a non-default project: %q", names)
	}
	// One project: refs drop their prefix even when the lane has no name.
	one := jobsFixture(t, "one-project", 120, 24)
	one.rebuild()
	_, _, rows := tabLayout(one, 1)
	if got := rows[0][0].Text; got != "#a02b" {
		t.Fatalf("one-project run issue %q, want #a02b", got)
	}
}

// Every fixed column is exactly as wide as its widest header or cell, so the
// space between two columns is Kata's one-cell gutter.
func TestListGutterMatchesKata(t *testing.T) {
	for _, width := range []int{80, 140} {
		m := multiProjectLists(t, width)
		for _, tc := range listTabs {
			cols, widths, rows := tabLayout(m, tc.tab)
			for i, c := range cols {
				if c.Flex || c.Title == " " {
					continue
				}
				want := ansi.StringWidth(c.Title)
				for _, cells := range rows {
					want = max(want, ansi.StringWidth(plain(cells[i].Text)))
				}
				if c.Width > 0 {
					want = min(want, c.Width)
				}
				if widths[i] > want {
					t.Fatalf("%s at %d: %s is %d wide, content needs %d; gutter exceeds Kata's %d", tc.name, width, c.Title, widths[i], want, kataGutter)
				}
			}
			header := ansi.Strip(renderTab(m, tc.tab)[0].Text)
			x := 2
			for i, c := range cols {
				if got := ansi.Cut(header, x, x+widths[i]); c.Title != " " && strings.TrimRight(got, " ") != ansi.Truncate(c.Title, widths[i], "") {
					t.Fatalf("%s at %d: %s not at %d:\n%s", tc.name, width, c.Title, x, header)
				}
				x += widths[i] + kataGutter
			}
		}
	}
}

func TestListFlexColumnTakesRemainder(t *testing.T) {
	for _, width := range []int{100, 140, 200} {
		m := multiProjectLists(t, width)
		for _, tc := range listTabs {
			cols, widths, _ := tabLayout(m, tc.tab)
			flex := -1
			for i, c := range cols {
				if c.Flex {
					if flex >= 0 {
						t.Fatalf("%s has two flex columns", tc.name)
					}
					flex = i
				}
			}
			if flex < 0 || cols[flex].Title != tc.flex {
				t.Fatalf("%s flex column %d, want %s: %+v", tc.name, flex, tc.flex, cols)
			}
			used := len(widths) - 1
			for _, w := range widths {
				used += w
			}
			if used != width-2 {
				t.Fatalf("%s at %d uses %d cells, want the whole %d", tc.name, width, used, width-2)
			}
		}
	}
}

func TestHeaderCellsShareStyle(t *testing.T) {
	preserveTheme(t)
	for _, mode := range []colorMode{colorDark, colorLight, colorNone} {
		applyColorMode(mode, mode == colorDark)
		for _, scene := range []string{"one-project", "multi-project"} {
			m := jobsFixture(t, scene, 120, 24)
			m.rebuild()
			for _, tc := range listTabs {
				got := renderTab(m, tc.tab)[0].Text
				want := tableHeaderStyle.Render(ansi.Strip(got))
				if got != want {
					t.Fatalf("mode %d %s %s header is not one Kata header span:\n got %q\nwant %q", mode, scene, tc.name, got, want)
				}
			}
		}
	}
}

func TestRunsNarrowKeepsIssueAndStatus(t *testing.T) {
	m := multiProjectLists(t, 80)
	long := "a long agent-authored summary that never fits an eighty-column pane"
	for i := range m.data.Runs {
		m.data.Runs[i].Job.Name = fmt.Sprintf("example-job-with-a-long-name-%d", i)
		m.data.Runs[i].Steps = []runner.StepState{{Key: "work", Note: long}}
	}
	m.rebuild()
	cols, widths, rows := tabLayout(m, 1)
	natural := func(i int) int {
		w := ansi.StringWidth(cols[i].Title)
		for _, cells := range rows {
			w = max(w, ansi.StringWidth(plain(cells[i].Text)))
		}
		return w
	}
	index := map[string]int{}
	for i, c := range cols {
		index[c.Title] = i
	}
	for _, title := range []string{"#", "status"} {
		if i := index[title]; widths[i] != natural(i) {
			t.Fatalf("%s shrank to %d at 80 columns, needs %d", title, widths[i], natural(i))
		}
	}
	if !strings.HasPrefix(ansi.Strip(renderTab(m, 1)[0].Text), "  # ") {
		t.Fatal("issue header clipped")
	}
	model, detail := index["model"], index["detail"]
	if widths[model] < natural(model) && widths[detail] > minListFlex {
		t.Fatalf("model shrank to %d while detail kept %d; the flex column gives way first", widths[model], widths[detail])
	}
	if widths[index["job"]] < natural(index["job"]) && widths[model] > 3 {
		t.Fatalf("job shrank before model: job %d model %d", widths[index["job"]], widths[model])
	}
	for _, line := range renderTab(m, 1) {
		if w := ansi.StringWidth(line.Text); w > 80 {
			t.Fatalf("row exceeds the pane: %d", w)
		}
	}
}

func TestSelectedRunDetailMarquee(t *testing.T) {
	long := "Checked the example tracker and verified the upstream issue remains open with zero comments"
	t.Run("runs tab", func(t *testing.T) {
		m := jobsFixture(t, "one-project", 80, 24)
		m.data.Runs[0].Reason = ""
		m.data.Runs[0].Phase = runner.Settled
		m.data.Runs[0].Outcome = "succeeded"
		m.data.Runs[0].Steps = []runner.StepState{{Key: "work", Note: long}}
		m.rebuild()
		m.selectTab(1)
		m.cursor = 0
		cols, widths, _ := tabLayout(m, 1)
		x := 2
		for i, c := range cols {
			if c.Title == "detail" {
				break
			}
			x += widths[i] + 1
		}
		first := m.renderRuns()[1]
		if !first.Selected {
			t.Fatal("first run not selected")
		}
		if !hasSpan(first.Animations, x) {
			t.Fatalf("selected detail at x=%d does not animate: %+v", x, first.Animations)
		}
		m.frame = 20
		if later := m.renderRuns()[1]; ansi.Strip(later.Text) == ansi.Strip(first.Text) {
			t.Fatal("selected detail does not scroll")
		}
	})
	t.Run("job detail runs", func(t *testing.T) {
		m := jobDetailFixture(t, `{"enabled":true,"action":{"kind":"execute","prompt":"work"}}`)
		m.width = 80
		started := time.Date(2026, 10, 16, 6, 0, 0, 0, time.UTC)
		m.data.Runs = []runner.Run{{UID: "run", Job: kata.Job{UID: "job"}, Target: runner.Target{Identity: kata.Identity{ProjectUID: "p"}}, Phase: runner.Settled, Outcome: "succeeded", StartedAt: &started, Steps: []runner.StepState{{Key: "work", Note: long}}}}
		m.rebuildJobIndex()
		line := selectedLine(t, m.buildJobDetail().Body())
		if len(line.Animations) == 0 {
			t.Fatalf("selected run detail does not animate: %q", ansi.Strip(line.Text))
		}
		m.frame = 20
		if later := selectedLine(t, m.buildJobDetail().Body()); ansi.Strip(later.Text) == ansi.Strip(line.Text) {
			t.Fatal("selected run detail does not scroll")
		}
	})
}

func hasSpan(spans []animationSpan, x0 int) bool {
	for _, s := range spans {
		if s.X0 == x0 {
			return true
		}
	}
	return false
}

func selectedLine(t *testing.T, body bodyLines) bodyLine {
	t.Helper()
	for _, l := range body {
		if l.Selected {
			return l
		}
	}
	t.Fatal("no selected line")
	return bodyLine{}
}

// naturalWidth is a fixed column's content width: its widest header or cell,
// capped at the column's Width when set.
func naturalWidth(cols []column, rows [][]cell, i int) int {
	w := ansi.StringWidth(cols[i].Title)
	for _, cells := range rows {
		w = max(w, ansi.StringWidth(plain(cells[i].Text)))
	}
	if cols[i].Width > 0 {
		w = min(w, cols[i].Width)
	}
	return w
}
