package daemon

import (
	"errors"
	"path/filepath"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/lease"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// LeaseStatus is a lane's latest lease outcome, saved locally for status.
type LeaseStatus struct {
	Held       bool      `json:"held"`
	Owner      string    `json:"owner,omitempty"`
	Generation int64     `json:"generation,omitempty"`
	Heartbeat  time.Time `json:"heartbeat,omitzero"`
	Error      string    `json:"error,omitempty"`
	CheckedAt  time.Time `json:"checked_at"`
}

func leaseFile(l state.Layout) string { return filepath.Join(l.Root, "lease.json") }

// saveLeaseStatus records hold, or the error that left the lane without one.
func saveLeaseStatus(l state.Layout, hold *lease.Hold, problem error, now time.Time) error {
	s := LeaseStatus{CheckedAt: now.UTC()}
	var current lease.Lease
	if held, ok := errors.AsType[*lease.HeldError](problem); ok {
		current = held.Lease
	} else if lost, ok := errors.AsType[*lease.LostError](problem); ok {
		current = lost.Lease
	}
	if problem == nil && hold != nil {
		current, s.Held = hold.Lease(), true
	}
	if problem != nil {
		s.Error = problem.Error()
	}
	s.Owner, s.Generation, s.Heartbeat = current.Owner, current.Generation, current.Heartbeat
	return state.WriteJSON(state.OSFS{}, leaseFile(l), s)
}

// leaseOwnerElsewhere names the installation that owns the lane when problem
// is a refused or lost lease with a known owner.
func leaseOwnerElsewhere(problem error) *runner.LeaseRefusal {
	var current lease.Lease
	if held, ok := errors.AsType[*lease.HeldError](problem); ok {
		current = held.Lease
	} else if lost, ok := errors.AsType[*lease.LostError](problem); ok {
		current = lost.Lease
	}
	if current.Owner == "" {
		return nil
	}
	return &runner.LeaseRefusal{Owner: current.Owner, Generation: current.Generation}
}
