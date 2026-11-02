// Package sched implements Kata's portable cron, anchored intervals and catch-up.
package sched

import (
	"fmt"
	"slices"
	"strings"
	"time"

	cron "github.com/netresearch/go-cron"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type Occurrence struct {
	Key string
	At  time.Time
}
type interval struct {
	anchor time.Time
	period time.Duration
}

func (s interval) Next(t time.Time) time.Time {
	t = t.UTC()
	if t.Before(s.anchor) {
		return s.anchor
	}
	seconds := int64(s.period / time.Second)
	elapsed := t.Unix() - s.anchor.Unix()
	fraction := time.Duration(t.Nanosecond() - s.anchor.Nanosecond())
	if fraction < 0 {
		elapsed--
		fraction += time.Second
	}
	left := seconds - elapsed%seconds
	return t.Add(time.Duration(left-1) * time.Second).Add(time.Second - fraction)
}

type once struct{ at time.Time }

func (s once) Next(t time.Time) time.Time {
	if t.Before(s.at) {
		return s.at
	}
	return time.Time{}
}
func val(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func compile(t kata.Trigger, created time.Time) (cron.Schedule, error) {
	switch t.Kind {
	case "interval":
		if t.IntervalSeconds == nil || *t.IntervalSeconds <= 0 || *t.IntervalSeconds > int64((1<<63-1)/time.Second) {
			return nil, fmt.Errorf("invalid interval")
		}
		return interval{created.UTC(), time.Duration(*t.IntervalSeconds) * time.Second}, nil
	case "once":
		at, err := time.Parse(time.RFC3339Nano, val(t.At))
		return once{at}, err
	case "manual", "issue-scheduled":
		return once{}, nil
	case "cron":
		zone := val(t.Timezone)
		expr := strings.TrimSpace(val(t.Cron))
		fields := strings.Fields(expr)
		if len(fields) > 0 && (strings.HasPrefix(fields[0], "TZ=") || strings.HasPrefix(fields[0], "CRON_TZ=")) {
			embedded := strings.SplitN(fields[0], "=", 2)[1]
			if zone != "" && embedded != zone {
				return nil, fmt.Errorf("embedded timezone conflicts with trigger timezone")
			}
			zone = embedded
			expr = strings.TrimSpace(strings.TrimPrefix(expr, fields[0]))
		}
		if zone == "" {
			zone = "UTC"
		}
		if _, err := time.LoadLocation(zone); err != nil {
			return nil, err
		}
		parsed, err := cron.MustNewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor | cron.DowOrDom).Parse("CRON_TZ=" + zone + " " + expr)
		if delay, ok := parsed.(cron.ConstantDelaySchedule); ok {
			return interval{created.UTC(), delay.Delay}, err
		}
		return parsed, err
	}
	return nil, fmt.Errorf("unsupported trigger: %s", t.Kind)
}

// Validate exposes compilation failures before a job enters scheduling.
func Validate(t kata.Trigger, created time.Time) error {
	_, err := compile(t, created)
	return err
}

// Occurrences returns the newest bounded occurrences in (after, now].
func Occurrences(trig kata.Trigger, created, after, now time.Time, catchup string, grace time.Duration) []Occurrence {
	now = now.UTC()
	after = after.UTC()
	if !now.After(after) {
		return nil
	}
	if cutoff := now.Add(-7 * 24 * time.Hour); after.Before(cutoff) {
		after = cutoff.Add(-time.Nanosecond)
	}
	s, err := compile(trig, created)
	if err != nil {
		return nil
	}
	limit := 100
	if catchup != "all" {
		limit = 1
	}
	var at []time.Time
	if i, ok := s.(interval); ok {
		last := i.Next(now.Add(-i.period))
		for !last.After(now) && last.After(after) && !last.Before(i.anchor) && len(at) < limit {
			at = append(at, last)
			last = last.Add(-i.period)
		}
		slices.Reverse(at)
	} else {
		head := 0
		for next := s.Next(after); !next.IsZero() && !next.After(now); next = s.Next(next) {
			if len(at) < limit {
				at = append(at, next)
			} else {
				at[head] = next
				head = (head + 1) % limit
			}
		}
		if head != 0 {
			at = append(append([]time.Time{}, at[head:]...), at[:head]...)
		}
	}
	if len(at) > 0 && catchup == "skip" && now.Sub(at[len(at)-1]) > grace {
		return nil
	}
	out := make([]Occurrence, 0, len(at))
	for _, t := range at {
		t = t.UTC()
		out = append(out, Occurrence{Key: t.Format(time.RFC3339Nano), At: t})
	}
	return out
}

type Checkpoint struct {
	Version          int
	JobUID, EventUID string
	Through          time.Time
	Pending          []Occurrence
}

// Merge supersedes old-definition pending work and retains a bounded due queue.
func (c *Checkpoint) Merge(event string, due []Occurrence, now time.Time) {
	now = now.UTC()
	c.Through = c.Through.UTC()
	if c.Version == 0 {
		c.Version = 1
	}
	if c.EventUID != event {
		c.Pending = nil
		c.EventUID = event
	}
	if now.After(c.Through) {
		c.Through = now
	}
	seen := make(map[string]bool)
	out := make([]Occurrence, 0, len(c.Pending)+len(due))
	for _, o := range append(slices.Clone(c.Pending), due...) {
		if !seen[o.Key] && !o.At.Before(now.Add(-7*24*time.Hour)) {
			seen[o.Key] = true
			out = append(out, o)
		}
	}
	slices.SortFunc(out, func(a, b Occurrence) int {
		if n := a.At.Compare(b.At); n != 0 {
			return n
		}
		return strings.Compare(a.Key, b.Key)
	})
	if len(out) > 100 {
		out = out[len(out)-100:]
	}
	c.Pending = out
}
