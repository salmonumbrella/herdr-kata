package kata

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type RouteReader interface {
	Health(context.Context) (Health, error)
	Capabilities(context.Context) (Capabilities, error)
	FederationIdentity(context.Context) (Identity, error)
	DaemonDiagnose(context.Context, string) (Diagnosis, error)
	ResolveRoute() Route
}
type Installation struct {
	Version     int       `json:"version"`
	Name        string    `json:"name,omitempty"`
	CreatedAt   time.Time `json:"state_created_at"`
	Executor    string    `json:"executor"`
	InstanceUID string    `json:"instance_uid"`
	ProjectUID  string    `json:"project_uid"`
	Actor       string    `json:"actor"`
	Route       Route     `json:"route"`
}

const RouteCheckInterval = 30 * time.Second

// StartupRouteReuse bounds how long a lane's start reuses one passing route
// check for its recovery, inbox and scheduler steps.
const StartupRouteReuse = 5 * time.Second

// RouteStatus records the identity observed by the latest completed check.
type RouteStatus struct {
	Project        string    `json:"project,omitempty"`
	ProjectUID     string    `json:"project_uid,omitempty"`
	InstanceUID    string    `json:"instance_uid,omitempty"`
	Actor          string    `json:"actor,omitempty"`
	CheckedAt      time.Time `json:"checked_at,omitzero"`
	AccessLost     bool      `json:"access_lost,omitempty"`
	SigningBlocked bool      `json:"signing_blocked,omitempty"`
	Allowed        bool      `json:"allowed"`
	Error          string    `json:"error,omitempty"`
}
type RouteGuard struct {
	projectUID  string
	mu          sync.Mutex
	cfg         config.Config
	k           RouteReader
	fs          state.FS
	l           state.Layout
	allowed     bool
	routeHeld   bool         // the latest check passed and no probe failed since
	checkedAt   time.Time    // the latest passing check
	checkedCaps Capabilities // the capabilities that check matched
}

func NewRouteGuard(cfg config.Config, k RouteReader, fs state.FS, l state.Layout, projectUID ...string) *RouteGuard {
	g := &RouteGuard{cfg: cfg, k: k, fs: fs, l: l}
	if len(projectUID) > 0 {
		g.projectUID = projectUID[0]
	}
	return g
}
func ResolveIdentity(ctx context.Context, k RouteReader) (Identity, Route, error) {
	type resolved struct {
		identity Identity
		route    Route
	}
	v, err := routeProbe(ctx, func(ctx context.Context) (resolved, error) {
		i, r, err := resolveIdentity(ctx, k)
		return resolved{i, r}, err
	})
	return v.identity, v.route, err
}

func resolveIdentity(ctx context.Context, k RouteReader) (Identity, Route, error) {
	h, err := k.Health(ctx)
	if err != nil {
		return Identity{}, Route{}, err
	}
	if h.SchemaVersion < 33 {
		return Identity{}, Route{}, fmt.Errorf("kata %s schema %d: schema >= 33 and a build containing #493 required", h.Version, h.SchemaVersion)
	}
	c, err := k.Capabilities(ctx)
	if err != nil {
		return Identity{}, Route{}, fmt.Errorf("access_lost: %w", err)
	}
	if c.ProjectUID == "" || !slices.Contains(c.EventFeatures, "cron_v1") {
		return Identity{}, Route{}, fmt.Errorf("cron_v1 and project UID required")
	}
	i, err := k.FederationIdentity(ctx)
	if err != nil {
		return i, Route{}, err
	}
	i.ProjectUID = c.ProjectUID
	i.Features = c.EventFeatures
	if i.InstanceUID == "" || i.Actor == "" {
		return i, Route{}, fmt.Errorf("instance UID and authenticated actor required")
	}
	d, err := k.DaemonDiagnose(ctx, c.ProjectUID)
	if err != nil {
		return i, Route{}, err
	}
	if (d.State != "ready" && d.State != "project_unverifiable") || d.Endpoint == "" {
		return i, Route{}, fmt.Errorf("kata diagnosis is not ready: %s", d.State)
	}
	if d.ProjectUID != "" && d.ProjectUID != c.ProjectUID {
		return i, Route{}, fmt.Errorf("diagnosed project UID differs from capabilities")
	}
	if d.RootSigning != nil && slices.Contains(d.RootSigning.Projects, c.ProjectUID) {
		return i, k.ResolveRoute(), fmt.Errorf("signing_blocked: project %s; root owner must run kata federation root-key repin --project <name>", c.ProjectUID)
	}
	return i, k.ResolveRoute(), nil
}
func ComparePin(s Installation, i Identity, r Route, executor string) error {
	if s.Version != 1 || s.CreatedAt.IsZero() {
		return fmt.Errorf("invalid installation state")
	}
	if s.InstanceUID != i.InstanceUID || s.ProjectUID != i.ProjectUID || s.Actor != i.Actor || s.Executor != executor || !reflect.DeepEqual(s.Route, r) {
		return fmt.Errorf("pinned Kata identity, route or executor differs: %s; repair: run herdr-kata stop and preserve the complete state directory; restore the differing fields to their pinned settings, then run herdr-kata doctor --json before herdr-kata up; for an intentional new target, resolve existing work against its original target, then use a separate state directory and unique executor; see docs/operations/runtime.md#4-resolve-a-route-mismatch-deliberately", pinDifferences(s, i, r, executor))
	}
	return nil
}

