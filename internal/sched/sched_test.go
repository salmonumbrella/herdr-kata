package sched

import (
	"reflect"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"hegel.dev/go/hegel"
)

func instant(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

// Port v1's hand-checked crontab and DST examples at our scheduling boundary.
func TestCronNextParity(t *testing.T) {
	for _, tt := range []struct {
		name, expr, zone, after string
		want                    []string
	}{
		{"Sunday zero", "0 9 * * 0", "", "2026-10-16T09:00:00Z", []string{"2026-10-18T09:00:00Z"}},
		{"Sunday seven", "0 9 * * 7", "", "2026-10-16T09:00:00Z", []string{"2026-10-18T09:00:00Z"}},
		{"Sunday range", "0 9 * * 5-7", "", "2026-10-15T09:00:00Z", []string{"2026-10-16T09:00:00Z", "2026-10-17T09:00:00Z", "2026-10-18T09:00:00Z"}},
		{"Sunday range step", "0 9 * * 5-7/2", "", "2026-10-15T09:00:00Z", []string{"2026-10-16T09:00:00Z", "2026-10-18T09:00:00Z"}},
		{"Sunday open step", "0 9 * * 5/2", "", "2026-10-16T09:00:00Z", []string{"2026-10-18T09:00:00Z"}},
		{"wrapping hours", "0 22-2 * * *", "", "2026-10-16T21:00:00Z", []string{"2026-10-16T22:00:00Z", "2026-10-16T23:00:00Z", "2026-10-17T00:00:00Z", "2026-10-17T01:00:00Z", "2026-10-17T02:00:00Z", "2026-10-17T22:00:00Z"}},
		{"either weekday", "0 9 1 * MON", "", "2026-10-01T09:00:00Z", []string{"2026-10-05T09:00:00Z"}},
		{"either month day", "0 9 1 * MON", "", "2026-10-31T09:00:00Z", []string{"2026-11-01T09:00:00Z"}},
		{"spring gap", "30 2 * * *", "America/New_York", "2026-03-08T00:00:00Z", []string{"2026-03-08T07:30:00Z"}},
		{"fall fold", "30 1 * * *", "America/New_York", "2026-11-01T04:00:00Z", []string{"2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"}},
		{"embedded zone", "TZ=America/New_York 0 9 * * 7", "America/New_York", "2026-10-16T09:00:00Z", []string{"2026-10-18T13:00:00Z"}},
		{"embedded CRON_TZ", "CRON_TZ=America/New_York 0 9 * * 7", "America/New_York", "2026-10-16T09:00:00Z", []string{"2026-10-18T13:00:00Z"}},
		{"Berlin spring gap", "30 2 * * *", "Europe/Berlin", "2026-03-28T23:00:00Z", []string{"2026-03-29T01:30:00Z"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			trig := kata.Trigger{Kind: "cron", Cron: &tt.expr, Timezone: &tt.zone}
			from := instant(tt.after)
			to := instant(tt.want[len(tt.want)-1])
			got, _ := Occurrences(trig, time.Time{}, from, to, "all", 0)
			var keys []string
			for _, o := range got {
				keys = append(keys, o.Key)
			}
			if !reflect.DeepEqual(keys, tt.want) {
				t.Fatalf("occurrences %v != %v", keys, tt.want)
			}
		})
	}
}

func TestEmbeddedTimezoneWithoutTriggerTimezone(t *testing.T) {
	for _, prefix := range []string{"CRON_TZ=", "TZ="} {
		t.Run(prefix, func(t *testing.T) {
			got, _ := Occurrences(kata.Trigger{Kind: "cron", Cron: new(prefix + "America/New_York 0 9 * * *")}, instant("2026-01-01T00:00:00Z"), instant("2026-01-01T13:59:00Z"), instant("2026-01-01T14:01:00Z"), "all", 0)
			if len(got) != 1 || got[0].Key != "2026-01-01T14:00:00Z" {
				t.Fatalf("embedded timezone ignored: %v", got)
			}
		})
	}
}

func TestIntervalAnchored(t *testing.T) {
	created := instant("2026-01-01T00:00:00.123Z")
	for _, tt := range []struct{ from, want string }{{"2025-12-31T23:59:59Z", "2026-01-01T00:00:00.123Z"}, {"2026-01-01T00:00:00.123Z", "2026-01-01T00:01:00.123Z"}, {"2026-01-01T00:01:12Z", "2026-01-01T00:02:00.123Z"}} {
		got, _ := Occurrences(kata.Trigger{Kind: "interval", IntervalSeconds: new(int64(60))}, created, instant(tt.from), instant(tt.want), "latest", 0)
		if len(got) != 1 || !got[0].At.Equal(instant(tt.want)) {
			t.Fatalf("from %s: %v", tt.from, got)
		}
	}
}

func TestEveryDescriptorAnchored(t *testing.T) {
	got, _ := Occurrences(kata.Trigger{Kind: "cron", Cron: new("@every 90s")}, instant("2026-01-01T00:00:00.123Z"), instant("2026-01-01T00:01:00Z"), instant("2026-01-01T00:01:30.123Z"), "all", 0)
	if len(got) != 1 || got[0].Key != "2026-01-01T00:01:30.123Z" {
		t.Fatal(got)
	}
}

func TestOnceAndManual(t *testing.T) {
	at := instant("2026-01-01T00:00:00Z")
	trig := kata.Trigger{Kind: "once", At: new(at.Format(time.RFC3339))}
	got, _ := Occurrences(trig, at, at.Add(-time.Second), at, "all", 0)
	if len(got) != 1 {
		t.Fatal(got)
	}
	if got, _ := Occurrences(trig, at, at, at.Add(time.Hour), "all", 0); len(got) != 0 {
		t.Fatal("once repeated", got)
	}
	if got, _ := Occurrences(kata.Trigger{Kind: "manual"}, at, at, at.Add(time.Hour), "all", 0); len(got) != 0 {
		t.Fatal("manual fired", got)
	}
}

func TestCatchupTables(t *testing.T) {
	now := instant("2026-01-10T00:00:00Z")
	for _, tt := range []struct {
		policy string
		age    time.Duration
		grace  time.Duration
		count  int
		first  string
	}{
		{"latest", 10 * time.Minute, 0, 1, "2026-01-10T00:00:00Z"},
		{"all", 200 * time.Minute, 0, 100, "2026-01-09T22:21:00Z"},
		{"all", 10 * 24 * time.Hour, 0, 100, "2026-01-09T22:21:00Z"},
		{"skip", 2 * time.Minute, 0, 1, "2026-01-10T00:00:00Z"},
		{"skip", 91 * time.Second, 30 * time.Second, 0, ""},
	} {
		created := now.Add(-tt.age)
		got, _ := Occurrences(kata.Trigger{Kind: "interval", IntervalSeconds: new(int64(60))}, created, created, now, tt.policy, tt.grace)
		if len(got) != tt.count || len(got) > 0 && got[0].Key != tt.first {
			t.Fatalf("%+v: %v", tt, got)
		}
	}
}

func TestOccurrenceKeyRFC3339UTC(t *testing.T) {
	got, _ := Occurrences(kata.Trigger{Kind: "once", At: new("2026-01-01T09:00:00+01:00")}, time.Time{}, instant("2026-01-01T00:00:00Z"), instant("2026-01-01T10:00:00Z"), "all", 0)
	if len(got) != 1 || got[0].Key != "2026-01-01T08:00:00Z" {
		t.Fatal(got)
	}
}

func TestDateOccurrenceKeyByRawValue(t *testing.T) {
	v := &kata.DateValue{Value: "2026-11-03", Instant: instant("2026-11-03T00:00:00Z")}
	got := DateOccurrence("01J00000000000000000000020", v)
	if got == nil || got.Key != "issue-scheduled:01J00000000000000000000020:2026-11-03" || !got.At.Equal(v.Instant) {
		t.Fatal(got)
	}
	v.Value = "2026-11-03T00:00:00Z"
	if other := DateOccurrence("01J00000000000000000000020", v); other.Key == got.Key {
		t.Fatal("lost raw date identity")
	}
	if DateOccurrence("01J00000000000000000000020", nil) != nil {
		t.Fatal("missing date fires")
	}
}

func TestSupersedeOnEventChange(t *testing.T) {
	at := instant("2026-01-01T00:00:00Z")
	c := Checkpoint{Version: 1, EventUID: "old", Through: at, Pending: []Occurrence{{Key: "old", At: at}}}
	c.Merge("new", []Occurrence{{Key: "new", At: at}}, at)
	if c.EventUID != "new" || len(c.Pending) != 1 || c.Pending[0].Key != "new" {
		t.Fatal(c)
	}
	c.Merge("new", []Occurrence{{Key: "new", At: at}}, at.Add(-time.Hour))
	if c.Through.Before(at) || len(c.Pending) != 1 {
		t.Fatal("checkpoint regressed or duplicated", c)
	}
}

func TestCheckpointBounds(t *testing.T) {
	now := instant("2026-01-10T00:00:00Z")
	c := Checkpoint{EventUID: "event"}
	var due []Occurrence
	for n := 200; n > 0; n-- {
		at := now.Add(-time.Duration(n) * time.Hour)
		due = append(due, Occurrence{Key: at.Format(time.RFC3339), At: at})
	}
	c.Merge("event", due, now)
	if len(c.Pending) != 100 || c.Pending[0].Key != "2026-01-05T20:00:00Z" {
		t.Fatal(c.Pending)
	}
}

func TestIntervalOccurrenceBounds(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		seconds := hegel.Draw(ht, hegel.Integers[int64](1, int64((1<<63-1)/time.Second)))
		offset := hegel.Draw(ht, hegel.Integers[int64](0, 7*24*3600))
		created := instant("2026-01-01T00:00:00.123Z")
		now := created.Add(time.Duration(offset) * time.Second)
		got, _ := Occurrences(kata.Trigger{Kind: "interval", IntervalSeconds: &seconds}, created, created, now, "all", 0)
		wantCount := min(int(offset/seconds), 100)
		if len(got) != wantCount {
			ht.Fatalf("period %d offset %d: count %d != %d", seconds, offset, len(got), wantCount)
		}
		var previous time.Time
		for _, o := range got {
			if !o.At.After(created) || o.At.After(now) || !previous.IsZero() && !o.At.After(previous) || (o.At.Unix()-created.Unix())%seconds != 0 || o.At.Nanosecond() != 123000000 {
				ht.Fatalf("invalid occurrence %+v for period %d", o, seconds)
			}
			previous = o.At
		}
		if len(got) > 100 {
			ht.Fatalf("unbounded: %d", len(got))
		}
	})
}

