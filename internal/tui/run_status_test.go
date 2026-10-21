package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

func TestFinishedRunStatusIsGray(t *testing.T) {
	preserveTheme(t)
	for _, mode := range []colorMode{colorDark, colorLight} {
		applyColorMode(mode, mode == colorDark)
		for _, r := range []runner.Run{
			{Phase: runner.Settled, Outcome: "succeeded"},
			{Phase: runner.Settled, Outcome: "cancelled"},
		} {
			word, style := runLabel(r)
			if style.GetForeground() != closedStyle.GetForeground() {
				t.Errorf("%v: finished %q is not Kata's closed gray", mode, word)
			}
			if style.GetForeground() == openStyle.GetForeground() {
				t.Errorf("%v: finished %q still uses the open green", mode, word)
			}
		}
		// A succeeded or skipped step is finished too.
		for _, s := range []runner.StepState{{Status: "succeeded"}, {Status: "skipped"}} {
			if d := stepDisplay(runner.Run{}, s); d.Role.GetForeground() != closedStyle.GetForeground() {
				t.Errorf("%v: finished step %q is not gray", mode, d.Outcome)
			}
		}
	}
}

func TestActiveRunStatusIsColored(t *testing.T) {
	preserveTheme(t)
	for _, mode := range []colorMode{colorDark, colorLight} {
		applyColorMode(mode, mode == colorDark)
		for _, tc := range []struct {
			run  runner.Run
			want string
		}{
			{runner.Run{Phase: runner.Running}, "open"},
			{runner.Run{Phase: runner.Admitted}, "open"},
			{runner.Run{Phase: runner.Prepared}, "open"},
			{runner.Run{Phase: runner.Held}, "attention"},
			{runner.Run{Phase: runner.Blocked}, "attention"},
			{runner.Run{Phase: runner.HandedOff, Outcome: "unknown"}, "attention"},
			{runner.Run{Phase: runner.Settled, Outcome: "unknown"}, "attention"},
			{runner.Run{Phase: runner.Settled, Outcome: "failed"}, "error"},
		} {
			word, style := runLabel(tc.run)
			want := map[string]any{"open": openStyle.GetForeground(), "attention": attentionStyle.GetForeground(), "error": errorStyle.GetForeground()}[tc.want]
			if style.GetForeground() != want {
				t.Errorf("%v: %q is not in the %s color", mode, word, tc.want)
			}
			if style.GetForeground() == closedStyle.GetForeground() {
				t.Errorf("%v: %q is gray like a finished run", mode, word)
			}
		}
	}
}

func TestRowsWithStepsShowKataMarker(t *testing.T) {
	preserveTheme(t)
	applyColorMode(colorDark, true)
	firstCell := func(line string) string { return strings.Join(strings.Fields(ansi.Strip(line)), " ") }

	// A workflow with steps is a parent row: ▸ collapsed, ▾ with its steps.
	m := workflowModel(t, workflowSnapshot())
	m.selectTab(2)
	rows := m.renderWorkflows()
	if got := firstCell(rows[1].Text); !strings.HasPrefix(got, "▸ ") {
		t.Fatalf("workflow row=%q, want Kata's ▸ marker", got)
	}
	send(m, " ")
	rows = m.renderWorkflows()
	if got := firstCell(rows[1].Text); !strings.HasPrefix(got, "▾ ") {
		t.Fatalf("expanded workflow row=%q, want ▾", got)
	}
	v := m.wfIndex[workflowKey{"project-a", "flow"}]
	if len(v.stepLabels) == 0 || len(rows) < 2+len(v.stepLabels) {
		t.Fatalf("expanded workflow shows %d lines for %d steps", len(rows), len(v.stepLabels))
	}
	for i, label := range v.stepLabels {
		if got := firstCell(rows[2+i].Text); !strings.Contains(got, strings.Join(strings.Fields(label), " ")) {
			t.Fatalf("step line %d=%q, want %q", i, got, label)
		}
	}
	send(m, " ")
	if got := firstCell(m.renderWorkflows()[1].Text); !strings.HasPrefix(got, "▸ ") {
		t.Fatalf("collapsed again=%q", got)
	}

	// A workflow without steps has no children and no marker.
	d := workflowSnapshot()
	d.Workflows = []kata.Workflow{{UID: "flow", Name: "Empty", Definition: json.RawMessage(`{"version":1,"steps":[]}`)}}
	m = workflowModel(t, d)
	m.selectTab(2)
	if got := ansi.Strip(m.renderWorkflows()[1].Text); strings.ContainsAny(got, "▸▾") {
		t.Fatalf("stepless workflow row=%q has a marker", got)
	}

	// A run with steps keeps the same marker.
	m, _ = fixture(t)
	m.Update(press("3"))
	if got := firstCell(m.renderRuns()[1].Text); !strings.HasPrefix(got, "▸ ") {
		t.Fatalf("run row=%q, want ▸", got)
	}
}
