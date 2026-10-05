// Package board renders herdr-kata's TUI, designed to sit in a Herdr split pane.
//
// The board is a viewer and a remote control, not a scheduler: it reads the
// store and drives Herdr, so it can be opened, closed, and reopened at any
// time without affecting running work.
package board

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/salmonumbrella/herdr-kata/internal/flow"
	"github.com/salmonumbrella/herdr-kata/internal/herdrcli"
	"github.com/salmonumbrella/herdr-kata/internal/katabridge"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

// refreshInterval is how often the board re-reads the store. The board is
// often left open in a split for hours, so this stays cheap.

const refreshInterval = 3 * time.Second

var (
	titleStyle    = lipgloss.NewStyle().Bold(true)
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	favoriteStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	// The selected row is rendered bright rather than dim, so the cursor is
	// obvious at a glance instead of being inferred from the marker alone.
	rowSelected = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231"))
	helpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	outcomeStyles = map[string]lipgloss.Style{
		"done":    lipgloss.NewStyle().Foreground(lipgloss.Color("42")),
		"failed":  lipgloss.NewStyle().Foreground(lipgloss.Color("203")),
		"parked":  lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
		"running": lipgloss.NewStyle().Foreground(lipgloss.Color("39")),
	}
)

// Model is the board's bubbletea model.

// Model is the board's bubbletea model.
type Model struct {
	store  *store.Store
	herdr  *herdrcli.Client
	runJob RunFunc
	deps   Deps
	// daemonUp is the last observed scheduler state, refreshed on the tick.
	daemonUp                                      bool
	deadHooks                                     int
	pendingDelivery, failedDelivery, pendingInbox int

	jobs []store.Job
	runs []store.Run
	last map[string]store.Run // job id -> most recent run
	// steps is the per-step record of the flow runs on screen, keyed by run
	// id. Ordinary runs have no entry, which is what makes a run a flow as
	// far as this view is concerned.
	steps map[string][]store.RunStep
	// expanded is which flow runs are showing their steps. Keyed by run id
	// so the block survives the three-second refresh under it.
	expanded map[string]bool
	leases   []store.Lease

	// flows is every flow on disk and flowErrs is the files that would not
	// parse. Both are re-read on the tick, because a flow is a file: it is
	// edited between one tick and the next by a person in an editor or an agent
	// with a filesystem, and a board that read them once at open would keep
	// offering a flow that has since been renamed.
	flows    []flow.Flow
	flowErrs []error
	// lastFlow is the most recent run of each flow, by flow id. It is kept apart
	// from `last`, which is keyed by job id: a flow called directly has no job
	// at all, so its own history cannot be found there.
	lastFlow map[string]store.Run
	// flowInput is the open "what is this flow called with" box; nil when
	// nothing is being launched.
	flowInput *flowPrompt
	// prune is the open "delete these finished one-shots?" box; nil when
	// nothing has been asked. See prune.go.
	prune *prunePrompt

	cursor        int
	focus         focus
	err           error
	status        string
	nativeLabel   string
	sharedRuns    map[string]katacli.ReportedRun
	historyTarget string
	historyLabel  string
	width         int
	height        int

	// detail is the job being inspected; nil means the list view.
	detail     *store.Job
	detailRuns []store.Run
	// running guards against launching a second run of the same job from the
	// board while the first is still going.
	running map[string]bool
	// watcher detects a rebuilt binary so an open pane never shows stale code.
	watcher *binaryWatcher
	// reloading is set when the board is quitting in order to exec a newer
	// build of itself.
	reloading bool
	// editor is the open edit form; nil when not editing.
	editor *editor
	// scroll is the first visible line of the windowed view.
	scroll int
	// frame advances on the marquee tick and drives scrolling of text too long
	// for its column.
	frame int
	// testRuns counts runs launched from the board. Tests set it to observe
	// what a keystroke actually did; it is nil in normal use.
	testRuns map[string]int
	// query filters both lists; searching is true while it is being typed.
	query     string
	searching bool
	// queryDraft is the in-progress search text, kept separate so cancelling
	// restores the previous filter rather than clearing it.
	queryDraft string
	// showFinished brings finished one-shots back into the jobs list. They are
	// hidden by default: a one-shot disables itself after it runs, so without
	// this the board fills with work that can never run again.
	showFinished bool
	// runDetail is the run being inspected; nil when not in that view.
	runDetail *store.Run
	// hits maps a body line to the row it draws, and hitTop and hitScroll say
	// where that body landed on screen. Together they turn a click coordinate
	// back into a row; they are written by the render pass and read by the next
	// mouse event. See mouse.go.
	hits      map[int]hit
	hitTop    int
	hitScroll int
	// hitRows is how many rows of the body are on screen, which is not always
	// how many the pane gave it: a windowed body spends its last row on the
	// scroll hint.
	hitRows int
	// tabRow is the screen row the folder tabs' labels are on, and tabHits their
	// spans across it. tabRow is -1 when the frame has no tabs.
	tabRow  int
	tabHits []tabHit
	// mouseOff is set while the mouse has been handed back to the terminal, so
	// text can be selected with it.
	mouseOff bool
}

