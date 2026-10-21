package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/lease"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// A lane records its lease state locally, so status reads it without Kata.
func TestLaneLeaseSnapshotFeedsStatus(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	held := &lease.HeldError{Lease: lease.Lease{Version: 1, Owner: "elsewhere", Generation: 4, Heartbeat: at}}
	if err := saveLeaseStatus(l, nil, held, at); err != nil {
		t.Fatal(err)
	}
	s, err := readLane(l, kata.Installation{})
	if err != nil || s.Lease == nil || s.Lease.Held || s.Lease.Owner != "elsewhere" || s.Lease.Generation != 4 || s.Lease.Error != held.Error() {
		t.Fatalf("%+v %v", s.Lease, err)
	}
	if err := saveLeaseStatus(l, nil, errors.New("daemon unavailable"), at); err != nil {
		t.Fatal(err)
	}
	if s, _ = readLane(l, kata.Installation{}); s.Lease == nil || s.Lease.Held || s.Lease.Owner != "" || s.Lease.Error != "daemon unavailable" {
		t.Fatalf("unknown lease: %+v", s.Lease)
	}
}

// An admission renews the lease first. After another installation took the
// lane, the renewal fails, the lane stands down and nothing is admitted.
func TestAdmissionAfterTakeoverAdmitsNothing(t *testing.T) {
	leases := &hubLeases{}
	k := kata.New(config.Kata{Bin: "/bin/kata", Project: "example-project", Actor: "worker"}, t.TempDir(), func(_ context.Context, argv, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
		if out, code, ok := leases.reply("01K00000000000000000000001", argv[8:]); ok {
			return out, nil, code, nil
		}
		t.Errorf("unexpected call: %v", argv[8:])
		return nil, nil, 1, errors.New("unexpected call")
	}, nil)
	hold, err := lease.Claim(t.Context(), k, "worker", false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	leases.takeover("taker", "01K00000000000000000000001")
	var lost error
	a := &laneAdmitter{hold: hold, onLost: func(err error) { lost = err }} // a nil Manager panics if reached
	if result, err := a.Admit(t.Context(), sched.Due{}); result != sched.NotAdmitted || !errors.Is(err, lease.ErrLost) || !errors.Is(lost, lease.ErrLost) {
		t.Fatalf("admission after takeover: %v %v, lost %v", result, err, lost)
	}
}

// The CLI refuses control requests from the lease outcome a stood-down lane
// saved, so its reader must accept exactly what saveLeaseStatus writes.
func TestSavedTakeoverRefusesControlRequests(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	lock, ok, err := state.TryLock(l.WorkerLock())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer lock.Unlock()
	started := time.Now().UTC().Add(-time.Minute)
	if err := state.WriteJSON(state.OSFS{}, l.ServeInfo(), ServeInfo{StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	lost := &lease.LostError{Lease: lease.Lease{Version: 1, Owner: "taker", Generation: 2, Heartbeat: started}}
	if err := saveLeaseStatus(l.Project("project"), nil, lost, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err = runner.SendProjectRequest(state.OSFS{}, l, "run", "job", "project")
	if err == nil || err.Error() != "another installation (taker) owns this lane: lease generation 2" {
		t.Fatalf("run on a stood-down lane: %v", err)
	}
	if refusal := leaseOwnerElsewhere(lost); refusal == nil || refusal.Error() != err.Error() {
		t.Fatalf("worker refusal %v differs from the CLI's %v", refusal, err)
	}
}
