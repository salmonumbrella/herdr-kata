package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"
)

type workflowWrapKey struct {
	workflow    workflowKey
	event, step string
	width       int
}

func (m *model) workflowSelection(v *workflowView) (kind, uid string, ok bool) {
	kind, project, uid, ok := parseDetailItemKey(m.detailKey)
	if !ok || v == nil || project != v.key.projectUID {
		return "", "", false
	}
	switch kind {
	case "step":
		ok = slices.Contains(v.check.Order, uid)
	case "used-by":
		ok = false
		for _, j := range v.jobs {
			if m.data.Jobs[j.index].UID == uid {
				ok = true
				break
			}
		}
	case "run":
		ok = false
		for _, i := range v.runs {
			r := m.data.Runs[i]
			if r.UID == uid && r.Target.Identity.ProjectUID == project {
				ok = true
				break
			}
		}
	default:
		ok = false
	}
	return kind, uid, ok
}
func (m *model) workflowDetailKey(k tea.KeyPressMsg) tea.Cmd {
	v := m.selectedWorkflow()
	s := k.String()
	kind, uid, valid := m.workflowSelection(v)
	if keys.Open.matchesString(s) || keys.Right.matchesString(s) || keys.Accept.matchesString(s) || keys.Attach.matchesString(s) || keys.Resume.matchesString(s) || keys.Run.matchesString(s) {
		if m.detailKey != "" && !valid {
			m.notify(statusInfo, "invalid detail selection")
			return nil
		}
	}
	switch {
	case matchesKey(s, keys.Quit, keys.Interrupt):
		return tea.Quit
	case matchesKey(s, keys.Back, keys.Left, keys.Cancel, keys.Backspace):
		m.popDetail()
	case matchesKey(s, keys.Down, keys.ScrollDown):
		m.move(1)
	case matchesKey(s, keys.Up, keys.ScrollUp):
		m.move(-1)
	case matchesKey(s, keys.HalfDown, keys.PageDown):
		m.scroll += m.pageSize()
		m.follow = false
	case matchesKey(s, keys.HalfUp, keys.PageUp):
		m.scroll = max(0, m.scroll-m.pageSize())
		m.follow = false
	case matchesKey(s, keys.Toggle, keys.Open, keys.Right, keys.Accept):
		if keys.Toggle.matchesString(s) && kind != "step" {
			return nil
		}
		if !valid {
			return nil
		}
		if kind == "step" {
			m.expanded[m.detailKey] = !m.expanded[m.detailKey]
			m.follow = true
			return nil
		}
		if kind == "used-by" {
			return m.pushDetail(viewJob, v.key.projectUID, uid)
		}
		if kind == "run" {
			return m.pushDetail(viewRun, v.key.projectUID, uid)
		}
	case matchesKey(s, keys.Resume):
		selected := ""
		if kind == "run" && valid {
			selected = uid
		}
		return m.resumeWorkflowRun(v, selected)
	case matchesKey(s, keys.Run):
		if kind == "used-by" && valid {
			return m.runWorkflowJob(v.key.projectUID, uid)
		}
		m.notify(statusInfo, "R runs a job: select one under used by")
	case matchesKey(s, keys.Attach):
		if kind != "run" || !valid {
			m.notify(statusInfo, "select a run to attach")
			return nil
		}
		r, ok := m.nestedRun()
		if !ok {
			return nil
		}
		opts, available := m.workflowOptions(v.key.projectUID, m.data.Workflows[v.index].Project)
		if !available {
			m.notify(statusInfo, "project "+clean(m.projectName(v.key.projectUID))+" unavailable")
			return nil
		}
		return m.runAction("a", m.data.Runs[r.run], opts)
	case matchesKey(s, keys.Edit):
		return m.editWorkflow(v)
	case matchesKey(s, keys.Kata):
		if v == nil {
			m.notify(statusInfo, "workflow was removed")
			return nil
		}
		opts, available := m.workflowOptions(v.key.projectUID, m.data.Workflows[v.index].Project)
		if !available {
			m.notify(statusInfo, "project "+clean(m.projectName(v.key.projectUID))+" unavailable")
			return nil
		}
		return m.openKata(opts)
	case matchesKey(s, keys.Reload):
		return m.refresh()
	case matchesKey(s, keys.Mouse):
		m.mouseOff = !m.mouseOff
		if m.mouseOff {
			m.notify(statusInfo, "mouse released — the terminal's own selection is back (M takes it again)")
			return nil
		}
		m.notify(statusInfo, "mouse on")
		return nil
	case matchesKey(s, keys.New, keys.Delete):
		m.notify(statusInfo, "workflows are created and deleted with kata cron workflow …")
	}
	return nil
}

func (m *model) reconcileWorkflowContent() {
	for k := range m.workflowWraps {
		v := m.wfIndex[k.workflow]
		if v == nil || m.data.Workflows[v.index].DefinitionEventUID != k.event || k.width != m.width || !slices.Contains(v.check.Order, k.step) {
			delete(m.workflowWraps, k)
		}
	}
	if m.currentView() != viewWorkflow {
		return
	}
	v := m.selectedWorkflow()
	if v == nil {
		m.expanded = map[string]bool{}
		return
	}
	for key := range m.expanded {
		kind, project, uid, ok := parseDetailItemKey(key)
		if !ok || kind != "step" || project != v.key.projectUID || !slices.Contains(v.check.Order, uid) {
			delete(m.expanded, key)
		}
	}
}
