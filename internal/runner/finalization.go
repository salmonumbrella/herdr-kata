package runner

import (
	"context"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

func terminalObservation(op Op) *kata.Observation {
	if op.Kind != "observe" || preAdmissionOp(op) {
		return nil
	}
	p, err := decodePayload(op)
	if err != nil || p.Observation == nil || p.Observation.Status == "running" {
		return nil
	}
	return p.Observation
}

func observationIdentity(got kata.Run, uid string, want *kata.Observation) bool {
	return got.UID == uid && got.JobUID == want.JobUID && got.DefinitionEventUID == want.DefinitionEventUID && got.WorkflowUID == want.WorkflowUID && got.WorkflowDefinitionEventUID == want.WorkflowDefinitionEventUID && got.OccurrenceKey == want.OccurrenceKey && got.IssueUID == want.IssueUID && got.Actor == want.Actor && got.ExecutorLabel == want.ExecutorLabel && sameOptionalString(got.Teammate, want.Teammate)
}

// recoverFinal only reopens the specific old inspection after read-back proves
// the frozen replay-safe observation can still land at its original revision.
// Issue inspections and all sent payloads, timestamps and guards survive.
func (q *Queue) recoverFinal(ctx context.Context, r *Run) error {
	if r.EndedAt == nil || !admitted(r) || preAdmissionHold(r) {
		return nil
	}
	for _, op := range r.Ops {
		want := terminalObservation(op)
		if want == nil || op.Done || op.Inspect != "operation effect not confirmed" || op.SigningRefused {
			continue
		}
		if err := q.checkRoute(ctx); err != nil {
			return err
		}
		got, err := q.k.ShowRun(ctx, r.UID)
		if err != nil {
			return err
		}
		if !observationIdentity(got, r.UID, want) {
			continue
		}
		landed := observationProvenance(got, want) && got.Status == want.Status && got.Summary == want.Summary && sameTime(got.StartedAt, want.StartedAt) && sameTime(got.EndedAt, want.EndedAt) && got.Revision > want.ExpectedRevision
		retry := got.Status == "running" && got.Revision == want.ExpectedRevision
		if !landed && !retry {
			continue
		}
		_, err = q.store.TransitionContext(ctx, r.UID, func(current *Run) error {
			saved, err := findOp(current, op.ID)
			if err != nil {
				return err
			}
			if saved.Done || intent(*saved) != intent(op) || saved.Inspect != op.Inspect {
				return nil
			}
			saved.Done, saved.Inspect, saved.NextAt = landed, "", time.Time{}
			clearResolvedInspection(current, op.Inspect)
			return nil
		})
		if err != nil {
			return err
		}
	}
	q.Kick(r.UID)
	return nil
}

func clearResolvedInspection(r *Run, reason string) {
	if r.Inspect != reason {
		return
	}
	for _, op := range r.Ops {
		if op.Blocked() {
			r.Inspect = op.Inspect
			return
		}
	}
	r.Inspect = ""
}
