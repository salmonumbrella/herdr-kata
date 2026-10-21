package runner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/state"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

// Manager reserves shared capacity before preparation. Durable prepared records
// keep their reservation even when delivery is interrupted or needs inspection.
type Manager struct {
	cfg   config.Config
	k     Kata
	h     *herdr.Client
	store *Store
	q     *Queue
	guard RouteChecker
	clock Clock
	*managerShared
	projectUID string
	pinLayout  state.Layout
	scheduler  ManualScheduler
	wake       func()
	// lease reports the lane lease generation this installation holds, and
	// generation 0 before this process first claims the lane.
	lease atomic.Pointer[func() (generation int64, held bool)]
	// leaseRefusal is set while another installation owns this lane.
	leaseRefusal atomic.Pointer[LeaseRefusal]

	environment   launchEnvironment
	activityGrace time.Duration
}

type managerShared struct {
	// OnManualJournal is configured before admissions start. It must not block.
	// A durable manual journal lets the control loop acknowledge its request.
	OnManualJournal func()

	lanesMu      sync.RWMutex // Registry and scheduler publication never wait on journal I/O.
	mu           sync.Mutex
	reservations map[string]string // run UID -> job UID
	admissions   sync.Map
	recoveries   sync.Map // Project UID -> startup reconciliation and handoff lock.
	workspaceMu  sync.Mutex
	requests     sync.Map
	executions   sync.Map
	runners      sync.WaitGroup
	// runCtx outlives any one lane: a run admitted before its lane stood
	// down is still supervised and settled.
	runCtx context.Context

	projects bool
	lanes    map[string]*Manager
}

var _ sched.Admitter = (*Manager)(nil)

func NewManager(cfg config.Config, k Kata, h *herdr.Client, s *Store, q *Queue, guard RouteChecker, clock Clock) *Manager {
	if clock == nil {
		clock = RealClock{}
	}
	return &Manager{environment: &herdr.EnvironmentProbe{}, cfg: cfg, k: k, h: h, store: s, q: q, guard: guard, clock: clock, activityGrace: 5 * time.Minute, pinLayout: s.layout, managerShared: &managerShared{reservations: map[string]string{}, lanes: map[string]*Manager{}}}
}
func holds(r *Run) bool { return r.Phase != Settled && r.Phase != HandedOff && !preAdmissionHold(r) }

// RestoreGates runs before dispatch. A failed scan is never interpreted as free
// capacity; even prepared records reserve room for their eventual execution.
func (m *Manager) RestoreGates() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids, err := m.store.ScanActive()
	if err != nil {
		return err
	}
	restored := map[string]string{}
	for _, uid := range ids {
		r, err := m.store.Load(uid)
		if err != nil {
			return err
		}
		if holds(r) {
			restored[uid] = m.runGate(r)
		}
	}
	clear(m.reservations)
	maps.Copy(m.reservations, restored)
	return nil
}
func (m *Manager) prune() {
	for uid := range m.reservations {
		if r, e := m.store.tryLoad(uid); e == nil && !holds(r) {
			delete(m.reservations, uid)
		}
	}
}
func (m *Manager) pruneContext(ctx context.Context) error {
	for uid := range m.reservations {
		r, e := m.store.LoadContext(ctx, uid)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e == nil && !holds(r) {
			delete(m.reservations, uid)
		}
	}
	return ctx.Err()
}
func (m *Manager) FreeSlots() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune()
	occupied, projectOccupied := m.tallyLocked("", m.store.tryLoad)
	free := max(0, m.cfg.Limits.MaxActiveRuns-occupied)
	if m.cfg.ProjectLimit > 0 {
		free = min(free, max(0, m.cfg.ProjectLimit-projectOccupied))
	}
	return free
}
func (m *Manager) GateHeld(jobUID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune()
	for _, job := range m.reservations {
		if job == gateKey(m.projectUID, jobUID) {
			return true
		}
	}
	return false
}
func (m *Manager) reserve(uid, job string, forbid bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune()
	if _, ok := m.reservations[uid]; ok {
		return true
	}
	occupied, projectOccupied := m.tallyLocked("", m.store.tryLoad)
	if occupied >= m.cfg.Limits.MaxActiveRuns {
		return false
	}
	if m.cfg.ProjectLimit > 0 && projectOccupied >= m.cfg.ProjectLimit {
		return false
	}
	if forbid {
		for _, held := range m.reservations {
			if held == gateKey(m.projectUID, job) {
				return false
			}
		}
	}
	m.reservations[uid] = gateKey(m.projectUID, job)
	return true
}
func (m *Manager) unreserve(uid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.reservations, uid)
}

func (m *Manager) admissionLock(uid string) *sync.Mutex {
	v, _ := m.admissions.LoadOrStore(uid, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (m *Manager) recoveryLock(project string) *sync.Mutex {
	v, _ := m.recoveries.LoadOrStore(project, &sync.Mutex{})
	return v.(*sync.Mutex)
}
func missing(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// tallyLocked counts uncertain journals as occupied. The caller holds m.mu;
// resume excludes its own reservation and supplies a context-bounded loader.
func (m *Manager) tallyLocked(exclude string, load func(string) (*Run, error)) (total, lane int) {
	for uid, key := range m.reservations {
		if uid == exclude {
			continue
		}
		r, e := load(uid)
		if e != nil || !r.SlotReleased {
			total++
			if strings.HasPrefix(key, m.projectUID+"\x00") {
				lane++
			}
		}
	}
	return total, lane
}

func gateKey(project, job string) string {
	if project == "" {
		return job
	}
	return project + "\x00" + job
}
func (m *Manager) runGate(r *Run) string {
	if !m.projects {
		return gateKey("", r.Job.UID)
	}
	return gateKey(r.Target.Identity.ProjectUID, r.Job.UID)
}

// SetRunContext sets the lifetime of run supervision, usually the worker's.
// Call it before any admission.
func (m *Manager) SetRunContext(ctx context.Context) { m.runCtx = ctx }

// SetLease makes this lane's preparations and pre-admission writes depend on
// holding its lease. Call it before the lane admits.
func (m *Manager) SetLease(lease func() (int64, bool)) {
	m.lease.Store(&lease)
	if m.q != nil {
		m.q.lease.Store(&lease)
	}
}

func (m *Manager) EnableProjects() {
	m.projects = true
	if m.q != nil {
		m.q.projects = true
	}
}
func (m *Manager) RegisterProject(uid string, cfg config.Config, k Kata, guard RouteChecker, l state.Layout) (*Manager, error) {
	m.lanesMu.Lock()
	defer m.lanesMu.Unlock()
	if old := m.lanes[uid]; old != nil {
		return nil, fmt.Errorf("duplicate project UID %s: entries %s and %s", uid, old.cfg.Kata.Project, cfg.Kata.Project)
	}
	lane := &Manager{environment: &herdr.EnvironmentProbe{}, cfg: cfg, k: k, h: m.h, store: m.store, q: m.q, guard: guard, clock: m.clock, managerShared: m.managerShared, projectUID: uid, pinLayout: l, activityGrace: m.activityGrace}
	m.lanes[uid] = lane
	if m.q != nil {
		lane.q = m.q.register(uid, k, guard, l)
	}
	return lane, nil
}
func (m *Manager) ForProject(uid string) *Manager {
	m.lanesMu.RLock()
	defer m.lanesMu.RUnlock()
	return m.lanes[uid]
}
func (m *Manager) forRun(r *Run) *Manager {
	if !m.projects {
		return m
	}
	return m.ForProject(r.Target.Identity.ProjectUID)
}
