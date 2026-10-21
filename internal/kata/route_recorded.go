package kata

import (
	"fmt"
	"maps"
	"path/filepath"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// Kata tab startup uses the worker's route evidence without launching Kata probes.
func RecordedRoute(cfg config.Config, uid string) (Installation, error) {
	var selected Installation
	root := state.Layout{Root: state.DefaultRoot()}
	layouts, err := state.ProjectLayouts(state.OSFS{}, root)
	if err != nil {
		return selected, fmt.Errorf("kata tab: recorded route unavailable: %w", err)
	}
	var refusal error
	for _, layout := range layouts {
		if uid != "" && layout != root && filepath.Base(layout.Root) != uid {
			continue
		}
		var pin Installation
		if err := state.ReadJSON(state.OSFS{}, layout.StateFile(), 64<<10, &pin); err != nil {
			return selected, fmt.Errorf("kata tab: project pin unavailable: %w", err)
		}
		name := pin.Name
		if name == "" && layout == root {
			name = cfg.Kata.Project
		}
		if name != cfg.Kata.Project || (uid != "" && pin.ProjectUID != uid) {
			continue
		}
		pin.Name = name
		if err := checkRecordedRoute(cfg, root, layout, pin); err != nil {
			refusal = err
			continue
		}
		if selected.ProjectUID != "" {
			return selected, fmt.Errorf("kata tab: ambiguous recorded project %s", name)
		}
		selected = pin
	}
	if selected.ProjectUID != "" {
		return selected, nil
	}
	if refusal != nil {
		return selected, refusal
	}
	return selected, fmt.Errorf("kata tab: recorded project pin missing; check the runtime")
}

func checkRecordedRoute(cfg config.Config, root, lane state.Layout, pin Installation) error {
	if lane != root && filepath.Base(lane.Root) != pin.ProjectUID {
		return fmt.Errorf("kata tab: recorded project pin differs from lane")
	}
	if pin.Version != 1 || pin.CreatedAt.IsZero() || pin.ProjectUID == "" || pin.InstanceUID == "" || pin.Actor != cfg.Kata.Actor || pin.Executor != cfg.Executor || pin.Route.Bin != cfg.Kata.Bin || pin.Route.Daemon != cfg.Kata.Daemon || !maps.Equal(pin.Route.Env, cfg.Kata.Env) {
		return fmt.Errorf("kata tab: recorded project pin missing or differs from configuration; check the runtime")
	}
	var recorded RouteStatus
	if err := state.ReadJSON(state.OSFS{}, lane.RouteFile(), 64<<10, &recorded); err != nil {
		return fmt.Errorf("kata tab: recorded route unavailable; check the runtime: %w", err)
	}
	if !recorded.Allowed || recorded.Error != "" || recorded.AccessLost || recorded.SigningBlocked {
		return fmt.Errorf("kata tab: recorded route refused: %s", recorded.Error)
	}
	age := time.Since(recorded.CheckedAt)
	if recorded.CheckedAt.IsZero() || age < 0 || age > 2*RouteCheckInterval {
		return fmt.Errorf("kata tab: recorded route missing a current check (maximum age %s); wait for the runtime's next route check", 2*RouteCheckInterval)
	}
	if recorded.Project != pin.Name || recorded.ProjectUID != pin.ProjectUID || recorded.InstanceUID != pin.InstanceUID || recorded.Actor != pin.Actor {
		return fmt.Errorf("kata tab: recorded route identity differs from project pin; check the runtime")
	}
	return nil
}
