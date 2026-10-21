package tui

import (
	tea "charm.land/bubbletea/v2"
	"encoding/json"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"strings"
	"testing"
)

func TestWorkflowRowsHealthPinResort(t *testing.T) {
	d := workflowSnapshot()
	d.Workflows = append(d.Workflows, kata.Workflow{UID: "another", Name: "Alpha", Definition: json.RawMessage(workflowF1)})
	m := workflowModel(t, d)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	send(m, "j")
	if v := m.selectedWorkflow(); v == nil || v.key != (workflowKey{"project-a", "flow"}) {
		t.Fatalf("legacy selected workflow: %+v", v)
	}
	d.Workflows[0].Name = "Aardvark selected"
	m.Update(dataMsg{data: d})
	if v := m.selectedWorkflow(); v == nil || v.key != (workflowKey{"project-a", "flow"}) || m.cursor != 0 {
		t.Fatalf("resort retargeted legacy workflow: %+v cursor=%d", v, m.cursor)
	}
	text := workflowText(m.renderWorkflows())
	if !strings.Contains(text, "Aardvark selected") || !strings.Contains(text, "Alpha") || strings.Contains(text, "lane unavailable") {
		t.Fatal(text)
	}
	send(m, "l")
	if text := workflowText(m.workflowDetail()); !strings.Contains(text, "Aardvark selected") || !strings.Contains(text, "Inspect an example") {
		t.Fatal(text)
	}
}