type focus int

const (
	focusJobs focus = iota
	focusRuns
	focusFlows
	focusLeases
)

// RunFunc executes a job and persists the result. The board takes this as a
// dependency so it can trigger runs without importing the command layer.

// RunFunc executes a job and persists the result. The board takes this as a
// dependency so it can trigger runs without importing the command layer.
type RunFunc func(job store.Job, trigger string) error

// RunFlowFunc starts a flow with an input, the same call a human types.
type RunFlowFunc func(flowID, input string) error

// ResumeFlowFunc picks a parked flow run up at the step that stopped it.
//
// It takes a run rather than a flow because that is what resuming is about: the
// steps already finished live in one run's directory, and starting the flow
// again by name would redo the ones that already cost money.
type ResumeFlowFunc func(runID string) error

// Deps are the behaviours the board needs from the command layer.

// Deps are the behaviours the board needs from the command layer.
type Deps struct {
	KataCommand func(issue string) (*exec.Cmd, error)
	Run         RunFunc
	// RunFlow and ResumeFlow are the two things the FLOWS tab does. Without
	// them the board could show a parked flow and never act on it, which is the
	// state that tab exists to end.
	RunFlow    RunFlowFunc
	ResumeFlow ResumeFlowFunc
	// FlowDir is the local draft directory. Saved flows come from the native
	// snapshot; this path is used only when opening a draft in the editor.
	FlowDir string
	// DaemonRunning reports whether a scheduler is alive. The board shows it,
	// because a stopped scheduler means nothing on screen will ever fire.
	DaemonRunning func() bool
	// EnsureDaemon revives a dead scheduler.
	EnsureDaemon func() error
}

// New builds a board model.
func New(s *store.Store, h *herdrcli.Client, deps Deps) *Model {
	return &Model{
		store: s, herdr: h, runJob: deps.Run, deps: deps,
		last:     map[string]store.Run{},
		lastFlow: map[string]store.Run{},
		steps:    map[string][]store.RunStep{},
		expanded: map[string]bool{},
		watcher:  newBinaryWatcher(),
		running:  map[string]bool{},
		hits:     map[int]hit{},
		// No frame has been drawn yet, so there is no tab row to click.
		tabRow: -1,
	}
}

type tickMsg time.Time

type dataMsg struct {
	sharedRuns                                    map[string]katacli.ReportedRun
	historyTarget                                 string
	historyLabel                                  string
	projectionErrs                                []error
	nativeLabel                                   string
	jobs                                          []store.Job
	runs                                          []store.Run
	deadHooks                                     int
	pendingDelivery, failedDelivery, pendingInbox int
	// lastRuns is every job's most recent run, asked of the store directly.
	// runs above is a window on the newest runs of all jobs together, so a job
	// that has not run lately is simply not in it — which is every job the
	// finished rule exists to hide.
	lastRuns map[string]store.Run
	leases   []store.Lease
	// steps is the per-step record of whichever of those runs are flows.
	steps map[string][]store.RunStep
	// flows is what is on disk, and flowErrs the files that would not parse.
	// The bad ones travel with the good ones rather than as an error on the
	// read: one unparseable flow must not empty the tab.
	flows    []flow.Flow
	flowErrs []error

	err error
}

