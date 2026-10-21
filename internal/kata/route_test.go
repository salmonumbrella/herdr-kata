package kata

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type fakeKata struct {
	mu                  sync.Mutex
	endpoint, diagnosis string
	items               chan TailItem
}

func (f *fakeKata) Health(context.Context) (Health, error) {
	return Health{SchemaVersion: 33, Version: "test"}, nil
}
func (f *fakeKata) Capabilities(context.Context) (Capabilities, error) {
	return Capabilities{ProjectUID: "project", EventFeatures: []string{"cron_v1"}}, nil
}
func (f *fakeKata) FederationIdentity(context.Context) (Identity, error) {
	return Identity{InstanceUID: "instance", Actor: "actor"}, nil
}
func (f *fakeKata) DaemonDiagnose(context.Context, string) (Diagnosis, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Diagnosis{State: f.diagnosis, Endpoint: f.endpoint, Authoritative: f.diagnosis == "ready"}, nil
}
func (f *fakeKata) ResolveRoute() Route {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Route{Bin: "/bin/kata", Endpoint: f.endpoint, DiagnoseState: f.diagnosis, Env: map[string]string{}}
}
func (f *fakeKata) Tail(ctx context.Context, _ int64) (<-chan TailItem, <-chan error) {
	errs := make(chan error)
	go func() { <-ctx.Done(); close(f.items); close(errs) }()
	return f.items, errs
}
func fixture(t *testing.T) (*fakeKata, config.Config, state.Layout) {
	t.Helper()
	return &fakeKata{endpoint: "http://localhost:1234", diagnosis: "ready", items: make(chan TailItem, 8)}, config.Config{Executor: "test", Kata: config.Kata{Actor: "actor", Env: map[string]string{"KATA_HOME": t.TempDir()}}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}, state.Layout{Root: t.TempDir()}
}

type blockedRouteHealth struct {
	RouteReader
	once             sync.Once
	entered, release chan struct{}
}

func (r *blockedRouteHealth) Health(ctx context.Context) (Health, error) {
	r.once.Do(func() { close(r.entered); <-r.release })
	return r.RouteReader.Health(ctx)
}

