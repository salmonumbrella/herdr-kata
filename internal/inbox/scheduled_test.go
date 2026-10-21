package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

func (r *scanReader) PlanningDates(_ context.Context, ref string) (kata.PlanningDates, error) {
	r.planReads = append(r.planReads, ref)
	if r.planErr != nil {
		return kata.PlanningDates{}, r.planErr
	}
	return r.planning[ref], nil
}

// scheduledFixture is an explicit request whose child Kata hides from ready
// until its scheduled_on instant, five minutes after the fixture clock.
func scheduledFixture(t *testing.T) (*Scanner, *scanReader, *scanClock, chan sched.Due, *Snapshot) {
	t.Helper()
	s, r, clock, got := scanFixture(t)
	child := r.issues[r.requests[0].Ref]
	delete(child.Metadata, "cron.occurrence")
	child.Metadata["herdr_kata.request"] = json.RawMessage(`{"version":1,"kind":"execute","request_uid":"01K00000000000000000000004","requested_at":"2026-01-02T03:04:05Z"}`)
	child.Metadata["scheduled_on"] = json.RawMessage(`"2026-10-13T00:05:00Z"`)
	r.issues[child.QualifiedID] = child
	parent := r.issues[parentUID]
	parent.Status = "open"
	r.issues[parentUID] = parent
	r.ready = nil
	r.planning = map[string]kata.PlanningDates{child.QualifiedID: {IssueUID: child.UID, Revision: 2, Scheduled: &kata.PlanningDate{Field: "scheduled_on", Value: "2026-10-13T00:05:00Z", Timezone: "UTC", Instant: time.Date(2026, 10, 13, 0, 5, 0, 0, time.UTC)}}}
	snapshot := &Snapshot{}
	s.SaveSnapshot = func(v Snapshot) error { *snapshot = v; return nil }
	return s, r, clock, got, snapshot
}

func noDue(t *testing.T, got chan sched.Due) {
	t.Helper()
	select {
	case d := <-got:
		t.Fatalf("scheduled request dispatched before its date: %+v", d)
	default:
	}
}

func TestScheduledRequestWakesAtItsDate(t *testing.T) {
	s, r, clock, got, snapshot := scheduledFixture(t)
	startScanner(t, s)
	clock.fire(nextTimer(t, clock))
	wake := nextTimer(t, clock)
	if want := time.Date(2026, 10, 13, 0, 5, 1, 0, time.UTC); !wake.at.Equal(want) {
		t.Fatalf("wake at %s, want %s", wake.at, want)
	}
	noDue(t, got)
	if len(snapshot.Requests) != 1 || snapshot.Requests[0].Status != WaitingReady || snapshot.Requests[0].Reason != "scheduled for 2026-10-13T00:05:00Z" {
		t.Fatalf("%+v", snapshot.Requests)
	}
	r.ready = []kata.IssueView{r.issues["test#cw1k"]}
	clock.fire(wake)
	d := takeDue(t, got)
	if d.IssueRequest == nil || d.IssueRequest.IssueUID != r.issues["test#cw1k"].UID {
		t.Fatalf("%+v", d)
	}
}

func TestRescheduledRequestReplacesItsWake(t *testing.T) {
	s, r, clock, got, snapshot := scheduledFixture(t)
	startScanner(t, s)
	clock.fire(nextTimer(t, clock))
	if first := nextTimer(t, clock); !first.at.Equal(time.Date(2026, 10, 13, 0, 5, 1, 0, time.UTC)) {
		t.Fatal("first wake", first.at)
	}
	reschedule(r, "test#cw1k", time.Date(2026, 10, 13, 0, 8, 0, 0, time.UTC))
	s.Poke()
	clock.fire(nextTimer(t, clock))
	if second := nextTimer(t, clock); !second.at.Equal(time.Date(2026, 10, 13, 0, 8, 1, 0, time.UTC)) {
		t.Fatal("rescheduled wake", second.at)
	}
	noDue(t, got)
	if snapshot.Requests[0].Reason != "scheduled for 2026-10-13T00:08:00Z" {
		t.Fatal(snapshot.Requests[0].Reason)
	}
}

