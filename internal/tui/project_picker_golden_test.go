package tui

import (
	"testing"
)

func TestProjectPickerGolden(t *testing.T) {
	assertFrames(t, "project-picker", "new-job", [][2]int{{120, 40}, {80, 24}}, func(w, h int) string {
		m, _, _ := projectBoard(t)
		m.width, m.height = w, h
		lane := m.data.Projects["project-b"]
		lane.Route = map[string]any{"allowed": false, "access_lost": true}
		m.data.Projects["project-b"] = lane
		m.chooseProject("for new job", "example-project")
		m.frameValid = false
		return m.viewContent()
	})
}
