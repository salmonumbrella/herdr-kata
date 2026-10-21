package runner

import (
	"context"
	"errors"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/crashpoint"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func sequenceAdvanced(ref HerdrRef, a herdr.Agent) bool {
	return ref.PreSubmitSeq == nil || a.CompletionSeq == nil || *a.CompletionSeq > *ref.PreSubmitSeq
}

// Missing sequences require observed activity before idle can start the
// missing-result grace. A valid attempt-bound result can also prove a fast turn.
func settlementEvidence(r *Run, index int, a herdr.Agent) bool {
	if AgentReference(r).PreSubmitSeq != nil && a.CompletionSeq != nil {
		return *a.CompletionSeq > *AgentReference(r).PreSubmitSeq
	}
	return r.Steps[index].SawActivity
}

func (m *Manager) observeStep(uid string, index int, a herdr.Agent) error {
	r, e := m.store.Load(uid)
	if e != nil {
		return e
	}
	if !m.matches(*AgentReference(r), a) {
		return errUnproven
	}
	activity := a.Status == "working" || a.Status == "blocked"
	if (AgentReference(r).Session != nil || a.Session == nil) && (!activity || r.Steps[index].SawActivity) {
		return nil
	}
	_, e = m.store.Transition(uid, func(r *Run) error {
		if e := m.saveSession(r, a); e != nil {
			return e
		}
		if activity && (r.Steps[index].Submission == Sending || r.Steps[index].Submission == Accepted) {
			r.Steps[index].SawActivity = true
		}
		return nil
	})
	return e
}

