package sched

import (
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"time"
)

// DropInfo retains each discarded range; retained occurrences never appear in it.
type DropInfo struct {
	Dropped  bool
	From, To time.Time // Bounds across all ranges, which may have gaps.
	Ranges   []DropRange
}
type DropRange struct{ From, To time.Time }

func (d *DropInfo) add(from, to time.Time) {
	if from.After(to) {
		return
	}
	if !d.Dropped || from.Before(d.From) {
		d.From = from
	}
	if !d.Dropped || to.After(d.To) {
		d.To = to
	}
	d.Dropped = true
	d.Ranges = append(d.Ranges, DropRange{from, to})
}

func Plan(c Checkpoint, s Schedule, j defs.JobDef, now time.Time) (queue []string, through time.Time, drop DropInfo) {
	c.HandledThrough = c.HandledThrough.UTC()
	now = now.UTC()
	defer func() {
		if len(queue) == 0 || j.Catchup == "all" {
			return
		}
		if len(queue) > 1 {
			first, _ := ident.ParseOccurrenceKey(queue[0])
			last, _ := ident.ParseOccurrenceKey(queue[len(queue)-2])
			drop.add(first, last)
			queue = queue[len(queue)-1:]
		}
		if j.Catchup == "skip" {
			at, _ := ident.ParseOccurrenceKey(queue[0])
			if now.Sub(at) > time.Duration(j.GraceSeconds)*time.Second {
				drop.add(at, at)
				queue = nil
			}
		}
	}()
	through = c.HandledThrough
	queue = append([]string(nil), c.Pending...)
	if !now.After(through) {
		return
	}
	through = now
	from := c.HandledThrough
	cutoff := now.Add(-7 * 24 * time.Hour)
	if from.Before(cutoff) {
		if next := s.Next(from); !next.IsZero() && next.Before(cutoff) {
			drop.add(next, cutoff.Add(-time.Nanosecond))
		}
		from = cutoff.Add(-time.Nanosecond)
	}
	limit := 100
	if j.Catchup != "all" {
		limit = 1
	}
	due, truncated := Between(s, from, now, limit)
	if truncated {
		drop.add(s.Next(from), due[0].Add(-time.Nanosecond))
	}
	if len(due) > 0 && j.Catchup == "skip" && now.Sub(due[len(due)-1]) > time.Duration(j.GraceSeconds)*time.Second {
		drop.add(due[0], due[len(due)-1])
		due = nil
	}
	for _, at := range due {
		queue = append(queue, ident.OccurrenceKey(at))
	}
	if len(queue) > 100 {
		a, _ := ident.ParseOccurrenceKey(queue[0])
		b, _ := ident.ParseOccurrenceKey(queue[len(queue)-101])
		drop.add(a, b)
		queue = queue[len(queue)-100:]
	}
	return
}
