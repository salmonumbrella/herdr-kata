package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

func TestEditorContextFrozen(t *testing.T) {
	m, _, _ := projectBoard(t)
	m.data.Projects["project-a"] = Data{Project: "example-project", Health: map[string]any{"version": float64(1), "project_uid": "project-a"}, Route: map[string]any{"allowed": true}}
	m.opts.Projects["example-project"].Config = config.Config{Checkouts: map[string]string{"example": "/srv/projects/example"}}
	m.data.Jobs[0].Definition = []byte(`{"checkout_key":"example"}`)
	m.data.Jobs[1].Name = "Alpha"
	m.rebuildJobIndex()
	m.rebuild()
	m.favorites[scopedRowKey("project-a", "same-job")] = true
	j := m.job(m.rows[0])
	ec, opts, err := m.editorContext(*m.opts.Projects["example-project"], &j)
	if err != nil || ec.ProjectUID != "project-a" || opts.ProjectUID != "project-a" || !ec.Favorite || ec.DefaultCheckout != "example" {
		t.Fatalf("context %+v opts %+v error %v", ec, opts, err)
	}
	if ec.NameTaken("Alpha", "same-job") || !ec.NameTaken(" Alpha ", "other") {
		t.Fatal("name exclusion/trim")
	}
	m.data.Jobs = nil
	m.rebuildJobIndex()
	if !ec.NameTaken("Alpha", "other") {
		t.Fatal("context changed with poll")
	}
}
func TestNewJobPickerRoutes(t *testing.T) {
	m, _, _ := projectBoard(t)
	lane := m.data.Projects["project-a"]
	lane.Route = map[string]any{"allowed": false, "access_lost": true}
	m.data.Projects["project-a"] = lane
	send(m, "n")
	send(m, "enter")
	if len(m.projectChoices) != 2 || m.notice.Text != "project example-project unavailable: access_lost" {
		t.Fatalf("picker %v status %q", m.projectChoices, m.notice.Text)
	}
	m.opts.Projects = nil
	send(m, "esc")
	send(m, "n")
	if m.notice.Text != "no project accepts new jobs" {
		t.Fatal(m.notice.Text)
	}
}
func TestProjectPickerLocalCounts(t *testing.T) {
	m, _, _ := projectBoard(t)
	send(m, "n")
	got := m.viewContent()
	if strings.Contains(got, "l detail") {
		t.Fatal("picker shows list help", got)
	}
	for _, want := range []string{"jobs", "new job in project", "example-project", "team-project", "1 jobs", "route ok"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %s", want, got)
		}
	}
}

func TestEditorLegacyWorkflowsLocal(t *testing.T) {
	m := newModel(Options{Root: t.TempDir()})
	m.data.Health = map[string]any{"version": float64(1), "project_uid": "p"}
	m.data.Workflows = []kata.Workflow{{UID: "flow", Name: "Example workflow"}}
	flows := m.workflowsFor(Lane{ProjectUID: "p", Project: "example-project"})
	if len(flows) != 1 || flows[0].UID != "flow" {
		t.Fatalf("legacy workflow lost: %v", flows)
	}
}

func TestExistingEditorOpensWithOfflineRoute(t *testing.T) {
	m, _, _ := projectBoard(t)
	opts := m.opts.Projects["example-project"]
	lane := m.data.Projects["project-a"]
	lane.Route = map[string]any{"allowed": false, "access_lost": true}
	m.data.Projects["project-a"] = lane
	j := m.job(m.rows[0])
	if _, _, err := m.editorContext(*opts, &j); err != nil {
		t.Fatal("offline favorite edit cannot open", err)
	}
	if cmd := m.newJob(*opts); cmd != nil || m.notice.Text != "project example-project unavailable: access_lost" {
		t.Fatal("offline new job route accepted", m.notice.Text)
	}
}

func TestProjectPickerKeepsSelectionVisible(t *testing.T) {
	m := newModel(Options{Root: t.TempDir()})
	m.width, m.height = 80, 12
	m.opts.Projects = map[string]*Lane{}
	for i := range 30 {
		name := fmt.Sprintf("project-%02d", i)
		m.opts.Projects[name] = &Lane{Project: name}
		m.projectChoices = append(m.projectChoices, name)
	}
	m.projectPurpose = "for new job"
	for range 25 {
		m.Update(press("j"))
	}
	text := ansi.Strip(m.viewContent())
	if !strings.Contains(text, "▶ project-25") {
		t.Fatal("selected project is outside the viewport", text)
	}
	for range 5 {
		m.Update(press("j"))
	}
	if text := ansi.Strip(m.viewContent()); !strings.Contains(text, "▶ project-00") {
		t.Fatal("wrapped selection stayed offscreen", text)
	}
}