type actionMsg struct {
	status      string
	nativeLabel string
	err         error
}

type detailMsg struct {
	job  *store.Job
	runs []store.Run
}

type runDoneMsg struct {
	jobID string
	err   error
}

// Init starts the refresh loop.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.load(), tick(), marqueeTick())
}

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) load() tea.Cmd {
	cachedShared := cloneReportedRuns(m.sharedRuns)
	cachedTarget := m.historyTarget
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var jobs []store.Job
		var err error
		if m.store.Native == nil {
			jobs, err = m.store.Jobs(ctx)
		}
		nativeLabel := ""
		shared := map[string]katacli.ReportedRun{}
		historyTarget, historyLabel := "", ""
		var nativeFlows []flow.Flow
		var projectionErrs, nativeFlowErrs []error
		if m.store.Native != nil {
			snapshot, e := m.store.Native.Refresh(ctx)
			if e != nil {
				return dataMsg{err: e, nativeLabel: "offline native cache · " + e.Error()}
			}
			nativeLabel = snapshot.Label()
			jobs, projectionErrs = m.store.Native.ProjectJobs(snapshot)
			nativeFlows, nativeFlowErrs = flow.ProjectDefinitions(snapshot.Flows)
			projectionErrs = append(projectionErrs, nativeFlowErrs...)
			if m.store.Native.Client != nil {
				historyTarget = katacli.LocalTargetKey(m.store.Native.Client.Target) + "/" + m.store.Native.Binding.ProjectUID
				if historyTarget == cachedTarget {
					shared = cachedShared
				}
				if snapshot.Offline {
					historyLabel = "offline reported history"
				} else {
					page, readErr := m.store.Native.Client.RunHistory(ctx, m.store.Native.Binding.ProjectUID, "")
					if readErr != nil {
						historyLabel = "offline reported history"
						projectionErrs = append(projectionErrs, fmt.Errorf("ordinary shared history: %w", readErr))
					} else {
						shared = map[string]katacli.ReportedRun{}
						for _, r := range page.Runs {
							shared[r.UID] = r
						}
						historyLabel = "reported history"
						if page.NextBeforeUID != "" {
							historyLabel += " · newest100"
						}
					}
				}
			}
			// Remote timeout must not cancel the separately bounded local tabs.
			ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			defer cancel()
		}
		if err != nil {
			return dataMsg{err: err}
		}
		runs, err := m.store.Runs(ctx, "", 100)
		if err != nil {
			return dataMsg{err: err}
		}
		runs = mergeReportedRuns(runs, shared, "", 100)
		deadHooks, err := m.store.DeadRunEventCount(ctx)
		if err != nil {
			return dataMsg{err: err}
		}
		pendingDelivery, failedDelivery, pendingInbox := 0, 0, 0
		if m.store.Native != nil && m.store.Native.Client != nil {
			repo := m.store.Native
			t := repo.Client.Target
			pendingDelivery, failedDelivery, err = katabridge.ObservationStatus(repo.StateDir)
			if err != nil {
				return dataMsg{err: err}
			}
			scope := katabridge.Scope{TargetKey: katacli.LocalTargetKey(t), ProjectUID: repo.Binding.ProjectUID, Actor: t.Actor}
			comments := katabridge.CommentOutbox{Dir: repo.StateDir, Scope: scope, Teammate: t.Teammate}
			n, f, e := comments.Status()
			if e != nil {
				return dataMsg{err: e}
			}
			pendingDelivery += n
			failedDelivery += f
			n, f, e = (katabridge.AttentionOutbox{Dir: repo.StateDir, Scope: scope, Teammate: t.Teammate}).Status()
			if e != nil {
				return dataMsg{err: e}
			}
			pendingDelivery += n
			failedDelivery += f
			pendingInbox, err = (katabridge.Bridge{Dir: repo.StateDir, Scope: scope}).PendingCount()
			if err != nil {
				return dataMsg{err: err}
			}
		}
		// The RUNS tab wants the newest hundred; the JOBS tab wants each job's
		// own latest, however long ago that was. They are two different
		// questions and deriving the second from the first answers it wrong.
		lastRuns, err := m.store.LastRuns(ctx)
		if err != nil {
			return dataMsg{err: err}
		}
		// The steps of every run on screen in one query rather than one query
		// per row: this runs every three seconds for as long as the board is
		// open.
		ids := make([]string, 0, len(runs))
		for _, r := range runs {
			ids = append(ids, r.ID)
		}
		steps, err := m.store.RunStepsFor(ctx, ids)
		if err != nil {
			return dataMsg{err: err}
		}
		leases, err := m.store.ListLeases(ctx, "", time.Now())
		if err != nil {
			return dataMsg{err: err}
		}
		// Flows are files, so they are listed rather than queried — here, beside
		// the store reads, because this is the goroutine that is allowed to
		// block. A directory read on the event loop would stall every keystroke
		// behind it.
		flows := nativeFlows
		flowErrs := nativeFlowErrs
		if m.store.Native == nil {
			flows, flowErrs = m.readFlows()
		}
		return dataMsg{sharedRuns: shared, historyTarget: historyTarget, historyLabel: historyLabel, projectionErrs: projectionErrs, nativeLabel: nativeLabel, jobs: jobs, runs: runs, deadHooks: deadHooks, pendingDelivery: pendingDelivery, failedDelivery: failedDelivery, pendingInbox: pendingInbox, lastRuns: lastRuns, steps: steps,
			leases: leases,
			flows:  flows, flowErrs: flowErrs}
	}
}

