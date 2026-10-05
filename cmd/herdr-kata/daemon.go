package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/lockfile"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

// daemonOpts is what `herdr-kata daemon` accepts.
type daemonOpts struct {
	tick        *time.Duration
	concurrency *int
	detach      *bool
}

// daemonFlagSet is separate from daemonCmd so a test can ask which flags exist
// without running a scheduler — which is how the plugin manifest came to invoke
// a --detach that nothing defined.
func daemonFlagSet() (*flag.FlagSet, *daemonOpts) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	return fs, &daemonOpts{
		tick:        fs.Duration("tick", defaultTick, "how often to check for due jobs"),
		concurrency: fs.Int("concurrency", defaultConcurrency, "maximum jobs running at once"),
		detach:      fs.Bool("detach", false, "start the scheduler in the background and return"),
	}
}

// daemonCmd runs the scheduler loop.
//
// The daemon is hosted by the Herdr plugin's startup hook, which fires again
// on live handoff, so it must be safe to start repeatedly: the lock makes
// every start after the first a no-op.
func daemonCmd(argv []string) error {
	fs, opts := daemonFlagSet()
	if err := fs.Parse(argv); err != nil {
		return err
	}
	tick, concurrency, detach := opts.tick, opts.concurrency, opts.detach

	// --detach is what a startup hook needs: the hook is reaped rather than
	// supervised, so a foreground daemon dies with it. The plugin manifest and
	// the README have both asked for this flag since before it existed, which
	// meant one failed hook printing "flag provided but not defined" on every
	// Herdr start.
	if *detach {
		return ensureRunning("--tick", tick.String())
	}

	lock, err := lockfile.Acquire(lockPath(roleDaemon))
	if err != nil {
		var held *lockfile.ErrHeld
		if errors.As(err, &held) {
			// Not an error: repeated starts are how the startup hook, the
			// board, and the sentinel all behave.
			fmt.Println("herdr-kata:", held.Error())
			return nil
		}
		return err
	}
	defer lock.Release()

	// Record which build is serving this role, so a later `herdr-kata ensure`
	// can tell a running scheduler from a current one.
	recordBuildStamp(roleDaemon)

	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	// Whatever killed the previous daemon may have stranded a run mid-flight,
	// and the sentinel restarts within seconds — so this is the first chance to
	// put those rows right.
	reconcileOnStart(s)

	ctx, stop := signalContext()
	defer stop()

	// The scheduler watches the sentinel just as the sentinel watches it, so
	// killing either one leaves a survivor that restores the pair.
	go watchPeer(ctx, roleDaemon, stop)

	d := &daemon{store: s, tick: *tick, slots: make(chan struct{}, *concurrency)}
	fmt.Printf("herdr-kata: daemon started (tick %s, concurrency %d, pid %d)\n",
		*tick, *concurrency, os.Getpid())
	d.run(ctx)
	fmt.Println("herdr-kata: daemon stopped")
	return nil
}

const (
	defaultTick        = 5 * time.Second
	defaultConcurrency = 4
	// defaultReconcileEvery is how often the daemon re-reads parked runs
	// against the disk. It is minutes rather than seconds because a park it
	// corrects is one an agent finished after the supervisor gave up, and an
	// agent that overran by that much is not going to finish within a tick.
	defaultReconcileEvery = 5 * time.Minute
)

// signalContext cancels on interrupt or termination.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

// daemon evaluates schedules and launches due runs.
type daemon struct {
	store *store.Store
	tick  time.Duration
	// slots bounds how many jobs run at once.
	slots chan struct{}

	mu sync.Mutex
	// inflight is the set of job ids currently running, which is what enforces
	// the no-overlap rule.
	inflight     map[string]bool
	dateProblems map[string]string
	wg           sync.WaitGroup

	// exec runs one job. It is a field so a test can count what a sweep
	// launches without starting agents — the whole execution path was
	// untestable, and a catchup policy that launched one run per sweep instead
	// of the backlog it owed went unnoticed for that reason.
	exec func(context.Context, *store.Store, store.Job, string) (*runner.Run, error)

	// reconcileEvery is how often parked runs are re-read from disk. Zero means
	// the default; a test sets it short.
	reconcileEvery time.Duration
	// reconcile corrects parks the disk contradicts. A field for the same
	// reason exec is one.
	reconcile func(context.Context, *store.Store) (int, error)

	// deliver is the daemon-owned hook worker's action. Tests inject a blocked
	// hook to prove that the scheduler loop keeps moving.
	deliver func(context.Context, *store.Store) error
}

