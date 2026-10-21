package kata

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestRouteCheckRecordsIdentityAndFreshness(t *testing.T) {
	f, cfg, layout := fixture(t)
	cfg.Kata.Env = map[string]string{"KATA_HOME": filepath.Join(layout.Root, "kata-home")}
	cfg.Kata.Project = "example-project"
	before := time.Now().UTC()
	if err := NewRouteGuard(cfg, f, state.OSFS{}, layout).Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	var recorded struct {
		Project, Actor string
		CheckedAt      time.Time `json:"checked_at"`
		ProjectID      string    `json:"project_uid"`
		InstanceID     string    `json:"instance_uid"`
		Allowed        bool
		Error          string
		AccessLost     bool `json:"access_lost"`
		SigningBlocked bool `json:"signing_blocked"`
	}
	if err := state.ReadJSON(state.OSFS{}, filepath.Join(layout.Root, "route.json"), 65536, &recorded); err != nil {
		t.Fatal(err)
	}
	if !recorded.Allowed || recorded.Project != "example-project" || recorded.ProjectID != "project" || recorded.InstanceID != "instance" || recorded.Actor != "actor" || recorded.CheckedAt.Before(before) || recorded.CheckedAt.After(time.Now().UTC()) {
		t.Fatalf("incomplete route snapshot: %+v", recorded)
	}
}

func TestRouteCheckTimestampUsesCompletion(t *testing.T) {
	f, cfg, layout := fixture(t)
	cfg.Kata.Env = map[string]string{"KATA_HOME": filepath.Join(layout.Root, "kata-home")}
	if err := NewRouteGuard(cfg, f, state.OSFS{}, layout).Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	blocked := &blockedRouteHealth{RouteReader: f, entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- NewRouteGuard(cfg, blocked, state.OSFS{}, layout).Check(t.Context()) }()
	<-blocked.entered
	releasedAt := time.Now().UTC()
	close(blocked.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var recorded RouteStatus
	if err := state.ReadJSON(state.OSFS{}, filepath.Join(layout.Root, "route.json"), 65536, &recorded); err != nil {
		t.Fatal(err)
	}
	if !recorded.Allowed || recorded.CheckedAt.Before(releasedAt) {
		t.Fatalf("route check was stamped before completion: %+v", recorded)
	}
}
