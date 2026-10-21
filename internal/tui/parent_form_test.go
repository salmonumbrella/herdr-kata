package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type bindingFake struct {
	issue             kata.IssueView
	key, value, guard string
	reads             int
}

func (f *bindingFake) ShowIssue(context.Context, string) (kata.IssueView, error) {
	f.reads++
	f.issue.Revision++
	return f.issue, nil
}
func (f *bindingFake) MetaSet(_ context.Context, ref, key, value string, c kata.Cond) error {
	f.key, f.value, f.guard = key, value, c.IfMatch
	return nil
}
func (f *bindingFake) MetaUnset(_ context.Context, ref, key string, c kata.Cond) error {
	f.key, f.guard = key, c.IfMatch
	return nil
}
func TestParentBindingOneKeyFreshRevision(t *testing.T) {
	f := &bindingFake{issue: kata.IssueView{UID: "parent", Revision: 7, Metadata: map[string]json.RawMessage{"herdr_kata.profile": json.RawMessage(`"default"`)}}}
	m := newParentForm(t.Context(), cfg(), f, "parent", nil)
	m.Update(m.Init()())
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	_ = cmd
	_, cmd = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m.Update(cmd())
	if f.key != "herdr_kata.profile" || f.value != "default" || f.guard != "rev-9" || f.reads != 2 {
		t.Fatal(f)
	}
	if m.View() == "" {
		t.Fatal("missing shared scope")
	}
}

func TestParentBindingChromeGolden(t *testing.T) {
	assertFrames(t, "parent-form", "binding", [][2]int{{120, 40}, {80, 24}}, func(int, int) string {
		m := newParentForm(t.Context(), cfg(), &bindingFake{}, "example-project#ab12", nil)
		view := m.View()
		if !strings.HasPrefix(view, titleStyle.Render("shared parent binding")) || !strings.Contains(view, cursorStyle.Render("▶ ")) {
			t.Fatal("binding editor uses legacy chrome", view)
		}
		return view
	})
}

func TestBindingShowsReferencesAndWorkflowNames(t *testing.T) {
	const uid = "01HZNQ7VFPK1XGD8R5MABCD4EX"
	f := &bindingFake{issue: kata.IssueView{UID: uid, QualifiedID: "example-project#cd4ex", Metadata: map[string]json.RawMessage{"herdr_kata.workflow": json.RawMessage(`"` + uid + `"`)}}}
	c := cfg()
	c.Kata.Project = "example-project"
	m := newParentForm(t.Context(), c, f, uid, []kata.Workflow{{UID: uid, Name: "Example workflow"}})
	m.Update(m.Init()())
	text := m.View()
	if !strings.Contains(text, "example-project#cd4ex") || !strings.Contains(text, "Example workflow") || strings.Contains(text, uid) {
		t.Fatal(text)
	}
}

func TestBindingKeepsWorkflowNameBindingsReadable(t *testing.T) {
	f := &bindingFake{issue: kata.IssueView{UID: "parent", QualifiedID: "example-project#ab12", Metadata: map[string]json.RawMessage{"herdr_kata.workflow": json.RawMessage(`"Example workflow"`)}}}
	m := newParentForm(t.Context(), cfg(), f, "parent", []kata.Workflow{{UID: "flow", Name: "Example workflow"}})
	m.Update(m.Init()())
	if text := m.View(); !strings.Contains(text, "Example workflow") {
		t.Fatal(text)
	}
}
