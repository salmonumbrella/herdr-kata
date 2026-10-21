package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/inbox"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/lease"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

const laneStartupConcurrency = 2

// A lane retains its slot through the first snapshot, but releases it during
// recovery backoff. Only that lane's goroutine acquires or releases its slot.
type laneStartup struct {
	slots chan struct{}
	held  bool
}

func (s *laneStartup) acquire(ctx context.Context) error {
	if s.held {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.slots <- struct{}{}:
		s.held = true
		return nil
	}
}

func (s *laneStartup) release() {
	if s.held {
		<-s.slots
		s.held = false
	}
}

func Worker(ctx context.Context, cfg config.Config, l state.Layout) error {
	return workerWithFactory(ctx, cfg, l, func(c config.Config) *kata.CLI { return kata.New(c.Kata, l.Root, nil, nil) })
}

func workerWithCLI(ctx context.Context, cfg config.Config, l state.Layout, k *kata.CLI) error {
	return workerWithFactory(ctx, cfg, l, func(config.Config) *kata.CLI { return k })
}
func workerWithFactory(ctx context.Context, cfg config.Config, l state.Layout, cli func(config.Config) *kata.CLI) (err error) {
	lock, ok, err := state.TryLock(l.WorkerLock())
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("worker already running")
	}
	defer lock.Unlock() //nolint:errcheck // Lock cleanup remains best-effort.
	if err = state.InitializeLayout(state.OSFS{}, l); err != nil {
		return err
	}
	layouts, e := state.ProjectLayouts(state.OSFS{}, l)
	if e != nil {
		return e
	}
	for _, pl := range layouts {
		var pin kata.Installation
		if e = state.ReadJSON(state.OSFS{}, pl.StateFile(), 65536, &pin); e != nil {
			return e
		}
		name := pin.Name
		if name == "" {
			name = pin.ProjectUID
		}
		if e = state.WriteJSON(state.OSFS{}, pl.RouteFile(), RouteStatus{Project: name, Allowed: false, Error: "waiting for project " + name}); e != nil {
			return e
		}
	}
	ctx, cancelCause := context.WithCancelCause(ctx)
	defer cancelCause(context.Canceled)
	store := runner.NewStore(state.OSFS{}, l)
	queue := runner.NewQueue(nil, store, nil, runner.RealClock{})
	manager := runner.NewManager(cfg, nil, &herdr.Client{Path: herdr.SocketPath(cfg.Session)}, store, queue, nil, runner.RealClock{})
	manager.EnableProjects()
	// Runs outlive their lane: a lane that stands down still settles them.
	manager.SetRunContext(ctx)
	requestWake := make(chan struct{}, 1)
	// The control tick reads only local request files. Lanes wake, and may
	// read Kata, only for a signal, a manual journal or a pending request.
	var wakeLanes atomic.Bool
	tick := func() {
		select {
		case requestWake <- struct{}{}:
		default:
		}
	}
	handle := func() { wakeLanes.Store(true); tick() }
	manager.OnManualJournal = handle
	if err = manager.RestoreGates(); err != nil {
		return err
	}
	if err = manager.Retain(time.Now()); err != nil {
		return err
	}
	var startupMu sync.Mutex
	startupErrors := map[string]string{}
	publishStartup := func(name string, problem error) error {
		startupMu.Lock()
		defer startupMu.Unlock()
		if problem == nil {
			delete(startupErrors, name)
		} else {
			startupErrors[name] = problem.Error()
		}
		return saveStartupErrors(l, startupErrors)
	}
	if err := saveStartupErrors(l, startupErrors); err != nil {
		return err
	}
	var wg sync.WaitGroup
	defer manager.Wait()
	defer wg.Wait()
	defer cancelCause(context.Canceled)
	failures := make(chan error, 16)
	var claimed sync.Map // Refuse duplicate UIDs before either entry rewrites its pin.
	entries := cfg.ProjectEntries()
	resolved := make(chan struct{}, len(entries))
	identitiesReady := make(chan struct{})
	wg.Go(func() {
		for range entries {
			select {
			case <-ctx.Done():
				return
			case <-resolved:
			}
		}
		close(identitiesReady)
	})
	startupSlots := make(chan struct{}, laneStartupConcurrency)
	wg.Go(func() { runQueue(ctx, queue, cancelCause, failures) })
	for _, pl := range layouts {
		var pin kata.Installation
		if err := state.ReadJSON(state.OSFS{}, pl.StateFile(), 65536, &pin); err != nil {
			return err
		}
		if configuredLane(cfg, pin) {
			continue
		}
		wg.Go(func() {
			// A changed name is not removal. Wait until every configured
			// identity is known before deciding that a saved UID is absent.
			select {
			case <-ctx.Done():
				return
			case <-identitiesReady:
			}
			if _, configured := claimed.Load(pin.ProjectUID); configured {
				return
			}
			if err := retireLane(ctx, cfg, pin, pl, manager, cli); err != nil && ctx.Err() == nil {
				failures <- err
				cancelCause(err)
			}
		})
	}
	for _, entry := range entries {
		view := cfg.View(entry)
		wg.Go(func() {
			startup := &laneStartup{slots: startupSlots}
			defer startup.release()
			var guard *kata.RouteGuard
			var laneLayout state.Layout
			var projectUID string
			k := cli(view)
			var e error
			waits := 0
			for ctx.Err() == nil {
				if e = startup.acquire(ctx); e != nil {
					return
				}
				initialize := (*kata.RouteGuard).Initialize
				if guard == nil {
					identity, route, resolveErr := kata.ResolveIdentity(ctx, k)
					e = resolveErr
					if e == nil && !state.SafeComponent(identity.ProjectUID) {
						e = errors.New("invalid project UID")
					}
					if e == nil {
						previous, exists := claimed.LoadOrStore(identity.ProjectUID, view.Kata.Project)
						resolved <- struct{}{}
						if exists {
							startup.release()
							slog.Warn("project refused", "project", view.Kata.Project, "error", fmt.Errorf("duplicate project UID %s: entries %s and %s", identity.ProjectUID, previous, view.Kata.Project))
							<-ctx.Done()
							return
						}
						// A different project UID at startup is a new lane (§22.3).
						// Existing project journals keep their original target and wait.
						laneLayout = l.Project(identity.ProjectUID)
						projectUID = identity.ProjectUID
						guard = kata.NewRouteGuard(view, k, state.OSFS{}, laneLayout, identity.ProjectUID)
						// The first pin reuses this resolution; a retry resolves again.
						initialize = func(g *kata.RouteGuard, ctx context.Context) error { return g.InitializeResolved(ctx, identity, route) }
					}
				}
				if guard != nil {
					if e = initialize(guard, ctx); e == nil {
						if err := publishStartup(view.Kata.Project, nil); err != nil {
							startup.release()
							failures <- err
							cancelCause(err)
							return
						}

						lane, registerErr := manager.RegisterProject(projectUID, view, k, guard, laneLayout)
						if registerErr != nil {
							startup.release()
							slog.Warn("project refused", "project", view.Kata.Project, "error", registerErr)
							<-ctx.Done()
							return
						}
						e = serveLane(ctx, view, laneLayout, k, guard, lane, startup, func(problem error) error { return publishStartup(view.Kata.Project, problem) })
						if e != nil && ctx.Err() == nil {
							failures <- e
							cancelCause(e)
						}
						return
					}
				}
				startup.release()
				if err := publishStartup(view.Kata.Project, e); err != nil {
					failures <- err
					cancelCause(err)
					return
				}
				slog.Warn("project lane waiting", "project", view.Kata.Project, "error", e)
				if state.LocalError(e) {
					failures <- e
					cancelCause(e)
					return
				}
				timer := time.NewTimer(kata.WaitDelay(2*time.Second, waits))
				waits++
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		})
	}
	wake := make(chan os.Signal, 1)
	signal.Notify(wake, syscall.SIGUSR1)
	defer signal.Stop(wake)
	requests := time.NewTicker(5 * time.Second)
	defer requests.Stop()
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	retention := time.NewTicker(24 * time.Hour)
	defer retention.Stop()
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-requestWake:
				pending := manager.PendingRequests()
				if e := manager.HandleRequests(ctx, nil); e != nil && ctx.Err() == nil {
					slog.Warn("control requests", "error", e)
				}
				if wakeLanes.Swap(false) || pending {
					manager.WakeProjects()
				}
			}
		}
	})
	handle()
	for {
		select {
		case <-ctx.Done():
			cause := context.Cause(ctx)
			if errors.Is(cause, context.Canceled) {
				return nil
			}
			return cause
		case e := <-failures:
			if ctx.Err() != nil && errors.Is(e, context.Canceled) {
				return nil
			}
			return e
		case <-wake:
			handle()
		case <-requests.C:
			tick()
		case <-retention.C:
			if e := manager.Retain(time.Now()); e != nil {
				return e
			}
		case <-ping.C:
			if exists(l.StopFile()) || exists(l.UpgradeFile()) {
				return nil
			}
			pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, e := (&herdr.Client{Path: herdr.SocketPath(cfg.Session)}).Ping(pctx)
			cancel()
			if e != nil {
				slog.Warn("Herdr unavailable", "error", e)
			}
		}
	}
}

