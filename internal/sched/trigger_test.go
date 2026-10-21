package sched

import (
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"testing"
	"time"
)

func instant(s string) time.Time {
	t, e := time.Parse(time.RFC3339Nano, s)
	if e != nil {
		panic(e)
	}
	return t
}
func TestCronDSTNewYork(t *testing.T) {
	s, e := Compile(defs.Trigger{Kind: "cron", Cron: "30 1 * * *", Timezone: "America/New_York"}, time.Time{})
	if e != nil {
		t.Fatal(e)
	}
	got, _ := Between(s, instant("2026-11-01T04:00:00Z"), instant("2026-11-01T07:00:00Z"), 10)
	if len(got) != 2 || !got[0].Equal(instant("2026-11-01T05:30:00Z")) || !got[1].Equal(instant("2026-11-01T06:30:00Z")) {
		t.Fatal(got)
	}
}
func TestCronSpringForwardBerlin(t *testing.T) {
	s, e := Compile(defs.Trigger{Kind: "cron", Cron: "30 2 * * *", Timezone: "Europe/Berlin"}, time.Time{})
	if e != nil {
		t.Fatal(e)
	}
	// Match Kata: the skipped 02:30 runs at 03:30 CEST on the same day.
	if got := s.Next(instant("2026-03-28T23:00:00Z")); !got.Equal(instant("2026-03-29T01:30:00Z")) {
		t.Fatal(got)
	}
}
func TestIntervalAnchored(t *testing.T) {
	anchor := instant("2026-01-01T00:00:00.123Z")
	s, e := Compile(defs.Trigger{Kind: "interval", IntervalSeconds: 60}, anchor)
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct{ from, want string }{{"2025-12-31T23:59:59Z", "2026-01-01T00:00:00.123Z"}, {"2026-01-01T00:00:00.123Z", "2026-01-01T00:01:00.123Z"}, {"2026-01-01T00:01:12Z", "2026-01-01T00:02:00.123Z"}} {
		if got := s.Next(instant(tc.from)); !got.Equal(instant(tc.want)) {
			t.Fatal(got, tc)
		}
	}
}
func TestEveryDescriptorAnchored(t *testing.T) {
	s, e := Compile(defs.Trigger{Kind: "cron", Cron: "@every 90s"}, instant("2026-01-01T00:00:00.123Z"))
	if e != nil {
		t.Fatal(e)
	}
	if got := s.Next(instant("2026-01-01T00:01:00Z")); !got.Equal(instant("2026-01-01T00:01:30.123Z")) {
		t.Fatal(got)
	}
}
func TestOnce(t *testing.T) {
	at := instant("2026-01-01T00:00:00.123Z")
	s, e := Compile(defs.Trigger{Kind: "once", At: at.Format(time.RFC3339Nano)}, time.Time{})
	if e != nil || !s.Next(at.Add(-time.Nanosecond)).Equal(at) || !s.Next(at).IsZero() {
		t.Fatal(s, e)
	}
}
func TestBetweenTruncates(t *testing.T) {
	s, _ := Compile(defs.Trigger{Kind: "interval", IntervalSeconds: 1}, instant("2026-01-01T00:00:00Z"))
	got, drop := Between(s, instant("2026-01-01T00:00:00Z"), instant("2026-01-01T00:00:10Z"), 3)
	if !drop || len(got) != 3 || !got[0].Equal(instant("2026-01-01T00:00:08Z")) {
		t.Fatal(got, drop)
	}
}
func FuzzIntervalBounds(f *testing.F) {
	f.Add(int64(60), int64(900))
	f.Fuzz(func(t *testing.T, seconds, offset int64) {
		if seconds <= 0 || seconds > defs.MaxSeconds || offset < 0 {
			return
		}
		anchor := instant("2026-01-01T00:00:00.123Z")
		from := anchor.Add(time.Duration(offset))
		s, e := Compile(defs.Trigger{Kind: "interval", IntervalSeconds: seconds}, anchor)
		if e != nil {
			t.Fatal(e)
		}
		next := s.Next(from)
		if !next.After(from) || next.Sub(from) > time.Duration(seconds)*time.Second {
			t.Fatal(from, next, seconds)
		}
		if (next.Unix()-anchor.Unix())%seconds != 0 || next.Nanosecond() != anchor.Nanosecond() {
			t.Fatal(next)
		}
	})
}
func TestBetweenUsesWallClock(t *testing.T) {
	base := time.Now()
	s, _ := Compile(defs.Trigger{Kind: "interval", IntervalSeconds: 60}, base.UTC())
	from := base.Add(time.Minute)
	to := wallJump(from, 3*time.Minute)
	got, _ := Between(s, from, to, 100)
	if len(got) != 3 {
		t.Fatal("wall-clock interval lost", got)
	}
}
func TestCronBetweenTruncates(t *testing.T) {
	s, e := Compile(defs.Trigger{Kind: "cron", Cron: "* * * * *"}, time.Time{})
	if e != nil {
		t.Fatal(e)
	}
	got, drop := Between(s, instant("2026-01-01T00:00:00Z"), instant("2026-01-01T00:05:00Z"), 2)
	if !drop || len(got) != 2 || !got[0].Equal(instant("2026-01-01T00:04:00Z")) || !got[1].Equal(instant("2026-01-01T00:05:00Z")) {
		t.Fatal(got, drop)
	}
}
