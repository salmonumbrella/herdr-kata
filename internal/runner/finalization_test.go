package runner

import (
	"context"
	"encoding/json"
	"testing"
	"testing/synctest"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

func TestFinalWaitRetainsReportingBarrier(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		comment := operation("report", "comment", Payload{Ref: "example-project#task", Body: "saved report"})
		final := operation("final", "observe", Payload{Observation: &kata.Observation{Status: "failed", ExpectedRevision: 1}})
		q, s, uid, k, c, _ := queueFixture(t, comment, final)
		if _, err := s.Transition(uid, func(r *Run) error { r.Phase, r.Outcome, r.EndedAt = Settled, "failed", new(c.Now()); return nil }); err != nil {
			t.Fatal(err)
		}
		k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) { return kata.Run{}, false, nil }
		k.comment = func(context.Context, string, string, string) error { return nil }
		if err := q.send(t.Context(), uid); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() { done <- q.wait(ctx, uid, final.ID) }()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("resume reporting barrier finished before issue report: %v", err)
		default:
		}
		if err := q.send(t.Context(), uid); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestTerminalDeliveryPreservesResumeObservationOrder(t *testing.T) {
	running := operation("herdr-kata:run:resume:request:observe", "observe", Payload{Observation: &kata.Observation{Status: "running", ExpectedRevision: 2}})
	final := operation("herdr-kata:run:resume:request:settle:observe", "observe", Payload{Observation: &kata.Observation{Status: "failed", ExpectedRevision: 3}})
	q, s, uid, k, c, _ := queueFixture(t, running, final)
	if _, err := s.Transition(uid, func(r *Run) error { r.Phase, r.Outcome, r.EndedAt = Settled, "failed", new(c.Now()); return nil }); err != nil {
		t.Fatal(err)
	}
	statuses := []string{}
	k.observe = func(_ context.Context, _ string, o kata.Observation) (kata.Run, bool, error) {
		statuses = append(statuses, o.Status)
		return kata.Run{}, false, nil
	}
	if err := q.send(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	if err := q.send(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 || statuses[0] != "running" || statuses[1] != "failed" {
		t.Fatal("observations delivered out of revision order", statuses)
	}
}

// A reporting refusal must not hide execution's outcome in Kata. This is the
// production case: an inspected attention/close intent precedes the final run.
func TestFinalObservationBypassesInspectedIssueWrite(t *testing.T) {
	for _, kind := range []string{"close", "meta.set"} {
		t.Run(kind, func(t *testing.T) {
			issue := operation("report", kind, Payload{Ref: "example-project#task"})
			issue.Inspect = "operation effect not confirmed"
			final := operation("herdr-kata:run:settle:observe", "observe", Payload{Observation: &kata.Observation{Status: "failed", ExpectedRevision: 1}})
			q, s, uid, k, c, _ := queueFixture(t, issue, final)
			if _, err := s.Transition(uid, func(r *Run) error {
				r.Phase, r.Outcome, r.Inspect = Settled, "failed", issue.Inspect
				r.EndedAt = new(c.Now())
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			observed := 0
			k.observe = func(_ context.Context, got string, o kata.Observation) (kata.Run, bool, error) {
				if got != uid || o.Status != "failed" || o.ExpectedRevision != 1 {
					t.Fatalf("wrong final observation: %s %+v", got, o)
				}
				observed++
				return kata.Run{}, false, nil
			}
			if err := q.send(t.Context(), uid); err != nil {
				t.Fatal(err)
			}
			r := loaded(t, s, uid)
			if observed != 1 || !r.Ops[1].Landed() || r.Ops[0].Done || r.Inspect != issue.Inspect {
				t.Fatalf("final report stranded or inspection lost: observed=%d run=%+v", observed, r)
			}
		})
	}
}

func TestRecoverFinalObservationReadsBackBeforeRetry(t *testing.T) {
	for _, remote := range []string{"running", "matching", "foreign", "advanced", "different-final"} {
		t.Run(remote, func(t *testing.T) {
			o := kata.Observation{JobUID: "job", DefinitionEventUID: "event", OccurrenceKey: "manual:run", Actor: "worker", ExecutorLabel: "installation", Status: "failed", Summary: kata.Summary{Version: 1, Message: "saved failure"}, ExpectedRevision: 1, Teammate: new("herdr-kata")}
			final := operation("herdr-kata:run:settle:observe", "observe", Payload{Observation: &o})
			final.Inspect = "operation effect not confirmed"
			final.FirstSent = new(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			q, s, uid, k, c, g := queueFixture(t, final)
			if _, err := s.Transition(uid, func(r *Run) error {
				r.Phase, r.Outcome, r.Inspect = Settled, "failed", final.Inspect
				r.EndedAt = new(c.Now())
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			reads, sends := 0, 0
			boundary := &admissionKata{fakeKata: k, run: func(context.Context, string) (kata.Run, error) {
				reads++
				var got kata.Run
				raw, _ := json.Marshal(o)
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatal(err)
				}
				got.UID, got.Revision = uid, 2
				if remote != "matching" {
					got.Status, got.Revision = "running", 1
				}
				if remote == "foreign" {
					got.ExecutorLabel = "other-installation"
				}
				if remote == "advanced" {
					got.Revision = 3
				}
				if remote == "different-final" {
					got.Status, got.Revision = "succeeded", 2
				}
				return got, nil
			}}
			q.k = boundary
			k.observe = func(_ context.Context, got string, body kata.Observation) (kata.Run, bool, error) {
				if reads == 0 || got != uid || body.ExpectedRevision != 1 || body.Summary.Message != "saved failure" {
					t.Fatal("retry changed frozen observation or skipped read-back", body)
				}
				sends++
				return kata.Run{}, false, nil
			}
			m := NewManager(config.Config{}, boundary, nil, s, q, g, c)
			if err := m.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := q.send(t.Context(), uid); err != nil {
				t.Fatal(err)
			}
			r := loaded(t, s, uid)
			wantSends := 0
			if remote == "running" {
				wantSends = 1
			}
			if reads == 0 || sends != wantSends || r.Ops[0].Landed() != (remote == "running" || remote == "matching") {
				t.Fatalf("recovery: reads=%d sends=%d run=%+v", reads, sends, r)
			}
		})
	}
}
