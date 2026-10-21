package daemon

import (
	"context"
	"log/slog"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/lease"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func configuredLane(cfg config.Config, pin kata.Installation) bool {
	return cfg.HasProject(pin.Name)
}

// A retired lane has a queue but no lease claim, definitions, events, scheduler
// or executor. Its original pin is the only authority for remaining reports.
func retireLane(ctx context.Context, cfg config.Config, pin kata.Installation, l state.Layout, manager *runner.Manager, cli func(config.Config) *kata.CLI) error {
	view := cfg
	view.Projects = nil
	view.Kata.Project, view.Kata.Actor = pin.Name, pin.Actor
	view.Kata.Bin, view.Kata.Daemon, view.Kata.Env = pin.Route.Bin, pin.Route.Daemon, pin.Route.Env
	k := cli(view)
	guard := kata.NewRouteGuard(view, k, state.OSFS{}, l, pin.ProjectUID)
	var lane *runner.Manager
	for waits := 0; ctx.Err() == nil; waits++ {
		err := guard.Check(ctx)
		if err == nil && lane == nil {
			if err = manager.RetireProject(ctx, pin.ProjectUID); err == nil {
				lane, err = manager.RegisterProject(pin.ProjectUID, view, k, guard, l)
			}
		}
		if err == nil {
			err = lease.Release(ctx, k, cfg.Executor)
		}
		if err == nil {
			err = saveLeaseStatus(l, nil, nil, time.Now())
		}
		if err == nil {
			err = lane.RetireProject(ctx, pin.ProjectUID)
		}
		if err == nil {
			return nil // The registered queue keeps retrying reports across route loss.
		}
		if state.LocalError(err) {
			return err
		}
		slog.Warn("removed lane retirement waiting", "project", pin.Name, "error", err)
		timer := time.NewTimer(kata.WaitDelay(2*time.Second, waits))
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return nil
}
