package tui

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

func TestKataShortcutPreservesDetails(t *testing.T) {
	for _, kind := range []string{"job", "run", "workflow"} {
		t.Run(kind, func(t *testing.T) {
			m, a, b := projectBoard(t)
			d := m.data
			for i := range d.Workflows {
				d.Workflows[i].Definition = json.RawMessage(workflowF1)
			}
			m.Update(dataMsg{data: d})
			tab := map[string]int{"job": 0, "run": 1, "workflow": 2}[kind]
			m.selectTab(tab)
			send(m, "/")
			send(m, "Beta")
			send(m, "enter")
			send(m, "l")
			if kind == "workflow" {
				send(m, " ")
			}
			depth := len(m.nav)
			selection, top, scroll := m.detailKey, m.nav[len(m.nav)-1], m.scroll
			expanded := maps.Clone(m.expanded)
			send(m, "K")
			if len(a.calls) != 0 || !slices.Equal(b.calls, []string{"kata"}) {
				t.Fatal("K did not open the selected project's Kata view", a.calls, b.calls)
			}
			now := m.nav[len(m.nav)-1]
			if len(m.nav) != depth || now.view != top.view || now.projectUID != top.projectUID || now.uid != top.uid || m.detailKey != selection || m.scroll != scroll || !maps.Equal(m.expanded, expanded) {
				t.Fatal("K changed detail navigation", m.nav, depth)
			}
			send(m, "h")
			if m.query != "Beta" || m.tab != tab {
				t.Fatal("Kata lost the filtered caller", m.query, m.tab)
			}
		})
	}
}

func TestWorkflowKataShortcutRefusesUnavailableLane(t *testing.T) {
	m, a, b := projectBoard(t)
	m.selectTab(2)
	m.pushDetail(viewWorkflow, "project-b", "same-flow")
	m.opts.Projects["team-project"].ProjectUID = "different-project"
	send(m, "K")
	if len(a.calls)+len(b.calls) != 0 || m.notice.Text != "project team-project unavailable" {
		t.Fatal("unavailable lane fell back", a.calls, b.calls, m.notice.Text)
	}
}

// Keep the workflow's owning project, even when the selected item is a run or
// the definition has no selectable steps.
func TestWorkflowKataShortcutWithRunSelection(t *testing.T) {
	m, a, b := projectBoard(t)
	d := m.data
	d.Workflows[1].Definition = json.RawMessage(`{"version":1,"steps":[]}`)
	d.Runs[1].Workflow = &kata.Workflow{UID: "same-flow"}
	m.Update(dataMsg{data: d})
	m.selectTab(2)
	m.pushDetail(viewWorkflow, "project-b", "same-flow")
	m.Update(struct{}{})
	if m.detailKey != detailItemKey("run", "project-b", "run-b") {
		t.Fatal("fixture did not select the run", m.detailKey)
	}
	send(m, "K")
	if len(a.calls) != 0 || !slices.Equal(b.calls, []string{"kata"}) {
		t.Fatal(a.calls, b.calls)
	}
}
