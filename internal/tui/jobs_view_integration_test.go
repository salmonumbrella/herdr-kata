package tui

import (
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

func TestJobsCrossScreenFlow(t *testing.T) {
	m, a, b := projectBoard(t)
	for i := range m.data.Runs {
		m.data.Runs[i].Job.UID = "same-job"
	}
	m.Update(dataMsg{data: m.data})
	var draft kata.Job

	send(m, "/")
	send(m, "Beta")
	send(m, "enter")
	m.follow = false
	floorKey := m.rows[m.cursor].identity()
	send(m, "l")
	selected := m.detailKey
	send(m, "l")
	if m.currentView() != viewRun {
		t.Fatal(m.nav)
	}
	send(m, "l")
	if m.currentView() != viewJob || len(m.nav) != 2 || m.detailKey != selected {
		t.Fatal(m.nav, m.detailKey)
	}
	send(m, "e")
	form, ok := m.editor.(*jobForm)
	if !ok {
		t.Fatal("job form did not open", m.notice.Text)
	}
	draft = form.job
	if form.projectUID != "project-b" || draft.UID != "same-job" || draft.ProjectUID != "project-b" || !strings.Contains(form.View(), "Beta") {
		t.Fatal("wrong project draft", draft, form.View())
	}
	saved := draft
	saved.Name = "Beta updated"
	saved.DefinitionEventUID = "next-event"
	m.Update(editorClosedMsg{Message: "Beta updated saved", Target: editorTarget{ProjectUID: "project-b", UID: "same-job"}, Saved: &saved})
	if m.currentView() != viewJob || len(m.nav) != 2 || m.detailKey != selected {
		t.Fatal("save lost the caller", m.nav)
	}
	data := m.data
	data.Jobs[1].Name = saved.Name
	data.Jobs[1].DefinitionEventUID = saved.DefinitionEventUID
	data.Jobs[0], data.Jobs[1] = data.Jobs[1], data.Jobs[0]
	m.Update(dataMsg{data: data})
	send(m, "h")
	if m.currentView() != viewList || m.query != "Beta" || m.follow || m.rows[m.cursor].identity() != floorKey || len(a.calls) != 0 || len(b.calls) != 0 {
		t.Fatal(m.nav, m.query, m.follow, a.calls, b.calls)
	}
}
