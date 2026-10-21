package runner

import (
	"context"
	"errors"
)

// Notify jobs use the same durable ordered queue as execution reporting. They
// have no readiness requirement and never take ownership of the target issue.
func (m *Manager) executeNotify(ctx context.Context, uid string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := m.store.Transition(uid, func(r *Run) error {
		if r.EndedAt != nil {
			return nil
		}
		if r.IssueOwned || r.IssueRef == "" || r.JobDef.Issue == nil || r.JobDef.Issue.Kind != "existing" {
			return errors.New("notify requires an existing issue")
		}
		to := r.JobDef.Action.Recipient
		if to == "current-owner-or-author" {
			to = ""
		}
		message := r.JobDef.Action.Message
		if message == "" {
			message = "Kata cron: " + r.Job.Name + " is due"
		}
		now := m.clock.Now().UTC()
		r.StartedAt = &now
		r.EndedAt = &now
		r.Phase = Settled
		r.Outcome = "succeeded"
		observation := AdmissionBody(r)
		observation.Status = "succeeded"
		observation.ExpectedRevision = 1
		observation.StartedAt = &now
		observation.EndedAt = &now
		observation.Summary.Message = capText(message, maxSummaryBytes)
		r.Ops = append(r.Ops,
			newOp("notify-recipient", "resolve-recipient", Payload{Ref: r.IssueRef, To: to}, ""),
			newOp("notify-deliver", "notify", Payload{Ref: r.IssueRef, Message: message}, ""),
			newOp("notify-observe", "observe", Payload{Observation: &observation}, ""),
		)
		return nil
	})
	if err == nil && m.q != nil {
		m.q.Kick(uid)
	}
	return err
}
