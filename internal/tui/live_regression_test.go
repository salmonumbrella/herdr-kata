package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

func TestJobsFooterKeepsFullActions(t *testing.T) {
	for _, width := range []int{80, 120, 200} {
		m := jobsFixture(t, "empty", width, 40)
		text := ansi.Strip(m.viewContent())
		for _, want := range []string{"search", "run", "fav", "finished", "prune", "pause", "new", "edit", "delete", "attach", "parent", "mouse", "quit"} {
			if !strings.Contains(text, want) {
				t.Errorf("width %d: missing %s in %s", width, want, text)
			}
		}
	}
}

func TestJobDetailMouseStatus(t *testing.T) {
	m := detailFixture(t, "empty", 120, 40)
	for _, want := range []string{"mouse released — the terminal's own selection is back (M takes it again)", "mouse on"} {
		send(m, "M")
		if !strings.Contains(ansi.Strip(m.viewContent()), want) {
			t.Fatal("missing mouse status", m.viewContent())
		}
	}
}

func TestJobDetailWorkflowSpacing(t *testing.T) {
	m := jobDetailFixture(t, `{"enabled":true,"action":{"kind":"execute","workflow_uid":"flow"}}`)
	m.data.Workflows = []kata.Workflow{{UID: "flow", Name: "Example workflow", Definition: json.RawMessage(`{"version":1,"steps":[{"key":"check","kind":"command","command":"true"}]}`), ProjectUID: "p"}}
	m.rebuildJobIndex()
	text := ansi.Strip(bodyTextForTest(m.buildJobDetail().Body()))
	if !strings.HasPrefix(text, "Example job\n\n") || !strings.Contains(text, "\nWorkflow\n  1 step: check\n\nRuns\n") {
		t.Fatal(text)
	}
}

func TestWorkflowDetailSingularAndSpacing(t *testing.T) {
	d := workflowSnapshot()
	d.Workflows[0].Definition = json.RawMessage(`{"version":1,"steps":[{"key":"check","kind":"prompt","prompt":"Check"}]}`)
	m := openWorkflow(t, d)
	m.Update(tea.WindowSizeMsg{Width: 240, Height: 80})
	text := ansi.Strip(bodyTextForTest(m.buildWorkflowDetail().Body()))
	if !strings.Contains(text, "up to 1 prompt per generation (1 step attempt + 0 extra loop attempts)") {
		t.Error(text)
	}
	lines := strings.Split(text, "\n")
	if len(lines) < 4 || strings.TrimSpace(lines[1]) != "" {
		t.Error("missing blank after title", text)
	}
	d.Workflows[0].Definition = json.RawMessage(`{"version":1,"options":{"on_failure":"retry"},"steps":[{"key":"check","kind":"prompt","prompt":"Check"}]}`)
	m.Update(dataMsg{data: d})
	text = ansi.Strip(bodyTextForTest(m.buildWorkflowDetail().Body()))
	if !strings.Contains(text, "fix it with e · r reloads\n\n") {
		t.Error("missing blank after problems", text)
	}
}

func TestJobDetailUsesKnownParentRef(t *testing.T) {
	for _, shape := range []string{"recurring", "prompt"} {
		for _, lane := range []string{"p", "other"} {
			raw := `{"enabled":true,"action":{"kind":"notify"},"issue":{"kind":"per-run","parent_uid":"parent"}}`
			if shape == "prompt" {
				raw = `{"enabled":true,"action":{"kind":"execute","prompt":"Work"},"options":{"run_parent":"parent"}}`
			}
			m := jobDetailFixture(t, raw)
			m.data.Runs = []runner.Run{{UID: "evidence", Target: runner.Target{Identity: kata.Identity{ProjectUID: lane}}, IssueUID: "parent", IssueRef: "example-project#ab12"}}
			m.rebuildJobIndex()
			text := ansi.Strip(bodyTextForTest(m.buildJobDetail().Body()))
			want := ""
			if lane == "p" {
				want = "example-project#ab12"
			}
			found := false
			for line := range strings.SplitSeq(text, "\n") {
				fields := strings.Fields(line)
				if len(fields) > 1 && (fields[0] == "parent" || fields[0] == "run" && fields[1] == "parent") {
					found = true
					if fields[len(fields)-1] != want {
						t.Error(shape, lane, line)
					}
				}
			}
			if !found && want != "" {
				t.Fatal("parent not shown", text)
			}
		}
	}
}

func TestDeletedJobKeepsNameAndHelp(t *testing.T) {
	m := detailFixture(t, "empty", 120, 40)
	m.Update(struct{}{})
	send(m, "h")
	send(m, "l")
	m.Update(dataMsg{data: Data{}})
	text := ansi.Strip(m.viewContent())
	if !strings.HasPrefix(text, "Example job\n") {
		t.Error("lost the known job name", text)
	}
	if !strings.Contains(text, "deleted in Kata") || !strings.Contains(text, "h/← back") || !strings.Contains(text, "M mouse") || !strings.Contains(text, "q quit") || strings.Contains(text, "R run") {
		t.Error("deleted frame offers unavailable actions", text)
	}
}

func TestMultiProjectBrokenWorkflowPrefix(t *testing.T) {
	d := workflowSnapshot()
	d.Workflows[0].Definition = json.RawMessage(`{`)
	d.Workflows[0].Project = "other-project"
	m := workflowModel(t, d)
	m.opts.Projects = map[string]*Lane{"example-project": {}, "other-project": {}}
	m.Update(dataMsg{data: d})
	text := ansi.Strip(bodyTextForTest(m.renderWorkflows()))
	if !strings.Contains(text, "! [other-project] Example: ") {
		t.Fatal(text)
	}
}
