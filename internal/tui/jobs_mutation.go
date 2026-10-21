package tui

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type jobDeletedMsg struct {
	target editorTarget
	status string
	err    error
}

func (m *model) selectedJobView() (*jobView, bool) {
	r, ok := m.selected()
	if !ok || r.kind != "job" {
		return nil, false
	}
	v := m.indexedJob(r)
	return v, v != nil
}
func (m *model) selectedJobRun() (runner.Run, bool) {
	v, ok := m.selectedJobView()
	if !ok {
		return runner.Run{}, false
	}
	if m.currentView() == viewJob {
		if m.detailKey == "" && len(v.runIndexes) == 0 {
			m.notify(statusInfo, "no local runs · K opens Kata")
			return runner.Run{}, false
		}
		kind, project, uid, valid := parseDetailItemKey(m.detailKey)
		if !valid || kind != "run" {
			m.notify(statusInfo, "invalid detail selection")
			return runner.Run{}, false
		}
		for _, i := range v.runIndexes {
			r := m.data.Runs[i]
			if r.UID == uid && r.Target.Identity.ProjectUID == project {
				return r, true
			}
		}
		m.notify(statusInfo, "run was removed")
		return runner.Run{}, false
	}
	if len(v.runIndexes) > 0 {
		return m.data.Runs[v.runIndexes[0]], true
	}
	m.notify(statusInfo, clean(v.entry.Name)+" has never run")
	return runner.Run{}, false
}
func (m *model) jobActionOptions(v *jobView) (Lane, error) {
	uid := v.key.projectUID
	if uid == "" {
		return Lane{}, fmt.Errorf("project unpinned")
	}
	name := v.entry.Project
	if name == "" {
		name = m.projectName(uid)
	}
	opts, ok := m.laneFor(uid, name)
	if !ok {
		return Lane{}, fmt.Errorf("project unavailable")
	}
	return opts, nil
}

