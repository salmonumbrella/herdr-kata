package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type runParentWriter struct {
	fakeWriter
	issue kata.IssueView
	reads int
	err   error
}

func (f *runParentWriter) ShowIssue(_ context.Context, ref string) (kata.IssueView, error) {
	f.reads++
	if ref != "work#abcd" && ref != "01K00000000000000000000001" {
		panic("unexpected ref " + ref)
	}
	return f.issue, f.err
}
func TestEditorRunParent(t *testing.T) {
	for _, tc := range []struct {
		name, status, project string
		valid                 bool
	}{
		{"open", "open", "project", true}, {"closed", "closed", "project", false}, {"outside", "open", "other", false},
	} {
		for _, ref := range []string{"work#abcd", "01K00000000000000000000001"} {
			t.Run(tc.name+ref, func(t *testing.T) {
				f := &runParentWriter{issue: kata.IssueView{UID: "01K00000000000000000000001", Status: tc.status, ProjectUID: tc.project}}
				m := newJobForm(t.Context(), cfg(), f, nil, nil, editorContext{ProjectUID: "project"})
				// The editor receives the worker's pinned project, never a name guess.

				for k, v := range map[string]string{"name": "Run", "prompt": "Check", "title": "Run child", "run_parent": ref} {
					m.Set(k, v)
				}
				m.Update(tea.WindowSizeMsg{Width: 120, Height: 60})
				if !strings.Contains(m.View(), "Run parent") {
					t.Fatal(m.View())
				}
				_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
				if cmd == nil {
					t.Fatal(m.View())
				}
				m.Update(cmd())
				if f.reads != 1 {
					t.Fatal("parent reads", f.reads)
				}
				if !tc.valid {
					if f.saved.UID != "" {
						t.Fatal("invalid parent saved")
					}
					return
				}
				var raw map[string]any
				_ = json.Unmarshal(f.saved.Definition, &raw)
				if get(raw, "options.run_parent") != "01K00000000000000000000001" || f.parent != "" {
					t.Fatal(string(f.saved.Definition), f.parent)
				}
				round := newJobForm(t.Context(), cfg(), f, &f.saved, nil, editorContext{})
				if round.Value("run_parent") != "01K00000000000000000000001" {
					t.Fatal("parent lost on reopen")
				}
			})
		}
	}
}
func TestSwitchFromRunParentToExisting(t *testing.T) {
	f := &fakeWriter{}
	j := kata.Job{UID: "job", Name: "Run", Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Check"},"issue":{"kind":"per-run","title":"Run child"},"overlap":"forbid","catchup":"latest","options":{"run_parent":"01K00000000000000000000001","unknown":"keep"}}`)}
	m := newJobForm(t.Context(), cfg(), f, &j, nil, editorContext{})
	m.Set("issue_kind", "existing")
	m.Set("issue", "01K00000000000000000000001")
	_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal(m.View())
	}
	m.Update(cmd())
	var raw map[string]any
	_ = json.Unmarshal(f.saved.Definition, &raw)
	if get(raw, "options.run_parent") != nil || get(raw, "options.unknown") != "keep" {
		t.Fatal(string(f.saved.Definition))
	}
}

func TestRunParentUncertainSaveKeepsResolvedRequest(t *testing.T) {
	f := &runParentWriter{fakeWriter: fakeWriter{err: &kata.Error{Code: "process_error"}}, issue: kata.IssueView{UID: "01K00000000000000000000001", Status: "open", ProjectUID: "project"}}
	m := newJobForm(t.Context(), cfg(), f, nil, nil, editorContext{ProjectUID: "project"})
	for k, v := range map[string]string{"name": "Run", "prompt": "Check", "title": "Run child", "run_parent": "work#abcd"} {
		m.Set(k, v)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal(m.View())
	}
	m.Update(cmd())
	first := string(f.saved.Definition)
	f.issue.Status = "closed"
	f.err = &kata.Error{Code: "not_found"}
	_, cmd = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal(m.View())
	}
	m.Update(cmd())
	if f.reads != 1 || string(f.saved.Definition) != first {
		t.Fatal("uncertain save re-resolved parent", f.reads, string(f.saved.Definition))
	}
}
