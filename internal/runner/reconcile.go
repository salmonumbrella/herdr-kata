package runner

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// ReconcileHeld returns capacity only after proving a held occupant stopped.
// The outcome, attempts, reports and overlap gate remain held for an operator.
func (m *Manager) ReconcileHeld(ctx context.Context) error {
	ids, err := m.store.scanActive(ctx, m.projectUID)
	if err != nil {
		return err
	}
	for _, uid := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, err := m.store.LoadContext(ctx, uid)
		if err != nil {
			if missing(err) || m.store.quarantinedError(uid, err) {
				continue
			}
			return err
		}
		lane := m.forRun(r)
		if m.projects && (lane == nil || m.projectUID != "" && lane != m) {
			continue
		}
		if r.Phase != Held || r.SlotReleased || r.Resume != nil && r.Resume.Pending {
			continue
		}
		// Keep proof and the capacity transition under the journal lock, shared
		// with release and queue completions. Never save an earlier snapshot.
		released := false
		_, err = m.store.TransitionContext(ctx, uid, func(r *Run) error {
			if r.Phase != Held || r.SlotReleased || r.Resume != nil && r.Resume.Pending {
				return errUnproven
			}
			proofCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			stopped, activity := m.heldStopped(proofCtx, r)
			for _, i := range activity {
				r.Steps[i].SawActivity = true
			}
			if !stopped {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if len(activity) > 0 {
					return nil // Retain late activity without releasing the slot.
				}
				return errUnproven
			}
			r.SlotReleased = true
			released = true
			return ctx.Err()
		})
		if err != nil && !errors.Is(err, errUnproven) {
			if missing(err) || m.store.quarantinedError(uid, err) {
				continue
			}
			return err
		}
		if err == nil && released {
			m.WakeProjects()
		}
	}
	return nil
}

// heldStopped is read-only; activity indexes refer to newly observed Steps.
func (m *Manager) heldStopped(ctx context.Context, r *Run) (bool, []int) {
	current := *AgentReference(r)
	for _, ref := range agentReferences(r) {
		if ref.PaneID == "" && ref.LaunchIntent {
			return false, nil
		}
		if ref.PaneID == "" || ref.PaneID == current.PaneID {
			continue
		}
		a, e := m.proof(ctx, ref)
		if (e != nil || !settledAgent(a) || !(sequenceAdvanced(ref, a) || retainedNoActivity(r, ref))) && !m.paneExited(ctx, ref) && !m.failedStartStopped(ctx, ref) {
			return false, nil
		}
	}
	if current.PaneID == "" && current.AgentName == "" && !current.LaunchIntent {
		for _, s := range r.Steps {
			if s.Kind == "prompt" && (s.Submission == Sending || s.Submission == Accepted) && (s.Status == "" || s.Status == "unknown") {
				return false, nil
			}
			if s.Kind == "command" && (s.Submission == Sending || s.Submission == Accepted) {
				if !m.commandStopped(r, s) {
					return false, nil
				}
			}
		}
		return r.Outcome != "", nil
	}
	if m.h == nil {
		return false, nil
	}
	return m.currentHeldStopped(ctx, r)
}
func (m *Manager) currentHeldStopped(ctx context.Context, r *Run) (stopped bool, activity []int) {
	ref := *AgentReference(r)
	if ref.PaneID == "" || ref.TerminalID == "" {
		return false, activity
	}
	for _, s := range r.Steps {
		if s.Kind == "command" && s.Submission == Sending && !m.commandStopped(r, s) {
			return false, activity
		}
	}
	if ref.AgentName == "" {
		// A crash can retain a tab before the first agent launch intent. A
		// preflight hold may reuse it only with fresh, matching shell proof.
		for i, step := range r.Steps {
			if step.Key == r.ActiveStep && unstartedHold(r, i) {
				return !ref.LaunchIntent && ref.HasSubmitted != nil && !*ref.HasSubmitted && m.failedStartStopped(ctx, ref), activity
			}
		}
		return false, activity
	}
	a, err := m.occupant(ctx, ref)
	if err == nil {
		noActivity := r.Reason == "no_activity"
		for _, s := range r.Steps {
			if s.Key == r.ActiveStep && s.Limit != nil && s.Reason == "no_activity" {
				noActivity = true
			}
		}
		for i := len(r.Steps) - 1; i >= 0; i-- {
			s := r.Steps[i]
			if s.Kind != "prompt" || s.Submission != Accepted && s.Submission != Sending || s.Status != "" && s.Status != "unknown" {
				continue
			}
			if (a.Status == "working" || a.Status == "blocked") && !s.SawActivity {
				activity = append(activity, i)
			}
			if !noActivity && !settlementEvidence(r, i, a) {
				_, path, e := m.attemptPaths(r, i)
				if e != nil {
					return false, activity
				}
				if _, e = ReadResult(m.store.fsys, path, r.UID, s.Key, s.Attempt); e != nil {
					return false, activity
				}
			}
			break
		}
		// Holding with an outcome resolves automatic execution, even if the
		// agent never reported. A late result must not settle or resume it.
		if r.Outcome == "" || !settledAgent(a) || !noActivity && !sequenceAdvanced(ref, a) {
			return false, activity
		}
		if ref.Session != nil {
			a, err = m.proof(ctx, ref)
		} else {
			// Startup may time out before Herdr supplies a session. Capacity
			// reconciliation is read-only and can use the saved launch intent.
			var tab herdr.TabInfo
			tab, err = m.h.TabGet(ctx, ref.TabID)
			if err == nil && (tab.PaneCount != 1 || tab.TabID != ref.TabID || tab.WorkspaceID != ref.WorkspaceID) {
				return false, activity
			}
		}
		return err == nil && settledAgent(a) && (noActivity || sequenceAdvanced(ref, a)), activity
	}
	if !herdrCode(err, "agent_not_found") {
		return false, activity
	}
	// Both lookups must confirm absence: a new name in the same pane keeps
	// its slot. A matching server generation also fences exited panes.
	if _, err = m.h.AgentGet(ctx, ref.PaneID); !herdrCode(err, "agent_not_found") {
		return false, activity
	}
	id, err := herdr.ReadServerID(m.h.Path)
	if err != nil || id == (herdr.ServerID{}) || id != ref.ServerID {
		return false, activity
	}
	_, err = m.h.PaneGet(ctx, ref.PaneID)
	return herdrCode(err, "pane_not_found") || err == nil && m.failedStartStopped(ctx, ref), activity
}

// An attempt-bound start record proves only group liveness, never its outcome.
func (m *Manager) commandStopped(r *Run, s StepState) bool {
	if validReceipt(s.Receipt, r.UID, s) {
		return GroupGone(s.Receipt.PGID)
	}
	var started Receipt
	path := filepath.Join(m.store.layout.AttemptDir(r.UID, s.Key, s.Attempt), "process.json")
	if err := state.ReadJSON(m.store.fsys, path, 4096, &started); err != nil {
		return false
	}
	return started.Version == 1 && started.Run == r.UID && started.Step == s.Key && started.Attempt == s.Attempt && s.Attempt > 0 && !started.StartedAt.IsZero() && started.EndedAt.IsZero() && started.Uncertain && GroupGone(started.PGID)
}
