package tui

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type capturedSaveWriter struct {
	fakeWriter
	refs []string
}

func (w *capturedSaveWriter) ShowIssue(_ context.Context, ref string) (kata.IssueView, error) {
	w.refs = append(w.refs, ref)
	return kata.IssueView{UID: "01K00000000000000000000001", Status: "open", ProjectUID: "project"}, nil
}

func TestJobSaveCapturesCommandInputs(t *testing.T) {
	for _, recurring := range []bool{false, true} {
		t.Run(map[bool]string{false: "run parent project", true: "recurring mode"}[recurring], func(t *testing.T) {
			w := &capturedSaveWriter{}
			m := newJobForm(t.Context(), cfg(), w, nil, nil, editorContext{ProjectUID: "project"})
			m.Set("name", "Example")
			m.Set("title", "Child")
			if recurring {
				m.Set("mode", "recurring")
				m.Set("trigger", "interval")
				m.Set("schedule", "1m")
				m.Set("parent", "work#abcd")
			} else {
				m.Set("prompt", "Work")
				m.Set("run_parent", "work#abcd")
			}
			cmd := m.save()
			if cmd == nil {
				t.Fatal("save refused", m.View())
			}
			// Commands own their input snapshot even if the live form changes.
			m.Set("mode", map[bool]string{true: "prompt", false: "recurring"}[recurring])
			m.projectUID = "other"
			replacement := &capturedSaveWriter{}
			m.writer = replacement
			msg, ok := cmd().(savedMsg)
			if !ok || msg.err != nil {
				t.Fatalf("captured save: %+v", msg)
			}
			if len(w.refs) != 1 || w.refs[0] != "work#abcd" || w.saved.Name != "Example" || replacement.saved.UID != "" || len(replacement.refs) != 0 {
				t.Fatalf("command used live inputs: original=%+v replacement=%+v", w, replacement)
			}
			var raw map[string]any
			if err := json.Unmarshal(w.saved.Definition, &raw); err != nil {
				t.Fatal(err)
			}
			path := "options.run_parent"
			parent := ""
			if recurring {
				path = "issue.parent_uid"
				parent = "01K00000000000000000000001"
			}
			if get(raw, path) != "01K00000000000000000000001" || w.parent != parent {
				t.Fatalf("payload=%s parent=%s", w.saved.Definition, w.parent)
			}
		})
	}
}
