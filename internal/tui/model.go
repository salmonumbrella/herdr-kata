package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type poller interface{ Poll() (Data, error) }

type requester interface{ RunNow(jobUID string) error }
type resumeRequester interface{ Resume(runUID string) error }
type pendingRequester struct{}

func (pendingRequester) RunNow(string) error { return fmt.Errorf("run-now arrives with unit 3c") }

type actions interface {
	Pause(kata.Job) (kata.Job, error)
	Delete(kata.Job) error
	Focus(runner.Run) error
}
type Options struct {
	Lane
	Now           func() time.Time
	InitialStatus string
	OpenKata      bool
	Root          string
	Projects      map[string]*Lane
	ForProject    func(uid, name string) Lane
	Pin           func(Data) error
	Stdout        io.Writer
	DisplayLanes  [][]string
}

// Lane is one project's configuration and guarded service.
type Lane struct {
	Config              config.Config
	ProjectUID, Project string
	Service             service
}

// service is cmd's guarded boundary. The TUI never constructs a Kata client.
type service interface {
	actions
	requester
	resumeRequester
	writer
	workflowWriter
	bindingWriter
	kataCommander
}

func (l Lane) requester() requester {
	if l.Service == nil {
		return pendingRequester{}
	}
	return l.Service
}

// pollState tracks coalesced reads of local snapshots.
type pollState struct {
	cache                                           poller
	polling, pollAgain                              bool
	pollGeneration, acceptedPoll, refreshGeneration uint64
	snapshotErr                                     error
}

type model struct {
	ctx context.Context
	artifactState
	boxState
	favoriteState
	formState
	kataTab
	listState
	marqueeState
	navStack
	pickerState
	pollState
	jobMatches    int
	jobHidden     int
	jobFavorites  bool
	jobs          *jobIndex
	pendingJobs   map[jobKey]pendingJob
	wfIndex       workflowIndex
	workflowWraps map[workflowWrapKey][]string
	resuming      map[string]resumeDisplay
	runViews      map[string]runViewData
	rendered      renderedFrame
	frameValid    bool
	boardProfile  *colorprofile.Profile
	notice        statusNotice
	opts          Options
	now           func() time.Time
	data          Data
	width         int
	height        int
	mouseOff      bool
	listOnly      bool
	busy          bool
	expanded      map[string]bool
	hits          hits
}

type row struct {
	kind               string
	projectUID         string
	uid, label, search string
	job, run, wf       int
}

