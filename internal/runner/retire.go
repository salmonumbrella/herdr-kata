package runner

import (
	"context"
	"encoding/json"
	"strings"
)

// RetireProject hands unfinished journals to the operator without running any
// admission, attempt or occupant command. Settled evidence stays unchanged.
// A registered reporting-only lane also recovers its saved terminal reports.
func (m *Manager) RetireProject(ctx context.Context, project string) error {
	recovery := m.recoveryLock(project)
	if err := lockControl(ctx, recovery); err != nil {
		return err
	}
	defer recovery.Unlock()
	ids, err := m.store.projectCandidates(ctx, project)
	if err != nil {
		return err
	}
	for _, uid := range ids {
		r, err := m.store.TransitionContext(ctx, uid, func(r *Run) error {
			if r.Target.Identity.ProjectUID != project || r.Phase == Settled || r.Phase == HandedOff || preAdmissionHold(r) {
				return nil
			}
			accepted := admitted(r)
			r.Admitted = new(accepted)
			r.Phase, r.Reason, r.SlotReleased = HandedOff, "project removed from configuration", true
			if r.Outcome == "" {
				r.Outcome = "unknown"
			}
			now := m.clock.Now().UTC()
			r.EndedAt = &now
			for i := range r.Ops {
				op := &r.Ops[i]
				if op.Done {
					continue
				}
				if op.FirstSent == nil {
					op.retire("cancelled by lane retirement")
				} else if !accepted {
					op.Inspect = "project removed before admission was confirmed"
					r.Inspect = op.Inspect
				}
			}
			if r.Resume != nil {
				r.Resume.Pending = false
			}
			if accepted {
				ops := SettleOps(r, nil, now)
				for i := range ops {
					ops[i].ID = strings.Replace(ops[i].ID, ":settle:", ":retire:", 1)
					if p, err := decodePayload(ops[i]); err != nil {
						return err
					} else if p.Observation != nil {
						for _, prior := range r.Ops {
							if prior.Kind != "observe" || prior.FirstSent == nil || prior.Done {
								continue
							}
							body, err := decodePayload(prior)
							if err != nil {
								return err
							}
							if body.Observation != nil {
								p.Observation.ExpectedRevision = max(p.Observation.ExpectedRevision, body.Observation.ExpectedRevision+1)
							}
						}
						ops[i].Payload, err = json.Marshal(p)
						if err != nil {
							return err
						}
					}
					if ops[i].Key != "" {
						ops[i].Key += "-retire"
					}
				}
				r.Ops = append(r.Ops, ops...)
			}
			return nil
		})
		if missing(err) || m.store.quarantinedError(uid, err) {
			continue
		}
		if err != nil {
			return err
		}
		m.unreserve(uid)
		if m.projectUID == project && m.q != nil {
			if err := m.q.recoverFinal(ctx, r); err != nil {
				return err
			}
			if err := m.q.recoverReportingOwner(ctx, r); err != nil {
				return err
			}
			m.q.Kick(uid)
		}
	}
	return nil
}
