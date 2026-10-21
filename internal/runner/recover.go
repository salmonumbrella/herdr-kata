package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// Recover reconciles the local index before admission. Artifacts can fill an
// unresolved receipt, but never replace a result already copied into run.json.
// The write queue must be running to resolve prepared admissions.
func (m *Manager) Recover(ctx context.Context) error {
	recovery := m.recoveryLock(m.projectUID)
	if err := lockControl(ctx, recovery); err != nil {
		return err
	}
	defer recovery.Unlock()
	ids, err := m.store.projectCandidates(ctx, m.projectUID)
	if err != nil {
		return err
	}

	// Reconstruct durable reservations before resuming preparation. A lowered
	// capacity limit cannot revoke work already reserved by a previous worker.
	for _, uid := range ids {
		r, e := m.store.LoadContext(ctx, uid)
		if missing(e) || m.store.quarantinedError(uid, e) {
			continue
		}
		if e != nil {
			return e
		}
		if holds(r) {
			m.mu.Lock()
			m.reservations[uid] = m.runGate(r)
			m.mu.Unlock()
		}
	}
	if m.guard != nil {
		check := m.guard.Check
		// Recovery runs as a lane starts, right after its passing route check.
		if fresh, ok := m.guard.(interface {
			CheckFresh(context.Context, time.Duration) error
		}); ok {
			check = func(ctx context.Context) error { return fresh.CheckFresh(ctx, kata.StartupRouteReuse) }
		}
		if err := check(ctx); err != nil {
			return err
		}
	}
	for _, uid := range ids {
		if err = ctx.Err(); err != nil {
			return err
		}
		r, e := m.store.LoadContext(ctx, uid)
		var corrupt *state.CorruptError
		if missing(e) || m.store.quarantinedError(uid, e) {
			continue
		}
		if e != nil {
			return e
		}
		lane := m.forRun(r)
		if lane == nil || m.projectUID != "" && lane != m {
			continue
		}
		if m.projectUID == "" && m.projects {
			continue
		}
		if _, active := m.executions.Load(uid); active && admitted(r) {
			continue
		}
		if requestAdmissionUnconfirmed(r) {
			_, e := m.Admit(ctx, sched.Due{IssueRequest: &sched.IssueRequestDue{IssueUID: r.IssueUID, IssueRef: r.IssueRef}})
			if e != nil {
				slog.Warn("issue request receipt recovery", "run", uid, "error", e)
			}
			continue
		}
		if r.Target.Identity.InstanceUID != "" {
			target, e := m.pin()
			if e != nil {
				return e
			}
			if !sameTarget(r.Target, target) {
				return fmt.Errorf("run %s target differs from pinned installation", uid)
			}
		}
		if m.q != nil {
			if e = m.q.recoverFinal(ctx, r); e != nil {
				return e
			}
			if e = m.q.recoverReportingOwner(ctx, r); e != nil {
				return e
			}
		}
		if r.Inspect != "" {
			continue
		}
		if preAdmissionHold(r) {
			continue
		}
		if r.Resume != nil && r.Resume.Pending {
			// Controls process queued releases before any saved resume activation.
			// Keep startup independent of the pending queue barrier.
			if m.q != nil {
				m.q.Kick(uid)
			}
			continue
		}
		if r.Phase == HandedOff || r.Phase == Held || r.Phase == Settled || r.EndedAt != nil {
			if r.EndedAt == nil && (r.Phase == HandedOff || r.Phase == Held && r.Outcome != "") {
				if e = m.settle(ctx, uid, nil); e != nil {
					return e
				}
			}
			if m.q != nil {
				m.q.Kick(uid)
			}
			if r.Phase == Settled && r.Outcome == "succeeded" {
				if e = m.cleanup(ctx, uid); e != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					slog.Warn("run tab retained", "run", uid, "error", e)
				}
			}
			continue
		}
		if r.Phase == Prepared {
			// Incomplete legacy journals remain reserved for inspection.
			if r.Admission == nil && r.IssueRequest == nil {
				_, e = m.store.Transition(uid, func(r *Run) error { r.Inspect = "missing frozen admission"; return nil })
				if e != nil {
					return e
				}
				continue
			}
			if r.IssueRequest != nil {
				// Reuse admission gates; the existing journal supplies frozen input.
				_, e := m.Admit(ctx, sched.Due{IssueRequest: &sched.IssueRequestDue{IssueUID: r.IssueUID, IssueRef: r.IssueRef}})
				if e != nil {
					slog.Warn("issue request recovery held", "run", uid, "error", e)
				}
				continue
			}
			at, e := ident.ParseOccurrenceKey(r.Occurrence)
			if r.Manual {
				at = r.ExpiresAt.Add(-m.cfg.Limits.DefaultTimeout)
			}
			if e != nil && !r.Manual {
				return e
			}
			due := sched.Due{Job: r.Job, Def: r.JobDef, Workflow: r.Workflow, WFDef: r.WFDef, Occurrence: at, Manual: r.Manual, ManualRunUID: uid}
			if r.Child != nil {
				due.Child = &r.Child.ChildDue
			}
			_, e = m.Admit(ctx, due)
			if e != nil {
				latest, loadErr := m.store.Load(uid)
				if loadErr == nil && preAdmissionHold(latest) {
					continue
				}
				return e
			}
			continue
		}
		// Notify jobs have no attempts; their executor freezes the delivery batch.
		if r.JobDef.Action.Kind == "notify" {
			if e = m.Execute(ctx, uid); e != nil {
				return e
			}
			continue
		}
		// Every step is examined, including mixed workflows whose current attempt
		// is not the first step. No command is executed during reconciliation.
		r, e = m.store.Transition(uid, func(r *Run) error {
			for i := range r.Steps {
				s := &r.Steps[i]
				if s.Kind != "command" || s.Status == "skipped" || s.Status == "unknown" {
					continue
				}
				if s.Status == "" && s.Submission != Sending && s.Submission != Accepted {
					continue
				}
				receipt := s.Receipt
				if receipt == nil && validUID(s.Key) && s.Attempt > 0 {
					var saved Receipt
					re := state.ReadJSON(m.store.fsys, filepath.Join(m.store.layout.AttemptDir(uid, s.Key, s.Attempt), "receipt.json"), 64<<10, &saved)
					if re == nil {
						receipt = &saved
					} else if !missing(re) && !errors.As(re, &corrupt) {
						return re
					}
				}
				if validReceipt(receipt, uid, *s) && (s.Status == "succeeded" || s.Status == "failed") {
					continue
				}
				if !validReceipt(receipt, uid, *s) {
					s.Status = "unknown"
					s.Reason = "command_no_receipt"
				} else {
					s.Receipt = receipt
					if receipt.definite() {
						note, e := m.commandNote(r, i)
						if e != nil {
							s.Status = "unknown"
							s.Reason = "invalid_note"
							continue
						}
						s.Note = note
					}
					switch {
					case !receipt.definite():
						s.Status = "unknown"
						s.Reason = "command_uncertain"
					case receipt.ExitCode == 0:
						s.Status = "succeeded"
					case !GroupGone(receipt.PGID):
						s.Status = "unknown"
						s.Reason = "command_group_unproven"
					default:
						s.Status = "failed"
						s.Reason = fmt.Sprintf("command %s exited %d", s.Key, receipt.ExitCode)
					}
				}
			}
			return nil
		})
		if e != nil {
			return e
		}
		var result *Result
		for _, s := range r.Steps {
			if s.Result != nil {
				result = resultFromRef(s.Result)
			}
			if s.Status == "unknown" {
				if e = m.hold(uid, s.Reason); e != nil {
					return e
				}
				if e = m.settle(ctx, uid, nil); e != nil {
					return e
				}
				break
			}
			if s.Status == "needs-human" {
				if e = m.settle(ctx, uid, result); e != nil {
					return e
				}
				break
			}
		}
		r, e = m.store.Load(uid)
		if e != nil {
			return e
		}
		if r.Phase == Held || r.Phase == HandedOff {
			continue
		}
		if pendingLoop(r) {
			e = m.holdPendingLoop(ctx, uid)
			if e != nil {
				return e
			}
			if e = m.settle(ctx, uid, nil); e != nil {
				return e
			}
			continue
		}
		// The workflow executor owns dependency skips, retries and aggregation.
		// Recovery supplies receipts and reconciles only unresolved prompts.
		if len(r.Steps) == 0 {
			_, e = m.store.Transition(uid, func(r *Run) error { r.Inspect = "missing frozen steps"; return nil })
			if e != nil {
				return e
			}
			continue
		}
		resume := true
		for index, s := range r.Steps {
			if s.Status != "" && !retryRemaining(r, s) || s.Kind != "prompt" {
				continue
			}
			sent := !retryRemaining(r, s) && (s.Submission == Sending || s.Submission == Accepted)
			if !sent && r.ActiveStep != "" && r.ActiveStep != s.Key {
				break
			}
			if AgentReference(r).AgentName == "" {
				if sent {
					e = m.hold(uid, "agent_lost")
					if e == nil {
						e = m.settle(ctx, uid, nil)
					}
					resume = false
				}
				break
			}
			if m.h == nil {
				return errors.New("herdr client unavailable during recovery")
			}
			a, ae := m.occupant(ctx, *AgentReference(r))
			if ae == nil {
				if !sent && readyAgent(a) {
					if AgentReference(r).Session == nil && AgentReference(r).LaunchIntent && a.Session != nil {
						r, e = m.store.Transition(uid, func(r *Run) error { AgentReference(r).Session = a.Session; return nil })
						if e != nil {
							return e
						}
					}
					_, ae = m.proof(ctx, *AgentReference(r))
				}
			}
			if ae == nil {
				if sent {
					if e = m.observeStep(uid, index, a); e != nil {
						return e
					}
				}
				break
			}
			gone := herdrCode(ae, "agent_not_found") || herdrCode(ae, "pane_not_found")
			if gone && !sent {
				if retryRemaining(r, s) && m.paneExited(ctx, *AgentReference(r)) {
					break
				}
				// Nothing was sent. Drop stale provenance and create a fresh tab/agent.
				_, e = m.store.Transition(uid, func(r *Run) error {
					report := AgentReference(r).ReportBin
					retainAgent(r, index)
					*AgentReference(r) = HerdrRef{ReportBin: report}
					return nil
				})
				break
			}
			if gone && sent {
				res, re := ReadResult(m.store.fsys, filepath.Join(m.store.layout.AttemptDir(uid, s.Key, s.Attempt), "result.json"), uid, s.Key, s.Attempt)
				if re == nil && m.paneExited(ctx, *AgentReference(r)) {
					_, e = m.store.Transition(uid, func(r *Run) error {
						for i := range r.Steps {
							if r.Steps[i].Key == s.Key {
								r.Steps[i].Status = res.Status
								r.Steps[i].Note = res.Note
								r.Steps[i].Result = refFromResult(res)
								if r.Steps[i].Herdr == nil && !retryRemaining(r, r.Steps[i]) {
									retainAgent(r, i)
									report := r.Herdr.ReportBin
									r.Herdr = HerdrRef{ReportBin: report}
								}
							}
						}
						return nil
					})
					if e == nil && res.Status == "needs-human" {
						e = m.finish(ctx, uid, res)
						resume = false
					} else if e == nil {
						resume = true
					}
				} else {
					e = m.holdNoResult(r, index, "agent_lost", m.paneExited(ctx, *AgentReference(r)) || m.failedStartStopped(ctx, *AgentReference(r)))
					if e == nil {
						e = m.settle(ctx, uid, nil)
					}
					resume = false
				}
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			e = m.hold(uid, "recovery_unproven")
			if e == nil {
				e = m.settle(ctx, uid, nil)
			}
			resume = false
			break
		}
		if e != nil {
			return e
		}
		if resume {
			// Preserve a blocked episode; Execute also accepts running and blocked
			// phases, so restarting does not enqueue another blocked notification.
			if e = m.Execute(ctx, uid); e != nil {
				return e
			}
		}
	}
	slog.Debug("run recovery complete", "runs", len(ids))
	return nil
}