func routeCheckDelay() time.Duration {
	return kata.RouteCheckInterval + time.Duration(rand.Int64N(int64(kata.RouteCheckInterval/2)))
}

// routeCadence spaces a lane's periodic route checks. An allowed lane with
// job definitions checks every 30-45 seconds; an idle one, with none, waits at
// the cap. A refused lane backs off from one interval to the cap. A lane that
// stopped admissions for another executor waits at the cap, because
// re-probing cannot clear that refusal.
type routeCadence struct {
	waits int
	idle  bool // the pending delay is an idle lane's
}

func (c *routeCadence) next(guard *kata.RouteGuard, idle bool) time.Duration {
	c.idle = false
	switch {
	case guard.Allowed() && !idle:
		c.waits = 0
		return routeCheckDelay()
	case guard.Allowed():
		c.waits, c.idle = 0, true
		return kata.WaitDelay(kata.WaitCap, 0)
	}
	c.waits++
	return kata.WaitDelay(2*kata.RouteCheckInterval, c.waits-1)
}

func runLane(ctx context.Context, cfg config.Config, l state.Layout, k *kata.CLI, guard *kata.RouteGuard, manager *runner.Manager, routeHealth <-chan time.Time, startup *laneStartup) error {
	return runHeldLane(ctx, cfg, l, k, guard, manager, routeHealth, startup, nil)
}