// reconcileStale aligns parked run records with their saved execution artifacts.
func (d *daemon) reconcileStale(ctx context.Context) {
	fn := d.reconcile
	if fn == nil {
		fn = reconcileParked
	}
	n, err := fn(ctx, d.store)
	if err != nil {
		fmt.Fprintln(os.Stderr, "herdr-kata: reconcile parked:", err)
		return
	}
	if n > 0 {
		fmt.Printf("herdr-kata: corrected %d park(s) the disk contradicts\n", n)
	}
}

// execute runs one job, through whatever exec is set to.
func (d *daemon) execute(ctx context.Context, j store.Job) (*runner.Run, error) {
	if d.exec != nil {
		return d.exec(ctx, d.store, j, "scheduled")
	}
	return Execute(ctx, d.store, j, "scheduled")
}

func (d *daemon) run(ctx context.Context) {
	d.inflight = map[string]bool{}
	hookWake := make(chan struct{}, 1)
	hookDone := make(chan struct{})
	go func() {
		defer close(hookDone)
		for {
			select {
			case <-ctx.Done():
				return
			case <-hookWake:
				if err := flushOrdinaryAttention(ctx, d.store); err != nil && ctx.Err() == nil {
					fmt.Fprintln(os.Stderr, "herdr-kata: attention:", err)
				}
				if err := flushHistoricalComments(ctx, d.store); err != nil && ctx.Err() == nil {
					fmt.Fprintln(os.Stderr, "herdr-kata: comments:", err)
				}
				if err := pollNativeInboxes(ctx, d.store); err != nil && ctx.Err() == nil {
					fmt.Fprintln(os.Stderr, "herdr-kata: inbox:", err)
				}
				if err := flushNativeObservations(ctx, d.store); err != nil && ctx.Err() == nil {
					fmt.Fprintln(os.Stderr, "herdr-kata: observation replay:", err)
				}
				fn := d.deliver
				if fn == nil {
					fn = func(ctx context.Context, s *store.Store) error {
						return deliverRunEvents(ctx, s, stateDir(), runSettledHook)
					}
				}
				if err := fn(ctx, d.store); err != nil && ctx.Err() == nil {
					fmt.Fprintln(os.Stderr, "herdr-kata: run-settled hook:", err)
				}
			}
		}
	}()
	hookWake <- struct{}{} // flush events recorded while the daemon was down
	t := time.NewTicker(d.tick)
	defer t.Stop()
	every := d.reconcileEvery
	if every == 0 {
		every = defaultReconcileEvery
	}
	rt := time.NewTicker(every)
	defer rt.Stop()
	for {
		select {
		case <-ctx.Done():
			// Let running jobs finish: killing an agent mid-turn would leave a
			// run that is neither done nor parked.
			d.wg.Wait()
			<-hookDone
			return
		case <-t.C:
			select {
			case hookWake <- struct{}{}:
			default:
			}
			d.sweep(ctx)
		case <-rt.C:
			d.reconcileStale(ctx)
		}
	}
}

