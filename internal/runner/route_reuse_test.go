package runner

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

// countingKata answers a healthy route and counts every Kata call by name.
type countingKata struct {
	*admissionKata
	mu    sync.Mutex
	calls map[string]int
	lost  bool
}

func (k *countingKata) hit(name string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls[name]++
}
func (k *countingKata) count(name string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.calls[name]
}
func (k *countingKata) total() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := 0
	for _, c := range k.calls {
		n += c
	}
	return n
}
func (k *countingKata) Health(context.Context) (kata.Health, error) {
	k.hit("health")
	return kata.Health{SchemaVersion: 33}, nil
}
func (k *countingKata) Capabilities(context.Context) (kata.Capabilities, error) {
	k.hit("capabilities")
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.lost {
		return kata.Capabilities{}, errors.New("access revoked")
	}
	return kata.Capabilities{ProjectUID: "project", EventFeatures: []string{"cron_v1"}}, nil
}
func (k *countingKata) FederationIdentity(context.Context) (kata.Identity, error) {
	k.hit("identity")
	return kata.Identity{InstanceUID: "instance", Actor: "worker"}, nil
}
func (k *countingKata) DaemonDiagnose(context.Context, string) (kata.Diagnosis, error) {
	k.hit("diagnose")
	return kata.Diagnosis{State: "ready", Endpoint: "local", Authoritative: true}, nil
}
func (k *countingKata) ResolveRoute() kata.Route {
	return kata.Route{Bin: "/bin/kata", Endpoint: "local", DiagnoseState: "ready"}
}

// newCountingRun admits a one-step command run with a per-run issue behind a
// real route guard, as eval slice 1 measured.
func newCountingRun(t *testing.T, clock Clock) (*admissionFixture, *countingKata, *kata.RouteGuard) {
	t.Helper()
	f := newAdmission(t)
	k := &countingKata{admissionKata: f.k, calls: map[string]int{}}
	create, observe, run := f.k.create, f.k.observe, f.k.run
	f.k.create = func(c context.Context, r kata.CreateReq) (kata.IssueView, error) {
		k.hit("create")
		return create(c, r)
	}
	f.k.observe = func(c context.Context, uid string, o kata.Observation) (kata.Run, bool, error) {
		k.hit("observe")
		return observe(c, uid, o)
	}
	f.k.show = func(context.Context, string) (kata.IssueView, error) {
		k.hit("show")
		return kata.IssueView{UID: "issue", QualifiedID: "project#task", Revision: 7, Status: "open"}, nil
	}
	f.k.run = func(c context.Context, uid string) (kata.Run, error) {
		k.hit("run show")
		return run(c, uid)
	}
	f.k.close = func(context.Context, kata.CloseReq) error {
		k.hit("close")
		return nil
	}
	f.k.comment = func(context.Context, string, string, string) error {
		k.hit("comment")
		return nil
	}
	f.k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) {
		k.hit("meta")
		return 8, nil
	}
	g := kata.NewRouteGuard(f.cfg, k, f.s.fsys, f.s.layout)
	f.q = NewQueue(k, f.s, g, clock)
	h := herdrtest.New(t)
	f.m = NewManager(f.cfg, k, &herdr.Client{Path: h.Path}, f.s, f.q, g, clock)
	f.m.environment = cleanLaunch{}
	return f, k, g
}

// settleCountingRun admits, executes and drains the run's reports.
func settleCountingRun(t *testing.T, f *admissionFixture) *Run {
	t.Helper()
	f.start(t)
	f.admit(t)
	f.m.executions.Delete(f.uid)
	if e := f.m.Execute(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for _, op := range loaded(t, f.s, f.uid).Ops {
		if e := f.q.wait(ctx, f.uid, op.ID); e != nil {
			t.Fatalf("op %s: %v", op.ID, e)
		}
	}
	r := loaded(t, f.s, f.uid)
	if r.Phase != Settled || head(r) != nil {
		t.Fatalf("run did not settle and drain: %+v", r)
	}
	return r
}

// A run's writes within the reuse window share one passing route check.
// Admission still checks the route itself, so a whole run costs two route
// checks, not one per write.
func TestRunWritesReuseFreshRouteCheck(t *testing.T) {
	f, k, _ := newCountingRun(t, &testClock{at: time.Now()})
	r := settleCountingRun(t, f)
	if r.Outcome != "succeeded" {
		t.Fatalf("outcome %q", r.Outcome)
	}
	if k.count("create") != 1 || k.count("close") != 1 {
		t.Fatalf("writes: %v", k.calls)
	}
	// Before reuse, each of the run's four writes cost a scan check and a send
	// check: 9 route checks of 4 calls, 42 Kata calls in all.
	if got := k.count("health"); got != 2 {
		t.Fatalf("route checks per run = %d, want 2 (admission and the first write); calls %v", got, k.calls)
	}
	if got := k.total(); got != 14 {
		t.Fatalf("kata calls per run = %d, want 14; calls %v", got, k.calls)
	}
}

func newReuseLane(t *testing.T) (*Queue, *countingKata, *kata.RouteGuard, *testClock) {
	t.Helper()
	clock := &testClock{at: time.Now()}
	f, k, g := newCountingRun(t, clock)
	return f.q, k, g, clock
}

// The window is bounded: a check exactly RouteReuse old probes Kata again.
func TestRouteReuseWindowExpires(t *testing.T) {
	q, k, _, clock := newReuseLane(t)
	for _, step := range []struct {
		advance time.Duration
		checks  int
	}{{0, 1}, {RouteReuse - time.Millisecond, 1}, {time.Millisecond, 2}, {time.Second, 2}} {
		clock.at = clock.at.Add(step.advance)
		if e := q.checkRoute(t.Context()); e != nil {
			t.Fatal(e)
		}
		if got := k.count("health"); got != step.checks {
			t.Fatalf("after %v: %d route checks, want %d", step.advance, got, step.checks)
		}
	}
}

// A route lost to any other check or access probe fences the lane's next
// write at once, inside the window.
func TestLostRouteEndsReuse(t *testing.T) {
	for name, lose := range map[string]func(*kata.RouteGuard, context.Context) error{
		"route check":  (*kata.RouteGuard).Check,
		"access probe": (*kata.RouteGuard).ProbeAccess,
	} {
		t.Run(name, func(t *testing.T) {
			q, k, g, _ := newReuseLane(t)
			if e := q.checkRoute(t.Context()); e != nil {
				t.Fatal(e)
			}
			k.mu.Lock()
			k.lost = true
			k.mu.Unlock()
			if e := lose(g, t.Context()); e == nil {
				t.Fatal("lost route passed")
			}
			before := k.count("capabilities")
			if e := q.checkRoute(t.Context()); e == nil || !strings.Contains(e.Error(), "access_lost") {
				t.Fatalf("write after a lost route: %v", e)
			}
			if k.count("capabilities") != before+1 {
				t.Fatal("write after a lost route reused the earlier check")
			}
			// A failed check is never reused either: recovery probes again.
			k.mu.Lock()
			k.lost = false
			k.mu.Unlock()
			if e := q.checkRoute(t.Context()); e != nil || k.count("capabilities") != before+2 {
				t.Fatalf("recovery: %v after %d probes", e, k.count("capabilities")-before)
			}
		})
	}
}
