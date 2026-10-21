package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type classificationWriter struct {
	fakeWriter
	calls []string
}

func (w *classificationWriter) SaveJob(ctx context.Context, j kata.Job, parent string) (kata.Job, error) {
	w.calls = append(w.calls, "save")
	return w.fakeWriter.SaveJob(ctx, j, parent)
}
func (w *classificationWriter) ShowJob(ctx context.Context, uid string) (kata.Job, error) {
	w.calls = append(w.calls, "show-job")
	return w.fakeWriter.ShowJob(ctx, uid)
}
func (w *classificationWriter) ShowIssue(ctx context.Context, uid string) (kata.IssueView, error) {
	w.calls = append(w.calls, "show-issue")
	return w.fakeWriter.ShowIssue(ctx, uid)
}
func completeSave(t *testing.T, m *jobForm) editorClosedMsg {
	t.Helper()
	cmd := editorKey(m, "ctrl+s")
	if cmd == nil {
		t.Fatal("save refused", m.View())
	}
	msg := cmd()
	if closed, ok := msg.(editorClosedMsg); ok {
		return closed
	}
	_, cmd = m.Update(msg)
	if cmd == nil {
		t.Fatal("save failed", m.View())
	}
	closed, ok := cmd().(editorClosedMsg)
	if !ok {
		t.Fatal("no completion")
	}
	return closed
}
func TestSaveClassification(t *testing.T) {
	j := kata.Job{UID: "job", Name: "example-job", DefinitionEventUID: "event", Definition: json.RawMessage(`{ "version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"work"},"issue":{"kind":"per-run","title":"example"},"overlap":"forbid","catchup":"latest","options":{"opaque":9007199254740993}}`)}
	for _, tc := range []struct {
		name                         string
		newJob, favorite, definition bool
		want                         string
		calls                        int
	}{
		{"unchanged", false, false, false, "no changes", 0}, {"favorite", false, true, false, "example-job pinned", 0}, {"restored", false, true, false, "example-job pinned", 0}, {"definition", false, false, true, "example-job saved", 1}, {"combined", false, true, true, "example-job saved", 1}, {"new", true, false, true, "example-job saved", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &classificationWriter{}
			original := &j
			if tc.newJob {
				original = nil
			}
			m := newJobForm(t.Context(), cfg(), w, original, nil, editorContext{ProjectUID: "project"})
			if tc.newJob {
				m.Set("name", "example-job")
				m.Set("prompt", "work")
			}
			if tc.name == "restored" {
				m.Set("prompt", "changed")
				m.Set("prompt", "work")
			}
			if tc.favorite {
				m.Set("favorite", "true")
			}
			if tc.definition {
				m.Set("description", "changed")
			}
			closed := completeSave(t, m)
			if closed.Message != tc.want || len(w.calls) != tc.calls || closed.Target.ProjectUID != "project" || closed.Target.UID == "" {
				t.Fatalf("completion %+v calls %v", closed, w.calls)
			}
			if (closed.Favorite != nil) != tc.favorite || (closed.Saved != nil) != (tc.calls == 1) {
				t.Fatalf("completion %+v", closed)
			}
			if !tc.newJob && tc.calls == 0 && string(m.job.Definition) != string(j.Definition) {
				t.Fatal("original bytes changed")
			}
		})
	}
	for _, extra := range []string{`,"options":{}`, `,"secret_refs":{}`, `,"timeout_seconds":0`, `,"checkout_key":""`, `,"options":{},"secret_refs":{},"timeout_seconds":0,"checkout_key":""`} {
		for _, favorite := range []bool{false, true} {
			native := kata.Job{UID: "job", Name: "example-job", DefinitionEventUID: "event", Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":false,"trigger":{"kind":"interval","interval_seconds":60},"action":{"kind":"notify","recipient":"worker/inbox"},"issue":{"kind":"per-run","parent_uid":"parent","title":"example"},"overlap":"allow","catchup":"latest"` + extra + `}`)}
			w := &classificationWriter{err: &kata.Error{Code: "offline"}}
			m := newJobForm(t.Context(), cfg(), w, &native, nil, editorContext{ProjectUID: "project"})
			if favorite {
				m.Set("favorite", "true")
			}
			completeSave(t, m)
			if len(w.calls) != 0 || string(m.job.Definition) != string(native.Definition) {
				t.Fatalf("native normalized/called Kata: %v", w.calls)
			}
		}
	}
}
func TestSaveStatusCopy(t *testing.T) {
	m := newJobForm(t.Context(), cfg(), &fakeWriter{}, nil, nil, editorContext{NameTaken: func(string, string) bool { return true }})
	m.Set("name", "example-job")
	m.Set("prompt", "work")
	if editorKey(m, "ctrl+s") != nil || m.status != "another job is already named example-job" {
		t.Fatal(m.status)
	}
	c := cfg()
	c.Kata.Project = "example-project"
	m = newJobForm(t.Context(), c, &fakeWriter{}, nil, nil, editorContext{})
	m.Set("name", "example-job")
	m.Set("mode", "workflow")
	if editorKey(m, "ctrl+s") != nil || m.status != "no workflows in project example-project" {
		t.Fatal(m.status)
	}
}

