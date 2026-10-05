package sched

import (
	"fmt"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/store"
)

func TestCronRelativeDescriptorsRetainCreationPhase(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, tc := range []struct {
			expr, created, first, next string
			all                        int
		}{
			{"@every 5m", "2026-10-05T11:50:30.7Z", "2026-10-05T12:00:30Z", "2026-10-05T12:00:30Z", 1},
			{"@every 1m", "2026-10-05T11:50:23.7Z", "2026-10-05T12:00:23Z", "2026-10-05T12:01:23Z", 2},
		} {
			for _, policy := range []string{store.CatchupAll, store.CatchupLatest, store.CatchupSkip} {
				t.Run(fmt.Sprintf("native=%v/%s/%s", native, tc.expr, policy), func(t *testing.T) {
					created, _ := time.Parse(time.RFC3339Nano, tc.created)
					j := store.Job{Enabled: true, CreatedAt: created, Schedule: store.ScheduleCron, CronExpr: tc.expr, Catchup: policy}
					if native {
						j.NativeEventUID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
					}
					now, _ := time.Parse(time.RFC3339Nano, "2026-10-05T12:00:30Z")
					for i, wantText := range []string{tc.first, tc.next} {
						tick := now.Add(time.Duration(i) * time.Minute)
						want, _ := time.Parse(time.RFC3339Nano, wantText)
						fires, err := Due(j, time.Time{}, tick)
						count := 1
						if policy == store.CatchupAll {
							count = tc.all
						}
						if err != nil || len(fires) != count || !fires[len(fires)-1].Equal(want) {
							t.Fatalf("relative first progression changed: created=%s tick=%s fires=%v want=%s count=%d err=%v", created, tick, fires, want, count, err)
						}
					}
					// Once a real invocation exists, ordinary anchored relative progression
					// applies, rather than creation or a first-run window.
					anchor := now.Add(10*time.Second + 500*time.Millisecond)
					if fires, err := Due(j, anchor, anchor.Add(10*time.Second)); err != nil || len(fires) != 0 {
						t.Fatalf("anchored descriptor fired early: %v %v", fires, err)
					}
					delay := time.Minute
					if tc.expr == "@every 5m" {
						delay = 5 * time.Minute
					}
					want := anchor.Truncate(time.Second).Add(delay)
					if fires, err := Due(j, anchor, want); err != nil || len(fires) != 1 || !fires[0].Equal(want) {
						t.Fatalf("anchored descriptor lost pinned rounding/progression: %v want=%s err=%v", fires, want, err)
					}
				})
			}
		}
	}
}

// The pinned @every law is a whole-second progression from creation's Unix
// second, even when creation has a fractional second. It is not a calendar cron.
func FuzzRelativeCronFirstRunPhase(f *testing.F) {
	f.Add(int64(600), uint32(300000), uint32(700000000), uint8(0), true)
	f.Add(int64(607), uint32(60000), uint32(700000000), uint8(1), false)
	f.Add(int64(14000000000), uint32(1000), uint32(900000000), uint8(2), true)
	f.Fuzz(func(t *testing.T, ageDraw int64, delayDraw, phaseDraw uint32, policyDraw uint8, native bool) {
		now := time.Date(2026, 10, 5, 12, 0, 30, int(phaseDraw%1000000000), time.UTC)
		maxAge := now.Unix() - time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
		age := 1 + (ageDraw%(maxAge-1)+(maxAge-1))%(maxAge-1)
		created := time.Unix(now.Unix()-age, int64(phaseDraw%1000000000)).UTC()
		millis := int64(delayDraw % 3600000)
		seconds := millis / 1000
		if seconds < 1 {
			seconds = 1
		} // Pinned rounding, including subsecond/zero descriptors.
		policy := []string{store.CatchupLatest, store.CatchupAll, store.CatchupSkip}[policyDraw%3]
		j := store.Job{Enabled: true, CreatedAt: created, Schedule: store.ScheduleCron, CronExpr: fmt.Sprintf("@every %dms", millis), Catchup: policy}
		if native {
			j.NativeEventUID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
		}
		fires, err := Due(j, time.Time{}, now)
		if created.IsZero() {
			if native && err == nil {
				t.Fatal("missing native creation accepted")
			}
			if !native && (err != nil || len(fires) != 0) {
				t.Fatal("missing legacy creation backfilled")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		cycles := age / seconds
		want := time.Unix(created.Unix()+cycles*seconds, 0).UTC()
		eligible := cycles > 0 && now.Sub(want) <= firstRunGrace
		if policy == store.CatchupSkip {
			eligible = eligible && now.Sub(want) <= missedAfter
		}
		if !eligible {
			if len(fires) != 0 {
				t.Fatalf("stale/unearned relative first fire: %v", fires)
			}
			return
		}
		if len(fires) == 0 || !fires[len(fires)-1].Equal(want) {
			t.Fatalf("relative phase/window lost: created=%s descriptor=%s now=%s fires=%v want=%s", created, j.CronExpr, now, fires, want)
		}
		if len(fires) > maxCatchup+1 {
			t.Fatalf("unbounded relative scan result: %d", len(fires))
		}
		for i, fire := range fires {
			if fire.Nanosecond() != 0 || (fire.Unix()-created.Unix())%seconds != 0 || now.Sub(fire) > firstRunGrace || (i > 0 && !fire.After(fires[i-1])) {
				t.Fatalf("relative phase/backlog/order changed: %v", fires)
			}
		}
	})
}
