package kata

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type routePublicationFS struct {
	state.OSFS
	path string
	fail bool
}

func (f *routePublicationFS) Rename(a, b string) error {
	if b == f.path && f.fail {
		return errors.New("route publication failed")
	}
	return f.OSFS.Rename(a, b)
}

func TestRouteCheckPublishesFreshEvidence(t *testing.T) {
	f, c, l := fixture(t)
	fsys := &routePublicationFS{path: filepath.Join(l.Root, "route.json")}
	g := NewRouteGuard(c, f, fsys, l)
	if err := g.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(fsys.path)
	if err != nil {
		t.Fatal(err)
	}
	fsys.fail = true
	if err := g.Check(t.Context()); err == nil || !strings.Contains(err.Error(), "route publication failed") || g.Allowed() {
		t.Fatal("unpublished completion evidence permitted access", err)
	}
	after, err := os.Stat(fsys.path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("failed evidence publication damaged the saved snapshot", err)
	}
	fsys.fail = false
	completedAfter := time.Now().UTC()
	if err := g.Check(t.Context()); err != nil || !g.Allowed() {
		t.Fatal("fresh route check failed", err)
	}
	after, err = os.Stat(fsys.path)
	if err != nil || os.SameFile(before, after) {
		t.Fatal("new completion evidence was not atomically published", err)
	}
	var evidence RouteStatus
	if err := state.ReadJSON(state.OSFS{}, fsys.path, 4096, &evidence); err != nil || evidence.CheckedAt.Before(completedAfter) || evidence.ProjectUID != "project" || evidence.InstanceUID != "instance" || evidence.Actor != "actor" {
		t.Fatal("fresh route check lost recorded evidence", evidence, err)
	}
	before = after
	fsys.fail = true
	f.endpoint = "https://hub.example"
	if err := g.Check(t.Context()); err == nil || g.Allowed() {
		t.Fatal("changed route publication failure did not refuse access", err)
	}
	var saved map[string]any
	if err := state.ReadJSON(state.OSFS{}, fsys.path, 4096, &saved); err != nil || saved["allowed"] != true {
		t.Fatal("failed atomic publication damaged old route", saved, err)
	}
	fsys.fail = false
	if err := g.Check(t.Context()); err == nil || g.Allowed() {
		t.Fatal("route mismatch permitted", err)
	}
	after, err = os.Stat(fsys.path)
	if err != nil || os.SameFile(before, after) {
		t.Fatal("changed route was not atomically replaced", err)
	}
}

type unavailableCapabilities struct {
	RouteReader
	unavailable bool
}

func (r *unavailableCapabilities) Capabilities(ctx context.Context) (Capabilities, error) {
	if r.unavailable {
		return Capabilities{}, errors.New("offline")
	}
	return r.RouteReader.Capabilities(ctx)
}

func TestRouteProbePublishesOnlyChanges(t *testing.T) {
	f, c, l := fixture(t)
	c.Kata.Project = "example-project"
	r := &unavailableCapabilities{RouteReader: f}
	fsys := &routePublicationFS{path: filepath.Join(l.Root, "route.json")}
	g := NewRouteGuard(c, r, fsys, l)
	if err := g.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.unavailable = true
	if err := g.ProbeAccess(t.Context()); err == nil || g.Allowed() {
		t.Fatal("capability loss permitted", err)
	}
	var refusal RouteStatus
	if err := state.ReadJSON(state.OSFS{}, fsys.path, 4096, &refusal); err != nil || refusal.Allowed || !refusal.AccessLost || refusal.Error != "access_lost: offline" {
		t.Fatal("capability loss did not publish its refusal", refusal, err)
	}
	var wire map[string]any
	if err := state.ReadJSON(state.OSFS{}, fsys.path, 4096, &wire); err != nil || len(wire) != 4 || wire["project"] != "example-project" || wire["allowed"] != false || wire["error"] != "access_lost: offline" || wire["access_lost"] != true {
		t.Fatal("probe refusal changed its published fields", wire, err)
	}
	before, err := os.Stat(fsys.path)
	if err != nil {
		t.Fatal(err)
	}
	fsys.fail = true
	for _, probe := range []func(context.Context) error{g.ProbeAccess, NewRouteGuard(c, r, fsys, l).ProbeAccess} {
		err = probe(t.Context())
		if err == nil || !strings.Contains(err.Error(), "access_lost: offline") || strings.Contains(err.Error(), "route publication failed") || g.Allowed() {
			t.Fatal("unchanged access loss republished or permitted", err)
		}
	}
	after, err := os.Stat(fsys.path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("unchanged access loss replaced route", err)
	}
}