// Losing any discarded instant or counting retained/duplicate instants breaks accounting.
func TestDroppedOccurrenceTables(t *testing.T) {
	start := instant("2026-11-03T00:00:00Z")
	for _, kind := range []string{"cron", "interval"} {
		for _, policy := range []string{"all", "latest", "skip"} {
			t.Run(kind+"/"+policy, func(t *testing.T) {
				trig := kata.Trigger{Kind: kind, Cron: new("0 * * * *"), IntervalSeconds: new(int64(3600))}
				kept, d := Occurrences(trig, start, start, start.Add(3*time.Hour+time.Minute), policy, 30*time.Second)
				count, dropped := 1, 2
				if policy == "all" {
					count, dropped = 3, 0
				}
				if policy == "skip" {
					count, dropped = 0, 3
				}
				if len(kept) != count || d.Count != dropped || d.Clipped {
					t.Fatalf("kept=%v dropped=%+v", kept, d)
				}
				if dropped > 0 && (!d.First.Equal(start.Add(time.Hour)) || !d.Last.Equal(start.Add(time.Duration(dropped)*time.Hour))) {
					t.Fatal(d)
				}
			})
		}
	}
	kept, d := Occurrences(kata.Trigger{Kind: "interval", IntervalSeconds: new(int64(60))}, start, start, start.Add(200*time.Minute), "all", 0)
	if len(kept) != 100 || d.Count != 100 || !d.First.Equal(start.Add(time.Minute)) || !d.Last.Equal(start.Add(100*time.Minute)) {
		t.Fatal(kept, d)
	}
	_, d = Occurrences(kata.Trigger{Kind: "interval", IntervalSeconds: new(int64(3600))}, start, start, start.Add(8*24*time.Hour), "all", 0)
	if !d.Clipped || d.Count != 69 {
		t.Fatal(d)
	} // 169 instants in inclusive seven-day window, 100 retained.
}

