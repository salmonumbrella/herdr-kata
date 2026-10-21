package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestWorkerKeepsRenamedProjectUID(t *testing.T) {
	f := workerFixtureFor(t)
	if err := state.InitializeLayout(state.OSFS{}, f.l); err != nil {
		t.Fatal(err)
	}
	route := kata.Route{Bin: f.cfg.Kata.Bin, Env: f.cfg.Kata.Env, Endpoint: "local", DiagnoseState: "ready"}
	pin := kata.Installation{Version: 1, Name: f.cfg.Kata.Project, CreatedAt: time.Now().Add(-time.Hour), Executor: f.cfg.Executor, InstanceUID: "instance", ProjectUID: "project", Actor: "worker", Route: route}
	if err := state.WriteJSON(state.OSFS{}, f.l.Project("project").StateFile(), pin); err != nil {
		t.Fatal(err)
	}
	uid := "01K00000000000000000000009"
	r := &runner.Run{Version: 1, UID: uid, Phase: runner.Held, Outcome: "unknown", Reason: "operator inspection", Inspect: "operator inspection", SlotReleased: true, EndedAt: new(time.Now()), Job: f.job, Target: runner.Target{Identity: kata.Identity{InstanceUID: "instance", ProjectUID: "project", Actor: "worker"}, Route: route, Executor: f.cfg.Executor}}
	if err := runner.NewStore(state.OSFS{}, f.l).Create(r); err != nil {
		t.Fatal(err)
	}
	f.cfg.Kata.Project = "renamed-project"
	ready := make(chan struct{}, 1)
	f.afterJobList = func(context.Context, int32) {
		select {
		case ready <- struct{}{}:
		default:
		}
	}
	stop := startWorkerFixture(t, f)
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("renamed configured lane did not start")
	case <-f.workerDone:
		t.Fatal("worker stopped during rename", f.workerErr)
	}
	stop()
	var saved runner.Run
	if err := state.ReadJSON(state.OSFS{}, f.l.RunFile(uid), 4<<20, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Phase != runner.Held || saved.Reason != "operator inspection" {
		t.Fatalf("rename retired a configured UID: phase=%s reason=%s", saved.Phase, saved.Reason)
	}
	if err := state.ReadJSON(state.OSFS{}, f.l.Project("project").StateFile(), 65536, &pin); err != nil {
		t.Fatal(err)
	}
	if pin.Name != "renamed-project" {
		t.Fatal("configured lane did not refresh its name", pin.Name)
	}
}