func TestRouteProbePreservesLegacyRefusalFile(t *testing.T) {
	f, c, l := fixture(t)
	c.Kata.Project = "example-project"
	if err := state.WriteJSON(state.OSFS{}, l.StateFile(), Installation{Version: 1, ProjectUID: "project"}); err != nil {
		t.Fatal(err)
	}
	// The previous map writer sorted its keys alphabetically.
	legacy := `{"access_lost":true,"allowed":false,"error":"access_lost: offline","project":"example-project"}`
	if err := os.WriteFile(l.RouteFile(), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(l.RouteFile())
	if err != nil {
		t.Fatal(err)
	}
	r := &unavailableCapabilities{RouteReader: f, unavailable: true}
	g := NewRouteGuard(c, r, state.OSFS{}, l)
	if err := g.ProbeAccess(t.Context()); err == nil || err.Error() != "access_lost: offline" || g.Allowed() {
		t.Fatal("legacy refusal changed probe access", err)
	}
	after, err := os.Stat(l.RouteFile())
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("equivalent legacy refusal was replaced", err)
	}
}

func TestRouteProbeRefusesInvalidPin(t *testing.T) {
	for _, tc := range []struct {
		name string
		pin  map[string]any
	}{
		{name: "missing project UID", pin: map[string]any{"version": 1}},
		{name: "malformed executor", pin: map[string]any{"version": 1, "project_uid": "project", "executor": 7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, c, l := fixture(t)
			g := NewRouteGuard(c, f, state.OSFS{}, l)
			if err := g.Initialize(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := state.WriteJSON(state.OSFS{}, l.StateFile(), tc.pin); err != nil {
				t.Fatal(err)
			}
			if err := g.ProbeAccess(t.Context()); err == nil || !strings.Contains(err.Error(), "access_lost:") || g.Allowed() {
				t.Fatal("invalid pin permitted probe access", err)
			}
			var refusal RouteStatus
			if err := state.ReadJSON(state.OSFS{}, l.RouteFile(), 4096, &refusal); err != nil || refusal.Allowed || !refusal.AccessLost {
				t.Fatal("invalid pin did not publish access loss", refusal, err)
			}
		})
	}
}

func TestRouteCheckPublishesEvidenceAfterProbe(t *testing.T) {
	f, c, l := fixture(t)
	r := &unavailableCapabilities{RouteReader: f}
	fsys := &routePublicationFS{path: filepath.Join(l.Root, "route.json")}
	g := NewRouteGuard(c, r, fsys, l)
	if err := g.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.unavailable = true
	if err := g.ProbeAccess(t.Context()); err == nil {
		t.Fatal("access loss permitted")
	}
	before, err := os.Stat(fsys.path)
	if err != nil {
		t.Fatal(err)
	}
	fsys.fail = true
	if err := g.Check(t.Context()); err == nil || !strings.Contains(err.Error(), "access_lost: offline") || !strings.Contains(err.Error(), "route publication failed") || g.Allowed() {
		t.Fatal("fresh refusal evidence was not published atomically", err)
	}
	after, err := os.Stat(fsys.path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("failed publication replaced previous refusal", err)
	}
	fsys.fail = false
	completedAfter := time.Now().UTC()
	if err := g.Check(t.Context()); err == nil || !strings.Contains(err.Error(), "access_lost: offline") || g.Allowed() {
		t.Fatal("route refusal changed semantics", err)
	}
	var evidence RouteStatus
	if err := state.ReadJSON(state.OSFS{}, fsys.path, 4096, &evidence); err != nil || evidence.CheckedAt.Before(completedAfter) || !evidence.AccessLost || evidence.Allowed || evidence.Error != "access_lost: offline" {
		t.Fatal("route check lost refusal completion evidence", evidence, err)
	}
}
