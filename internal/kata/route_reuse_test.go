package kata

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type countedRoute struct {
	*accessRoute
	health atomic.Int32
}

func (r *countedRoute) Health(ctx context.Context) (Health, error) {
	r.health.Add(1)
	return r.accessRoute.Health(ctx)
}

// A lane's start reuses one passing route check: initializing from the
// identity it just resolved and rechecking within the reuse window call Kata
// no further. Refusal or an expired window makes the next check call Kata.
func TestStartupReusesOnlyARecentPassingRouteCheck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, c, l := fixture(t)
		k := &countedRoute{accessRoute: &accessRoute{fakeKata: f}}
		i, r, err := ResolveIdentity(t.Context(), k)
		if err != nil {
			t.Fatal(err)
		}
		g := NewRouteGuard(c, k, state.OSFS{}, l, i.ProjectUID)
		if err := g.InitializeResolved(t.Context(), i, r); err != nil || !g.Allowed() {
			t.Fatal("resolved identity did not initialize the lane", err)
		}
		if err := g.CheckFresh(t.Context(), StartupRouteReuse); err != nil {
			t.Fatal(err)
		}
		if caps := g.CheckedCapabilities(); caps.ProjectUID != "project" || len(caps.EventFeatures) != 1 {
			t.Fatalf("checked capabilities = %+v", caps)
		}
		if n := k.health.Load(); n != 1 {
			t.Fatalf("start called health %d times, want 1", n)
		}

		k.refused = true
		if err := g.ProbeAccess(t.Context()); err == nil {
			t.Fatal("refused access probe passed")
		}
		if err := g.CheckFresh(t.Context(), StartupRouteReuse); err == nil || g.Allowed() {
			t.Fatal("refused lane reused its earlier check")
		}
		if n := k.health.Load(); n != 2 {
			t.Fatalf("refused lane called health %d times, want 2", n)
		}

		k.refused = false
		if err := g.Check(t.Context()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(StartupRouteReuse)
		if err := g.CheckFresh(t.Context(), StartupRouteReuse); err != nil {
			t.Fatal(err)
		}
		if n := k.health.Load(); n != 4 {
			t.Fatalf("expired check called health %d times in total, want 4", n)
		}
	})
}

// Initializing from a resolved identity keeps the pin comparison.
func TestInitializeResolvedRefusesAChangedPin(t *testing.T) {
	f, c, l := fixture(t)
	if err := NewRouteGuard(c, f, state.OSFS{}, l).Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	i, r, err := ResolveIdentity(t.Context(), f)
	if err != nil {
		t.Fatal(err)
	}
	r.Endpoint = "http://localhost:9999"
	g := NewRouteGuard(c, f, state.OSFS{}, l)
	if err := g.InitializeResolved(t.Context(), i, r); err == nil || g.Allowed() {
		t.Fatal("changed route initialized against the old pin")
	}
}