// sweep launches every job that is due and not already running.
func (d *daemon) sweep(ctx context.Context) {
	jobs, err := d.store.Jobs(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "herdr-kata: read jobs:", err)
		if d.store.Native == nil || len(jobs) == 0 {
			return
		}
	}
	now := time.Now()
	// Retain diagnostics only for currently activated jobs, bounding this
	// transient map to the current native view and allowing reactivation notice.
	if d.store.Native != nil {
		active := map[string]bool{}
		for _, j := range jobs {
			if j.Enabled && !j.NativeOffline {
				active[j.ID] = true
			}
		}
		d.mu.Lock()
		for id := range d.dateProblems {
			if !active[id] {
				delete(d.dateProblems, id)
			}
		}
		d.mu.Unlock()
	}
	for _, j := range jobs {
		if d.store.Native != nil && (j.NativeOffline || !j.Enabled) {
			continue
		}
		var anchor time.Time
		if last, err := d.store.LastRun(ctx, j.ID); err == nil {
			anchor = last.StartedAt
		}
		var fires []time.Time
		var source string
		if d.store.Native != nil && (j.Schedule == "issue-scheduled" || j.Schedule == "issue-deadline") {
			j, fires, source, err = d.nativeDateDue(ctx, j, now)
			d.nativeDateDiagnostic(j, err)
		} else {
			fires, err = sched.Due(j, anchor, now)
		}
		if err != nil {
			// A single malformed schedule must not stop the sweep.
			if d.store.Native == nil || (j.Schedule != "issue-scheduled" && j.Schedule != "issue-deadline") {
				fmt.Fprintln(os.Stderr, "herdr-kata:", err)
			}
			continue
		}
		if len(fires) == 0 {
			continue
		}
		if d.stillRunning(ctx, j) {
			// Still running from a previous fire. Skipping is the safe
			// default: a job whose runs outlast its interval would otherwise
			// stack up agents without bound.
			continue
		}
		if !d.claim(j.ID) {
			continue
		}
		// One claim for the whole backlog, and the runs go in series inside
		// it. This loop used to call claim per fire and break as soon as one
		// was refused, so catchup=all launched exactly one run per sweep — and
		// the next sweep measured from that run, which put the rest of the
		// backlog behind the anchor and lost it. Three documented policies,
		// one behaviour.
		if len(fires) > 1 {
			fmt.Printf("herdr-kata: job %s owes %d fires; replaying them in series\n",
				j.ID, len(fires))
		}
		launchCtx := ctx
		if source != "" {
			launchCtx = context.WithValue(ctx, nativeSourceKey{}, source)
		}
		d.launch(launchCtx, j, fires)
	}
}

func (d *daemon) stillRunning(ctx context.Context, j store.Job) bool {
	runs, err := d.store.JobRuns(ctx, j.ID, inFlightLookback)
	if err != nil {
		// The claim below still guards the common case. Refusing to fire
		// because the store could not be read would turn a transient error
		// into a silently skipped schedule.
		fmt.Fprintln(os.Stderr, "herdr-kata: check running runs:", err)
		return false
	}
	timeout := j.Timeout
	if timeout <= 0 {
		timeout = defaultJobTimeout
	}
	now := time.Now()
	for _, r := range runs {
		if r.Outcome != store.StepRunning {
			continue
		}
		if r.StartedAt.IsZero() || now.Sub(r.StartedAt) < timeout+inFlightGrace {
			return true
		}
	}
	return false
}

// inFlightLookback bounds how far back stillRunning reads. A job's own recent
// rows are all that can describe a fire still in flight, and the newest rows
// come first.
const inFlightLookback = 5

// claim marks a job as running, reporting false when it already is.
func (d *daemon) claim(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.inflight[id] {
		return false
	}
	d.inflight[id] = true
	return true
}

func (d *daemon) release(id string) {
	d.mu.Lock()
	delete(d.inflight, id)
	d.mu.Unlock()
}

func (d *daemon) launch(ctx context.Context, j store.Job, fires []time.Time) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer d.release(j.ID)

		for i, fire := range fires {
			// Block until a slot frees. The job is already claimed, so it
			// cannot be launched twice while it waits here.
			select {
			case d.slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			// A backlog stops at shutdown; a run already started does not.
			if i > 0 && ctx.Err() != nil {
				<-d.slots
				return
			}

			// The run gets a context that shutdown does not cancel. The
			// scheduler's ctx reaches exec.CommandContext through herdrcli, so
			// SIGTERM used to kill every agent mid-turn — leaving exactly the
			// run that is neither done nor parked that d.run() claims to
			// prevent by waiting. The run's own timeout still bounds it.
			runCtx := context.WithoutCancel(ctx)
			if d.store.Native != nil {
				runCtx = context.WithValue(runCtx, nativeFireTime{}, fire.UTC())
			}
			run, err := d.execute(runCtx, j)
			<-d.slots
			switch {
			case err != nil:
				fmt.Fprintf(os.Stderr, "herdr-kata: job %s: %v\n", j.ID, err)
			case run != nil:
				fmt.Printf("herdr-kata: job %s %s (%s)\n", j.ID, run.Outcome, run.RunID)
			}
		}
	}()
}
