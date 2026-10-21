package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/lease"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

// A route can recover before the definition snapshot does. Fence new scheduler
// admissions until every known refresh has completed; already frozen runs keep
// their payloads. Generations preserve events arriving during a refresh.
type laneAdmitter struct {
	*runner.Manager
	mu             sync.Mutex
	dirty, applied atomic.Uint64
	// hold is the lane's lease, renewed before every admission so the lane
	// never admits after another installation took it over.
	hold   *lease.Hold
	onLost func(error)
}

// confirm renews the lane lease. An unproven renewal admits nothing.
func (a *laneAdmitter) confirm(ctx context.Context) error {
	if a.hold == nil {
		return nil
	}
	err := a.hold.Renew(ctx, time.Now())
	if err != nil && errors.Is(err, lease.ErrLost) && a.onLost != nil {
		a.onLost(err)
	}
	if err != nil {
		return fmt.Errorf("lane lease unproven: %w", err)
	}
	return nil
}

func (a *laneAdmitter) pending() bool { return a.dirty.Load() != a.applied.Load() }

func (a *laneAdmitter) markDirty() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dirty.Add(1)
}

func (a *laneAdmitter) lockPreparation() (func(), bool) {
	a.mu.Lock()
	if a.pending() {
		a.mu.Unlock()
		return nil, false
	}
	return a.mu.Unlock, true
}

func (a *laneAdmitter) Admit(ctx context.Context, d sched.Due) (sched.AdmitResult, error) {
	if err := a.confirm(ctx); err != nil {
		return sched.NotAdmitted, err
	}
	return a.Manager.AdmitWithPreparationGuard(ctx, d, a.lockPreparation)
}