func TestSaveMissingWorkflow(t *testing.T) {
	c := cfg()
	c.Profiles = nil
	m := newJobForm(t.Context(), c, &fakeWriter{}, nil, []kata.Workflow{{UID: "flow", Name: "Example workflow"}}, editorContext{})
	m.Set("name", "example-job")
	m.Set("mode", "workflow")
	m.Set("workflow", "flow")
	if editorKey(m, "ctrl+s") != nil || m.status != "workflow: cannot decode selected workflow" {
		t.Fatal("malformed workflow accepted", m.status)
	}
}
func TestSaveOriginalBlankTitleFavorite(t *testing.T) {
	j := kata.Job{UID: "job", Name: "example-job", DefinitionEventUID: "event", Definition: []byte(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"work"},"issue":{"kind":"per-run","title":""},"overlap":"forbid","catchup":"latest"}`)}
	w := &classificationWriter{}
	m := newJobForm(t.Context(), cfg(), w, &j, nil, editorContext{ProjectUID: "project"})
	m.Set("favorite", "true")
	closed := completeSave(t, m)
	if len(w.calls) != 0 || closed.Message != "example-job pinned" {
		t.Fatalf("favorite normalized original: %v %+v", w.calls, closed)
	}
}
func TestSaveKnownConflictWithoutReadbackCannotRetry(t *testing.T) {
	w := &classificationWriter{err: &kata.Error{Code: "conflict"}}
	m := newJobForm(t.Context(), cfg(), w, nil, nil, editorContext{ProjectUID: "project"})
	m.Set("name", "example-job")
	m.Set("prompt", "work")
	cmd := editorKey(m, "ctrl+s")
	if cmd == nil {
		t.Fatal(m.View())
	}
	m.Update(cmd())
	view := m.View()
	if !strings.Contains(view, "conflict") || strings.Contains(view, "ctrl+s") || strings.Contains(view, "outcome uncertain") {
		t.Fatal("known conflict banner offers a refused retry", view)
	}
	calls := len(w.calls)
	if editorKey(m, "ctrl+s") != nil || len(w.calls) != calls || m.attempt == nil {
		t.Fatal("known conflict retried/discarded guard")
	}
}

func TestSaveNativeAndValidation(t *testing.T) {
	for _, mode := range []string{"prompt", "workflow", "notify", "recurring"} {
		t.Run(mode, func(t *testing.T) {
			c := cfg()
			c.Kata.Project = "example-project"
			c.Profiles = nil
			w := &classificationWriter{}
			flows := []kata.Workflow{{UID: "flow", Definition: []byte(`{"version":1,"steps":[{"key":"work","kind":"command","command":"true"}]}`)}}
			m := newJobForm(t.Context(), c, w, nil, flows, editorContext{ProjectUID: "project"})
			m.Set("name", "example-job")
			m.Set("mode", mode)
			switch mode {
			case "prompt":
				m.Set("prompt", "work")
			case "workflow":
				m.Set("workflow", "flow")
			case "notify":
				m.Set("recipient", "worker")
				m.Set("message", "due")
				m.Set("issue", "01K00000000000000000000001")
			case "recurring":
				m.Set("trigger", "interval")
				m.Set("schedule", "1m")
				m.Set("parent", "01K00000000000000000000001")
				m.Set("title", "Example child")
			}
			if mode == "prompt" {
				if editorKey(m, "ctrl+s") != nil || len(w.calls) != 0 || m.status != "no profile allowed for project example-project" {
					t.Fatal(m.status, w.calls)
				}
				return
			}
			completeSave(t, m)
			if len(w.calls) != 1 {
				t.Fatal(w.calls)
			}
			if mode == "workflow" {
				var raw map[string]any
				_ = json.Unmarshal(w.saved.Definition, &raw)
				if get(raw, "issue.title") != "example-job" {
					t.Fatal("blank title not filled", string(w.saved.Definition))
				}
			}
			if mode == "recurring" {
				var raw map[string]any
				_ = json.Unmarshal(w.saved.Definition, &raw)
				if raw["enabled"] != false || raw["options"] != nil || raw["secret_refs"] != nil {
					t.Fatal("native output", string(w.saved.Definition))
				}
			}
		})
	}
}
func TestSaveFrozenUncertainty(t *testing.T) {
	j := kata.Job{UID: "job", Name: "example-job", DefinitionEventUID: "old-event", Definition: []byte(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"work"},"issue":{"kind":"per-run","title":"example"},"overlap":"forbid","catchup":"latest"}`)}
	w := &classificationWriter{err: &kata.Error{Code: "process_error"}}
	m := newJobForm(t.Context(), cfg(), w, &j, nil, editorContext{ProjectUID: "project"})
	m.Set("description", "changed")
	cmd := editorKey(m, "ctrl+s")
	if cmd == nil {
		t.Fatal(m.View())
	}
	m.Update(cmd())
	first := w.saved
	if m.attempt == nil {
		t.Fatal("uncertain draft not frozen")
	}
	editorKey(m, "j")
	editorKey(m, "enter")
	editorKey(m, "changed")
	w.err = nil
	completeSave(t, m)
	if len(w.calls) != 3 || w.saved.UID != "job" || w.saved.DefinitionEventUID != "old-event" || string(w.saved.Definition) != string(first.Definition) {
		t.Fatal("frozen retry changed", w.calls, w.saved)
	}
}

