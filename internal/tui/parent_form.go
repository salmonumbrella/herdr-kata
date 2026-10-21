package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type field struct {
	key, label, kind string
	choices          []string
}

type bindingWriter interface {
	ShowIssue(context.Context, string) (kata.IssueView, error)
	MetaSet(context.Context, string, string, string, kata.Cond) error
	MetaUnset(context.Context, string, string, kata.Cond) error
}
type parentForm struct {
	ctx                context.Context
	writer             bindingWriter
	parent             string
	parentRef, project string
	workflowNames      map[string]string
	fields             []field
	values             []string
	index              int
	busy               bool
	status             string
}
type bindingMsg struct {
	issue  *kata.IssueView
	err    error
	status string
}

func newParentForm(ctx context.Context, cfg config.Config, w bindingWriter, parent string, flows []kata.Workflow) *parentForm {
	workflow := []string{""}
	names := map[string]string{}
	for _, f := range flows {
		if f.DeletedAt == nil {
			workflow = append(workflow, f.UID)
			names[f.UID] = f.Name
		}
	}
	profile, checkout := []string{""}, []string{""}
	for k := range cfg.Profiles {
		profile = append(profile, k)
	}
	for k := range cfg.Checkouts {
		checkout = append(checkout, k)
	}
	sort.Strings(profile)
	sort.Strings(checkout)
	parentRef := ""
	if strings.Contains(parent, "#") {
		parentRef = parent
	}
	return &parentForm{parentRef: parentRef, project: cfg.Kata.Project, workflowNames: names, ctx: ctx, writer: w, parent: parent, values: make([]string, 3), fields: []field{{"herdr_kata.workflow", "Workflow", "choice", workflow}, {"herdr_kata.profile", "Profile", "choice", profile}, {"herdr_kata.checkout", "Checkout", "choice", checkout}}}
}
func (m *parentForm) Init() tea.Cmd {
	m.busy = true
	return func() tea.Msg { i, err := m.writer.ShowIssue(m.ctx, m.parent); return bindingMsg{issue: &i, err: err} }
}
func (m *parentForm) Update(msg tea.Msg) (form, tea.Cmd) {
	switch v := msg.(type) {
	case bindingMsg:
		m.busy = false
		if v.err != nil {
			m.status = v.err.Error()
		} else {
			m.status = v.status
			if v.issue != nil {
				m.parentRef = kata.IssueReference(m.project, *v.issue)
				for i, f := range m.fields {
					_ = json.Unmarshal(v.issue.Metadata[f.key], &m.values[i])
				}
			}
		}
	case tea.KeyPressMsg:
		if m.busy {
			return m, nil
		}
		s := v.String()
		switch {
		case matchesKey(s, keys.Cancel):
			return m, func() tea.Msg { return editorClosedMsg{Message: "parent binding closed"} }
		case matchesKey(s, keys.NextTab):
			m.index = (m.index + 1) % 3
		case matchesKey(s, keys.PrevTab):
			m.index = (m.index + 2) % 3
		case matchesKey(s, keys.Left, keys.Right, keys.Toggle, keys.Accept):
			f := m.fields[m.index]
			idx := 0
			for i, c := range f.choices {
				if c == m.values[m.index] {
					idx = i
				}
			}
			n := 1
			if keys.Left.matchesString(s) {
				n = -1
			}
			m.values[m.index] = f.choices[(idx+n+len(f.choices))%len(f.choices)]
		case matchesKey(s, keys.Save):
			key, value := m.fields[m.index].key, m.values[m.index]
			m.busy = true
			return m, func() tea.Msg {
				i, err := m.writer.ShowIssue(m.ctx, m.parent)
				if err != nil {
					return bindingMsg{err: err}
				}
				// Kata accepts N or rev-N; the real CLI integration verifies this form.
				guard := kata.Cond{IfMatch: fmt.Sprintf("rev-%d", i.Revision)}
				if value == "" {
					err = m.writer.MetaUnset(m.ctx, i.UID, key, guard)
				} else {
					err = m.writer.MetaSet(m.ctx, i.UID, key, value, guard)
				}
				return bindingMsg{err: err, status: key + " saved: " + m.displayBinding(key, value)}
			}
		}
	}
	return m, nil
}
func (m *parentForm) View() string {
	lines := []string{titleStyle.Render("shared parent binding"), subtleStyle.Render(safe(m.parentRef)), "", "This binding affects every job and not-yet-admitted child of this parent.", "Prepared runs keep their frozen binding.", "One key per save · empty value removes that key.", ""}
	for i, f := range m.fields {
		prefix := "  "
		if i == m.index {
			prefix = cursorStyle.Render("▶ ")
		}
		lines = append(lines, prefix+subtleStyle.Render(pad(f.label, 18))+" "+safe(m.displayBinding(f.key, m.values[i])))
	}
	help := footerHelp{Full: []helpItem{hintGroup("tab", "fields", 0, keys.NextTab, keys.PrevTab), hintGroup("\u2190/\u2192", "choose", 0, keys.Left, keys.Right, keys.Accept, keys.Toggle), hintGroup("ctrl+s", "save key", 0, keys.Save, keys.SaveAlt), hintGroup("esc", "close", 0, keys.Cancel)}}
	lines = append(lines, "", subtleStyle.Render(safe(m.status)), help.Render(80))
	return strings.Join(lines, "\n")
}

func (m *parentForm) displayBinding(key, value string) string {
	if key != "herdr_kata.workflow" || value == "" {
		return value
	}
	if name := m.workflowNames[value]; name != "" {
		return name
	}
	for _, name := range m.workflowNames {
		if value == name {
			return name
		}
	}
	return "workflow unavailable"
}
