package kata

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type outageTail struct{ fail chan struct{} }

func (k outageTail) Tail(ctx context.Context, _ int64) (<-chan TailItem, <-chan error) {
	items, errs := make(chan TailItem), make(chan error, 1)
	go func() {
		defer close(errs)
		select {
		case <-k.fail:
			close(items)
			errs <- errors.New("daemon unavailable")
		case <-ctx.Done():
			close(items)
		}
	}()
	return items, errs
}
func TestEventFailureRefreshesCachedRoute(t *testing.T) {
	k, c, l := fixture(t)
	g := NewRouteGuard(c, k, state.OSFS{}, l)
	if err := g.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	tail := outageTail{fail: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	started := make(chan struct{}, 1)
	go func() {
		done <- RunEvents(ctx, tail, l, EventHooks{OnReset: func() {
			_ = g.Check(ctx)
			select {
			case started <- struct{}{}:
			default:
			}
		}})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	<-started
	k.mu.Lock()
	k.diagnosis = "daemon_unavailable"
	k.mu.Unlock()
	close(tail.fail)
	<-started
	var route map[string]any
	if err := state.ReadJSON(state.OSFS{}, filepath.Join(l.Root, "route.json"), 4096, &route); err != nil {
		t.Fatal(err)
	}
	detail, _ := route["error"].(string)
	if g.Allowed() || route["allowed"] != false || detail == "" {
		t.Fatalf("cached route still allowed during outage: %+v", route)
	}
}