// A failed save must offer only actions that its retained guard permits.
func TestSaveOutcomePresentationAndGuard(t *testing.T) {
	j := kata.Job{UID: "job", Name: "example-job", DefinitionEventUID: "event", Definition: []byte(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"work"},"issue":{"kind":"per-run","title":"example"},"overlap":"forbid","catchup":"latest"}`)}
	canonical := j
	canonical.Definition = append([]byte(nil), j.Definition...)
	for _, tc := range []struct {
		name                  string
		msg                   savedMsg
		phase                 savePhase
		locked, retry, frozen bool
		banner                string
	}{
		{"plain failure", savedMsg{err: &kata.Error{Code: "invalid"}}, phaseEditing, false, false, false, "kata invalid"},
		{"uncertain", savedMsg{job: canonical, parent: "canonical-parent", err: errors.New("offline"), uncertain: true}, phaseUncertain, true, true, true, "outcome uncertain"},
		{"conflict no readback", savedMsg{err: &kata.Error{Code: "conflict"}}, phaseConflict, true, false, true, "newer definition unavailable"},
		{"conflict readback", savedMsg{newer: &j, err: &kata.Error{Code: "conflict"}}, phaseConflict, true, false, true, "Kata has a newer definition"},
		{"readback with ambiguous cause", savedMsg{job: canonical, parent: "canonical-parent", newer: &j, err: &ConflictError{Newer: j, Cause: errors.New("readback")}, uncertain: true}, phaseConflict, true, false, true, "Kata has a newer definition"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newJobForm(t.Context(), cfg(), &fakeWriter{}, &j, nil, editorContext{})
			m.Set("description", "changed")
			if editorKey(m, "ctrl+s") == nil {
				t.Fatal("save refused")
			}
			m.Update(tc.msg)
			if m.phase != tc.phase || m.locked() != tc.locked || m.canRetry() != tc.retry || (m.attempt != nil) != tc.frozen {
				t.Fatalf("phase=%v locked=%v retry=%v frozen=%v", m.phase, m.locked(), m.canRetry(), m.attempt)
			}
			view := m.View()
			if !strings.Contains(view, tc.banner) || tc.phase == phaseConflict && (strings.Contains(view, "ctrl+s") || strings.Contains(view, "outcome uncertain")) {
				t.Fatal(view)
			}
			if tc.msg.uncertain && (!bytes.Equal(m.attempt.job.Definition, canonical.Definition) || m.attempt.parent != "canonical-parent" || !m.attempt.resolved) {
				t.Fatal("canonical uncertain payload not retained")
			}
			chooseField(t, m, "name")
			editorKey(m, "enter")
			if (m.active == "name") == tc.locked {
				t.Fatal("edit lock disagrees with save outcome", m.View())
			}
			if tc.phase == phaseConflict && editorKey(m, "ctrl+s") != nil {
				t.Fatal("conflict retried")
			}
		})
	}
}
