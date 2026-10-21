package runner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/lease"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// ReasonLeaseHeldElsewhere is the request result reason of a control request
// refused because another installation owns the run's lane.
const ReasonLeaseHeldElsewhere = "lease_held_elsewhere"

// ErrLeaseHeldElsewhere marks a LeaseRefusal.
var ErrLeaseHeldElsewhere = errors.New("lane lease held by another installation")

// LeaseRefusal refuses a control request on a lane this installation stood
// down from. Its message matches up's refusal of the same lane.
type LeaseRefusal struct {
	Owner      string
	Generation int64
}

func (e *LeaseRefusal) Error() string {
	return fmt.Sprintf("another installation (%s) owns this lane: lease generation %d", e.Owner, e.Generation)
}
func (e *LeaseRefusal) Is(target error) bool { return target == ErrLeaseHeldElsewhere }

// SetLeaseRefusal records that another installation owns this lane, or clears
// that record with nil once the lane holds its lease again. A refusal wakes
// the control loop, so requests already queued for the lane settle at once.
func (m *Manager) SetLeaseRefusal(refusal *LeaseRefusal) {
	if refusal != nil {
		copied := *refusal
		refusal = &copied
	}
	m.leaseRefusal.Store(refusal)
	if refusal != nil && m.OnManualJournal != nil {
		m.OnManualJournal()
	}
}

// laneRefusal returns the lane's lease refusal, or nil while the lane may
// still hold its lease.
func (m *Manager) laneRefusal() error {
	if m == nil {
		return nil
	}
	if refusal := m.leaseRefusal.Load(); refusal != nil {
		return refusal
	}
	return nil
}

// savedLeaseRefusal reads the lease outcome the running worker saved for a
// project lane (daemon.LeaseStatus). Only an outcome saved since the
// runtime started, naming another owner, refuses; anything else defers to
// the worker.
func savedLeaseRefusal(fsys state.FS, l state.Layout, project string, started time.Time) error {
	if project == "" || !state.SafeComponent(project) {
		return nil
	}
	// The decode is strict, so this mirrors every field of daemon.LeaseStatus.
	var saved struct {
		Held       bool      `json:"held"`
		Owner      string    `json:"owner,omitempty"`
		Generation int64     `json:"generation,omitempty"`
		Heartbeat  time.Time `json:"heartbeat,omitzero"`
		Error      string    `json:"error,omitempty"`
		CheckedAt  time.Time `json:"checked_at"`
	}
	if state.ReadJSON(fsys, filepath.Join(l.Project(project).Root, "lease.json"), 64<<10, &saved) != nil {
		return nil
	}
	if saved.Held || saved.Owner == "" || started.IsZero() || saved.CheckedAt.Before(started) {
		return nil
	}
	return &LeaseRefusal{Owner: saved.Owner, Generation: saved.Generation}
}

// requestRefusal is the result reason for a refused request.
func requestRefusal(err error) string {
	if errors.Is(err, ErrLeaseHeldElsewhere) {
		return ReasonLeaseHeldElsewhere
	}
	return ""
}

// laneLeaseElsewhere refuses a Kata write for a lane whose live lease another
// installation holds. Abandon writes to Kata without the runtime, so it reads
// the lease itself. A failed read is an error; a free or expired lease, or
// one of this installation, refuses nothing.
func laneLeaseElsewhere(ctx context.Context, k Kata, owner string) error {
	reader, ok := k.(lease.Kata)
	if !ok {
		return nil
	}
	current, found, err := lease.Read(ctx, reader)
	if err != nil {
		return fmt.Errorf("read lane lease: %w", err)
	}
	if !found || current.Owner == owner || !current.Live(time.Now()) {
		return nil
	}
	return &LeaseRefusal{Owner: current.Owner, Generation: current.Generation}
}