func TestReachedDateArmsNoWake(t *testing.T) {
	for name, edit := range map[string]func(*scanReader){
		"date passed but child blocked": func(r *scanReader) {
			p := r.planning["test#cw1k"]
			p.Scheduled.Instant = time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
		},
		"unreadable planning dates": func(r *scanReader) { r.planErr = errors.New("daemon unavailable") },
	} {
		t.Run(name, func(t *testing.T) {
			s, r, clock, got, snapshot := scheduledFixture(t)
			edit(r)
			startScanner(t, s)
			clock.fire(nextTimer(t, clock))
			if slow := nextTimer(t, clock); !fallbackWake(clock, slow) {
				t.Fatal("unexpected wake", slow.at)
			}
			noDue(t, got)
			if len(snapshot.Requests) != 1 || snapshot.Requests[0].Status != WaitingReady || snapshot.Requests[0].Reason != "child is not ready" {
				t.Fatalf("%+v", snapshot.Requests)
			}
		})
	}
}

func TestOnlyScheduledRequestsReadPlanningDates(t *testing.T) {
	for name, edit := range map[string]func(*scanReader){
		"no scheduled_on": func(r *scanReader) { delete(r.issues["test#cw1k"].Metadata, "scheduled_on") },
		"native child": func(r *scanReader) {
			c := validChild()
			c.Metadata["notify.d29ya2VyL2luYm94"] = json.RawMessage(`{"from":"system"}`)
			c.Metadata["scheduled_on"] = json.RawMessage(`"2026-10-13T00:05:00Z"`)
			r.issues[c.QualifiedID] = c
		},
		"ready request": func(r *scanReader) { r.ready = []kata.IssueView{r.issues["test#cw1k"]} },
	} {
		t.Run(name, func(t *testing.T) {
			s, r, _, _, _ := scheduledFixture(t)
			edit(r)
			if err := s.scan(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(r.planReads) != 0 {
				t.Fatal("planning dates read", r.planReads)
			}
		})
	}
}

func TestFailedScanKeepsScheduledWake(t *testing.T) {
	s, r, clock, got, _ := scheduledFixture(t)
	startScanner(t, s)
	clock.fire(nextTimer(t, clock))
	nextTimer(t, clock)
	r.inboxErr = errors.New("daemon unavailable")
	s.Poke()
	clock.fire(nextTimer(t, clock))
	if wake := nextTimer(t, clock); !wake.at.Equal(time.Date(2026, 10, 13, 0, 5, 1, 0, time.UTC)) {
		t.Fatal("wake after failed scan", wake.at)
	}
	noDue(t, got)
}

// reschedule changes a child's scheduled_on as Kata does: a new value and a
// new revision.
func reschedule(r *scanReader, ref string, at time.Time) {
	child := r.issues[ref]
	child.Revision++
	child.Metadata["scheduled_on"] = json.RawMessage(`"` + at.Format(time.RFC3339) + `"`)
	r.issues[ref] = child
	r.planning[ref] = kata.PlanningDates{IssueUID: child.UID, Revision: child.Revision, Scheduled: &kata.PlanningDate{Field: "scheduled_on", Value: at.Format(time.RFC3339), Timezone: "UTC", Instant: at}}
}

// Scans run every few seconds while the worker is up. A waiting child's
// date is read once per change of the child, not once per scan.
func TestScheduledDateReadOncePerChange(t *testing.T) {
	s, r, _, _, snapshot := scheduledFixture(t)
	for range 5 {
		if err := s.scan(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.planReads) != 1 || s.scheduled != time.Date(2026, 10, 13, 0, 5, 0, 0, time.UTC) {
		t.Fatal("unchanged child", r.planReads, s.scheduled)
	}
	reschedule(r, "test#cw1k", time.Date(2026, 10, 13, 0, 8, 0, 0, time.UTC))
	for range 5 {
		if err := s.scan(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.planReads) != 2 || s.scheduled != time.Date(2026, 10, 13, 0, 8, 0, 0, time.UTC) || snapshot.Requests[0].Reason != "scheduled for 2026-10-13T00:08:00Z" {
		t.Fatal("rescheduled child", r.planReads, s.scheduled, snapshot.Requests)
	}
}