func TestMergeDroppedUniqueAgeAndCap(t *testing.T) {
	now := instant("2026-11-03T00:00:00Z")
	old := Occurrence{Key: "old", At: now.Add(-8 * 24 * time.Hour)}
	c := Checkpoint{EventUID: "event", Pending: []Occurrence{old, old}}
	due := []Occurrence{old}
	for i := 101; i > 0; i-- {
		at := now.Add(-time.Duration(i) * time.Minute)
		due = append(due, Occurrence{Key: at.Format(time.RFC3339), At: at})
	}
	d := c.Merge("event", due, now)
	if d.Count != 2 || !d.First.Equal(old.At) || !d.Last.Equal(now.Add(-101*time.Minute)) || len(c.Pending) != 100 {
		t.Fatal(d, c)
	}
	if d = c.Merge("new", nil, now); d.Count != 0 {
		t.Fatal("definition replacement counted", d)
	}
}

func TestIntervalAccountingConservesInstants(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		period := hegel.Draw(ht, hegel.Integers[int64](1, int64((1<<63-1)/time.Second)))
		seconds := hegel.Draw(ht, hegel.Integers[int64](0, 7*24*3600))
		policy := hegel.Draw(ht, hegel.SampledFrom([]string{"all", "latest", "skip"}))
		grace := hegel.Draw(ht, hegel.Integers[int64](0, 7*24*3600))
		start := instant("2026-11-03T00:00:00.123Z")
		kept, d := Occurrences(kata.Trigger{Kind: "interval", IntervalSeconds: &period}, start, start, start.Add(time.Duration(seconds)*time.Second), policy, time.Duration(grace)*time.Second)
		if int64(len(kept)+d.Count) != seconds/period {
			ht.Fatalf("lost instants: period=%d seconds=%d kept=%v dropped=%+v", period, seconds, kept, d)
		}
	}, hegel.WithTestCases(2000))
}