func pinDifferences(s Installation, i Identity, r Route, executor string) string {
	var fields []string
	add := func(name, pinned, current string) {
		if pinned != current {
			fields = append(fields, fmt.Sprintf("%s (pinned=%q, current=%q)", name, pinned, current))
		}
	}
	add("executor", s.Executor, executor)
	add("actor", s.Actor, i.Actor)
	add("instance UID", s.InstanceUID, i.InstanceUID)
	add("project UID", s.ProjectUID, i.ProjectUID)
	add("kata.bin", s.Route.Bin, r.Bin)
	add("daemon", s.Route.Daemon, r.Daemon)
	add("daemon diagnosis", s.Route.DiagnoseState, r.DiagnoseState)
	if s.Route.Endpoint != r.Endpoint {
		// Diagnosis can echo kata.env routing values. Do not expose them here.
		fields = append(fields, "daemon endpoint (values withheld)")
	}
	keys := make([]string, 0, len(s.Route.Env)+len(r.Env))
	for key := range s.Route.Env {
		keys = append(keys, key)
	}
	for key := range r.Env {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range slices.Compact(keys) {
		pinned, wasSet := s.Route.Env[key]
		current, isSet := r.Env[key]
		if wasSet != isSet || pinned != current {
			fields = append(fields, "kata.env."+key+" (values withheld)")
		}
	}
	if len(keys) == 0 && (s.Route.Env == nil) != (r.Env == nil) {
		fields = append(fields, fmt.Sprintf("kata.env (pinned present=%t, current present=%t)", s.Route.Env != nil, r.Env != nil))
	}
	detail := strings.Join(fields, "; ")
	// Bound the persisted error, including JSON escaping, below the status limit.
	if len(detail) > 4096 {
		detail = strings.ToValidUTF8(detail[:4096], "") + "... (further differences omitted; compare config.toml and the saved pin)"
	}
	return detail
}

// Initialize is called only by the worker holding worker.lock. Inspection never pins.
func (g *RouteGuard) Initialize(ctx context.Context) error { return g.check(ctx, true, true, nil) }
func (g *RouteGuard) Check(ctx context.Context) error      { return g.check(ctx, false, true, nil) }

// InitializeResolved pins the lane from the identity and route the worker
// resolved moments ago to choose it, without resolving them again.
func (g *RouteGuard) InitializeResolved(ctx context.Context, i Identity, r Route) error {
	return g.check(ctx, true, true, &resolvedRoute{i, r})
}

// CheckFresh reuses the latest passing check while it is younger than maxAge
// and the lane is still allowed. Any refusal clears the lane, so the next
// call checks the live route.
func (g *RouteGuard) CheckFresh(ctx context.Context, maxAge time.Duration) error {
	if err := g.lock(ctx); err != nil {
		return err
	}
	fresh := g.allowed && !g.checkedAt.IsZero() && time.Since(g.checkedAt) < maxAge
	g.mu.Unlock()
	if fresh {
		return nil
	}
	return g.Check(ctx)
}

// CheckedCapabilities returns the capabilities of the latest passing check,
// whose project UID matched the pin.
func (g *RouteGuard) CheckedCapabilities() Capabilities {
	g.mu.Lock()
	defer g.mu.Unlock()
	return Capabilities{ProjectUID: g.checkedCaps.ProjectUID, EventFeatures: slices.Clone(g.checkedCaps.EventFeatures)}
}

type resolvedRoute struct {
	identity Identity
	route    Route
}

// CheckReadOnly validates the live route without publishing inspection evidence.
func (g *RouteGuard) CheckReadOnly(ctx context.Context) error { return g.check(ctx, false, false, nil) }
func (g *RouteGuard) check(ctx context.Context, initialize, publish bool, resolved *resolvedRoute) (err error) {
	if err = g.lock(ctx); err != nil {
		return err
	}
	defer g.mu.Unlock()
	var observed Identity
	defer func() {
		if !publish {
			return
		}
		detail := ""
		if err != nil {
			detail = strings.ReplaceAll(err.Error(), "<name>", g.cfg.Kata.Project)
		}
		saveErr := state.WriteJSON(g.fs, g.l.RouteFile(), RouteStatus{Project: g.cfg.Kata.Project, ProjectUID: observed.ProjectUID, InstanceUID: observed.InstanceUID, Actor: observed.Actor, CheckedAt: time.Now().UTC(), Allowed: err == nil, Error: detail, AccessLost: strings.Contains(detail, "access_lost"), SigningBlocked: strings.Contains(detail, "signing_blocked")})
		err = errors.Join(err, saveErr)
		if err != nil {
			g.allowed = false
		}
		g.routeHeld = err == nil
		if g.allowed {
			g.checkedAt = time.Now()
			g.checkedCaps = Capabilities{ProjectUID: observed.ProjectUID, EventFeatures: observed.Features}
		}
	}()
	g.allowed, g.routeHeld = false, false
	var i Identity
	var r Route
	if resolved != nil {
		i, r = resolved.identity, resolved.route
	} else {
		i, r, err = ResolveIdentity(ctx, g.k)
	}
	observed = i
	if err != nil {
		return err
	}
	if g.projectUID != "" && i.ProjectUID != g.projectUID {
		return fmt.Errorf("project UID differs from lane")
	}
	if i.Actor != g.cfg.Kata.Actor {
		return fmt.Errorf("authenticated actor differs from configured actor")
	}
	var s Installation
	err = state.ReadJSON(g.fs, g.l.StateFile(), 64<<10, &s)
	if errors.Is(err, fs.ErrNotExist) && initialize {
		s = Installation{Version: 1, Name: g.cfg.Kata.Project, CreatedAt: time.Now().UTC(), Executor: g.cfg.Executor, InstanceUID: i.InstanceUID, ProjectUID: i.ProjectUID, Actor: i.Actor, Route: r}
		err = state.WriteJSON(g.fs, g.l.StateFile(), s)
	} else if err == nil {
		err = ComparePin(s, i, r, g.cfg.Executor)
		if err == nil && initialize && s.Name != g.cfg.Kata.Project {
			s.Name = g.cfg.Kata.Project
			err = state.WriteJSON(g.fs, g.l.StateFile(), s)
		}
	}
	if err == nil {
		g.allowed = true
	}
	return err
}
func (g *RouteGuard) Allowed() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.allowed }

