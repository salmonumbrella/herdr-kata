package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

func editorGolden(t *testing.T, name string, width, height int) *jobForm {
	t.Helper()
	c := cfg()
	c.Kata.Project = "example-project"
	j := kata.Job{UID: "example-job", Name: "example-job", DefinitionEventUID: "event", Definition: []byte(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"interval","interval_seconds":86400,"timezone":"UTC"},"action":{"kind":"execute","prompt":"Check the example and report any changes.\nKeep a short summary."},"issue":{"kind":"per-run","title":"Example {{date}}","body":""},"overlap":"forbid","catchup":"skip","grace_seconds":600,"timeout_seconds":600,"options":{"tags":["example","daily"],"description":"Example daily check"}}`)}
	m := newJobForm(t.Context(), c, &fakeWriter{}, &j, nil, editorContext{ProjectUID: "project", Favorite: true})
	m.width, m.height = width, height
	switch name {
	case "new-job":
		m = newJobForm(t.Context(), c, &fakeWriter{}, nil, nil, editorContext{ProjectUID: "project"})
		m.width, m.height = width, height
	case "edit-prompt":
		chooseField(t, m, "schedule")
	case "textarea-last-field-active", "narrow", "tiny":
		m.Set("body", strings.Repeat("Example body with Unicode 界é and wrapped text. ", 20)+"CARET_END")
		chooseField(t, m, "body")
		editorKey(m, "enter")
	case "error-name-selected":
		m.status = "schedule: interval needs a duration like 1h or 30m"
		m.bannerFocus = true
	case "conflict-below-fold":
		chooseField(t, m, "favorite")
		newer := j
		newer.Name = "example-renamed"
		newer.DefinitionEventUID = "new-event"
		m.Update(savedMsg{newer: &newer, err: errors.New("conflict")})
	case "uncertain":
		chooseField(t, m, "favorite")
		m.phase = phaseUncertain
		m.attempt = &saveAttempt{job: j}
		m.status = "connection interrupted"
		m.bannerFocus = true
	case "workflow-command-only":
		m.workflows = []kata.Workflow{{UID: "flow", Name: "Example command workflow", Definition: []byte(`{"version":1,"steps":[{"key":"check","kind":"command","command":"true"}]}`)}}
		m.Set("mode", "workflow")
		m.Set("workflow", "flow")
		chooseField(t, m, "profile")
	case "recurring-native":
		j.Definition = []byte(`{"version":1,"kind":"job","enabled":false,"trigger":{"kind":"interval","interval_seconds":60,"timezone":"UTC"},"action":{"kind":"notify","recipient":"worker/inbox"},"issue":{"kind":"per-run","parent_uid":"example-parent","title":"Example child","body":"Check the example"},"overlap":"allow","catchup":"latest"}`)
		m = newJobForm(t.Context(), c, &fakeWriter{}, &j, nil, editorContext{ProjectUID: "project"})
		m.width, m.height = width, height
		chooseField(t, m, "parent")
	case "recurring-switch-notice":
		m.Set("mode", "recurring")
		chooseField(t, m, "mode")
	case "dirty-prompt":
		m.Set("name", "changed")
		editorKey(m, "esc")
	}
	return m
}
func TestEditorGolden(t *testing.T) {
	names := []string{"edit-prompt", "new-job", "textarea-last-field-active", "error-name-selected", "conflict-below-fold", "uncertain", "workflow-command-only", "recurring-native", "recurring-switch-notice", "dirty-prompt"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			assertFrames(t, "job-form", name, [][2]int{{120, 40}, {80, 24}}, func(w, h int) string { return editorGolden(t, name, w, h).View() })
		})
	}
	for _, tc := range []struct {
		name string
		w, h int
	}{{"narrow", 40, 12}, {"tiny", 80, 6}} {
		t.Run(tc.name, func(t *testing.T) {
			assertFrames(t, "job-form", tc.name, [][2]int{{tc.w, tc.h}}, func(w, h int) string { return editorGolden(t, tc.name, w, h).View() })
		})
	}
}
