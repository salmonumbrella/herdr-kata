package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestLaneBuildsJobFormLocally(t *testing.T) {
	svc := &laneFormService{fakeActions: &fakeActions{}, fakeWriter: &fakeWriter{}}
	view := cfg()
	view.Kata.Project = "example-project"
	m := newModel(Options{Root: t.TempDir(), Config: view, ProjectUID: "project", Project: "example-project", Service: svc})
	m.Update(dataMsg{data: Data{Health: map[string]any{"version": float64(1), "project_uid": "project"}, Route: map[string]any{"allowed": true}}})
	send(m, "n")
	send(m, "enter")
	send(m, "Draft")
	if text := ansi.Strip(m.viewContent()); !strings.Contains(text, "new job") || !strings.Contains(text, "Draft") || !strings.Contains(text, "project example-project") {
		t.Fatal(text)
	}
	if len(svc.calls) != 0 || svc.saved.UID != "" {
		t.Fatal("opening and typing reached the service", svc.calls, svc.saved)
	}
}

func TestLaneRefusesMismatchedProject(t *testing.T) {
	m := newModel(Options{Root: t.TempDir(), Projects: map[string]*Lane{"example-project": {Project: "example-project", Service: &fakeActions{}}}, ForProject: func(_, name string) Lane { return Lane{Project: name, ProjectUID: "other", Service: &fakeActions{}} }})
	m.Update(dataMsg{data: Data{Health: map[string]any{"version": float64(1), "project_uid": "project"}}})
	send(m, "n")
	if m.editor != nil || !strings.Contains(m.viewContent(), "project identity changed") {
		t.Fatal(m.viewContent())
	}
}