// refreshDetail reloads the open job detail, keeping the cursor where it is.
// It is a no-op in the list view.

// refreshDetail reloads the open job detail, keeping the cursor where it is.
// It is a no-op in the list view.
func (m *Model) refreshDetail() tea.Cmd {
	if m.detail == nil {
		return nil
	}
	id := m.detail.ID
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
		return detailRefreshMsg{job: j, runs: mergeReportedRuns(runs, shared, id, 20)}
	}
}

type detailRefreshMsg struct {
	job  *store.Job
	runs []store.Run
}

// Update handles input and refresh messages.

// Update handles input and refresh messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.load(), m.refreshDetail(), m.checkReload(),
			m.checkDaemon(), tick())

	case marqueeTickMsg:
		m.frame++
		return m, marqueeTick()

	case daemonMsg:
		m.daemonUp = bool(msg)
		return m, nil

	case reloadMsg:
		// Quit first, then exec once bubbletea has restored the terminal:
		// exec'ing from inside the event loop would leave the alt screen and
		// raw mode set for the new process.
		m.reloading = true
		return m, tea.Quit

	case dataMsg:
		if msg.err != nil {
			m.err = msg.err
			if msg.nativeLabel != "" {
				m.nativeLabel = msg.nativeLabel
				for i := range m.jobs {
					m.jobs[i].NativeOffline = true
				}
			}
			return m, nil
		}
		m.err = errors.Join(msg.projectionErrs...)
		m.jobs, m.runs, m.steps = msg.jobs, msg.runs, msg.steps
		m.nativeLabel = msg.nativeLabel
		m.sharedRuns, m.historyTarget, m.historyLabel = msg.sharedRuns, msg.historyTarget, msg.historyLabel
		m.deadHooks = msg.deadHooks
		m.pendingDelivery, m.failedDelivery, m.pendingInbox = msg.pendingDelivery, msg.failedDelivery, msg.pendingInbox
		m.flows, m.flowErrs = msg.flows, msg.flowErrs
		m.leases = msg.leases
		// Each job's latest run comes from the store, not from the window above:
		// the LAST column and the finished rule both read this, and both are
		// about jobs whose news is old.
		m.last = msg.lastRuns
		if m.last == nil {
			m.last = map[string]store.Run{}
		}
		m.lastFlow = map[string]store.Run{}
		// runs arrive newest first, so the first sighting of a flow wins.
		for _, r := range msg.runs {
			// A run that names a flow is that flow's history.
			if r.Flow == "" {
				continue
			}
			if _, seen := m.lastFlow[r.Flow]; !seen {
				m.lastFlow[r.Flow] = r
			}
		}
		m.clampCursor()
		return m, nil

	case actionMsg:
		m.status, m.err = msg.status, msg.err
		// Refresh the open detail too: an action taken from the detail view
		// (pause, run) changes what that view is showing.
		return m, tea.Batch(m.load(), m.refreshDetail())

	case detailMsg:
		m.detail, m.detailRuns, m.cursor = msg.job, msg.runs, 0
		m.err, m.status = nil, ""
		return m, nil

	case detailRefreshMsg:
		// Only the data changes here; the cursor stays put so a periodic
		// refresh does not move the selection under the reader.
		m.detail, m.detailRuns = msg.job, msg.runs
		m.clampCursor()
		return m, nil

	case runDetailMsg:
		m.runDetail, m.err, m.status = msg.run, nil, ""
		return m, nil

	case jobFromRunMsg:
		// Leaving the run behind: the job detail is now the view, reached from
		// its run rather than from the jobs list.
		m.runDetail = nil
		m.detail, m.detailRuns, m.cursor = msg.job, msg.runs, 0
		m.focus = focusJobs
		return m, nil

	case jobDeletedMsg:
		m.status = msg.jobID + " deleted"
		if msg.wasDetail {
			// The page we were on no longer exists.
			m.detail, m.detailRuns, m.cursor = nil, nil, 0
		}
		return m, m.load()

	case jobsPrunedMsg:
		// The count is reported even when the deletion failed partway, so a
		// half-finished prune does not read as though nothing was written.
		m.status = itoa(msg.n) + " finished one-shot(s) pruned; runs kept"
		if msg.err != nil {
			m.err = msg.err
		}
		return m, m.load()

	case editActivationFailedMsg:
		if m.editor != nil {
			m.editor.original = msg.accepted
			m.editor.job = msg.accepted
			m.editor.job.Enabled = msg.desiredEnabled
			m.editor.isNew = false
			m.editor.editedID = msg.accepted.ID
			m.editor.errMsg = msg.reason
			m.editor.saving = false
		}
		return m, nil

	case editFailedMsg:
		if m.editor != nil {
			m.editor.errMsg = msg.reason
			m.editor.saving = false
		}
		return m, nil

	case editSavedMsg:
		m.editor = nil
		m.scroll = 0
		m.status = msg.jobID + " saved"
		// Reload the detail so a job edited from its own page shows the new
		// values rather than the ones it was opened with.
		return m, tea.Batch(m.load(), m.refreshDetail())

	case runDoneMsg:
		delete(m.running, msg.jobID)
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.status = msg.jobID + " finished"
		}
		return m, m.refreshDetail()

	case flowDoneMsg:
		delete(m.running, flowRunKey(msg.flowID))
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		// A flow that parked is not an error and does not arrive as one — the
		// row's own STATE column is what says so, and the next tick fills it in.
		m.status = "flow " + msg.flowID + " finished"
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

const (
	colNextWidth = 14
	colDescWidth = 30
	// colFlowWidth fits a flow id comfortably; ids are shaped like job ids, so
	// they are short by construction, and a long one scrolls rather than being
	// cut — the tail of an id is what tells two of them apart.
	colFlowWidth = 14
)

// Run starts the board TUI, restarting into a newer build if one appears.
func Run(s *store.Store, h *herdrcli.Client, deps Deps) error {
	m := New(s, h, deps)
	// Cell motion rather than all motion: the board has nothing that responds to
	// the pointer merely passing over it, and reporting every idle movement
	// wakes the event loop for frames that would render identically.
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		return err
	}
	if m.reloading && m.watcher != nil {
		// The terminal is restored at this point, so the new process starts
		// clean. On success this call does not return.
		return m.watcher.restart()
	}
	return nil
}