func TestCanceledRouteProbeDoesNotWaitForAnotherCheck(t *testing.T) {
	f, c, l := fixture(t)
	r := &blockedRouteHealth{RouteReader: f, entered: make(chan struct{}), release: make(chan struct{})}
	g := NewRouteGuard(c, r, state.OSFS{}, l)
	first := make(chan error, 1)
	go func() { first <- g.Initialize(t.Context()) }()
	<-r.entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	results := make(chan error, 2)
	go func() { results <- g.Check(ctx) }()
	go func() { results <- g.ProbeAccess(ctx) }()
	var received []error
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
collect:
	for len(received) < 2 {
		select {
		case e := <-results:
			received = append(received, e)
		case <-timer.C:
			t.Error("canceled control waited for an unrelated route RPC")
			break collect
		}
	}
	close(r.release)
	if e := <-first; e != nil {
		t.Fatal(e)
	}
	for len(received) < 2 {
		received = append(received, <-results)
	}
	for _, e := range received {
		if !errors.Is(e, context.Canceled) {
			t.Error("canceled route call did not return cancellation", e)
		}
	}
}
func TestCheckAfterCatalogEditWithoutTimerFails(t *testing.T) {
	f, c, l := fixture(t)
	g := NewRouteGuard(c, f, state.OSFS{}, l)
	if err := g.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.endpoint = "http://localhost:5678"
	if err := g.Check(context.Background()); err == nil || g.Allowed() {
		t.Fatal("changed route permitted")
	}
}
func TestRouteChangeWithLiveTailBlocks(t *testing.T) {
	f, c, l := fixture(t)
	g := NewRouteGuard(c, f, state.OSFS{}, l)
	if err := g.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	live := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- RunEvents(ctx, f, l, EventHooks{OnIssue: func(Event) { close(live) }}) }()
	f.items <- TailItem{Event: Event{EventID: 1, Type: "issue.updated"}}
	<-live
	f.mu.Lock()
	f.endpoint = "http://localhost:5678"
	f.mu.Unlock()
	if err := g.Check(ctx); err == nil || g.Allowed() {
		t.Fatal("route changed while healthy tail permitted")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestDiagnoseExit0SelectionErrorFails(t *testing.T) {
	f, c, l := fixture(t)
	f.diagnosis = "selection_error"
	g := NewRouteGuard(c, f, state.OSFS{}, l)
	if err := g.Initialize(context.Background()); err == nil {
		t.Fatal("selection error accepted")
	}
	if _, err := os.Stat(l.StateFile()); !os.IsNotExist(err) {
		t.Fatal("invalid route pinned")
	}
}

// The configured endpoint is trusted; capabilities and the installation pin
// prove project identity even when remote diagnosis cannot see local storage.
func TestConfiguredRemoteAndSpokeRoutes(t *testing.T) {
	for _, tc := range []struct{ name, selector, diagnosis string }{
		{"remote URL", "https://hub.example", `{"state":"project_unverifiable","endpoint":"https://hub.example"}`},
		{"remote catalog", "example-hub", `{"state":"project_unverifiable","endpoint":"https://hub.example"}`},
		{"spoke", "example-spoke", `{"state":"ready","endpoint":"local","project_uid":"project-a","federation_role":"spoke"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := state.Layout{Root: t.TempDir()}
			cfg := config.Config{Executor: "worker", Kata: config.Kata{Bin: "/bin/kata", Project: "example-project", Actor: "worker", Daemon: tc.selector}}
			if tc.name == "remote URL" {
				cfg.Kata.Daemon = ""
				cfg.Kata.Env = map[string]string{"KATA_SERVER": tc.selector}
			}
			project, instance := "project-a", "instance-a"
			diagnosis := tc.diagnosis
			k := New(cfg.Kata, l.Root, func(_ context.Context, args, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
				var reply any
				switch joined := strings.Join(args, " "); {
				case strings.HasSuffix(joined, " health"):
					reply = Health{SchemaVersion: 33}
				case strings.HasSuffix(joined, " cron capabilities"):
					reply = Capabilities{ProjectUID: project, EventFeatures: []string{"cron_v1"}}
				case strings.HasSuffix(joined, " federation identity"):
					reply = map[string]any{"instance_uid": instance, "auth": map[string]string{"actor": "worker"}}
				case strings.Contains(joined, " daemon diagnose --expect-project-uid "):
					return []byte(diagnosis), nil, 0, nil
				default:
					t.Fatalf("unexpected Kata call: %v", args)
				}
				data, _ := json.Marshal(reply)
				return data, nil, 0, nil
			}, nil)
			guard := NewRouteGuard(cfg, k, state.OSFS{}, l, "project-a")
			if e := guard.Initialize(t.Context()); e != nil || !guard.Allowed() {
				t.Fatal("configured route was refused", e)
			}
			var pin Installation
			if e := state.ReadJSON(state.OSFS{}, l.StateFile(), 65536, &pin); e != nil || pin.ProjectUID != "project-a" || pin.InstanceUID != "instance-a" {
				t.Fatal("route was not pinned to capabilities and instance", pin, e)
			}
			if e := guard.Check(t.Context()); e != nil || !guard.Allowed() {
				t.Fatal("pinned configured route was refused", e)
			}
			project = "project-b"
			if e := guard.Check(t.Context()); e == nil || guard.Allowed() {
				t.Fatal("capabilities project mismatch was accepted")
			}
			project, instance = "project-a", "instance-b"
			if e := guard.Check(t.Context()); e == nil || guard.Allowed() {
				t.Fatal("instance mismatch was accepted")
			}
			instance = "instance-a"
			diagnosis = `{"state":"ready","endpoint":"local","project_uid":"project-b"}`
			if e := guard.Check(t.Context()); e == nil || guard.Allowed() {
				t.Fatal("diagnosis project mismatch was accepted")
			}
		})
	}
}
func TestCursorAfterHandling(t *testing.T) {
	f, _, l := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handled := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	// A definition event before the start snapshot is covered by it; this
	// one follows the snapshot, so its handler runs.
	started := make(chan struct{}, 1)
	go func() {
		done <- RunEvents(ctx, f, l, EventHooks{OnReset: func() { started <- struct{}{} }, OnDefs: func(Event) { close(handled); <-release }})
	}()
	<-started
	f.items <- TailItem{Event: Event{EventID: 42, Type: "cron.job.updated"}}
	<-handled
	if _, err := os.Stat(l.EventsCursor()); !os.IsNotExist(err) {
		t.Fatal("cursor advanced before handling")
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		var c Cursor
		if state.ReadJSON(state.OSFS{}, l.EventsCursor(), 1024, &c) == nil && c.AfterID == 42 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cursor not saved")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
}
func TestOwnRunObservationDoesNotRefreshDefinitions(t *testing.T) {
	f, _, l := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	issue := make(chan struct{})
	go func() {
		done <- RunEvents(ctx, f, l, EventHooks{OnDefs: func(Event) { t.Error("own observation dirtied definitions") }, OnIssue: func(Event) { close(issue) }})
	}()
	f.items <- TailItem{Event: Event{Type: "cron.run.observed", EventID: 1}}
	f.items <- TailItem{Event: Event{Type: "issue.updated", EventID: 2}}
	<-issue
	cancel()
	<-done
}
func TestResetWithoutChildExitRefreshesAll(t *testing.T) {
	f, _, l := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	calls := make(chan struct{}, 4)
	done := make(chan error, 1)
	go func() { done <- RunEvents(ctx, f, l, EventHooks{OnReset: func() { calls <- struct{}{} }}) }()
	<-calls
	f.items <- TailItem{Reset: &Reset{AfterID: 8}}
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("reset not handled")
	}
	cancel()
	<-done
}

func TestCorruptCursorDoesNotBlockSnapshots(t *testing.T) {
	f, _, l := fixture(t)
	os.WriteFile(l.EventsCursor(), []byte("{"), 0600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reset := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- RunEvents(ctx, f, l, EventHooks{OnReset: func() { close(reset) }}) }()
	select {
	case <-reset:
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	case err := <-done:
		t.Fatalf("optimization cursor blocked startup: %v", err)
	}
}

type tailFunc func(context.Context, int64) (<-chan TailItem, <-chan error)

func (f tailFunc) Tail(ctx context.Context, id int64) (<-chan TailItem, <-chan error) {
	return f(ctx, id)
}
func TestHealthyTailResetsBackoff(t *testing.T) {
	_, _, l := fixture(t)
	// A duplicate event is healthy too. Persist its cursor before measuring
	// backoff so filesystem latency is outside the scheduling window.
	if err := state.WriteJSON(state.OSFS{}, l.EventsCursor(), Cursor{AfterID: 1}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	third := make(chan struct{})
	calls := 0
	k := tailFunc(func(_ context.Context, after int64) (<-chan TailItem, <-chan error) {
		if after != 1 {
			t.Errorf("tail cursor = %d, want 1", after)
		}
		calls++
		items := make(chan TailItem, 1)
		errs := make(chan error)
		if calls == 2 {
			items <- TailItem{Event: Event{EventID: 1, Type: "issue.updated"}}
		}
		if calls == 3 {
			close(third)
		}
		close(items)
		close(errs)
		return items, errs
	})
	done := make(chan error, 1)
	go func() { done <- RunEvents(ctx, k, l, EventHooks{}) }()
	select {
	case <-third:
		cancel()
		<-done
	case <-time.After(2700 * time.Millisecond):
		t.Fatal("healthy tail retained escalated backoff")
	}
}

func TestRouteRejectsReportedActorMismatch(t *testing.T) {
	f, c, l := fixture(t)
	c.Kata.Actor = "other"
	g := NewRouteGuard(c, f, state.OSFS{}, l)
	if err := g.Initialize(t.Context()); err == nil || g.Allowed() {
		t.Fatal("reported actor mismatch passed")
	}
}

func TestEventsCancellationWaitsForTailReap(t *testing.T) {
	_, _, l := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	items := make(chan TailItem)
	errs := make(chan error)
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	defer finish()
	k := tailFunc(func(ctx context.Context, _ int64) (<-chan TailItem, <-chan error) {
		go func() { <-ctx.Done(); <-release; close(items); close(errs) }()
		return items, errs
	})
	done := make(chan error, 1)
	go func() { done <- RunEvents(ctx, k, l, EventHooks{OnReset: func() { close(started) }}) }()
	<-started
	cancel()
	select {
	case <-done:
		t.Fatal("events loop exited before its child was reaped")
	case <-time.After(20 * time.Millisecond):
	}
	finish()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("events loop did not exit after child reap")
	}
}

type healthRoute struct {
	*fakeKata
	schema  int
	signing bool
}

func (f *healthRoute) Health(context.Context) (Health, error) {
	return Health{Version: "test-build", SchemaVersion: f.schema}, nil
}
func (f *healthRoute) DaemonDiagnose(ctx context.Context, uid string) (Diagnosis, error) {
	d, e := f.fakeKata.DaemonDiagnose(ctx, uid)
	if f.signing {
		d.RootSigning = &RootSigning{Projects: []string{uid}}
	}
	return d, e
}
func TestRouteMinimumSchemaAndSigningRepair(t *testing.T) {
	f, c, l := fixture(t)
	k := &healthRoute{fakeKata: f, schema: 32}
	g := NewRouteGuard(c, k, state.OSFS{}, l)
	if err := g.Initialize(t.Context()); err == nil {
		t.Fatal("schema 32 pinned")
	}
	k.schema = 33
	if err := g.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	k.signing = true
	if err := g.Check(t.Context()); err == nil {
		t.Fatal("signing diagnosis permitted delivery")
	}
	k.signing = false
	if err := g.Check(t.Context()); err != nil {
		t.Fatal("repair requires restart", err)
	}
}

type accessRoute struct {
	*fakeKata
	refused bool
}

func (f *accessRoute) Capabilities(ctx context.Context) (Capabilities, error) {
	if f.refused {
		return Capabilities{}, &Error{Code: "not_found"}
	}
	return f.fakeKata.Capabilities(ctx)
}
func TestAccessProbeRefusesUntilPassingRouteCheck(t *testing.T) {
	f, c, l := fixture(t)
	k := &accessRoute{fakeKata: f}
	g := NewRouteGuard(c, k, state.OSFS{}, l)
	if err := g.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	k.refused = true
	if err := g.ProbeAccess(t.Context()); err == nil || g.Allowed() {
		t.Fatal("failed access probe allowed lane")
	}
	var status map[string]any
	if err := state.ReadJSON(state.OSFS{}, filepath.Join(l.Root, "route.json"), 4096, &status); err != nil || status["access_lost"] != true {
		t.Fatal("access loss not visible", status, err)
	}
	k.refused = false
	if err := g.Check(t.Context()); err != nil || !g.Allowed() {
		t.Fatal("access repair requires restart", err)
	}
}

func TestProjectGuardRefusesUIDChangeBeforeFirstPin(t *testing.T) {
	f, c, l := fixture(t)
	// The route changed between initial discovery and pinning the lane.
	lane := l.Project("expected-project")
	guard := NewRouteGuard(c, f, state.OSFS{}, lane, "expected-project")
	if e := guard.Initialize(t.Context()); e == nil {
		t.Fatal("pinned a different UID in the lane")
	}
	if _, e := os.Stat(lane.StateFile()); !os.IsNotExist(e) {
		t.Fatal("wrong project pin persisted", e)
	}
}

func TestProjectRenameUpdatesDisplayNameWithoutResettingPin(t *testing.T) {
	f, c, l := fixture(t)
	c.Kata.Project = "old-name"
	if e := NewRouteGuard(c, f, state.OSFS{}, l).Initialize(t.Context()); e != nil {
		t.Fatal(e)
	}
	var before Installation
	if e := state.ReadJSON(state.OSFS{}, l.StateFile(), 65536, &before); e != nil {
		t.Fatal(e)
	}
	c.Kata.Project = "new-name"
	if e := NewRouteGuard(c, f, state.OSFS{}, l).Initialize(t.Context()); e != nil {
		t.Fatal(e)
	}
	var after Installation
	if e := state.ReadJSON(state.OSFS{}, l.StateFile(), 65536, &after); e != nil || after.Name != "new-name" || !after.CreatedAt.Equal(before.CreatedAt) || after.ProjectUID != before.ProjectUID {
		t.Fatal(after, e)
	}
}