func newModel(o Options) *model {
	if o.Now == nil {
		o.Now = time.Now
	}

	m := &model{ctx: context.Background(), opts: o, now: o.Now, width: 80, height: 24, expanded: map[string]bool{}, favorites: map[string]bool{}, kataRetryEvery: 2 * time.Second, follow: true, nav: []navFrame{{view: viewList, follow: true, expanded: map[string]bool{}}}, cache: newCache(o.Root, nil)}
	if len(m.opts.Projects) == 0 {
		name := m.opts.Project
		if name == "" {
			name = m.opts.Config.Kata.Project
		}
		m.opts.Projects = map[string]*Lane{name: &m.opts.Lane}
	}
	if m.opts.ForProject == nil {
		m.opts.ForProject = func(uid, name string) Lane {
			lane := m.opts.Projects[name]
			if lane == nil {
				return Lane{}
			}
			opts := *lane
			opts.ProjectUID, opts.Project = uid, name
			return opts
		}
	}
	m.notify(statusInfo, o.InitialStatus)
	_ = state.ReadJSON(state.OSFS{}, filepath.Join(o.Root, "board-preferences.json"), 64<<10, &m.favorites)
	return m
}
func tick() tea.Cmd { return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }
func (m *model) poll() tea.Cmd {
	cache := m.cache
	return func() tea.Msg { d, e := cache.Poll(); return dataMsg{d, e} }
}
func (m *model) requestPoll() tea.Cmd {
	if m.polling {
		m.pollAgain = true
		return nil
	}
	m.polling = true
	m.pollGeneration++
	return m.poll()
}
func (m *model) refresh() tea.Cmd {
	m.refreshGeneration = m.pollGeneration + 1
	return m.requestPoll()
}
func (m *model) Init() tea.Cmd {
	var initial tea.Cmd
	if m.opts.OpenKata {
		// At boot the worker's first route check can land after the TUI
		// starts; keep asking until it does, for a bounded time.
		m.kataRetryUntil = time.Now().Add(90 * time.Second)
		initial = m.requestKata()
	}
	colorWait := tea.Tick(terminalColorWait, func(time.Time) tea.Msg { return terminalColorsLateMsg{} })
	return tea.Batch(m.requestPoll(), tick(), tea.RequestBackgroundColor, tea.RequestForegroundColor, colorWait, initial)
}
func (m *model) Update(msg tea.Msg) (model tea.Model, cmd tea.Cmd) {
	m.frameValid = false
	m.expireStatus()
	accepted := false
	defer func() {
		artifacts := m.updateArtifacts(accepted)
		if artifacts != nil {
			cmd = tea.Batch(cmd, artifacts)
		}
		m.renderFrame()
		animation := m.updateMarquee()
		if animation != nil {
			if cmd == nil {
				cmd = animation
			} else {
				cmd = tea.Batch(cmd, animation)
			}
		}
	}()
	switch v := msg.(type) {
	case tea.BackgroundColorMsg:
		m.terminalBackground = v.Color
		if mode, dark := activeColorMode, activeHasDarkBackground; mode == colorAuto && dark != v.IsDark() {
			applyColorMode(mode, v.IsDark())
			m.workflowWraps = nil
		}
		return m, m.readKata()
	case tea.ForegroundColorMsg:
		m.terminalForeground = v.Color
		return m, m.readKata()
	case terminalColorsLateMsg:
		m.terminalColorsLate = true
		return m, m.readKata()
	case kataStartedMsg:
		return m, m.acceptKataStart(v)
	case kataOutputMsg:
		return m, m.acceptKataOutput(v)
	case marqueeTickMsg:
		if m.marqueePending && v.generation == m.marqueeGeneration {
			m.marqueePending = false
			m.frame++
		}
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
		m.workflowWraps = nil
		if m.kata != nil && m.kata.terminal != nil {
			w, h := m.kataSize()
			if err := m.kata.terminal.resize(w, h); err != nil {
				m.notify(statusError, err.Error())
			}
		}
		if m.editor != nil {
			return m, m.editorUpdate(msg)
		}
	case tickMsg:
		m.retryFavorites()
		return m, tea.Batch(m.requestPoll(), tick())
	case artifactsMsg:
		m.acceptArtifacts(v)
	case dataMsg:
		accepted = true
		m.polling = false
		m.acceptedPoll = m.pollGeneration
		detailWasPresent := false
		if m.currentView() != viewList {
			_, detailWasPresent = m.resolveFrame(m.nav[len(m.nav)-1])
		}
		m.data = v.data
		if detailWasPresent {
			if _, present := m.resolveFrame(m.nav[len(m.nav)-1]); !present {
				m.clearStatus()
			}
		}
		m.projectResumes()
		m.reproject()
		m.reconcileWorkflowContent()
		m.snapshotErr = v.err
		if m.refreshGeneration > 0 && m.acceptedPoll >= m.refreshGeneration {
			if m.currentView() != viewRun {
				m.refreshGeneration = 0
			}
			if v.err == nil {
				m.notify(statusInfo, "reloaded")
			}
		}
		m.rebuild()
		if m.currentView() != viewList {
			m.reconcileDetailSelection(m.detailItems())
		}
		var commands []tea.Cmd
		if m.opts.Pin != nil {
			d, pin := m.data, m.opts.Pin
			commands = append(commands, func() tea.Msg { _ = pin(d); return nil })
		}
		if m.pollAgain {
			m.pollAgain = false
			commands = append(commands, m.requestPoll())
		}
		return m, tea.Batch(commands...)
	case jobDeletedMsg:
		m.completeAction(actionMsg{v.status, v.err})
		if v.err == nil && m.currentView() == viewJob {
			f := m.nav[len(m.nav)-1]
			if f.projectUID == v.target.ProjectUID && f.uid == v.target.UID {
				m.popDetail()
			}
		}
	case resumeMsg:
		m.completeAction(v.actionMsg)
		if v.err == nil {
			if m.resuming == nil {
				m.resuming = map[string]resumeDisplay{}
			}
			key := scopedRowKey(v.run.Target.Identity.ProjectUID, v.run.UID)
			m.resuming[key] = resumeDisplay{signature: resumeSignature(v.run), until: m.now().Add(10 * time.Second)}
			m.projectResumes()
			m.reproject()
			m.rebuild()
		}
	case jobPausedMsg:
		m.completeAction(v.actionMsg)
		if v.err == nil {
			m.rememberSavedJob(v.saved)
			m.reproject()
			m.rebuild()
			if m.currentView() != viewList {
				m.reconcileDetailSelection(m.detailItems())
			}
		}
	case actionMsg:
		m.completeAction(v)
	case openKataRetryMsg:
		if m.kataRetryUntil.IsZero() || !time.Now().Before(m.kataRetryUntil) {
			m.kataRetryUntil = time.Time{}
			return m, nil
		}
		return m, m.requestKata()
	case newEditorMsg:
		if !m.openingEditor || v.generation != m.generation {
			if closer, ok := v.editor.(interface{ Close() error }); ok {
				if err := closer.Close(); err != nil {
					m.notify(statusError, "editor cleanup: "+err.Error())
				}
			}
			return m, nil
		}
		m.clearStatus()
		m.openingEditor = false
		m.editor = v.editor
		_, sizeCmd := m.editor.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		return m, tea.Batch(m.editor.Init(), sizeCmd)
	case editorClosedMsg:
		m.handleEditorClosed(v)
	case tea.KeyPressMsg:
		// The user took over; stop opening Kata on their behalf.
		m.kataRetryUntil = time.Time{}
		if m.kataActive {
			if keys.LeaveKata.matches(v) {
				m.leaveKata()
			} else if keys.NextTab.matches(v) || keys.PrevTab.matches(v) {
				m.leaveKata()
				m.nav = m.nav[:1]
				m.restoreFrame(m.nav[0])
				if keys.NextTab.matches(v) {
					m.selectTab(0)
				} else {
					m.selectTab(2)
				}
			} else if m.kata != nil && m.kata.terminal != nil {
				m.kata.terminal.key(v)
			}
			return m, nil
		}
		return m, m.routeKey(v)
	case tea.PasteMsg:
		m.kataRetryUntil = time.Time{}
		if m.kataActive {
			if m.kata != nil && m.kata.terminal != nil {
				m.kata.terminal.screen.Paste(v.Content)
			}
			return m, nil
		}
		if m.editor != nil {
			return m, m.editorUpdate(msg)
		}
		return m, m.paste(v.Content)
	case tea.MouseMsg:
		if m.kataActive {
			click, _ := v.(tea.MouseClickMsg)
			if index, ok := m.hits.TabAt(click.X, click.Y); ok && click.Button == tea.MouseLeft {
				if index > 0 {
					m.leaveKata()
					if index-1 != m.tab {
						m.nav = m.nav[:1]
						m.restoreFrame(m.nav[0])
						m.selectTab(index - 1)
					}
				}
			} else if m.kata != nil && m.kata.terminal != nil {
				top, _, _ := m.kataTop()
				m.kata.terminal.mouse(v, len(top))
			}
			return m, nil
		}
		if m.editor == nil {
			return m, m.mouse(v)
		}
	default:
		if m.editor != nil {
			return m, m.editorUpdate(msg)
		}
	}
	return m, nil
}
func (m *model) editorUpdate(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(msg)
	return cmd
}
func (m *model) action(status string, f func() error) tea.Cmd {
	if m.busy {
		m.notify(statusInfo, "Working; wait for the current action to finish")
		return nil
	}
	m.busy = true
	return func() tea.Msg { return actionMsg{status, f()} }
}
func (m *model) job(r row) kata.Job {
	if v := m.indexedJob(r); v != nil {
		return v.job
	}
	j := m.data.Jobs[r.job]
	return kata.Job{ProjectUID: j.ProjectUID, Project: j.Project, UID: j.UID, Name: j.Name, DefinitionEventUID: j.DefinitionEventUID, Definition: j.Definition, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt, DeletedAt: j.DeletedAt}
}
func (m *model) isFinished(i int) bool {
	if i < 0 || i >= len(m.data.Jobs) {
		return false
	}
	if m.jobs == nil {
		m.rebuildJobIndex()
	}
	entry := m.data.Jobs[i]
	v := m.jobs.byKey[jobKey{effectiveProjectUID(m.data, entry.ProjectUID), entry.UID}]
	return v != nil && v.finished
}

