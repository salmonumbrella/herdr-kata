package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

type runDetailData struct {
	View          runViewData
	ResultStep    *runner.StepState
	PendingWrites int
}

func runDetailView(r runner.Run) runDetailData {
	v := runDetailData{View: runView(r), ResultStep: selectStep(r)}
	for _, op := range r.Ops {
		if !op.Done {
			v.PendingWrites++
		}
	}
	return v
}
func selectStep(r runner.Run) *runner.StepState {
	if s := activeStep(r); s != nil && (r.Phase == runner.Held || r.Phase == runner.Blocked || r.Phase == runner.HandedOff || s.Status == "failed" || s.Status == "needs-human" || s.Status == "unknown") {
		return s
	}
	if s := runner.FailedStep(&r); s != nil {
		return s
	}
	for i := len(r.Steps) - 1; i >= 0; i-- {
		if r.Steps[i].Status == "succeeded" {
			return &r.Steps[i]
		}
	}
	return nil
}

func (m *model) detailRun() (runner.Run, bool) {
	if m.currentView() != viewRun {
		return runner.Run{}, false
	}
	r, ok := m.resolveFrame(m.nav[len(m.nav)-1])
	if !ok {
		return runner.Run{}, false
	}
	return m.data.Runs[r.run], true
}
func (m *model) runOptions(r runner.Run) (Lane, bool) {
	return m.laneFor(r.Target.Identity.ProjectUID, "")
}

func (m *model) runDetailKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "q", "ctrl+c":
		return tea.Quit
	case "h", "left", "esc", "backspace":
		m.popDetail()
		return nil
	case "j", "down":
		m.scroll++
		m.follow = false
	case "k", "up":
		m.scroll = max(0, m.scroll-1)
		m.follow = false
	case "pgdown", "ctrl+d":
		m.scroll += m.pageSize()
		m.follow = false
	case "pgup", "ctrl+u":
		m.scroll = max(0, m.scroll-m.pageSize())
		m.follow = false
	case "M":
		m.mouseOff = !m.mouseOff
		if m.mouseOff {
			m.notify(statusInfo, "mouse released — the terminal's own selection is back (M takes it again)")
			return nil
		}
		m.notify(statusInfo, "mouse on")
		return nil
	case "r":
		return m.refresh()
	case "l", "right", "enter":
		return m.openJobOfRun()
	case "a", "u", "i", "K":
		r, ok := m.detailRun()
		if !ok {
			m.notify(statusInfo, "run was removed")
			return nil
		}
		opts, ok := m.runOptions(r)
		if !ok {
			m.notify(statusInfo, "project "+clean(m.projectName(r.Target.Identity.ProjectUID))+" unavailable")
			return nil
		}
		if k.String() == "K" {
			return m.openKata(opts)
		}
		return m.runAction(k.String(), r, opts)
	}
	return nil
}
func (m *model) openJobOfRun() tea.Cmd {
	r, ok := m.detailRun()
	if !ok {
		return nil
	}
	uid := r.Job.UID
	if r.Child != nil {
		uid = r.Child.DeliveryJobUID
	}
	for _, j := range m.data.Jobs {
		if j.UID == uid && j.DeletedAt == nil && effectiveProjectUID(m.data, j.ProjectUID) == r.Target.Identity.ProjectUID {
			cmd := m.pushDetail(viewJob, effectiveProjectUID(m.data, j.ProjectUID), j.UID)
			if j.DefinitionEventUID != r.Job.DefinitionEventUID {
				m.notify(statusInfo, "job definition changed since this run")
			}
			return cmd
		}
	}
	name := r.Job.Name
	if name == "" || r.Child != nil {
		name = "from this run"
	}
	m.notify(statusInfo, "job "+clean(name)+" no longer exists")
	return nil
}
