package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// Startup includes recovery, subscribing and the initial snapshot, rather
// than just identity discovery. All 25 lanes must finish on a two-lane host.
func TestWorkerBoundsTwentyFiveLaneStartups(t *testing.T) {
	checkWorkerLaneStartups(t, false)
}

func TestWorkerBoundsRetryingLaneStartups(t *testing.T) {
	checkWorkerLaneStartups(t, true)
}

func checkWorkerLaneStartups(t *testing.T, retryRecovery bool) {
	t.Helper()
	l := state.Layout{Root: t.TempDir()}
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(l.Root, "absent.sock"))
	t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	cfg := config.Config{Executor: "worker", Kata: config.Kata{Bin: "/bin/kata", Actor: "worker", Env: map[string]string{"KATA_HOME": t.TempDir()}}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}
	for i := range 25 {
		cfg.Projects = append(cfg.Projects, config.Project{Name: fmt.Sprintf("project-%02d", i), Actor: "worker"})
	}
	var mu sync.Mutex
	active, peak := 0, 0
	ready := make(chan string, 25)
	overload := make(chan struct{}, 1)
	leases := &hubLeases{}
	factory := func(view config.Config) *kata.CLI {
		started, finished, attemptActive := false, false, false
		healthCalls := 0
		run := func(ctx context.Context, argv, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
			cmd := strings.Join(argv[8:], " ")
			mu.Lock()
			first := !started
			started = true
			if !attemptActive && !finished {
				attemptActive = true
				active++
				peak = max(peak, active)
			}
			overloaded := active > 2
			if cmd == "health" {
				healthCalls++
			}
			// Refuse the first recovery check after discovery and pinning.
			refuse := retryRecovery && cmd == "health" && healthCalls == 3
			if refuse {
				attemptActive = false
				active--
			}
			mu.Unlock()
			if first {
				// Model slow startup without racing a subsecond host deadline.
				timer := time.NewTimer(time.Second)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return nil, nil, 1, ctx.Err()
				case <-timer.C:
				}
			}
			if overloaded {
				select {
				case overload <- struct{}{}:
				default:
				}
				return nil, nil, 1, context.DeadlineExceeded
			}
			if refuse {
				return []byte(`{"error":{"code":"not_found"}}`), nil, 1, nil
			}
			if out, code, ok := leases.reply(view.Kata.Project, argv[8:]); ok {
				return out, nil, code, nil
			}
			var reply any
			switch {
			case cmd == "health":
				reply = kata.Health{SchemaVersion: 33}
			case cmd == "cron capabilities":
				reply = kata.Capabilities{ProjectUID: view.Kata.Project, EventFeatures: []string{"cron_v1"}}
			case cmd == "federation identity":
				reply = map[string]any{"instance_uid": "instance", "auth": map[string]string{"actor": "worker"}}
			case strings.HasPrefix(cmd, "daemon diagnose --expect-project-uid "):
				reply = kata.Diagnosis{State: "ready", Endpoint: "local", ProjectUID: view.Kata.Project}
			case cmd == "cron job list":
				reply = map[string]any{"jobs": []kata.Job{}}
			case cmd == "cron workflow list":
				reply = map[string]any{"workflows": []kata.Workflow{}}
				mu.Lock()
				if !finished {
					finished = true
					attemptActive = false
					active--
					ready <- view.Kata.Project
				}
				mu.Unlock()
			default:
				t.Errorf("unexpected call: %s", cmd)
				return nil, nil, 1, errors.New("unexpected call")
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
		return kata.New(view.Kata, l.Root, run, tail)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	done := make(chan error, 1)
	go func() { done <- workerWithFactory(ctx, cfg, l, factory) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for range 25 {
		select {
		case <-overload:
			t.Fatal("more than two startup attempts reached Kata together")
		case project := <-ready:
			var route kata.RouteStatus
			if err := state.ReadJSON(state.OSFS{}, l.Project(project).RouteFile(), 4096, &route); err != nil || !route.Allowed {
				t.Fatalf("lane %s did not reach allowed: %+v, %v", project, route, err)
			}
		case <-ctx.Done():
			t.Fatal("25 lanes did not complete startup", ctx.Err())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if peak > 2 {
		t.Fatalf("host saw %d concurrent lane startups, can serve only two", peak)
	}
}

func TestPeriodicRouteChecksSpreadLaneWakes(t *testing.T) {
	delays := make(map[time.Duration]bool)
	for range 25 {
		delay := routeCheckDelay()
		if delay < 30*time.Second || delay > 45*time.Second {
			t.Fatalf("route check outside the documented cadence: %s", delay)
		}
		delays[delay] = true
	}
	if len(delays) < 2 {
		t.Fatal("lanes wake together for every periodic route check")
	}
}

func TestRefusedRecoveryReleasesLaneStartupSlot(t *testing.T) {
	f := workerFixtureFor(t)
	k := kata.New(f.cfg.Kata, f.l.Root, func(_ context.Context, argv, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
		switch cmd := strings.Join(argv[8:], " "); cmd {
		case "health":
			return []byte(`{"schema_version":33}`), nil, 0, nil
		case "cron capabilities":
			return []byte(`{"error":{"code":"not_found"}}`), nil, 1, nil
		default:
			t.Errorf("unexpected call: %s", cmd)
			return nil, nil, 1, errors.New("unexpected call")
		}
	}, nil)
	guard := kata.NewRouteGuard(f.cfg, k, state.OSFS{}, f.l.Project("project"))
	manager := runner.NewManager(f.cfg, k, nil, runner.NewStore(state.OSFS{}, f.l), nil, guard, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	startup := &laneStartup{slots: make(chan struct{}, 1)}
	if err := startup.acquire(t.Context()); err != nil {
		t.Fatal(err)
	}
	go func() {
		done <- runLane(ctx, f.cfg, f.l.Project("project"), k, guard, manager, nil, startup)
	}()
	defer func() { cancel(); <-done }()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case startup.slots <- struct{}{}:
		<-startup.slots
	case <-timer.C:
		t.Fatal("refused recovery kept its startup slot, blocking other lanes")
	}
	var status kata.RouteStatus
	if err := state.ReadJSON(state.OSFS{}, f.l.Project("project").RouteFile(), 4096, &status); err != nil || status.Allowed || !status.AccessLost {
		t.Fatalf("recovery refusal did not fence lane: %+v, %v", status, err)
	}
}