func (m *model) jobKeyAction(key string) tea.Cmd {
	if m.busy && (keys.Run.matchesString(key) || keys.Pause.matchesString(key) || keys.Delete.matchesString(key) || keys.Prune.matchesString(key)) {
		m.notify(statusInfo, "Working; wait for the current action to finish")
		return nil
	}
	if keys.Finished.matchesString(key) {
		m.finished = !m.finished
		m.cursor, m.scroll = 0, 0
		m.rows = nil
		m.rebuild()
		return nil
	}
	if keys.Prune.matchesString(key) {
		m.confirm = nil
		m.confirmMode = "prune"
		m.modalScroll = 0
		for _, v := range m.visibleJobs(true) {
			if v.finished {
				m.confirm = append(m.confirm, v.job)
			}
		}
		if len(m.confirm) == 0 {
			m.notify(statusInfo, "nothing to prune")
		}
		return nil
	}
	v, ok := m.selectedJobView()
	if !ok {
		if m.currentView() == viewJob {
			m.notify(statusInfo, "job was removed")
		}
		return nil
	}
	if keys.Attach.matchesString(key) || keys.Resume.matchesString(key) || keys.Issue.matchesString(key) {
		r, ok := m.selectedJobRun()
		if !ok {
			return nil
		}
		opts, err := m.jobActionOptions(v)
		if err != nil {
			m.notify(statusInfo, err.Error())
			return nil
		}
		return m.runAction(key, r, opts)
	}
	if keys.Favorite.matchesString(key) {
		want := !m.favoriteKey(v.key.projectUID, v.key.uid)
		delete(m.pendingFavorites, scopedRowKey(v.key.projectUID, v.key.uid))
		if err := m.setFavorite(v.key.projectUID, v.key.uid, want); err != nil {
			m.favoriteFailure(m.pendingFavorites[scopedRowKey(v.key.projectUID, v.key.uid)], err)
		} else {
			m.notify(statusInfo, m.acknowledgement(favoriteStatus(v.entry.Name, want), m.selectedOptions()))
		}
		return nil
	}
	if keys.Run.matchesString(key) {
		if v.shape == "recurring" {
			m.notify(statusInfo, "Kata owns native delivery and has no force-fire")
			return nil
		}
		if !v.def.Enabled {
			m.notify(statusInfo, clean(v.entry.Name)+" is paused; p resumes it")
			return nil
		}
		if !v.entry.Supported {
			m.notify(statusInfo, "job unsupported")
			if len(v.entry.Reasons) > 0 {
				m.notify(statusInfo, v.entry.Reasons[0])
			}
			return nil
		}
	}
	opts, err := m.jobActionOptions(v)
	if err != nil {
		m.notify(statusInfo, err.Error())
		return nil
	}
	j := v.job
	switch {
	case matchesKey(key, keys.Run):
		if opts.requester() == nil {
			return nil
		}
		return m.action(m.acknowledgement("run requested · "+j.Name, opts), func() error { return opts.requester().RunNow(j.UID) })
	case matchesKey(key, keys.Pause):
		if opts.Service == nil {
			return nil
		}
		status := j.Name + " paused"
		if !v.def.Enabled {
			status = j.Name + " resumed"
		}
		m.busy = true
		status = m.acknowledgement(status, opts)
		return func() tea.Msg {
			saved, err := opts.Service.Pause(j)
			saved.ProjectUID, saved.Project = j.ProjectUID, j.Project
			return jobPausedMsg{actionMsg{status, jobMutationError(err, j, "pause", nil, nil)}, saved}
		}
	case matchesKey(key, keys.Delete):
		m.confirm = []kata.Job{j}
		m.confirmMode = "delete"
		m.modalScroll = 0

	case matchesKey(key, keys.Parent):
		if v.shape != "recurring" {
			m.notify(statusInfo, "b edits recurring jobs only")
			return nil
		}
		if v.def.Issue == nil || v.def.Issue.ParentUID == "" {
			m.notify(statusInfo, "recurring job has no parent issue")
			return nil
		}
		if opts.Service != nil {
			m.editorProject = opts.Project
			parent := v.def.Issue.ParentUID
			if ref := m.knownIssueRef(v.key.projectUID, parent); ref != "" {
				parent = ref
			}
			m.editor = newParentForm(m.ctx, opts.Config, opts.Service, parent, m.workflowsFor(opts))
			return m.editor.Init()
		}
	}
	return nil
}
func jobMutationError(err error, pending kata.Job, operation string, done, remaining []kata.Job) error {
	if err == nil {
		return nil
	}
	if conflict, ok := errors.AsType[*ConflictError](err); ok {
		copy := *conflict
		if copy.Pending.UID == "" {
			copy.Pending = pending
		}
		copy.Operation = operation
		copy.Done = append([]kata.Job(nil), done...)
		copy.Remaining = append([]kata.Job(nil), remaining...)
		return &copy
	}
	if operation == "prune" || operation == "delete" {
		return fmt.Errorf("deleted %d: %s; pending %d: %s: %w", len(done), jobNames(done), len(remaining), jobNames(remaining), err)
	}
	return err
}
func jobNames(jobs []kata.Job) string {
	names := make([]string, len(jobs))
	for i, j := range jobs {
		names[i] = clean(j.Name)
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
func (m *model) confirmJobKey(s string) tea.Cmd {
	switch {
	case matchesKey(s, keys.Interrupt):
		return tea.Quit
	case matchesKey(s, keys.Down, keys.ScrollDown):
		m.modalScroll++
		return nil
	case matchesKey(s, keys.Up, keys.ScrollUp):
		m.modalScroll = max(0, m.modalScroll-1)
		return nil
	case matchesKey(s, keys.NextPage, keys.PageDown):
		m.modalScroll += m.pageSize()
		return nil
	case matchesKey(s, keys.PrevPage, keys.PageUp):
		m.modalScroll = max(0, m.modalScroll-m.pageSize())
		return nil
	}
	mode := m.confirmMode
	if mode == "" {
		mode = "delete"
	}
	jobs := append([]kata.Job(nil), m.confirm...)
	m.confirm = nil
	m.modalScroll = 0
	if !keys.Yes.matchesString(s) {
		m.notify(statusInfo, mode+" cancelled")
		return nil
	}
	// Resolve all lanes before starting work; the command captures no mutable model.
	lanes := make([]Lane, len(jobs))
	routeErrors := make([]error, len(jobs))
	for i, j := range jobs {
		v := &jobView{key: jobKey{j.ProjectUID, j.UID}, entry: workerJob(j)}
		lanes[i], routeErrors[i] = m.jobActionOptions(v)
		if routeErrors[i] != nil {
			continue
		}
		if lanes[i].Service == nil {
			routeErrors[i] = fmt.Errorf("project unavailable")
		}
	}
	status := fmt.Sprintf("%d finished one-shot(s) pruned; run history kept", len(jobs))
	if mode == "delete" {
		status = clean(jobs[0].Name) + " deleted; run history kept"
	}
	cmd := m.action(status, func() error {
		for i, j := range jobs {
			if routeErrors[i] != nil {
				return jobMutationError(routeErrors[i], j, mode, jobs[:i], jobs[i:])
			}
			if err := lanes[i].Service.Delete(j); err != nil {
				return jobMutationError(err, j, mode, jobs[:i], jobs[i:])
			}
		}
		return nil
	})
	if cmd == nil {
		return nil
	}
	if mode == "delete" && len(jobs) == 1 {
		target := editorTarget{ProjectUID: jobs[0].ProjectUID, UID: jobs[0].UID}
		return func() tea.Msg { msg := cmd().(actionMsg); return jobDeletedMsg{target, msg.status, msg.err} }
	}
	return cmd
}

func workerJob(j kata.Job) defs.JobEntry {
	return defs.JobEntry{ProjectUID: j.ProjectUID, Project: j.Project, UID: j.UID, Name: j.Name}
}

type editorTarget struct{ ProjectUID, UID string }
type pendingFavorite struct {
	target      editorTarget
	name        string
	want, saved bool
}

// setFavorite publishes a copy only after the atomic local write succeeds.
func (m *model) setFavorite(projectUID, uid string, want bool) error {
	key := scopedRowKey(projectUID, uid)
	if m.pendingFavorites == nil {
		m.pendingFavorites = map[string]pendingFavorite{}
	}
	pending := m.pendingFavorites[key]
	pending.target = editorTarget{projectUID, uid}
	pending.want = want
	if pending.name == "" {
		pending.name = "job"
		for _, entry := range m.data.Jobs {
			if entry.UID == uid && effectiveProjectUID(m.data, entry.ProjectUID) == projectUID {
				pending.name = entry.Name
				break
			}
		}
	}
	next := maps.Clone(m.favorites)
	if next == nil {
		next = map[string]bool{}
	}
	next[key] = want
	write := m.favoriteWrite
	if write == nil {
		write = func(value map[string]bool) error {
			return state.WriteJSON(state.OSFS{}, filepath.Join(m.opts.Root, "board-preferences.json"), value)
		}
	}
	if err := write(next); err != nil {
		m.pendingFavorites[key] = pending
		return err
	}
	m.favorites = next
	delete(m.pendingFavorites, key)
	m.rebuild()
	return nil
}
func favoriteStatus(name string, want bool) string {
	if want {
		return name + " pinned"
	}
	return name + " unpinned"
}
func (m *model) favoriteFailure(p pendingFavorite, err error) {
	lead := p.name
	if p.saved {
		lead += " saved"
	}
	m.notify(statusError, fmt.Sprintf("%s · favorite not saved: %s · retrying", lead, err))
}
func (m *model) retryFavorites() {
	keys := make([]string, 0, len(m.pendingFavorites))
	for key := range m.pendingFavorites {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		pending := m.pendingFavorites[key]
		if err := m.setFavorite(pending.target.ProjectUID, pending.target.UID, pending.want); err != nil {
			m.favoriteFailure(pending, err)
			continue
		}
		m.notify(statusSuccess, m.acknowledgement(favoriteStatus(pending.name, pending.want), Lane{Project: m.projectName(pending.target.ProjectUID)}))
	}
}

func (m *model) handleEditorClosed(v editorClosedMsg) {
	project, name, creating := m.editorProject, m.editorName, m.editorCreating
	m.editor = nil
	m.editorProject = ""
	m.editorName = ""
	m.editorCreating = false
	if project == "" {
		lane, _ := m.laneFor(v.Target.ProjectUID, "")
		project = lane.Project
	}
	if v.Saved != nil || v.Favorite != nil {
		if v.Target.UID == "" || v.Target.ProjectUID == "" {
			m.notify(statusError, "invalid editor target")
			return
		}
	}
	if v.Saved != nil {
		saved := *v.Saved
		saved.ProjectUID = v.Target.ProjectUID
		m.rememberSavedJob(saved)
		m.reproject()
		name = v.Saved.Name
	}
	m.notify(statusSuccess, m.acknowledgement(v.Message, Lane{Project: project}))
	if v.Saved != nil && creating {
		target := v.Target
		m.pendingJobSelection = &target
		if m.jobs == nil || m.jobs.byKey[jobKey{target.ProjectUID, target.UID}] == nil {
			m.notify(statusSuccess, m.acknowledgement(v.Message+" · appears after the worker's next definition refresh", Lane{Project: project}))
		}
	}
	if v.Favorite != nil {
		key := scopedRowKey(v.Target.ProjectUID, v.Target.UID)
		if m.pendingFavorites == nil {
			m.pendingFavorites = map[string]pendingFavorite{}
		}
		m.pendingFavorites[key] = pendingFavorite{target: v.Target, name: name, want: *v.Favorite, saved: v.Saved != nil}
		if err := m.setFavorite(v.Target.ProjectUID, v.Target.UID, *v.Favorite); err != nil {
			m.favoriteFailure(m.pendingFavorites[key], err)
			m.notify(statusError, m.acknowledgement(m.notice.Text, Lane{Project: project}))
		}
	}
	m.rebuild()
	if m.currentView() != viewList {
		m.reconcileDetailSelection(m.detailItems())
	}
}

// ConflictError retains the attempted definition guard until explicit discard.
type ConflictError struct {
	Pending, Newer kata.Job
	Remaining      []kata.Job
	Done           []kata.Job
	Operation      string
	Cause          error
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("guarded mutation needs review: %v", e.Cause)
}
func (e *ConflictError) Unwrap() error { return e.Cause }

// favoriteState is owned by this view.
type favoriteState struct {
	favoriteWrite    func(map[string]bool) error
	pendingFavorites map[string]pendingFavorite
	favorites        map[string]bool
}

// Keep acknowledged writes ahead of lagging snapshots, preserving their new
// conditional-write guards. A snapshot with a different revision supersedes it.
type pendingJob struct {
	saved kata.Job
	stale map[string]bool
}
type jobPausedMsg struct {
	actionMsg
	saved kata.Job
}

func (m *model) rememberSavedJob(saved kata.Job) {
	key := jobKey{saved.ProjectUID, saved.UID}
	if m.jobs == nil || m.jobs.byKey[key] == nil {
		return
	}
	if m.pendingJobs == nil {
		m.pendingJobs = map[jobKey]pendingJob{}
	}
	pending := m.pendingJobs[key]
	if pending.stale == nil {
		pending.stale = map[string]bool{}
	}
	pending.stale[m.jobs.byKey[key].job.DefinitionEventUID] = true
	pending.saved = saved
	m.pendingJobs[key] = pending
}
func (m *model) pendingJobEntry(entry defs.JobEntry) defs.JobEntry {
	key := jobKey{effectiveProjectUID(m.data, entry.ProjectUID), entry.UID}
	pending, ok := m.pendingJobs[key]
	if !ok {
		return entry
	}
	if !pending.stale[entry.DefinitionEventUID] {
		delete(m.pendingJobs, key)
		return entry
	}
	entry.Name = pending.saved.Name
	entry.Definition = pending.saved.Definition
	entry.DefinitionEventUID = pending.saved.DefinitionEventUID
	if pending.saved.UpdatedAt.After(entry.UpdatedAt) {
		entry.UpdatedAt = pending.saved.UpdatedAt
	}
	return entry
}
