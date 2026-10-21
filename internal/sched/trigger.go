package sched

import (
	"fmt"
	"github.com/netresearch/go-cron"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"time"
)

// Schedule.Next is strictly after its argument; zero means no future occurrence.
type Schedule interface{ Next(time.Time) time.Time }
type interval struct {
	anchor time.Time
	period time.Duration
}

func (s interval) Next(t time.Time) time.Time {
	t = t.UTC()
	if t.Before(s.anchor) {
		return s.anchor
	}
	// Work in seconds so a long-lived anchor does not overflow time.Duration.
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
func Compile(t defs.Trigger, createdAt time.Time) (Schedule, error) {
	createdAt = createdAt.UTC()
	if e := defs.ValidateTrigger(t); e != nil {
		return nil, e
	}
	switch t.Kind {
	case "interval":
		return interval{createdAt, time.Duration(t.IntervalSeconds) * time.Second}, nil
	case "once":
		at, _ := time.Parse(time.RFC3339Nano, t.At)
		return once{at}, nil
	case "manual":
		return once{}, nil
	case "cron":
		s, e := defs.CronExpression(t)
		if e != nil {
			return nil, e
		}
		if d, ok := s.(cron.ConstantDelaySchedule); ok {
			return interval{createdAt, d.Delay}, nil
		}
		return s, nil
	}
	return nil, fmt.Errorf("unsupported trigger")
}

// Between returns the newest max occurrences in (from,to], in order. The bool
// reports truncation. Interval enumeration is constant in the size of history.
func Between(s Schedule, from, to time.Time, max int) ([]time.Time, bool) {
	from, to = from.UTC(), to.UTC()
	if max <= 0 || !to.After(from) {
		return nil, false
	}
	if i, ok := s.(interval); ok {
		last := i.Next(to.Add(-i.period))
		if last.After(to) || last.Before(i.anchor) {
			return nil, false
		}
		q := make([]time.Time, 0, max)
		for last.After(from) && !last.Before(i.anchor) && len(q) < max {
			q = append(q, last)
			last = last.Add(-i.period)
		}
		dropped := last.After(from) && !last.Before(i.anchor)
		for a, b := 0, len(q)-1; a < b; a, b = a+1, b-1 {
			q[a], q[b] = q[b], q[a]
		}
		return q, dropped
	}
	q := make([]time.Time, 0, max)
	dropped := false
	head := 0
	for next := s.Next(from); !next.IsZero() && !next.After(to); next = s.Next(next) {
		if len(q) < max {
			q = append(q, next)
		} else {
			q[head] = next
			head = (head + 1) % max
			dropped = true
		}
	}
	if head != 0 {
		q = append(append([]time.Time{}, q[head:]...), q[:head]...)
	}
	return q, dropped
}
