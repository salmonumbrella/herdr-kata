package clock

import (
	"context"
	"testing"
	"time"
)

// Reporting freezes evidence at the tick while deadline decisions follow I/O.
func TestFrozenEvidenceAndAdvancingDeadlinesShareSource(t *testing.T) {
	tick := time.Date(2026, 11, 4, 1, 0, 0, 0, time.UTC)
	current := tick.Add(time.Second)
	ctx := WithSource(WithNow(context.Background(), tick), func() time.Time { return current })
	ctx = Progress(ctx, tick)
	current = current.Add(time.Minute)
	if got := Now(ctx); !got.Equal(tick) {
		t.Fatalf("observation time advanced: %s", got)
	}
	if got := Current(ctx, tick); !got.Equal(current) {
		t.Fatalf("deadline clock froze: %s", got)
	}
}
