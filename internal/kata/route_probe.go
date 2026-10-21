package kata

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

const (
	routeProbeConcurrency = 2
	routeProbeTimeout     = time.Minute
	routeProbeAttempts    = 3
)

// Shared by discovery, guards and access probes across all lanes in this
// process. Queueing does not consume an attempt's deadline.
var routeProbeSlots = make(chan struct{}, routeProbeConcurrency)

// Only read-only route probes may use this retry policy. Mutations keep their
// existing conditional-write and ambiguous-reply handling.
func routeProbe[T any](ctx context.Context, call func(context.Context) (T, error)) (value T, err error) {
	backoff := time.Second
	for attempt := range routeProbeAttempts {
		select {
		case <-ctx.Done():
			return value, ctx.Err()
		case routeProbeSlots <- struct{}{}:
		}
		probeCtx, cancel := context.WithTimeout(ctx, routeProbeTimeout)
		value, err = call(probeCtx)
		if err == nil {
			err = probeCtx.Err()
		}
		cancel()
		<-routeProbeSlots
		if err == nil || ctx.Err() != nil || attempt == routeProbeAttempts-1 || !probeDeadline(err) {
			return value, err
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return value, ctx.Err()
		case <-timer.C:
		}
		backoff *= 2
	}
	return value, err
}

// WaitCap bounds how long a waiting lane sleeps between Kata probes.
const WaitCap = 5 * time.Minute

// WaitDelay doubles base for each earlier attempt, up to WaitCap, and returns
// a random delay between half and all of it, so lanes that started waiting
// together do not probe together.
func WaitDelay(base time.Duration, attempt int) time.Duration {
	limit := WaitCap
	if attempt < 16 {
		limit = min(WaitCap, base<<attempt)
	}
	return limit/2 + time.Duration(rand.Int64N(int64(limit/2)+1))
}

func probeDeadline(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	e, ok := errors.AsType[*Error](err)
	return ok && e.Code == "timeout"
}
