package runner

import (
	"reflect"

	"github.com/salmonumbrella/herdr-kata/internal/agentlimit"
	"github.com/salmonumbrella/herdr-kata/internal/crashpoint"
)

func attemptLimit(r *Run, index int, env agentlimit.Env) *agentlimit.Limit {
	s := r.Steps[index]
	ref := AgentReference(r)
	if s.Kind != "prompt" || s.Result != nil || s.SubmittedAt == nil || ref.Session == nil || s.Submission != Accepted && s.Submission != Sending {
		return nil
	}
	return agentlimit.Classify(*ref.Session, r.Cwd, ref.LaunchedAt, *s.SubmittedAt, env)
}

// Read outside the journal lock; apply only to the unchanged, unresolved
// submission. Stopped proof belongs to the caller. Saved holds are final.
func (m *Manager) holdNoResult(snapshot *Run, index int, reason string, stopped bool) error {
	var limit *agentlimit.Limit
	if stopped {
		_, path, e := m.attemptPaths(snapshot, index)
		if e != nil {
			return e
		}
		// A reported result may still lack the proof needed to accept it
		// (for example, a gone agent in a pane that has not exited). Keep
		// that uncertainty rather than replacing the report with a limit.
		if _, e := ReadResult(m.store.fsys, path, snapshot.UID, snapshot.Steps[index].Key, snapshot.Steps[index].Attempt); e != nil {
			limit = attemptLimit(snapshot, index, m.agentEnv())
		}
	}
	crashpoint.Hit("agent-limit.classified")
	_, e := m.store.Transition(snapshot.UID, func(r *Run) error {
		if r.Phase == Held || r.Phase == HandedOff || r.Phase == Settled || index >= len(r.Steps) {
			return nil
		}
		s, old := &r.Steps[index], snapshot.Steps[index]
		if s.Key != old.Key || s.Attempt != old.Attempt || s.Submission != old.Submission || !reflect.DeepEqual(s.SubmittedAt, old.SubmittedAt) || !reflect.DeepEqual(AgentReference(r), AgentReference(snapshot)) || s.Result != nil || s.Status != "" {
			return nil
		}
		r.Phase, r.Outcome, r.Reason = Held, "unknown", reason
		r.ActiveStep = s.Key
		s.Reason = reason // Retain no-activity proof for resume and old occupants.
		s.Limit = limit
		if limit != nil {
			r.Reason = limit.Kind
		}
		r.SlotReleased = r.SlotReleased || stopped
		return nil
	})
	return e
}

// HoldReason is shared by reporting and status. Only recognized codes produce
// explanatory text; agent messages never enter the journal or Kata payloads.
func HoldReason(r *Run) string {
	for _, s := range r.Steps {
		if s.Key == r.ActiveStep && s.Limit != nil && s.Limit.Kind == r.Reason {
			return limitReason(s.Limit, r.UID)
		}
	}
	return r.Reason
}

func limitDiagnosis(limit *agentlimit.Limit) string {
	labels := map[string]string{
		"codex:usage_limit_exceeded": "codex usage limit", "codex:rate_limit_exceeded": "codex rate limit",
		"codex:unauthorized": "codex login failed", "claude:rate_limit": "claude rate limit",
		"claude:billing_error": "claude billing error", "claude:authentication_failed": "claude authentication failed",
		"claude:unauthenticated": "claude unauthenticated", "claude:invalid_grant": "claude login failed",
	}
	label, ok := labels[limit.Code]
	if !ok {
		return limit.Kind
	}
	if limit.ResetsAt != nil {
		label += "; resets " + limit.ResetsAt.UTC().Format("2006-01-02T15:04Z")
	}
	return limit.Kind + " (" + label + ")"
}

func limitReason(limit *agentlimit.Limit, uid string) string {
	reason := limitDiagnosis(limit) + ". "
	if limit.Kind == "agent_login" {
		reason += "Log the agent in, then resume: herdr-kata resume " + uid
	} else {
		reason += "Resume: herdr-kata resume " + uid
	}
	return reason
}