func (m *model) reproject() {
	m.rebuildJobIndex()
	m.wfIndex = buildWorkflowIndex(m.data, *m.jobs, m.workflowOptions, m.now())
}

func (m *model) rebuild() {
	m.runViews = make(map[string]runViewData, len(m.data.Runs))
	refs := m.issueRefIndex()
	for _, r := range m.data.Runs {
		v := runView(r)
		v.Issue = refs.run(m, r)
		m.runViews[scopedRowKey(r.Target.Identity.ProjectUID, r.UID)] = v
	}
	selected := ""
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		selected = m.rows[m.cursor].identity()
	}
	rows := []row{}
	switch m.tab {
	case 0:
		if m.jobs == nil {
			m.rebuildJobIndex()
		}
		m.jobMatches = len(m.visibleJobs(true))
		for _, v := range m.visibleJobs(m.finished) {
			rows = append(rows, row{kind: "job", projectUID: v.key.projectUID, uid: v.key.uid, label: v.entry.Name, search: v.search, job: v.index})
		}

		m.jobHidden = m.jobMatches - len(rows)
	case 1:
		for i, r := range m.data.Runs {
			v := m.runViews[scopedRowKey(r.Target.Identity.ProjectUID, r.UID)]
			rows = append(rows, row{kind: "run", projectUID: r.Target.Identity.ProjectUID, uid: r.UID, label: v.Job, search: runSearch(v, refs.project(m, r.Target.Identity.ProjectUID), m.now()), run: i})
		}
	case 2:
		rows = m.workflowRows()
	}
	m.rows = nil
	if m.tab == 0 {
		m.jobFavorites = false
	}
	for _, r := range rows {
		if strings.Contains(strings.ToLower(r.search), strings.ToLower(m.query)) {
			m.rows = append(m.rows, r)
			if r.kind == "job" && m.favoriteKey(r.projectUID, r.uid) {
				m.jobFavorites = true
			}
		}
	}
	if selected != "" {
		m.cursor = 0
		for i, r := range m.rows {
			if r.identity() == selected {
				m.cursor = i
				break
			}
		}
	}

	m.cursor = max(0, min(m.cursor, len(m.rows)-1))
	if m.pendingJobSelection != nil && m.tab == 0 && m.currentView() == viewList {
		target := *m.pendingJobSelection
		for i, r := range m.rows {
			if r.identity() == scopedRowKey(target.ProjectUID, target.UID) {
				m.cursor = i
				m.pendingJobSelection = nil
				break
			}
		}
	}
}
func (m *model) move(n int) {
	if m.currentView() != viewList {
		items := m.detailItems()
		if len(items) > 0 {
			s := moveSelection(detailSelection{Key: m.detailKey, Ordinal: m.detailOrdinal}, items, n)
			m.detailKey, m.detailOrdinal = s.Key, s.Ordinal
			m.follow = true
		} else {
			m.scroll = max(0, m.scroll+n)
			m.follow = false
		}
		return
	}
	before := m.cursor / m.pageRows()
	m.cursor = max(0, min(m.cursor+n, len(m.rows)-1))
	m.follow = true
	if m.cursor/m.pageRows() != before {
		m.scroll = 0
	}
}
func (m *model) selectTab(n int) {
	m.refreshGeneration = 0
	if m.currentView() != viewList {
		return
	}
	m.clearStatus()
	m.searching = false
	next := (n + len(tabNames)) % len(tabNames)
	if next == m.tab {
		return
	}
	m.saveFrame()
	if m.tabFrames == nil {
		m.tabFrames = map[int]navFrame{}
	}
	m.tabFrames[m.tab] = m.nav[0]
	frame, ok := m.tabFrames[next]
	if !ok {
		frame = navFrame{view: viewList, tab: next, query: m.query, follow: true}
	}
	m.generation++
	m.nav = []navFrame{frame}
	m.restoreFrame(frame)
}
func (m *model) selected() (row, bool) {
	if m.currentView() != viewList {
		return m.resolveFrame(m.nav[len(m.nav)-1])
	}
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return row{}, false
	}
	return m.rows[m.cursor], true
}
func (m *model) open() tea.Cmd {
	r, ok := m.selected()
	if !ok {
		return nil
	}
	if m.currentView() == viewRun {
		return m.openJobOfRun()
	}
	if m.currentView() == viewJob || m.currentView() == viewWorkflow {
		kind, project, uid, valid := parseDetailItemKey(m.detailKey)
		if !valid {
			m.notify(statusInfo, "invalid detail selection")
			return nil
		}
		if kind == "run" {
			if _, ok := m.nestedRun(); ok {
				return m.pushDetail(viewRun, project, uid)
			}
		} else if kind == "used-by" || kind == "job" {
			if m.detailItemExists(m.detailKey) {
				return m.pushDetail(viewJob, project, uid)
			}
		}
		return nil
	}
	switch r.kind {
	case "job":
		return m.pushDetail(viewJob, r.projectUID, r.uid)
	case "run":
		return m.pushDetail(viewRun, r.projectUID, r.uid)
	case "workflow":
		return m.pushDetail(viewWorkflow, r.projectUID, r.uid)
	}
	return nil
}