// boundReceipt proves identity independently of timing and outcome certainty.
func boundReceipt(r *Receipt, uid string, s StepState) bool {
	return r != nil && r.Version == 1 && r.Run == uid && r.Step == s.Key && r.Attempt == s.Attempt && s.Attempt > 0
}

func certainReceipt(uid string, s StepState) bool {
	return validReceipt(s.Receipt, uid, s) && s.Receipt.definite()
}

func validReceipt(r *Receipt, uid string, s StepState) bool {
	return boundReceipt(r, uid, s) && !r.StartedAt.IsZero() && !r.EndedAt.IsZero() && !r.EndedAt.Before(r.StartedAt)
}
func refFromResult(r *Result) *ResultRef {
	return &ResultRef{Version: r.Version, Run: r.Run, Step: r.Step, Attempt: r.Attempt, Status: r.Status, Summary: r.Summary, Note: r.Note, Assert: r.Assert, Target: r.Target, Evidence: r.Evidence, CloseReason: r.CloseReason}
}

func retryRemaining(r *Run, s StepState) bool {
	if s.Status != "failed" || r.WFDef == nil {
		return false
	}
	for _, def := range r.WFDef.Steps {
		if def.Key == s.Key && s.Attempt-s.Uncharged <= def.Retries {
			if s.Kind == "command" {
				return certainReceipt(r.UID, s) && GroupGone(s.Receipt.PGID)
			}
			return s.Result != nil && s.Result.Status == "failed"
		}
	}
	return false
}
