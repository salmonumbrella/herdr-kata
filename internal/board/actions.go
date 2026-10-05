package board

import (
	"context"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"time"
)

// What a keystroke does to a job: open it, run it, pin it, pause it, delete it,
// or jump to its agent. Each returns a command rather than acting inline, so the
// store write happens off the update loop and reports back as a message.

// openDetail loads the selected job's full record.
func (m *Model) openDetail() tea.Cmd {
	jobs := m.visibleJobs()
	if m.cursor >= len(jobs) {
		return nil
	}
	id := jobs[m.cursor].ID
	shared := cloneReportedRuns(m.sharedRuns)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		j, err := m.store.Job(ctx, id)
		if err != nil {
			return actionMsg{err: err}
		}
		runs, err := m.store.JobRuns(ctx, id, 20)
		if err != nil {
			return actionMsg{err: err}
		}
		return detailMsg{job: j, runs: mergeReportedRuns(runs, shared, id, 20)}
	}
}

// selectedJob resolves the cursor to a job in either view.

// runSelected launches the selected job now.
func (m *Model) runSelected() tea.Cmd {
	j, ok := m.selectedJob()
	if !ok {
		return nil
	}
	if m.running[j.ID] {
		return func() tea.Msg { return actionMsg{status: j.ID + " is already running"} }
	}
	m.running[j.ID] = true
	return func() tea.Msg {
		err := m.runJob(j, "manual")
		return runDoneMsg{jobID: j.ID, err: err}
	}
}

// deleteSelected removes the selected job. Run history is kept, so a delete
// never destroys the record of what already happened.

// deleteSelected removes the selected job. Run history is kept, so a delete
// never destroys the record of what already happened.
func (m *Model) deleteSelected() tea.Cmd {
	j, ok := m.selectedJob()
	if !ok {
		return nil
	}
	inDetail := m.detail != nil
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.store.DeleteJobAt(ctx, j); err != nil {
			return actionMsg{err: err}
		}
		return jobDeletedMsg{jobID: j.ID, wasDetail: inDetail}
	}
}

type jobDeletedMsg struct {
	jobID     string
	wasDetail bool
}

// toggleFavorite pins or unpins the selected job.
//
// Favorites sort to the top of the list, so this is how a long schedule keeps
// the handful of jobs that matter within reach.

// toggleFavorite pins or unpins the selected job.
//
// Favorites sort to the top of the list, so this is how a long schedule keeps
// the handful of jobs that matter within reach.
func (m *Model) toggleFavorite() tea.Cmd {
	j, ok := m.selectedJob()
	if !ok {
		return nil
	}
	j.Favorite = !j.Favorite
	want := j.Favorite
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.store.PutJob(ctx, j); err != nil {
			return actionMsg{err: err}
		}
		state := "unpinned"
		if want {
			state = "pinned"
		}
		return actionMsg{status: j.ID + " " + state}
	}
}

// togglePause changes local activation for native jobs and enabled state for legacy rows.
func (m *Model) togglePause() tea.Cmd {
	j, ok := m.selectedJob()
	if !ok {
		return nil
	}
	want := !j.Enabled
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.store.SetEnabled(ctx, j.ID, want); err != nil {
			return actionMsg{err: err}
		}
		state := "paused"
		if want {
			state = "resumed"
		}
		return actionMsg{status: j.ID + " " + state}
	}
}

// focusRun brings the selected run's pane into view. Parked runs are the point
// of the board: this is how a human gets to the agent that is waiting on them.
func (m *Model) focusRun() tea.Cmd {
	run, ok := m.selectedRun()
	if !ok {
		return nil
	}
	if run.AgentName == "" {
		return func() tea.Msg { return actionMsg{status: "run has no agent to focus"} }
	}
	name := run.AgentName
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.herdr.AgentFocus(ctx, name); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{status: "focused " + name}
	}
}

// selectedRun resolves the cursor to a run, whichever view is showing.

// openKata temporarily runs the installed public TUI in the board's Herdr pane.
func (m *Model) openKata() tea.Cmd {
	if m.deps.KataCommand == nil {
		return func() tea.Msg { return actionMsg{err: fmt.Errorf("configure native Kata before opening its TUI")} }
	}
	cmd, err := m.deps.KataCommand("")
	if err != nil {
		return func() tea.Msg { return actionMsg{err: err} }
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return actionMsg{err: err} })
}

// openRunIssue opens the actual linked issue through the installed public TUI.
func (m *Model) openRunIssue() tea.Cmd {
	run, ok := m.selectedRun()
	if m.runDetail != nil {
		run, ok = *m.runDetail, true
	}
	if !ok {
		return nil
	}
	uid, err := katacli.NormalizeUID(run.Ref)
	if err != nil || uid != run.Ref {
		return func() tea.Msg { return actionMsg{status: "run has no canonical linked issue"} }
	}
	if m.deps.KataCommand == nil {
		return func() tea.Msg { return actionMsg{err: fmt.Errorf("configure native Kata before opening its TUI")} }
	}
	cmd, err := m.deps.KataCommand(uid)
	if err != nil {
		return func() tea.Msg { return actionMsg{err: err} }
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return actionMsg{err: err} })
}
