package sched

import (
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"testing"
	"time"
)

func TestIntervalDurationOverflowIsRejected(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	j := store.Job{Enabled: true, Schedule: store.ScheduleInterval, IntervalSeconds: 9223372037}
	if _, err := Due(j, now.Add(-time.Hour), now); err == nil {
		t.Fatal("overflowing interval accepted")
	}
	if next := NextFire(j, now, now); !next.IsZero() {
		t.Fatalf("overflowing interval has next fire %v", next)
	}
}
func TestNextFireSkipsOldCyclesArithmetically(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	anchor := time.Date(1600, 1, 1, 0, 0, 0, 123, time.UTC)
	j := store.Job{Schedule: store.ScheduleInterval, IntervalSeconds: 1}
	if got := NextFire(j, anchor, now); got != now.Add(123*time.Nanosecond) {
		t.Fatalf("lost interval phase: %v", got)
	}
}
