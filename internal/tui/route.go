package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

func (m *model) routeScreenKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()

	opts := m.selectedOptions()

	if m.currentView() == viewList && m.tab == 0 {
		switch {
		case matchesKey(s, keys.Run, keys.Pause, keys.Favorite, keys.Finished, keys.Prune, keys.Delete, keys.Attach, keys.Resume, keys.Issue, keys.Parent):
			return m.jobKeyAction(s)
		}
	}
	if m.currentView() == viewList && m.tab == 2 {
		if cmd, handled := m.workflowListKey(k); handled {
			return cmd
		}
	}
	r, ok := m.selected()
	if (keys.Attach.matchesString(s) || keys.Resume.matchesString(s) || keys.Issue.matchesString(s)) && (m.currentView() == viewJob || m.currentView() == viewWorkflow) {
		m.reconcileDetailSelection(m.detailItems())
		r, ok = m.nestedRun()
		if !ok {
			return nil
		}
		return m.runAction(s, m.data.Runs[r.run], m.optionsForRow(r, true))
	}
	if m.currentView() == viewList && m.tab == 1 {
		switch {
		case matchesKey(s, keys.Run, keys.Pause, keys.Favorite, keys.Finished, keys.Prune, keys.Delete, keys.Edit, keys.New, keys.Parent):
			return nil
		case matchesKey(s, keys.Attach, keys.Resume, keys.Issue):
			if ok {
				return m.runAction(s, m.data.Runs[r.run], opts)
			}
			return nil
		}
	}

	switch {
	case matchesKey(s, keys.Reload):
		return m.refresh()
	case matchesKey(s, keys.Quit, keys.Interrupt):
		return tea.Quit
	case matchesKey(s, keys.KataTab):
		if _, ok := m.selectedOptions().Service.(kataCommander); !ok && !m.multipleProjects() {
			m.notify(statusInfo, "Kata unavailable")
			return nil
		}
		return m.requestKata()
	case matchesKey(s, keys.JobsTab, keys.RunsTab, keys.WorkflowsTab):
		m.selectTab(int(s[0] - '2'))
	case matchesKey(s, keys.NextTab):
		if m.tab == 2 {
			return m.cycleToKata(0)
		}
		m.selectTab(m.tab + 1)
	case matchesKey(s, keys.PrevTab):
		if m.tab == 0 {
			return m.cycleToKata(2)
		}
		m.selectTab(m.tab - 1)
	case matchesKey(s, keys.Layout):
		_, split := m.splitGeometry()
		m.listOnly = split
	case matchesKey(s, keys.Down, keys.ScrollDown):
		m.move(1)
	case matchesKey(s, keys.Up, keys.ScrollUp):
		m.move(-1)
	case matchesKey(s, keys.NextPage, keys.NextPageAlt):
		m.move(m.pageRows())
	case matchesKey(s, keys.PrevPage, keys.PrevPageAlt):
		m.move(-m.pageRows())
	case matchesKey(s, keys.PageDown, keys.HalfDown):
		m.scroll += m.pageSize()
		m.follow = false
	case matchesKey(s, keys.PageUp, keys.HalfUp):
		m.scroll = max(0, m.scroll-m.pageSize())
		m.follow = false
	case matchesKey(s, keys.Back, keys.Left, keys.Cancel, keys.Backspace):
		if m.currentView() != viewList {
			m.popDetail()
			return nil
		}
		m.query = ""
		m.rebuild()
	case matchesKey(s, keys.Open, keys.Right):
		return m.open()
	case matchesKey(s, keys.Accept):
		return m.open()
	case matchesKey(s, keys.Search):
		m.searching = true
	case matchesKey(s, keys.Mouse):
		m.mouseOff = !m.mouseOff
		if m.mouseOff {
			m.notify(statusInfo, "mouse released — the terminal's own selection is back (M takes it again)")
			return nil
		}
		m.notify(statusInfo, "mouse on")
		return nil
	case matchesKey(s, keys.Kata):
		return m.requestKata()
	case matchesKey(s, keys.Toggle):
		if r.kind == "run" && ok && runExpandable(m.data.Runs[r.run]) {
			uid := m.data.Runs[r.run].UID
			m.expanded[uid] = !m.expanded[uid]
			m.follow = true
			m.rebuild()
		}
		if r.kind == "workflow" && ok && m.workflowExpandable(r) {
			key := workflowExpandKey(r)
			m.expanded[key] = !m.expanded[key]
			m.follow = true
		}
	case matchesKey(s, keys.Edit, keys.New):
		if keys.New.matchesString(s) && m.tab == 0 {
			if m.multipleProjects() {
				m.chooseProject("for new job", opts.Project)
				return nil
			}
			if len(m.opts.Projects) == 1 {
				for _, lane := range m.opts.Projects {
					opts = *lane
				}
			}
			if opts.Service == nil {
				m.notify(statusInfo, "no project accepts new jobs")
				return nil
			}
			return m.newJob(opts)
		}
		if r.kind == "workflow" && keys.Edit.matchesString(s) && ok && opts.Service != nil {
			m.editorProject = opts.Project
			m.editor = workflowFormForLane(m.ctx, opts, m.data.Workflows[r.wf])
			return m.editor.Init()
		}
		if r.kind == "job" {
			return m.editJob()
		}
	}
	return nil
}