// Lease waits. A refused lane re-reads its lease no sooner than
// leaseRetryFirst, then backs off to leaseRetryCap. An unreadable lease is
// retried sooner, but neither refuses nor admits.
const (
	leaseRetryFirst = 3 * time.Minute
	leaseRetryCap   = 5 * time.Minute
)

// serveLane holds the lane's lease before any work. A live lease of another
// installation refuses the lane at once: no definitions, events or
// admissions, only a route check and a lease read on a slow backoff. When the
// lease is free, expired or already this installation's, the lane runs until
// another installation takes it over, then waits again.
func serveLane(ctx context.Context, cfg config.Config, l state.Layout, k *kata.CLI, guard *kata.RouteGuard, manager *runner.Manager, startup *laneStartup, publish func(error) error) error {
	refusals, unknowns := 0, 0
	var current atomic.Pointer[lease.Hold]
	manager.SetLease(func() (int64, bool) {
		if hold := current.Load(); hold != nil {
			return hold.Held()
		}
		return 0, false
	})
	for ctx.Err() == nil {
		hold, err := claimLane(ctx, cfg, k, guard)
		if err == nil {
			current.Store(hold)
			manager.SetLeaseRefusal(nil)
			refusals, unknowns = 0, 0
			if err := errors.Join(publish(nil), saveLeaseStatus(l, hold, nil, time.Now())); err != nil {
				return err
			}
			err = runHeldLane(ctx, cfg, l, k, guard, manager, nil, startup, hold)
			if !errors.Is(err, lease.ErrLost) {
				return err
			}
			slog.Warn("lane stood down", "project", cfg.Kata.Project, "error", err)
		} else if state.LocalError(err) {
			return err
		}
		if ctx.Err() == nil {
			if err := saveLeaseStatus(l, nil, err, time.Now()); err != nil {
				return err
			}
		}
		// Control requests for the lane are refused, and queued ones settle,
		// until this installation holds the lease again.
		if owner := leaseOwnerElsewhere(err); owner != nil {
			manager.SetLeaseRefusal(owner)
		}
		if startup != nil {
			startup.release()
		}
		if ctx.Err() != nil {
			return nil
		}
		var delay time.Duration
		switch {
		case errors.Is(err, lease.ErrLost), errors.Is(err, lease.ErrHeld):
			delay = min(leaseRetryCap, leaseRetryFirst+time.Duration(refusals)*time.Minute) + rand.N(time.Minute)
			refusals++
		default:
			slog.Warn("lane lease unreadable", "project", cfg.Kata.Project, "error", err)
			delay = kata.WaitDelay(10*time.Second, unknowns)
			unknowns++
		}
		if err := publish(err); err != nil {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}

// claimLane claims the lane's lease on a currently checked route. The route
// check that started the lane counts; a later attempt checks it again.
func claimLane(ctx context.Context, cfg config.Config, k *kata.CLI, guard *kata.RouteGuard) (*lease.Hold, error) {
	if err := guard.CheckFresh(ctx, kata.StartupRouteReuse); err != nil {
		return nil, err
	}
	return lease.Claim(ctx, k, cfg.Executor, false, time.Now())
}

// runHeldLane serves a lane while hold, its lease, stays this installation's.
// It returns the LostError once another installation takes the lane over.
func runHeldLane(ctx context.Context, cfg config.Config, l state.Layout, k *kata.CLI, guard *kata.RouteGuard, manager *runner.Manager, routeHealth <-chan time.Time, startup *laneStartup, hold *lease.Hold) (laneErr error) {
	if startup != nil {
		defer startup.release()
	}
	ctx, cancelCause := context.WithCancelCause(ctx)
	cancel := func() { cancelCause(context.Canceled) }
	defer cancel()
	defer func() {
		// A takeover seen by an admission ends the lane like a heartbeat's.
		if cause := context.Cause(ctx); laneErr == nil && errors.Is(cause, lease.ErrLost) {
			laneErr = cause
		}
	}()
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	failures := make(chan error, 4)
	poke := func(ch chan struct{}) {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	defsDirty, resetDirty, leaseChanged := make(chan struct{}, 1), make(chan struct{}, 1), make(chan struct{}, 1)
	reader := kata.GuardedReader{CLI: k, Guard: guard}
	cache := defs.NewCache(cfg, reader, state.OSFS{}, l)
	if err := retryLane(ctx, guard, func() error { return manager.Recover(ctx) }, startup); err != nil {
		return err
	}
	admitter := &laneAdmitter{Manager: manager, hold: hold, onLost: cancelCause}
	leaseIssue := ""
	if hold != nil {
		leaseIssue = hold.Issue()
	}
	scheduler := sched.New(cfg, reader, admitter, state.OSFS{}, l, sched.RealClock{})
	scheduler.RecordDecision = cache.RecordDecision
	scheduler.Definitions = cache.Listing
	scheduler.OnManualComplete = manager.OnManualJournal
	var scanner *inbox.Scanner
	if cfg.Kata.Inbox != "" {
		var caps kata.Capabilities
		if err := retryLane(ctx, guard, func() error { caps = guard.CheckedCapabilities(); return nil }, startup); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		scanner = inbox.New(cfg, reader, caps, func(ctx context.Context, d sched.Due) {
			if err := admitter.confirm(ctx); err != nil {
				if ctx.Err() == nil {
					slog.Warn("child admission", "issue", d.ChildUID(), "error", err)
				}
				return
			}
			if _, err := manager.Admit(ctx, d); err != nil && ctx.Err() == nil {
				slog.Warn("child admission", "issue", d.ChildUID(), "error", err)
			}
		}, sched.RealClock{})
		scanner.CheckRequest = manager.CheckIssueRequest
		scanner.SaveSnapshot = func(s inbox.Snapshot) error { return inbox.WriteSnapshot(state.OSFS{}, l.Root, s) }
	}
	pokeInbox := func() {
		if scanner != nil {
			scanner.Poke()
		}
	}
	manager.SetScheduler(scheduler, func() { scheduler.Poke(); pokeInbox() })

	markDirty := func(ch chan struct{}) { admitter.markDirty(); poke(ch) }
	wg.Go(func() {
		failures <- kata.RunEvents(ctx, k, l, kata.EventHooks{
			OnDefs: func(kata.Event) { markDirty(defsDirty) },
			// A lease issue event is a renewal or a takeover: check which,
			// and wake nothing else.
			OnIssue: func(e kata.Event) {
				if e.IssueUID != "" && e.IssueUID == leaseIssue {
					// Only a metadata change after this hold's last write can
					// be a takeover; an unreadable one is checked.
					var change struct {
						Revision int64 `json:"revision_new"`
					}
					if e.Type == "issue.metadata_updated" && (json.Unmarshal(e.Payload, &change) != nil || hold.Newer(change.Revision)) {
						poke(leaseChanged)
					}
					return
				}
				scheduler.Poke()
				if scanner != nil {
					scanner.PokeWaiting()
				}
			},
			OnInbox: func(e kata.Event) {
				if e.IssueUID == "" || e.IssueUID != leaseIssue {
					pokeInbox()
				}
			},
			OnReset: func() { markDirty(resetDirty); pokeInbox() },
		})
	})
	// The events reader starts the subscription before requesting the first
	// snapshot. Dirty flags queued during refresh survive the refresh.
	select {
	case <-ctx.Done():
		cause := context.Cause(ctx)
		if errors.Is(cause, context.Canceled) {
			return nil
		}
		return cause
	case err := <-failures:
		return err
	case <-resetDirty:
	}
	var routeTimer *time.Timer
	var cadence routeCadence
	idle := func() bool { return len(cache.Snapshot()) == 0 }
	refresh := func() error {
		if !admitter.pending() || !guard.Allowed() {
			return nil
		}
		generation := admitter.dirty.Load()
		if err := cache.Refresh(ctx); err != nil {
			return err
		}
		scheduler.RefreshDefs(func() { admitter.applied.Store(generation) })
		// The first job ends an idle lane's slow wait.
		if routeTimer != nil && cadence.idle && !idle() {
			cadence.idle = false
			routeTimer.Reset(routeCheckDelay())
		}
		return nil
	}
	if err := refresh(); err != nil {
		if state.LocalError(err) {
			return err
		}
		slog.Warn("definition refresh", "project", cfg.Kata.Project, "error", err)
	}
	if startup != nil {
		startup.release()
	}
	wg.Go(func() { failures <- rerunLane(ctx, guard, func() error { return scheduler.Run(ctx) }) })
	if scanner != nil && scanner.Enabled() {
		wg.Go(func() { failures <- scanner.Run(ctx) })
	}
	slow := time.NewTimer(inbox.FallbackDelay())
	defer slow.Stop()
	fallback := time.NewTimer(15*time.Minute + rand.N(3*time.Minute))
	defer fallback.Stop()
	var beat <-chan time.Time
	if hold != nil {
		ticker := time.NewTicker(lease.Heartbeat)
		defer ticker.Stop()
		beat = ticker.C
	}
	if routeHealth == nil {
		routeTimer = time.NewTimer(routeCheckDelay())
		routeHealth = routeTimer.C
		defer routeTimer.Stop()
	}
	check := func() error {
		if e := guard.Check(ctx); e != nil {
			if state.LocalError(e) {
				return e
			}
			slog.Warn("route refused", "project", cfg.Kata.Project, "error", e)
		}
		if e := refresh(); e != nil {
			if state.LocalError(e) {
				return e
			}
			slog.Warn("definition refresh", "project", cfg.Kata.Project, "error", e)
		}
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-failures:
			return e
		case <-defsDirty:
			if e := refresh(); e != nil {
				if state.LocalError(e) {
					return e
				}
				slog.Warn("definition refresh", "error", e)
			}
		case <-resetDirty:
			// A waiting lane keeps its backoff; its next timed check refreshes.
			if guard.Allowed() {
				if e := check(); e != nil {
					return e
				}
			}
			scheduler.Poke()
			pokeInbox()
		case <-routeHealth:
			if e := check(); e != nil {
				return e
			}
			if routeTimer != nil {
				routeTimer.Reset(cadence.next(guard, idle()))
			}
		case <-slow.C:
			slow.Reset(inbox.FallbackDelay())
			if e := check(); e != nil {
				return e
			}
			if guard.Allowed() {
				if e := manager.ReconcileHeld(ctx); e != nil {
					return e
				}
			}
			scheduler.Poke()
			pokeInbox()
		case <-leaseChanged:
			if err := hold.Check(ctx); errors.Is(err, lease.ErrLost) {
				return err
			} else if err != nil && ctx.Err() == nil {
				slog.Warn("lane lease check", "project", cfg.Kata.Project, "error", err)
			}
		case <-beat:
			// Renew only on a held route; a missed beat is retried, and the
			// lease outlives several of them.
			if guard.RouteHeld() {
				if err := hold.Renew(ctx, time.Now()); errors.Is(err, lease.ErrLost) {
					return err
				} else if err != nil && ctx.Err() == nil {
					slog.Warn("lane lease heartbeat", "project", cfg.Kata.Project, "error", err)
				}
			}
		case <-fallback.C:
			fallback.Reset(15*time.Minute + rand.N(3*time.Minute))
			admitter.markDirty()
			if e := refresh(); e != nil {
				if state.LocalError(e) {
					return e
				}
				slog.Warn("fallback refresh", "error", e)
			}
		}
	}
}

// Queue I/O failures must interrupt admission waits during startup recovery,
// before the worker enters its main failure-selection loop.
func runQueue(ctx context.Context, q *runner.Queue, cancel context.CancelCauseFunc, failures chan<- error) {
	err := q.Run(ctx)
	failures <- err
	if err != nil && !errors.Is(err, context.Canceled) {
		cancel(err)
	}
}

// rerunLane restarts work after the lane's route timer has started. The
// timer owns probes of a refused route, so a refused lane waits on the
// guard's local state instead of probing Kata again.
func rerunLane(ctx context.Context, guard *kata.RouteGuard, work func() error) error {
	waits := 0
	for ctx.Err() == nil {
		delay := 2 * time.Second
		if guard.Allowed() {
			err := checkStartRoute(ctx, guard, waits)
			if err == nil {
				err = work()
			}
			if err == nil {
				return nil
			}
			if state.LocalError(err) {
				return err
			}
			if ctx.Err() != nil {
				return nil
			}
			slog.Warn("project lane waiting", "error", err)
			delay = kata.WaitDelay(2*time.Second, waits)
			waits++
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}

// checkStartRoute lets a lane's first attempt at a start step reuse the
// start's passing route check. A retry checks the live route.
func checkStartRoute(ctx context.Context, guard *kata.RouteGuard, waits int) error {
	if waits == 0 {
		return guard.CheckFresh(ctx, kata.StartupRouteReuse)
	}
	return guard.Check(ctx)
}

// Kata failures fence one lane. Local filesystem failures stop the worker.
func retryLane(ctx context.Context, guard *kata.RouteGuard, work func() error, startup ...*laneStartup) error {
	waits := 0
	for ctx.Err() == nil {
		for _, gate := range startup {
			if gate != nil {
				if err := gate.acquire(ctx); err != nil {
					return nil
				}
			}
		}
		err := checkStartRoute(ctx, guard, waits)
		if err == nil {
			err = work()
		}
		if err == nil {
			return nil
		}
		// A refused lane must not reserve startup capacity during recovery
		// backoff; healthy projects still need to start.
		for _, gate := range startup {
			if gate != nil {
				gate.release()
			}
		}
		if state.LocalError(err) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		slog.Warn("project lane waiting", "error", err)
		timer := time.NewTimer(kata.WaitDelay(2*time.Second, waits))
		waits++
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}