func (m *model) Close() error {
	m.stopKata()
	if closer, ok := m.editor.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

// selectedOptions freezes the chosen lane before an asynchronous action starts.
func (m *model) selectedOptions() Lane {
	if m.currentView() == viewRun {
		if r, ok := m.detailRun(); ok {
			opts, _ := m.runOptions(r)
			return opts
		}
		return Lane{}
	}
	r, ok := m.selected()
	return m.optionsForRow(r, ok)
}
func (m *model) optionsForRow(r row, ok bool) Lane {
	if !ok {
		// With no selection, explicit opens use the single configured discovery lane.
		if len(m.opts.Projects) == 1 {
			for _, lane := range m.opts.Projects {
				if lane != nil {
					return *lane
				}
			}
		}
		return Lane{}
	}
	name := ""
	switch r.kind {
	case "job":
		name = m.data.Jobs[r.job].Project
	case "workflow":
		name = m.data.Workflows[r.wf].Project
	}
	opts, _ := m.laneFor(r.projectUID, name)
	return opts
}

func (m *model) workflowsFor(opts Lane) []kata.Workflow {
	name, uid := opts.Project, opts.ProjectUID
	if uid == "" {
		for candidate, lane := range m.data.Projects {
			if lane.Project == name && lane.Route["allowed"] == true {
				if uid != "" {
					return nil
				}
				uid = candidate
			}
		}
	}
	var rows []kata.Workflow
	for _, w := range m.data.Workflows {
		if uid != "" && effectiveProjectUID(m.data, w.ProjectUID) == uid || uid == "" && (w.Project == name || len(m.opts.Projects) == 1 && w.Project == "") {
			rows = append(rows, w)
		}
	}
	return rows
}

func (m *model) newJob(opts Lane) tea.Cmd {
	ec, opts, err := m.editorContext(opts, nil)
	if err != nil {
		m.notify(statusInfo, err.Error())
		return nil
	}
	m.openingEditor = true
	m.editorCreating = true
	m.editorName = ""
	m.editorProject = opts.Project
	m.generation++
	generation := m.generation
	flows := append([]kata.Workflow(nil), m.workflowsFor(opts)...)
	return func() tea.Msg {
		return newEditorMsg{generation, newJobForm(m.ctx, opts.Config, opts.Service, nil, flows, ec)}
	}
}

func (m *model) chooseProject(purpose, selected string) {
	m.clearStatus()
	m.projectPurpose = purpose
	m.projectChoices, m.projectWrapTo = nil, nil
	for name := range m.opts.Projects {
		m.projectChoices = append(m.projectChoices, name)
	}
	sort.Strings(m.projectChoices)
	m.projectCursor = 0
	for i, name := range m.projectChoices {
		if name == selected {
			m.projectCursor = i
		}
	}
}
func (m *model) openKata(opts Lane) tea.Cmd {
	return m.openKataIssue(opts, "")
}

func (m *model) requestKata() tea.Cmd {
	m.searching = false
	if _, ok := m.selected(); !ok && m.multipleProjects() {
		m.chooseProject("to open Kata", "")
		return nil
	}
	return m.openKata(m.selectedOptions())
}

func (m *model) completeAction(v actionMsg) {
	m.busy = false
	if v.err != nil {
		m.notify(statusError, v.err.Error())
		var apiError *herdr.Error
		if errors.As(v.err, &apiError) && apiError.Code == "ui_busy" {
			m.notify(statusError, "Close the current modal, then try K")
		}
		if conflict, ok := errors.AsType[*ConflictError](v.err); ok {
			m.conflict = conflict
			m.modalScroll = 0
		}
	} else if v.status != "" {
		m.notify(statusSuccess, v.status)
	} else if strings.HasPrefix(m.notice.Text, "Working;") {
		m.notify(statusInfo, "Ready")
	}
}

// Form renders within the TUI frame.
type form interface {
	Init() tea.Cmd
	Update(tea.Msg) (form, tea.Cmd)
	View() string
}

const terminalColorWait = 500 * time.Millisecond

type terminalColorsLateMsg struct{}
