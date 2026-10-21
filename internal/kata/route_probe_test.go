package kata

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// The host can serve two slow capabilities calls; excess concurrent calls
// time out. Removing the shared probe limit must refuse some of the 25 lanes.
func TestRouteProbesAllowTwentyFiveLanesOnLoadedHost(t *testing.T) {
	f, cfg, _ := fixture(t)
	host := &loadedRouteHost{}
	var wg sync.WaitGroup
	for range 25 {
		lane := state.Layout{Root: t.TempDir()}
		wg.Go(func() {
			reader := &slowCapabilities{RouteReader: f, host: host}
			guard := NewRouteGuard(cfg, reader, state.OSFS{}, lane)
			if err := guard.Initialize(t.Context()); err != nil || !guard.Allowed() {
				t.Errorf("lane refused: %v", err)
			}
			if err := guard.Check(t.Context()); err != nil || !guard.Allowed() {
				t.Errorf("periodic check refused lane: %v", err)
			}
		})
	}
	wg.Wait()
	if host.peak > 2 {
		t.Errorf("host saw %d concurrent probes, can serve only two", host.peak)
	}
}

type loadedRouteHost struct {
	mu           sync.Mutex
	active, peak int
}

type slowCapabilities struct {
	RouteReader
	host *loadedRouteHost
}

func (r *slowCapabilities) Capabilities(ctx context.Context) (Capabilities, error) {
	r.host.mu.Lock()
	r.host.active++
	r.host.peak = max(r.host.peak, r.host.active)
	overloaded := r.host.active > 2
	r.host.mu.Unlock()
	defer func() { r.host.mu.Lock(); r.host.active--; r.host.mu.Unlock() }()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return Capabilities{}, ctx.Err()
	case <-timer.C:
	}
	if overloaded {
		return Capabilities{}, context.DeadlineExceeded
	}
	return r.RouteReader.Capabilities(ctx)
}

type failingProbe struct {
	RouteReader
	err   error
	left  int
	calls int
}

func (r *failingProbe) Capabilities(ctx context.Context) (Capabilities, error) {
	r.calls++
	if r.left > 0 {
		r.left--
		return Capabilities{}, r.err
	}
	return r.RouteReader.Capabilities(ctx)
}

func TestRouteProbeRetriesDeadlinesBeforeFencing(t *testing.T) {
	for _, operation := range []string{"initialize", "check", "access"} {
		for _, deadline := range []error{context.DeadlineExceeded, &Error{Code: "timeout"}} {
			t.Run(operation+"/"+deadline.Error(), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					f, cfg, layout := fixture(t)
					r := &failingProbe{RouteReader: f}
					g := NewRouteGuard(cfg, r, state.OSFS{}, layout)
					if operation != "initialize" {
						if err := g.Initialize(t.Context()); err != nil {
							t.Fatal(err)
						}
					}
					r.err, r.left, r.calls = deadline, 1, 0
					before := time.Now()
					var err error
					switch operation {
					case "initialize":
						err = g.Initialize(t.Context())
					case "check":
						err = g.Check(t.Context())
					case "access":
						err = g.ProbeAccess(t.Context())
					}
					if err != nil || !g.Allowed() || r.calls != 2 {
						t.Fatalf("transient deadline fenced lane: calls=%d err=%v", r.calls, err)
					}
					if time.Since(before) < time.Second {
						t.Fatal("deadline retried without backoff")
					}
					var status RouteStatus
					if err := state.ReadJSON(state.OSFS{}, layout.RouteFile(), 4096, &status); err != nil || !status.Allowed || status.AccessLost {
						t.Fatalf("transient deadline published refusal: %+v, %v", status, err)
					}
				})
			})
		}
	}
}

func TestRouteProbeFencesDefinitiveRefusalsWithoutRetry(t *testing.T) {
	for _, code := range []string{"not_found", "unauthorized"} {
		for _, operation := range []string{"check", "access"} {
			t.Run(code+"/"+operation, func(t *testing.T) {
				f, cfg, layout := fixture(t)
				r := &failingProbe{RouteReader: f}
				g := NewRouteGuard(cfg, r, state.OSFS{}, layout)
				if err := g.Initialize(t.Context()); err != nil {
					t.Fatal(err)
				}
				r.err, r.left, r.calls = &Error{Code: code}, 1, 0
				check := g.Check
				if operation == "access" {
					check = g.ProbeAccess
				}
				if err := check(t.Context()); !errors.Is(err, r.err) || g.Allowed() || r.calls != 1 {
					t.Fatalf("refusal did not fence immediately: calls=%d err=%v", r.calls, err)
				}
				var status RouteStatus
				if err := state.ReadJSON(state.OSFS{}, layout.RouteFile(), 4096, &status); err != nil || status.Allowed || !status.AccessLost {
					t.Fatalf("refusal not published: %+v, %v", status, err)
				}
			})
		}
	}
}

type stalledHealth struct {
	RouteReader
	calls int
}

func (r *stalledHealth) Health(ctx context.Context) (Health, error) {
	r.calls++
	<-ctx.Done()
	return Health{}, ctx.Err()
}

func TestRouteProbeExhaustedDeadlinesFenceLane(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, cfg, layout := fixture(t)
		r := &stalledHealth{RouteReader: f}
		g := NewRouteGuard(cfg, r, state.OSFS{}, layout)
		before := time.Now()
		if err := g.Initialize(t.Context()); !errors.Is(err, context.DeadlineExceeded) || g.Allowed() {
			t.Fatalf("exhausted deadlines did not fence lane: %v", err)
		}
		if r.calls != 3 || time.Since(before) != 183*time.Second {
			t.Fatalf("unbounded or immediate retry: calls=%d elapsed=%s", r.calls, time.Since(before))
		}
	})
}

// Waiting lanes back off exponentially to a five-minute cap. Lanes that
// started waiting together must not re-probe together.
func TestWaitingLaneDelaysSpreadUnderCap(t *testing.T) {
	for _, tc := range []struct {
		attempt  int
		min, max time.Duration
	}{
		{0, time.Second, 2 * time.Second},
		{3, 8 * time.Second, 16 * time.Second},
		{20, 150 * time.Second, 5 * time.Minute},
		{100, 150 * time.Second, 5 * time.Minute},
	} {
		delays := make(map[time.Duration]bool)
		for range 25 {
			delay := WaitDelay(2*time.Second, tc.attempt)
			if delay < tc.min || delay > tc.max {
				t.Fatalf("attempt %d waited %s, want %s to %s", tc.attempt, delay, tc.min, tc.max)
			}
			delays[delay] = true
		}
		if len(delays) < 2 {
			t.Fatalf("attempt %d: waiting lanes wake together", tc.attempt)
		}
	}
}