func (m *Manager) awaitResult(ctx context.Context, uid string) (*Result, error) {
	return m.awaitStepResult(ctx, uid, 0)
}
func (m *Manager) awaitStepResult(ctx context.Context, uid string, index int) (*Result, error) {
	waitFailures := 0
	for {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		r, e := m.store.Load(uid)
		if e != nil {
			return nil, e
		}
		if r.Phase == Held {
			return nil, nil
		}
		if index < 0 || index >= len(r.Steps) || r.Steps[index].Submission != Accepted && r.Steps[index].Submission != Sending {
			return nil, errors.New("result wait before submission")
		}
		if !m.clock.Now().Before(r.ExpiresAt) {
			return nil, m.hold(uid, "timeout")
		}
		if r.Steps[index].SubmittedAt == nil {
			// Older journals have no timestamp. Save a conservative new grace
			// once, rather than guessing when a possibly accepted prompt landed.
			r, e = m.store.Transition(uid, func(r *Run) error {
				if r.Steps[index].SubmittedAt == nil {
					now := m.clock.Now()
					r.Steps[index].SubmittedAt = &now
				}
				return nil
			})
			if e != nil {
				return nil, e
			}
		}
		graceDeadline := r.Steps[index].SubmittedAt.Add(m.activityGrace)
		a, ae := m.occupant(ctx, *AgentReference(r))
		if ae == nil {
			ae = m.observeStep(uid, index, a)
			if ae == nil && r.Steps[index].Submission == Sending {
				// A recovered in-flight request remains possibly accepted. Once
				// the occupant is reconciled, leave the sending/drain barrier.
				r, e = m.store.Transition(uid, func(r *Run) error {
					if r.Steps[index].Submission == Sending {
						r.Steps[index].Submission = Accepted
					}
					return nil
				})
				if e != nil {
					return nil, e
				}
			} else if ae == nil {
				// Use the newly captured session and durable activity evidence.
				r, e = m.store.Load(uid)
				if e != nil {
					return nil, e
				}
			}
		}
		if errors.Is(ae, errUnproven) {
			return nil, m.hold(uid, "agent_unproven")
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		gone := herdrCode(ae, "agent_not_found") || herdrCode(ae, "pane_not_found")
		stopped := ae == nil && settledAgent(a) && settlementEvidence(r, index, a)
		withoutResult := false
		if ae == nil && settledAgent(a) && !stopped {
			// A fast turn may finish entirely between observations. Only a valid
			// result for this attempt authorizes settlement without activity.
			_, path, e := m.attemptPaths(r, index)
			if e != nil {
				return nil, e
			}
			_, re := ReadResult(m.store.fsys, path, uid, r.Steps[index].Key, r.Steps[index].Attempt)
			var corrupt *state.CorruptError
			if re != nil && !missing(re) && !errors.As(re, &corrupt) {
				return nil, re
			}
			withoutResult = re != nil
			stopped = re == nil && sequenceAdvanced(*AgentReference(r), a)
			if withoutResult && !r.Steps[index].SawActivity && !m.clock.Now().Before(graceDeadline) {
				return nil, m.holdNoResult(r, index, "no_activity", true)
			}
		}
		if stopped || gone {
			res, changed, e := m.pollStepResult(ctx, r, index, gone)
			if e != nil || res != nil {
				return res, e
			}
			if !changed {
				return nil, nil
			}
			continue
		}
		if ae != nil {
			// Failed reads do not create a startup poll.
			if e = m.pause(ctx, min(30*time.Second, r.ExpiresAt.Sub(m.clock.Now()))); e != nil {
				return nil, e
			}
			continue
		}
		until := []string{"idle", "done", "blocked"}
		waitTimeout := r.ExpiresAt.Sub(m.clock.Now())
		activityWait := settledAgent(a) && !settlementEvidence(r, index, a)
		if activityWait {
			until = []string{"working", "blocked"}
			waitTimeout = min(30*time.Second, waitTimeout)
			if withoutResult && !r.Steps[index].SawActivity {
				waitTimeout = min(waitTimeout, graceDeadline.Sub(m.clock.Now()))
			}
		}
		if a.Status == "blocked" {
			if e = m.blockedStep(uid, index, true); e != nil {
				return nil, e
			}
			until = []string{"idle", "done"}
		} else if r.Phase == Blocked {
			if e = m.blockedStep(uid, index, false); e != nil {
				return nil, e
			}
		}
		waitStarted := m.clock.Now()
		e = m.waitStepAgent(ctx, r, index, until, waitTimeout)
		if errors.Is(e, errUnproven) {
			return nil, m.hold(uid, "agent_unproven")
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if herdrCode(e, "timeout") && activityWait {
			// An activity slice timing out is not the attempt deadline. The next
			// iteration checks agent.get and result.json without resubmitting.
			// Pace an early server timeout too; it must not cause a read loop.
			if remaining := waitTimeout - m.clock.Now().Sub(waitStarted); remaining > 0 {
				if e = m.pause(ctx, remaining); e != nil {
					return nil, e
				}
			}
			continue
		}
		if herdrCode(e, "timeout") {
			return nil, m.hold(uid, "timeout")
		}
		// Reconcile every ambiguous reply before deciding whether to wait again.
		if e != nil {
			waitFailures++
			if waitFailures > 1 {
				if e = m.pause(ctx, min(30*time.Second, r.ExpiresAt.Sub(m.clock.Now()))); e != nil {
					return nil, e
				}
			}
		} else {
			waitFailures = 0
		}

	}
}

// pollResult's 2 s loop reads only local artifacts. Herdr is consulted again
// only when evidence arrives or for paced checks that the occupant has stopped.
func (m *Manager) pollResult(ctx context.Context, r *Run, gone bool) (*Result, bool, error) {
	return m.pollStepResult(ctx, r, 0, gone)
}
func (m *Manager) pollStepResult(ctx context.Context, r *Run, index int, gone bool) (*Result, bool, error) {
	_, path, e := m.attemptPaths(r, index)
	if e != nil {
		return nil, false, e
	}
	until := r.ExpiresAt
	reason := "agent_lost"
	if !gone {
		until = minTime(until, m.clock.Now().Add(10*time.Second))
		reason = "no_result"
	}
	var nextExitProof, nextStopProof time.Time
	for {
		if e := ctx.Err(); e != nil {
			return nil, false, e
		}
		if !m.clock.Now().Before(r.ExpiresAt) {
			why := "timeout"
			if gone {
				why = "agent_lost"
			}
			if gone {
				return nil, false, m.holdNoResult(r, index, why, m.paneExited(ctx, *AgentReference(r)) || m.failedStartStopped(ctx, *AgentReference(r)))
			}
			return nil, false, m.holdStopped(r.UID, why, false)
		}
		res, re := ReadResult(m.store.fsys, path, r.UID, r.Steps[index].Key, r.Steps[index].Attempt)
		var corrupt *state.CorruptError
		if re != nil && !missing(re) && !errors.As(re, &corrupt) {
			return nil, false, re
		}
		countResult := re == nil && !gone
		// Missing-artifact probes must not defer a new result's exit proof.
		nextProof := nextExitProof
		if re != nil {
			nextProof = nextStopProof
		}
		if gone && !m.clock.Now().Before(nextProof) {
			if re == nil {
				nextExitProof = m.clock.Now().Add(30 * time.Second)
			} else {
				nextStopProof = m.clock.Now().Add(30 * time.Second)
			}
			exited := m.paneExited(ctx, *AgentReference(r))
			if re != nil && (exited || m.failedStartStopped(ctx, *AgentReference(r))) {
				// A final artifact can arrive during the exit proof RPCs. Give
				// it precedence before permanently holding a stopped attempt.
				res, re = ReadResult(m.store.fsys, path, r.UID, r.Steps[index].Key, r.Steps[index].Attempt)
				if re != nil && !missing(re) && !errors.As(re, &corrupt) {
					return nil, false, re
				}
				if re != nil {
					return nil, false, m.holdNoResult(r, index, "agent_lost", true)
				}
			}
			countResult = re == nil && exited
		}
		if countResult {
			crashpoint.Hit("result.detected")
			if !gone {
				a, ae := m.occupant(ctx, *AgentReference(r))
				if ae == nil {
					if e = m.observeStep(r.UID, index, a); e != nil {
						return nil, false, e
					}
				}
				if ae != nil || !settledAgent(a) || !sequenceAdvanced(*AgentReference(r), a) {
					return nil, true, nil
				}
			}
			if e = m.blockedStep(r.UID, index, false); e != nil {
				return nil, false, e
			}
			_, e = m.store.Transition(r.UID, func(r *Run) error {
				s := &r.Steps[index]
				s.Status = res.Status
				s.Note = res.Note
				s.Result = &ResultRef{Version: res.Version, Run: res.Run, Step: res.Step, Attempt: res.Attempt, Status: res.Status, Summary: res.Summary, Note: res.Note, Assert: res.Assert, Target: res.Target, Evidence: res.Evidence, CloseReason: res.CloseReason}
				return nil
			})
			if e == nil {
				e = m.collectStepUsage(ctx, r.UID, index)
			}
			return res, false, e
		}
		if !m.clock.Now().Before(until) {
			if !gone {
				a, ae := m.occupant(ctx, *AgentReference(r))
				if ae == nil {
					if e = m.observeStep(r.UID, index, a); e != nil {
						return nil, false, e
					}
				}
				if ae != nil || !settledAgent(a) || !settlementEvidence(r, index, a) {
					return nil, true, nil
				}
				// The recheck may have captured a late session identity. Use
				// that saved reference for classification and the hold guard.
				latest, loadErr := m.store.Load(r.UID)
				if loadErr != nil {
					return nil, false, loadErr
				}
				if latest.Steps[index].Attempt != r.Steps[index].Attempt {
					return nil, true, nil
				}
				r = latest
				e = m.holdNoResult(r, index, reason, true)
				return nil, false, e
			}
			return nil, false, m.holdNoResult(r, index, reason, m.paneExited(ctx, *AgentReference(r)) || m.failedStartStopped(ctx, *AgentReference(r)))
		}
		if e = m.pause(ctx, min(2*time.Second, until.Sub(m.clock.Now()))); e != nil {
			return nil, false, e
		}
	}
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (m *Manager) paneExited(ctx context.Context, ref HerdrRef) bool {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	id, e := herdr.ReadServerID(m.h.Path)
	if e != nil || id != ref.ServerID || id == (herdr.ServerID{}) {
		return false
	}
	_, e = m.h.PaneGet(ctx, ref.PaneID)
	return herdrCode(e, "pane_not_found")
}