// RouteHeld reports that the latest route check passed and no probe has
// failed since. Writes may reuse that check.
func (g *RouteGuard) RouteHeld() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.routeHeld }

// A control deadline also bounds waiting for an in-flight lane probe.
func (g *RouteGuard) lock(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if g.mu.TryLock() {
			return nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (g *RouteGuard) ProbeAccess(ctx context.Context) error {
	_, err := g.ProbeCapabilities(ctx)
	return err
}

// ProbeCapabilities returns only the capabilities whose UID matched the pin.
func (g *RouteGuard) ProbeCapabilities(ctx context.Context) (Capabilities, error) {
	if err := g.lock(ctx); err != nil {
		return Capabilities{}, err
	}
	defer g.mu.Unlock()
	var caps Capabilities
	var pin Installation
	err := state.ReadJSON(g.fs, g.l.StateFile(), 64<<10, &pin)
	if err == nil {
		caps, err = routeProbe(ctx, g.k.Capabilities)
		if err == nil && (pin.ProjectUID == "" || caps.ProjectUID != pin.ProjectUID) {
			err = errors.New("project differs from pin")
		}
	}
	if err != nil {
		g.allowed, g.routeHeld = false, false
		accessErr := fmt.Errorf("access_lost: %w", err)
		return caps, errors.Join(accessErr, g.saveRoute(RouteStatus{Project: g.cfg.Kata.Project, Allowed: false, Error: accessErr.Error(), AccessLost: true}))
	}
	return caps, nil
}

// Callers hold mu. Compare the saved content so a new guard also preserves an
// unchanged file; changed or unreadable status is still published atomically.
func (g *RouteGuard) saveRoute(next RouteStatus) error {
	path := g.l.RouteFile()
	var previous RouteStatus
	if err := state.ReadJSON(g.fs, path, 4096, &previous); err == nil && previous == next {
		return nil
	}
	return state.WriteJSON(g.fs, path, next)
}