// Counts formats the pinned sidebar row using the header's run classifier.
func Counts(data Data) string {
	c := countRuns(data.Runs)
	var parts []string
	if c.Running > 0 {
		parts = append(parts, fmt.Sprintf("%d running", c.Running))
	}
	if c.Held > 0 {
		parts = append(parts, fmt.Sprintf("%d parked", c.Held))
	}
	return strings.Join(parts, " · ")
}

func (m *model) runAction(key string, r runner.Run, opts Lane) tea.Cmd {
	switch {
	case matchesKey(key, keys.Attach):
		if runner.AgentReference(&r).PaneID == "" {
			m.notify(statusInfo, "no agent pane for this run")
			if m.currentView() == viewList {
				m.notify(statusInfo, m.notice.Text+" · l opens its record")
			}
			return nil
		}
		focus := opts.Service
		if focus == nil && m.currentView() == viewList {
			focus = m.opts.Service
		}
		if focus != nil {
			return m.action("agent focused", func() error { return focus.Focus(r) })
		}
	case matchesKey(key, keys.Resume):
		if r.Phase != runner.Held {
			m.notify(statusInfo, "Resume requires a parked run")
			return nil
		}
		if admissionHold(r) {
			m.notify(statusInfo, runNextAction(r))
			return nil
		}
		if requester, ok := opts.requester().(resumeRequester); ok {
			return m.resumeAction(m.acknowledgement("resume requested", opts), r, requester)
		}
		m.notify(statusInfo, "resume unavailable for this project")
		return nil
	case matchesKey(key, keys.Issue):
		if r.IssueRef == "" && r.IssueUID == "" {
			m.notify(statusInfo, "run has no Kata issue")
			return nil
		}
		ref := r.IssueRef
		if ref == "" {
			ref = r.IssueUID
		}
		return m.openKataIssue(opts, ref)
	}
	m.notify(statusInfo, "project "+clean(m.projectName(r.Target.Identity.ProjectUID))+" unavailable")
	return nil
}

// paste types bracketed-paste text into the search query, as v1 delivered it
// to the search field as typed runes.
func (m *model) paste(text string) tea.Cmd {
	if m.openingEditor || len(m.projectChoices) > 0 || m.conflict != nil || len(m.confirm) > 0 || m.currentView() != viewList || !m.searching {
		return nil
	}
	m.query += text
	m.follow = true
	m.rebuild()
	return nil
}

// listState is owned by this view.
type listState struct {
	follow    bool
	tab       int
	cursor    int
	scroll    int
	query     string
	searching bool
	finished  bool
	rows      []row
}

func (m *model) openingKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()

	switch {
	case matchesKey(s, keys.Quit, keys.Interrupt):
		return tea.Quit
	case matchesKey(s, keys.Cancel):
		m.openingEditor = false
		m.generation++
		m.notify(statusInfo, "edit cancelled")
	}
	return nil

}

