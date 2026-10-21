package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

// markSubtle gives the subtle role a unique color so a test can tell a dim
// value from a default one, as Kata's labels and values differ.
func markSubtle(t *testing.T) string {
	t.Helper()
	preserveTheme(t)
	applyColorMode(colorDark, false)
	subtleStyle = subtleStyle.Foreground(lipgloss.Color("123"))
	return "\x1b[38;5;123m"
}

// dimAt reports whether the last SGR sequence before value is the subtle one.
func dimAt(text, value, subtle string) bool {
	before, _, found := strings.Cut(text, value)
	if !found {
		return false
	}
	j := strings.LastIndex(before, "\x1b[")
	return j >= 0 && strings.HasPrefix(before[j:], subtle)
}

// Kata's metadata rows draw the label subtle and the value in the default
// color, including default values such as "none".
func TestDetailValuesUseDefaultColor(t *testing.T) {
	subtle := markSubtle(t)
	m := jobDetailFixture(t, `{"enabled":true,"action":{"kind":"execute","prompt":"Example"},"options":{"agent":"worker","extra":true}}`)
	text := bodyTextForTest(m.buildJobDetail().Body())
	for _, value := range []string{"run directory", "agent default", "(preserved)", "(default)", "codex -m example-model"} {
		line := ""
		for l := range strings.SplitSeq(text, "\n") {
			if strings.Contains(l, value) {
				line = l
				break
			}
		}
		if line == "" {
			t.Fatalf("missing %q:\n%s", value, text)
		}
		if dimAt(line, value, subtle) {
			t.Errorf("value %q is dim: %q", value, line)
		}
		if !strings.HasPrefix(line, subtle) {
			t.Errorf("label beside %q lost its subtle role: %q", value, line)
		}
	}
	v := m.jobs.byKey[jobKey{"p", "job"}]
	inspector := bodyTextForTest(m.renderJobInspector(v, 100))
	for _, value := range []string{"run dir", "(default)"} {
		if dimAt(inspector, value, subtle) {
			t.Errorf("inspector value %q is dim: %q", value, inspector)
		}
	}
}

// List rows keep the default color in every cell, as Kata's issue list does;
// only status carries color and a — placeholder is subtle.
func TestListCellsUseDefaultColor(t *testing.T) {
	subtle := markSubtle(t)
	m := jobsFixture(t, "multi-project", 160, 24)
	m.rebuildJobIndex()
	for _, tab := range []int{0, 1} {
		m.selectTab(tab)
		var cols []column
		var cells []cell
		if tab == 0 {
			cols, cells = m.jobsColumns(), m.jobCells(m.jobs.byKey[jobKey{"p", "job-0"}])
		} else {
			cols = m.runsColumns()
			for _, r := range m.rows {
				if run := m.data.Runs[r.run]; run.Phase == runner.Settled && run.Outcome == "succeeded" {
					cells = m.runCells(r)
				}
			}
		}
		if len(cells) != len(cols) {
			t.Fatalf("tab %d: no row cells", tab)
		}
		for i, c := range cols {
			text := cells[i].Style.Render(cells[i].Text)
			switch {
			case c.Title == "status" || c.Title == " " || cells[i].Text == "":
			case cells[i].Text == "—":
				if !strings.HasPrefix(text, subtle) {
					t.Errorf("tab %d %s placeholder is not subtle: %q", tab, c.Title, text)
				}
			case strings.HasPrefix(text, subtle):
				t.Errorf("tab %d %s cell is dim: %q", tab, c.Title, text)
			}
		}
	}
}

func TestWorkflowCellsUseDefaultColor(t *testing.T) {
	subtle := markSubtle(t)
	m := workflowModel(t, workflowF1GoldenSnapshot())
	cols, cells := m.workflowColumns(), m.workflowCells(m.selectedWorkflow())
	for i, c := range cols {
		if c.Title == "status" || cells[i].Text == "—" {
			continue
		}
		if text := cells[i].Style.Render(cells[i].Text); strings.HasPrefix(text, subtle) {
			t.Errorf("%s cell is dim: %q", c.Title, text)
		}
	}
}
