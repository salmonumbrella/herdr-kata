package sched

import (
	"math/big"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/store"
)

func TestOldNativeFirstRunReachesCurrentWindow(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 30, 0, time.UTC)
	for _, kind := range []store.ScheduleType{store.ScheduleCron, store.ScheduleInterval} {
		for _, policy := range []string{store.CatchupAll, store.CatchupLatest, store.CatchupSkip} {
			t.Run(string(kind)+"/"+policy, func(t *testing.T) {
				created := now.Add(-24*time.Hour - time.Second*7)
				j := store.Job{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", NativeEventUID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", Enabled: true, Schedule: kind, CronExpr: "* * * * *", IntervalSeconds: 60, CreatedAt: created, Catchup: policy}
				want := now.Add(-7 * time.Second)
				if kind == store.ScheduleCron {
					want = now.Truncate(time.Minute)
				}
				fires, err := Due(j, time.Time{}, now)
				if err != nil || len(fires) == 0 || !fires[len(fires)-1].Equal(want) {
					t.Fatalf("old activated definition starved/current phase changed: fires=%v want=%s err=%v", fires, want, err)
				}
				for _, fire := range fires {
					if fire.Before(now.Add(-firstRunGrace)) {
						t.Fatalf("first-run backlog: %s", fire)
					}
				}
				// The local first execution is now the next sweep's anchor.
				anchor := now
				if fires, err := Due(j, anchor, now.Add(10*time.Second)); err != nil || len(fires) != 0 {
					t.Fatalf("subsequent tick duplicated execution: %v %v", fires, err)
				}
				if fires, err := Due(j, anchor, now.Add(time.Minute)); err != nil || len(fires) == 0 {
					t.Fatalf("subsequent tick lost ordinary schedule: %v %v", fires, err)
				}
			})
		}
	}
}

// Exact interval phase is independent of the first scan's window. The oracle
// uses arbitrary-precision nanoseconds rather than the bounded production loop.
func FuzzOldNativeIntervalFirstRun(f *testing.F) {
	f.Add(int64(86400), uint32(60), uint16(7), uint8(0))
	f.Add(int64(86400), uint32(1), uint16(0), uint8(1))
	f.Add(int64(14000000000), uint32(59), uint16(7), uint8(0))
	f.Add(int64(0), uint32(0), uint16(7), uint8(0))
	f.Fuzz(func(t *testing.T, ageDraw int64, intervalDraw uint32, offsetDraw uint16, policyDraw uint8) {
		interval := int64(intervalDraw%3600) + 1
		// Native timestamps retain the public calendar range, including sources
		// older than time.Duration's ~292-year difference limit.
		maxAge := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC).Unix() - time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
		width := maxAge - interval + 1
		age := interval + (ageDraw%width+width)%width
		now := time.Date(2026, 10, 5, 12, 0, 0, int(offsetDraw)*1000, time.UTC)
		created := time.Unix(now.Unix()-age, int64(offsetDraw%1000)*int64(time.Millisecond)).UTC()
		policy := []string{store.CatchupLatest, store.CatchupAll, store.CatchupSkip}[policyDraw%3]
		j := store.Job{Enabled: true, NativeEventUID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", CreatedAt: created, Schedule: store.ScheduleInterval, IntervalSeconds: int(interval), Catchup: policy}
		// Arbitrary-precision nanoseconds independently locate the phase;
		// production uses finite Unix seconds with a subsecond borrow.
		elapsed := new(big.Int).Mul(big.NewInt(age), big.NewInt(int64(time.Second)))
		elapsed.Add(elapsed, big.NewInt(int64(now.Nanosecond()-created.Nanosecond())))
		cycles := new(big.Int).Div(elapsed, big.NewInt(interval*int64(time.Second))).Int64()
		want := time.Unix(created.Unix()+cycles*interval, int64(created.Nanosecond())).UTC()
		eligible := cycles > 0 && now.Sub(want) <= firstRunGrace
		if policy == store.CatchupSkip {
			eligible = eligible && now.Sub(want) <= missedAfter
		}
		fires, err := Due(j, time.Time{}, now)
		if created.IsZero() {
			if err == nil {
				t.Fatal("native missing creation did not hold")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if !eligible {
			if len(fires) != 0 {
				t.Fatalf("stale first fire accepted: %v", fires)
			}
			return
		}
		if len(fires) == 0 || !fires[len(fires)-1].Equal(want) {
			t.Fatalf("current first interval phase lost: created=%s interval=%d now=%s fires=%v want=%s", created, interval, now, fires, want)
		}
		if len(fires) > maxCatchup+1 {
			t.Fatalf("unbounded first-run result: %d", len(fires))
		}
		for i, fire := range fires {
			if now.Sub(fire) > firstRunGrace || (fire.Unix()-created.Unix())%interval != 0 || (i > 0 && !fire.After(fires[i-1])) {
				t.Fatalf("backlog/phase/order changed: %v", fires)
			}
		}
	})
}

func TestOldNativeDenseCronKeepsLatestBoundedFires(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	j := store.Job{Enabled: true, NativeEventUID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", CreatedAt: now.Add(-24 * time.Hour), Schedule: store.ScheduleCron, CronExpr: "@every 1s", Catchup: store.CatchupAll}
	fires, err := Due(j, time.Time{}, now)
	if err != nil || len(fires) == 0 || len(fires) > maxCatchup+1 || !fires[len(fires)-1].Equal(now) {
		t.Fatalf("dense first window lost current fire/bounds: %v %v", fires, err)
	}
	for _, fire := range fires {
		if now.Sub(fire) > firstRunGrace {
			t.Fatalf("dense cron replayed old backlog: %s", fire)
		}
	}
}
