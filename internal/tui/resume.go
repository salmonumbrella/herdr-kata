package tui

import (
	"encoding/json"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

type resumeMsg struct {
	actionMsg
	run runner.Run
}
type resumeDisplay struct {
	signature, projected string
	until                time.Time
}

func resumeSignature(r runner.Run) string {
	b, _ := json.Marshal(struct {
		Phase                       runner.Phase
		Reason, Outcome, ActiveStep string
		Resume                      *runner.ResumeState
		Consumed                    []string
		Steps                       []runner.StepState
		Deadline                    time.Time
	}{r.Phase, r.Reason, r.Outcome, r.ActiveStep, r.Resume, r.ConsumedResumes, r.Steps, r.ExpiresAt})
	return string(b)
}
func (m *model) resumeAction(status string, r runner.Run, requester resumeRequester) tea.Cmd {
	if m.busy {
		m.notify(statusInfo, "Working; wait for the current action to finish")
		return nil
	}
	m.busy = true
	return func() tea.Msg { return resumeMsg{actionMsg{status, requester.Resume(r.UID)}, r} }
}

// The requester acknowledges a queued control, before the next worker snapshot
// arrives. Bridge that gap locally; an unchanged runtime wins again after ten
// seconds so a refused or unprocessed request cannot mask a parked run forever.
func (m *model) projectResumes() {
	if len(m.data.Runs) == 0 {
		clear(m.resuming)
		return
	}
	m.data.Runs = append([]runner.Run(nil), m.data.Runs...)
	retained := map[string]bool{}
	for i, r := range m.data.Runs {
		key := scopedRowKey(r.Target.Identity.ProjectUID, r.UID)
		retained[key] = true
		optimistic := false
		if pending, ok := m.resuming[key]; ok {
			if m.now().Before(pending.until) && (resumeSignature(r) == pending.signature || resumeSignature(r) == pending.projected) {
				optimistic = true
			} else {
				delete(m.resuming, key)
			}
		}
		accepted := r.Resume != nil && (r.Phase == runner.Held && r.Resume.Pending || r.Phase == runner.Admitted)
		if !optimistic && !accepted {
			continue
		}
		r.Phase, r.Outcome, r.Reason, r.EndedAt = runner.Running, "", "", nil
		r.Steps = append([]runner.StepState(nil), r.Steps...)
		for j := range r.Steps {
			if r.Steps[j].Key == r.ActiveStep {
				r.Steps[j].Status, r.Steps[j].Reason = "", ""
				r.Steps[j].Submission = runner.Sending
				r.Steps[j].Limit = nil
			}
		}
		if pending, ok := m.resuming[key]; ok {
			pending.projected = resumeSignature(r)
			m.resuming[key] = pending
		}
		m.data.Runs[i] = r
	}
	for key := range m.resuming {
		if !retained[key] {
			delete(m.resuming, key)
		}
	}
}
