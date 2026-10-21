package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// The guard's real route snapshot is acknowledged after its directory sync.
// Other worker files use their own OSFS and cannot satisfy this barrier.
type routeSnapshotFS struct {
	state.FS
	root  string
	saved chan struct{}
}

func TestLaneRearmsPeriodicRouteCheckAfterCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := state.Layout{Root: t.TempDir()}
		if err := state.InitializeLayout(state.OSFS{}, root); err != nil {
			t.Fatal(err)
		}
		l := root.Project("project")
		cfg := config.Config{Executor: "worker", Kata: config.Kata{Bin: "/bin/kata", Project: "example-project", Actor: "worker", Env: map[string]string{"KATA_HOME": t.TempDir()}}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}
		started := time.Now()
		var periodic atomic.Int32
		run := func(ctx context.Context, argv, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
			var reply any
			switch cmd := strings.Join(argv[8:], " "); {
			case cmd == "health":
				if time.Now().After(started) {
					if periodic.Add(1) == 1 {
						// Longer than a route interval, but inside the probe deadline.
						timer := time.NewTimer(45 * time.Second)
						defer timer.Stop()
						select {
						case <-ctx.Done():
							return nil, nil, 1, ctx.Err()
						case <-timer.C:
						}
					}
				}
				reply = kata.Health{SchemaVersion: 33}
			case cmd == "cron capabilities":
				reply = kata.Capabilities{ProjectUID: "project", EventFeatures: []string{"cron_v1"}}
			case cmd == "federation identity":
				reply = map[string]any{"instance_uid": "instance", "auth": map[string]string{"actor": "worker"}}
			case cmd == "daemon diagnose --expect-project-uid project":
				reply = kata.Diagnosis{State: "ready", Endpoint: "local", ProjectUID: "project"}
			case cmd == "cron job list":
				// A lane with jobs keeps the 30-45 second cadence.
				reply = map[string]any{"jobs": []workerJobEnvelope{manualJob()}}
			case cmd == "cron workflow list":
				reply = map[string]any{"workflows": []kata.Workflow{}}
			default:
				return nil, nil, 1, fmt.Errorf("unexpected call: %s", cmd)
			}
			b, err := json.Marshal(reply)
			return b, nil, 0, err
		}
		tail := func(ctx context.Context, _, _ []string, _ string) (kata.Proc, error) {
			r, w := io.Pipe()
			done := make(chan struct{})
			go func() { <-ctx.Done(); _ = w.Close(); close(done) }()
			return kata.Proc{Stdout: r, Stderr: strings.NewReader(""), Wait: func() error { <-done; return nil }}, nil
		}
		k := kata.New(cfg.Kata, l.Root, run, tail)
		guard := kata.NewRouteGuard(cfg, k, state.OSFS{}, l)
		if err := guard.Initialize(t.Context()); err != nil {
			t.Fatal(err)
		}
		manager := runner.NewManager(cfg, k, nil, runner.NewStore(state.OSFS{}, root), nil, guard, nil)
		manager.EnableProjects()
		manager, err := manager.RegisterProject("project", cfg, k, guard, l)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- runLane(ctx, cfg, l, k, guard, manager, nil, nil) }()
		defer func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}()
		synctest.Wait()
		// The first periodic check starts at 30–45s and takes 45s. Even with
		// the earliest delay, its next interval must begin after completion.
		time.Sleep(91 * time.Second)
		synctest.Wait()
		var first, second kata.RouteStatus
		if err := state.ReadJSON(state.OSFS{}, l.RouteFile(), 4096, &first); err != nil || !first.Allowed || periodic.Load() != 1 {
			t.Fatalf("route timer did not wait for completion: checks=%d route=%+v err=%v", periodic.Load(), first, err)
		}
		time.Sleep(time.Until(first.CheckedAt.Add(46 * time.Second)))
		synctest.Wait()
		if err := state.ReadJSON(state.OSFS{}, l.RouteFile(), 4096, &second); err != nil || !second.Allowed || periodic.Load() != 2 || !second.CheckedAt.After(first.CheckedAt) {
			t.Fatalf("route timer did not rearm: checks=%d route=%+v err=%v", periodic.Load(), second, err)
		}
	})
}

func (f routeSnapshotFS) SyncDir(path string) error {
	if err := f.FS.SyncDir(path); err != nil {
		return err
	}
	if path == f.root {
		f.saved <- struct{}{}
	}
	return nil
}

