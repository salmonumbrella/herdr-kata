package sched

import (
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
)

// Protect Compile's next-occurrence contract using hand-checked UTC instants.
func TestCronNextParity(t *testing.T) {
	for _, tc := range []struct {
		name, expression, timezone, after string
		want                              []string
	}{
		{"Sunday zero", "0 9 * * 0", "", "2026-10-16T09:00:00Z", []string{"2026-10-18T09:00:00Z"}},
		{"Sunday seven", "0 9 * * 7", "", "2026-10-16T09:00:00Z", []string{"2026-10-18T09:00:00Z"}},
		{"Sunday range", "0 9 * * 5-7", "", "2026-10-15T09:00:00Z", []string{"2026-10-16T09:00:00Z", "2026-10-17T09:00:00Z", "2026-10-18T09:00:00Z"}},
		{"Sunday range step", "0 9 * * 5-7/2", "", "2026-10-15T09:00:00Z", []string{"2026-10-16T09:00:00Z", "2026-10-18T09:00:00Z"}},
		{"Sunday open step", "0 9 * * 5/2", "", "2026-10-16T09:00:00Z", []string{"2026-10-18T09:00:00Z"}},
		{"wrapping hours", "0 22-2 * * *", "", "2026-10-16T21:00:00Z", []string{"2026-10-16T22:00:00Z", "2026-10-16T23:00:00Z", "2026-10-17T00:00:00Z", "2026-10-17T01:00:00Z", "2026-10-17T02:00:00Z", "2026-10-17T22:00:00Z"}},
		{"either day matches weekday", "0 9 1 * MON", "", "2026-10-01T09:00:00Z", []string{"2026-10-05T09:00:00Z"}},
		{"either day matches month day", "0 9 1 * MON", "", "2026-10-31T09:00:00Z", []string{"2026-11-01T09:00:00Z"}},
		{"New York spring gap", "30 2 * * *", "America/New_York", "2026-03-08T00:00:00Z", []string{"2026-03-08T07:30:00Z"}},
		{"New York fall fold", "30 1 * * *", "America/New_York", "2026-11-01T04:00:00Z", []string{"2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"}},
		{"matching embedded timezone", "TZ=America/New_York 0 9 * * 7", "America/New_York", "2026-10-16T09:00:00Z", []string{"2026-10-18T13:00:00Z"}},
		{"matching CRON_TZ", "CRON_TZ=America/New_York 0 9 * * 7", "America/New_York", "2026-10-16T09:00:00Z", []string{"2026-10-18T13:00:00Z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Compile(defs.Trigger{Kind: "cron", Cron: tc.expression, Timezone: tc.timezone}, time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			cursor := instant(tc.after)
			for _, want := range tc.want {
				got := s.Next(cursor)
				if !got.Equal(instant(want)) {
					t.Fatalf("Next(%s) = %s, want %s", cursor, got, want)
				}
				cursor = got
			}
		})
	}
}