func (m *model) pickerKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()

	switch {
	case matchesKey(s, keys.Cancel):
		m.projectChoices, m.projectWrapTo = nil, nil
	case matchesKey(s, keys.Interrupt):
		return tea.Quit
	case matchesKey(s, keys.Down, keys.ScrollDown, keys.NextTab):
		m.projectCursor = (m.projectCursor + 1) % len(m.projectChoices)
	case matchesKey(s, keys.Up, keys.ScrollUp, keys.PrevTab):
		m.projectCursor = (m.projectCursor + len(m.projectChoices) - 1) % len(m.projectChoices)
	case matchesKey(s, keys.Accept):
		opts := *m.opts.Projects[m.projectChoices[m.projectCursor]]
		if m.projectPurpose == "to open Kata" {
			wrap := m.projectWrapTo
			m.projectChoices, m.projectWrapTo = nil, nil
			return m.wrapRefusedKata(m.openKata(opts), wrap)
		}
		cmd := m.newJob(opts)
		if cmd != nil {
			m.projectChoices = nil
		}
		return cmd
	}
	return nil

}

func (m *model) conflictKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()

	switch {
	case matchesKey(s, keys.Cancel):
		m.conflict = nil
		m.modalScroll = 0
		m.notify(statusInfo, "operation discarded; nothing retried")
	case matchesKey(s, keys.Quit, keys.Interrupt):
		return tea.Quit
	case matchesKey(s, keys.Down, keys.ScrollDown):
		m.modalScroll++
	case matchesKey(s, keys.Up, keys.ScrollUp):
		m.modalScroll = max(0, m.modalScroll-1)
	case matchesKey(s, keys.NextPage, keys.PageDown):
		m.modalScroll += m.pageSize()
	case matchesKey(s, keys.PrevPage, keys.PageUp):
		m.modalScroll = max(0, m.modalScroll-m.pageSize())
	}
	return nil

}

func (m *model) searchKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()

	switch {
	case matchesKey(s, keys.Interrupt):
		return tea.Quit
	case matchesKey(s, keys.Cancel):
		m.searching = false

	case matchesKey(s, keys.Accept):
		m.searching = false
	case matchesKey(s, keys.Toggle):
		m.query += " "
	case matchesKey(s, keys.HalfUp):
		m.query = ""
	case matchesKey(s, keys.Backspace):
		if m.query == "" {
			m.searching = false
		}
		r := []rune(m.query)
		if len(r) > 0 {
			m.query = string(r[:len(r)-1])
		}
	default:
		m.query += typedText(k)
	}
	m.follow = true
	m.rebuild()
	return nil

}

// routeKey's precedence is form, picker, box, detail, search, then screen.
func (m *model) routeKey(k tea.KeyPressMsg) tea.Cmd {
	if cmd, handled := m.routeFormKey(k); handled {
		return cmd
	}
	if cmd, handled := m.routePickerKey(k); handled {
		return cmd
	}
	if cmd, handled := m.routeBoxKey(k); handled {
		return cmd
	}
	if cmd, handled := m.routeDetailKey(k); handled {
		return cmd
	}
	if cmd, handled := m.routeSearchKey(k); handled {
		return cmd
	}
	return m.routeScreenKey(k)
}
func (m *model) routeFormKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	if m.editor != nil {
		return m.editorUpdate(k), true
	}
	if m.openingEditor {
		return m.openingKey(k), true
	}
	return nil, false
}
func (m *model) routePickerKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	if len(m.projectChoices) > 0 {
		return m.pickerKey(k), true
	}
	return nil, false
}
func (m *model) routeBoxKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	if m.conflict != nil {
		return m.conflictKey(k), true
	}
	if len(m.confirm) > 0 {
		return m.confirmJobKey(k.String()), true
	}
	return nil, false
}
func (m *model) routeDetailKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	switch m.currentView() {
	case viewWorkflow:
		return m.workflowDetailKey(k), true
	case viewRun:
		return m.runDetailKey(k), true
	case viewJob:
		return m.jobDetailKey(k), true
	}
	return nil, false
}
func (m *model) routeSearchKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	if m.searching {
		return m.searchKey(k), true
	}
	return nil, false
}