func TestLaneRechecksRouteWhileEventStreamStaysOpen(t *testing.T) {
	f := workerFixtureFor(t)
	if err := os.Remove(f.l.Project("project").JobCheckpoint(f.job.UID)); err != nil {
		t.Fatal(err)
	}
	fsys := routeSnapshotFS{FS: state.OSFS{}, root: f.l.Project("project").Root, saved: make(chan struct{}, 8)}
	guard := kata.NewRouteGuard(f.cfg, f.k, fsys, f.l.Project("project"))
	store := runner.NewStore(state.OSFS{}, f.l)
	manager := runner.NewManager(f.cfg, f.k, &herdr.Client{Path: herdr.SocketPath(f.cfg.Session)}, store, nil, guard, nil)
	manager.EnableProjects()
	manager, err := manager.RegisterProject("project", f.cfg, f.k, guard, f.l.Project("project"))
	if err != nil {
		t.Fatal(err)
	}
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runLane(ctx, f.cfg, f.l.Project("project"), f.k, guard, manager, ticks, nil) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	// The recovery wrapper's check must finish before the route changes;
	// recovery and scheduler startup reuse it.
	select {
	case <-fsys.saved:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	f.redirected.Store(true)
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case ticks <- time.Now():
	case <-timer.C:
		t.Fatal("lane ignored its route health tick while the event stream stayed open")
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	select {
	case <-fsys.saved:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	status, err := ReadStatus(f.l)
	if err != nil || status.Route == nil || status.Route.Allowed || status.Route.Error == "" || !f.tail.Load() {
		t.Fatalf("stale route during a live stream: %+v, %v", status.Route, err)
	}
}

// cadenceLane runs one lane against a fake hub that counts route checks per
// simulated minute. Its job list and event stream can change while it runs.
type cadenceLane struct {
	started time.Time
	mu      sync.Mutex
	checks  []time.Duration // since started
	jobs    atomic.Pointer[[]workerJobEnvelope]
	events  chan string
}

// between counts route checks from one elapsed time up to another.
func (c *cadenceLane) between(from, to time.Duration) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, at := range c.checks {
		if at >= from && at < to {
			n++
		}
	}
	return n
}

// startCadenceLane must run inside a synctest bubble. The lane starts with
// jobs; prepare runs after the guard pins the route and before the lane
// starts; stop ends the lane.
func startCadenceLane(t *testing.T, jobs []workerJobEnvelope, prepare func(*kata.RouteGuard)) (lane *cadenceLane, stop func()) {
	t.Helper()
	root := state.Layout{Root: t.TempDir()}
	if err := state.InitializeLayout(state.OSFS{}, root); err != nil {
		t.Fatal(err)
	}
	l := root.Project("project")
	cfg := config.Config{Executor: "worker", Kata: config.Kata{Bin: "/bin/kata", Project: "example-project", Actor: "worker", Env: map[string]string{"KATA_HOME": t.TempDir()}}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}
	c := &cadenceLane{started: time.Now(), events: make(chan string, 1)}
	c.jobs.Store(&jobs)
	run := func(_ context.Context, argv, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
		var reply any
		switch cmd := strings.Join(argv[8:], " "); {
		case cmd == "health":
			c.mu.Lock()
			c.checks = append(c.checks, time.Since(c.started))
			c.mu.Unlock()
			reply = kata.Health{SchemaVersion: 33}
		case cmd == "cron capabilities":
			reply = kata.Capabilities{ProjectUID: "project", EventFeatures: []string{"cron_v1"}}
		case cmd == "federation identity":
			reply = map[string]any{"instance_uid": "instance", "auth": map[string]string{"actor": "worker"}}
		case cmd == "daemon diagnose --expect-project-uid project":
			reply = kata.Diagnosis{State: "ready", Endpoint: "local", ProjectUID: "project"}
		case cmd == "cron job list":
			reply = map[string]any{"jobs": *c.jobs.Load()}
		case cmd == "cron workflow list":
			reply = map[string]any{"workflows": []kata.Workflow{}}
		default:
			t.Errorf("unexpected call: %s", cmd)
			return nil, nil, 1, fmt.Errorf("unexpected call: %s", cmd)
		}
		b, err := json.Marshal(reply)
		return b, nil, 0, err
	}
	tail := func(ctx context.Context, _, _ []string, _ string) (kata.Proc, error) {
		r, w := io.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer w.Close()
			for {
				select {
				case <-ctx.Done():
					return
				case line := <-c.events:
					if _, err := io.WriteString(w, line+"\n"); err != nil {
						return
					}
				}
			}
		}()
		return kata.Proc{Stdout: r, Stderr: strings.NewReader(""), Wait: func() error { <-done; return nil }}, nil
	}
	k := kata.New(cfg.Kata, l.Root, run, tail)
	guard := kata.NewRouteGuard(cfg, k, state.OSFS{}, l)
	if err := guard.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	if prepare != nil {
		prepare(guard)
	}
	manager := runner.NewManager(cfg, k, nil, runner.NewStore(state.OSFS{}, root), nil, guard, nil)
	manager.EnableProjects()
	manager, err := manager.RegisterProject("project", cfg, k, guard, l)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runLane(ctx, cfg, l, k, guard, manager, nil, nil) }()
	return c, func() {
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func manualJob() workerJobEnvelope {
	return workerJobEnvelope{Author: "worker", UID: "01K00000000000000000000001", Name: "inspect", DefinitionEventUID: "event", Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Inspect the task"},"issue":{"kind":"per-run","title":"Inspect"},"overlap":"forbid","catchup":"skip"}`)}
}

// A project with no job definitions has nothing to admit, so its lane checks
// the route at the slow cap. Its event stream stays open, and the first job
// returns the lane to the 30-45 second cadence at once.
func TestIdleLaneChecksRouteSlowlyUntilFirstJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, stop := startCadenceLane(t, []workerJobEnvelope{}, nil)
		defer stop()
		time.Sleep(10*time.Minute + time.Second)
		// At most one check per 2.5 minutes after the startup checks.
		if checks := c.between(time.Minute, 10*time.Minute); checks > 4 {
			t.Fatalf("idle lane made %d route checks in nine minutes, want at most 4", checks)
		}
		// Past the ten-minute fallback check, deliver the first job just
		// after a timed check, when the next is at least 2.5 minutes away.
		for seen := c.between(10*time.Minute+time.Second, time.Hour); c.between(10*time.Minute+time.Second, time.Hour) == seen; {
			time.Sleep(time.Second)
		}
		time.Sleep(time.Second)
		arrived := time.Since(c.started)
		c.jobs.Store(&[]workerJobEnvelope{manualJob()})
		c.events <- `{"event_id":1,"type":"cron.job.created"}`
		time.Sleep(46 * time.Second)
		if c.between(arrived, time.Hour) == 0 {
			t.Fatal("lane with its first job kept waiting at the idle cap")
		}
	})
}