func TestDroppedAddOrderIndependent(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		a := hegel.Draw(ht, hegel.Integers[int64](-1000000, 1000000))
		b := hegel.Draw(ht, hegel.Integers[int64](-1000000, 1000000))
		x := Dropped{Count: 1, First: time.Unix(a, 0), Last: time.Unix(a, 0)}
		y := Dropped{Count: 1, First: time.Unix(b, 0), Last: time.Unix(b, 0), Clipped: true}
		left, right := x, y
		left.Add(y)
		right.Add(x)
		if left.Count != 2 || !left.First.Equal(time.Unix(min(a, b), 0)) || !left.Last.Equal(time.Unix(max(a, b), 0)) || !left.Clipped || !reflect.DeepEqual(left, right) {
			ht.Fatal(left, right)
		}
	}, hegel.WithTestCases(2000))
}

// The anchored hourly interval and UTC hourly cron describe the same instants.
func TestHourlyCronAndIntervalAccountingAgree(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		seconds := hegel.Draw(ht, hegel.Integers[int64](0, 9*24*3600))
		policy := hegel.Draw(ht, hegel.SampledFrom([]string{"all", "latest", "skip"}))
		grace := hegel.Draw(ht, hegel.Integers[int64](0, 9*24*3600))
		start := instant("2026-11-03T00:00:00Z")
		now := start.Add(time.Duration(seconds) * time.Second)
		cron, cd := Occurrences(kata.Trigger{Kind: "cron", Cron: new("0 * * * *")}, start, start, now, policy, time.Duration(grace)*time.Second)
		interval, id := Occurrences(kata.Trigger{Kind: "interval", IntervalSeconds: new(int64(3600))}, start, start, now, policy, time.Duration(grace)*time.Second)
		if !reflect.DeepEqual(cron, interval) || !reflect.DeepEqual(cd, id) {
			ht.Fatalf("cron=%v/%+v interval=%v/%+v", cron, cd, interval, id)
		}
	}, hegel.WithTestCases(2000))
}
func TestDroppedAddAssociativeWithEmptyClipped(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		var batches [3]Dropped
		total := 0
		var first, last time.Time
		clipped := false
		for i := range batches {
			count := hegel.Draw(ht, hegel.Integers[int](0, 604900))
			second := hegel.Draw(ht, hegel.Integers[int64](-604800, 604800))
			clip := hegel.Draw(ht, hegel.Booleans())
			d := Dropped{Count: count, Clipped: clip}
			if count > 0 {
				d.First = time.Unix(second, 0)
				d.Last = d.First.Add(time.Duration(count-1) * time.Second)
				if total == 0 || d.First.Before(first) {
					first = d.First
				}
				if total == 0 || d.Last.After(last) {
					last = d.Last
				}
			}
			total += count
			clipped = clipped || clip
			batches[i] = d
		}
		left := batches[0]
		left.Add(batches[1])
		left.Add(batches[2])
		tail := batches[1]
		tail.Add(batches[2])
		right := batches[0]
		right.Add(tail)
		if !reflect.DeepEqual(left, right) || left.Count != total || !left.First.Equal(first) || !left.Last.Equal(last) || left.Clipped != clipped {
			ht.Fatal(left, right)
		}
	}, hegel.WithTestCases(2000))
}
func TestMergeConservesUniqueInstants(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		n := min(hegel.Draw(ht, hegel.Integers[int](0, int(^uint(0)>>1))), 300) // Materialize at most 300 entries.
		now := instant("2026-11-03T00:00:00Z")
		c := Checkpoint{Version: 1, EventUID: "event", Through: now.Add(time.Hour)}
		var due []Occurrence
		unique := map[string]time.Time{}
		for range n {
			age := hegel.Draw(ht, hegel.Integers[int](0, 8*24))
			at := now.Add(-time.Duration(age) * time.Hour)
			o := Occurrence{Key: at.Format(time.RFC3339), At: at}
			unique[o.Key] = at
			if hegel.Draw(ht, hegel.Booleans()) {
				c.Pending = append(c.Pending, o)
			} else {
				due = append(due, o)
			}
		}
		d := c.Merge("event", due, now)
		if d.Count+len(c.Pending) != len(unique) || len(c.Pending) > 100 || !c.Through.Equal(now.Add(time.Hour)) {
			ht.Fatalf("lost unique work: %d retained=%v dropped=%+v", len(unique), c.Pending, d)
		}
		for _, o := range c.Pending {
			if o.At.Before(now.Add(-7 * 24 * time.Hour)) {
				ht.Fatal("retained expired work", o)
			}
			delete(unique, o.Key)
		}
		var first, last time.Time
		count := 0
		for _, at := range unique {
			if count == 0 || at.Before(first) {
				first = at
			}
			if count == 0 || at.After(last) {
				last = at
			}
			count++
		}
		if d.Count != count || !d.First.Equal(first) || !d.Last.Equal(last) {
			ht.Fatal("wrong dropped endpoints", d, first, last)
		}
	}, hegel.WithTestCases(2000))
}
