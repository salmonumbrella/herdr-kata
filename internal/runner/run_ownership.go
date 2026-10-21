package runner

import (
	"context"
	"encoding/json"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

// Kata attributes agent claims to the lane actor, not its teammate. Such a
// claim does not transfer a runtime-created run issue outside the lane.
func reportingOwner(r *Run, issue kata.IssueView, expected string) bool {
	if issue.Owner == expected {
		return true
	}
	if takeover(r) != nil || !r.IssueOwned || issue.Owner != r.Target.Identity.Actor || issue.Owner == "" || issue.UID != r.IssueUID || issue.ProjectUID != r.Target.Identity.ProjectUID {
		return false
	}
	var uid string
	return json.Unmarshal(issue.Metadata["herdr_kata.run"], &uid) == nil && uid == r.UID
}

// Only an unsent ownership refusal can be reconsidered. Ambiguous writes keep
// their original inspection and frozen guards.
func (q *Queue) recoverReportingOwner(ctx context.Context, r *Run) error {
	for _, op := range r.Ops {
		if op.Done || op.FirstSent != nil || op.SigningRefused || (op.Inspect != "close ownership changed" && op.Inspect != "attention ownership changed") {
			continue
		}
		p, err := decodePayload(op)
		if err != nil {
			return err
		}
		ref := p.Ref
		if p.Close != nil {
			ref = p.Close.Ref
		}
		issue, err := q.showIssue(ctx, r.UID, ref)
		if err != nil {
			return err
		}
		if issue.Status != "open" || !childIssueMatches(r, issue) || !reportingOwner(r, issue, attentionOwner(r)) {
			continue
		}
		_, err = q.store.TransitionContext(ctx, r.UID, func(current *Run) error {
			saved, err := findOp(current, op.ID)
			if err != nil {
				return err
			}
			if !saved.Done && saved.FirstSent == nil && intent(*saved) == intent(op) && saved.Inspect == op.Inspect {
				saved.Inspect = ""
				clearResolvedInspection(current, op.Inspect)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	q.Kick(r.UID)
	return nil
}
