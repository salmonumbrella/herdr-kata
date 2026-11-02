package runner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

var errResumePending = errors.New("resume waiting for reports or capacity")

func resumePrefix(r *Run) string { return "herdr-kata:" + r.UID + ":resume:" + r.Resume.UID }

// Resume records authorization once, then finishes the saved queue barrier.
func (m *Manager) Resume(ctx context.Context, req Request) error {
	if m.projects {
		r, e := m.store.LoadContext(ctx, req.RunUID)
		if e != nil {
			return e
		}
		lane := m.forRun(r)
		if lane == nil || lane.projectScheduler() == nil {
			return errResumePending
		}
		if lane != m {
			return lane.Resume(ctx, req)
		}
	}
	admission := m.admissionLock(req.RunUID)
	if e := lockControl(ctx, admission); e != nil {
		return e
	}
	defer admission.Unlock()
	r, e := m.store.LoadContext(ctx, req.RunUID)
	if e != nil {
		return e
	}
	if slices.Contains(r.ConsumedResumes, req.UID) {
		if r.Resume != nil && r.Resume.UID == req.UID && r.Resume.Pending {
			return m.activateResume(ctx, r.UID)
		}
		return nil
	}
	if r.Phase != Held {
		return fmt.Errorf("%w: resume requires a held run", errControlRefused)
	}
	if preAdmissionHold(r) || !admitted(r) {
		return fmt.Errorf("%w: admission unproven; use abandon --local", errControlRefused)
	}
	if r.Inspect != "" {
		return fmt.Errorf("%w: %s", errControlRefused, r.Inspect)
	}
	if r.Resume != nil && r.Resume.Pending {
		return errResumePending
	}
	if m.q == nil {
		return errors.New("resume requires the write queue")
	}
	if e = lockControl(ctx, m.q.issueMu); e != nil {
		return e
	}
	e = m.acceptResume(ctx, req)
	m.q.issueMu.Unlock()
	if e != nil {
		return e
	}
	return m.activateResume(ctx, req.RunUID)
}

func (m *Manager) acceptResume(ctx context.Context, req Request) error {
	if m.guard == nil {
		return errors.New("missing route checker")
	}
	if e := m.guard.Check(ctx); e != nil {
		return e
	}
	target, e := m.pin()
	if e != nil {
		return e
	}
	r, e := m.store.LoadContext(ctx, req.RunUID)
	if e != nil {
		return e
	}
	if !sameTarget(r.Target, target) {
		return fmt.Errorf("%w: pinned installation differs", errControlRefused)
	}
	if r.Reason == "secret_unavailable" {
		if _, err := resolveSecrets(m.cfg, r.JobDef); err != nil {
			return fmt.Errorf("%w: %w", errControlRefused, err)
		}
	}
	if r.Reason == "herdr_env_unclean" {
		if _, err := m.checkEnvironment(ctx, r.Cwd, AgentReference(r).ReportBin); err != nil {
			return fmt.Errorf("%w: %w", errControlRefused, err)
		}
	}
	if r.Timeout <= 0 {
		return fmt.Errorf("%w: missing frozen timeout", errControlRefused)
	}
	if _, busy := m.executions.Load(req.RunUID); busy {
		return errResumePending
	}
	for _, op := range r.Ops {
		if op.Inspect != "" {
			return fmt.Errorf("%w: %s", errControlRefused, op.Inspect)
		}
		if !op.Done {
			m.q.Kick(r.UID)
			return errResumePending
		}
	}
	if pendingChain(r) != nil {
		return fmt.Errorf("%w: finding UID unavailable", errControlRefused)
	}
	issue, e := m.showIssue(ctx, r.UID, r.IssueRef)
	if e != nil {
		return e
	}
	if issue.UID != r.IssueUID || issue.ProjectUID != r.Target.Identity.ProjectUID || issue.Status != "open" || takeover(r) != nil && issue.Owner != r.Target.Identity.Actor {
		return fmt.Errorf("%w: issue closed, changed or child reassigned", errControlRefused)
	}
	reader, ok := m.k.(interface {
		Ready(context.Context) ([]kata.IssueView, error)
	})
	if !ok {
		return errors.New("readiness reader unavailable")
	}
	ready, e := reader.Ready(ctx)
	if e != nil {
		return e
	}
	if !slices.ContainsFunc(ready, func(i kata.IssueView) bool { return i.UID == r.IssueUID }) {
		return fmt.Errorf("%w: issue not ready", errControlRefused)
	}
	if e = lockControl(ctx, &m.mu); e != nil {
		return e
	}
	defer m.mu.Unlock()
	if e := m.pruneContext(ctx); e != nil {
		return e
	}
	// Even an existing reservation must pass current capacity and peer gates.
	for uid, job := range m.reservations {
		if uid != r.UID && r.JobDef.Overlap == "forbid" && job == gateKey(m.projectUID, r.Job.UID) {
			return errResumePending
		}
	}
	occupied, projectOccupied := m.tallyLocked(r.UID, func(uid string) (*Run, error) {
		return m.store.LoadContext(ctx, uid)
	})
	if occupied >= m.cfg.Limits.MaxActiveRuns {
		return errResumePending
	}
	if m.cfg.ProjectLimit > 0 && projectOccupied >= m.cfg.ProjectLimit {
		return errResumePending
	}
	_, e = m.store.TransitionContext(ctx, r.UID, func(r *Run) error {
		if r.Phase != Held || r.Inspect != "" {
			return fmt.Errorf("%w: run no longer resumable", errControlRefused)
		}
		for _, op := range r.Ops {
			if !op.Landed() {
				return errResumePending
			}
		}
		if !m.resumeStopped(ctx, r) {
			if e := ctx.Err(); e != nil {
				return e
			}
			// A socket deadline can fire before cancellation becomes observable.
			if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
				return context.DeadlineExceeded
			}
			return fmt.Errorf("%w: occupants not proven stopped", errControlRefused)
		}
		index := -1
		parked := r.ActiveStep
		for i, s := range r.Steps {
			if s.Key == parked && finishedResumeStep(r, i) {
				parked = ""
				break
			}
		}
		edge := loopEdge(r)
		for i, s := range r.Steps {
			if (r.Reason == "loop_pending" || r.Reason == "loop_exhausted" || r.Reason == "loop_stuck") && edge != nil {
				if s.Key == edge.Checker {
					index = i
					break
				}
			} else if !finishedResumeStep(r, i) && (s.Key == parked || parked == "") {
				index = i
				break
			}
		}
		complete := len(r.Steps) > 0
		for i := range r.Steps {
			if !finishedResumeStep(r, i) {
				complete = false
				break
			}
		}
		if index < 0 && complete {
			// Final finding publication can time out after every step finishes.
			// Resume the reporting barrier and aggregate the saved results.
			index = len(r.Steps) - 1
		}
		if index < 0 {
			return fmt.Errorf("%w: parked step unavailable", errControlRefused)
		}
		generation := 1
		if r.Resume != nil {
			generation = r.Resume.Generation + 1
		}
		reason := r.Reason
		if req.ResetLoops {
			r.Loop = LoopState{}
		}
		r.PromptsSent = 0
		r.ConsumedResumes = append(r.ConsumedResumes, req.UID)
		r.Resume = &ResumeState{UID: req.UID, Generation: generation, Pending: true}
		if reason == "loop_pending" {
			if edge == nil || r.Steps[index].Status != "failed" || !certainVerdict(r, index) {
				return fmt.Errorf("%w: saved loop verdict unavailable", errControlRefused)
			}
			if e := applyLoop(r, index); e != nil {
				return e
			}
			if r.Reason == "loop_exhausted" || r.Reason == "loop_stuck" {
				r.Resume.Pending = false
				r.Resume.Step = r.Steps[index].Key
				r.Resume.Attempt = r.Steps[index].Attempt
				return nil // The saved decision exhausted its budget; no checker rerun.
			}
			for i, s := range r.Steps {
				if s.Key == edge.Goto {
					index = i
					break
				}
			}
		} else if req.ResetLoops && edge != nil && (reason == "loop_exhausted" || reason == "loop_stuck") {
			for i, s := range r.Steps {
				if slices.Contains(edge.Span, s.Key) {
					resetResumeStep(r, i, true)
				}
				if s.Key == edge.Goto {
					index = i
				}
			}
		} else if !complete {
			if unstartedHold(r, index) {
				if req.ResetLoops && r.Steps[index].Kind == "prompt" {
					// Only a proven-unsent prompt may be rebuilt in the reset
					// context. Make its removal durable before saving the reset.
					prompt, _, e := m.attemptPaths(r, index)
					if e != nil {
						return e
					}
					if e = m.store.fsys.Remove(prompt); e != nil && !missing(e) {
						return e
					}
					if e = m.store.fsys.SyncDir(filepath.Dir(prompt)); e != nil && !missing(e) {
						return e
					}
				}
				r.Steps[index].Reason = ""
			} else {
				replaceLimitAgent := r.Steps[index].Limit != nil && r.Steps[index].Herdr == nil && (m.paneExited(ctx, *AgentReference(r)) || m.failedStartStopped(ctx, *AgentReference(r)))
				resetResumeStep(r, index, false)
				if replaceLimitAgent {
					retainSharedAgentRefs(r)
					r.Herdr = HerdrRef{ReportBin: r.Herdr.ReportBin}
				}
			}
		}
		r.ActiveStep = r.Steps[index].Key
		r.Resume.Step = r.ActiveStep
		r.Resume.Attempt = r.Steps[index].Attempt
		r.SlotReleased = false
		r.ExpiresAt = m.clock.Now().UTC().Add(r.Timeout)
		o := AdmissionBody(r)
		o.Status = "running"
		o.IssueUID = r.IssueUID
		o.StartedAt = r.StartedAt
		o.EndedAt = nil
		o.Summary = kata.Summary{Version: 1, Message: "resumed"}
		o.ExpectedRevision = 1
		for _, op := range r.Ops {
			if op.Kind == "observe" && op.Landed() {
				p, e := decodePayload(op)
				if e == nil && p.Observation != nil {
					o.ExpectedRevision = max(o.ExpectedRevision, p.Observation.ExpectedRevision+1)
				}
			}
		}
		prefix := resumePrefix(r)
		if r.IssueRequest != nil {
			r.Ops = append(r.Ops, requestOutcomeOp(r, prefix+":observe", Admitted, "", "", "resumed", nil))
		} else {
			r.Ops = append(r.Ops, newOp(prefix+":observe", "observe", Payload{Observation: &o}, ""))
		}
		if r.IssueOwned {
			for _, value := range []struct{ suffix, name, value string }{{"attention", "work.attention", "ok"}, {"attention-msg", "work.attention_msg", "herdr-kata: running"}} {
				op := newOp(prefix+":"+value.suffix, "meta.set", Payload{Ref: r.IssueRef, Name: value.name, Value: value.value, Owner: attentionOwner(r)}, "")
				op.Guard = &kata.Cond{}
				r.Ops = append(r.Ops, op)
			}
		}
		return ctx.Err()
	})
	if e == nil {
		m.reservations[r.UID] = gateKey(m.projectUID, r.Job.UID)
		m.q.Kick(r.UID)
	}
	return e
}

// A default-policy exhausted failure outside the loop is a saved aggregate
// result. Parking, remaining retries and checker edges still need continuation.
func finishedResumeStep(r *Run, index int) bool {
	s := r.Steps[index]
	if s.Status == "succeeded" || s.Status == "skipped" {
		return true
	}
	if s.Status != "failed" || r.WFDef == nil || defs.ParkFailure(r.WFDef) || !certainVerdict(r, index) {
		return false
	}
	if edge := loopEdge(r); edge != nil && s.Key == edge.Checker {
		return false
	}
	for _, def := range r.WFDef.Steps {
		if def.Key == s.Key {
			return s.Attempt-s.Uncharged > def.Retries
		}
	}
	return false
}

func certainVerdict(r *Run, index int) bool {
	s := r.Steps[index]
	if s.Kind == "command" {
		return certainReceipt(r.UID, s) && s.Receipt.ExitCode != 0
	}
	return reportedFailure(r.UID, s)
}

func resetResumeStep(r *Run, index int, loop bool) {
	old := r.Steps[index]
	if old.Kind == "prompt" {
		if r.Reason == "no_activity" && !old.SawActivity {
			r.Steps[index].Reason = "no_activity"
		}
		ref := r.Herdr
		if old.Herdr != nil {
			ref = *old.Herdr
		}
		retainAgentRef(r, index, ref)
	} else {
		r.PastAttempts = append(r.PastAttempts, old)
	}
	uncharged := old.Uncharged
	if old.Limit != nil {
		uncharged++
	}
	r.Steps[index] = nextAttempt(old, uncharged, loop || old.LoopRerun || old.Kind == "prompt" && old.Limit == nil)
}

// Proof is fresh even if reconciliation already released this run's slot.
func (m *Manager) resumeStopped(ctx context.Context, r *Run) bool {
	for _, s := range append(append([]StepState{}, r.PastAttempts...), r.Steps...) {
		if s.Kind == "command" && (s.Submission == Sending || s.Submission == Accepted) && !m.commandStopped(r, s) {
			return false
		}
	}
	hasAgent := false
	for _, ref := range agentReferences(r) {
		if ref.LaunchIntent || ref.AgentName != "" || ref.PaneID != "" {
			hasAgent = true
			if ref.AgentName == "" && ref.LaunchIntent || ref.PaneID == "" || ref.TerminalID == "" {
				return false
			}
		}
	}
	if !hasAgent {
		for _, s := range append(append([]StepState{}, r.PastAttempts...), r.Steps...) {
			if s.Kind == "prompt" && (s.Submission == Sending || s.Submission == Accepted) {
				return false
			}
		}
		return true
	}
	if m.h == nil {
		return false
	}
	// Command stop proof was checked above, independent of verdict certainty.
	view := *r
	view.Steps = nil
	current := AgentReference(r)
	for _, s := range r.Steps {
		if s.Kind == "prompt" {
			view.Steps = append(view.Steps, s)
			if s.Herdr == current {
				view.ActiveStep = s.Key
			}
		}
	}
	stopped, _ := m.heldStopped(ctx, &view)
	return stopped
}

func (m *Manager) activateResume(ctx context.Context, uid string) error {
	r, e := m.store.LoadContext(ctx, uid)
	if e != nil {
		return e
	}
	if r.Resume == nil || !r.Resume.Pending {
		return nil
	}
	last := resumeBarrierID(r)
	if e = m.q.wait(ctx, uid, last); e != nil {
		latest, loadErr := m.store.LoadContext(ctx, uid)
		if loadErr == nil && latest.Inspect != "" {
			return fmt.Errorf("%w: %s", errControlRefused, latest.Inspect)
		}
		return e
	}
	if e = m.checkResumeRoute(ctx, r); e != nil {
		return e
	}
	_, e = m.store.TransitionContext(ctx, uid, func(r *Run) error {
		if !r.Resume.Pending {
			return nil
		}
		if r.Phase != Held {
			return fmt.Errorf("%w: run no longer held", errControlRefused)
		}
		r.Resume.Pending = false
		r.Phase = Admitted
		r.EndedAt = nil
		r.Outcome = ""
		r.Reason = ""
		return nil
	})
	if e != nil {
		return e
	}
	return nil
}

func (m *Manager) checkResumeRoute(ctx context.Context, r *Run) error {
	if m.guard == nil {
		return errors.New("missing route checker")
	}
	if e := m.guard.Check(ctx); e != nil {
		if state.LocalError(e) {
			return e
		}
		return fmt.Errorf("%w: %w", errResumePending, e)
	}
	target, e := m.pin()
	if e != nil {
		return e
	}
	if !sameTarget(r.Target, target) {
		return fmt.Errorf("%w: pinned installation differs", errResumePending)
	}
	return nil
}

func resumeBarrierID(r *Run) string {
	if r.IssueOwned {
		return resumePrefix(r) + ":attention-msg"
	}
	return resumePrefix(r) + ":observe"
}

// Periodic controls finish a saved acceptance without waiting on retries or
// requiring a request file. Startup leaves activation to the control scan.
func (m *Manager) finishReadyResume(ctx context.Context, uid string) error {
	controlCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	err := m.continueResume(ctx, controlCtx, uid)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, errControlRefused) || errors.Is(err, errResumePending) || errors.Is(err, context.DeadlineExceeded) && !state.LocalError(err) {
		return nil
	}
	return err
}

func (m *Manager) continueResume(ctx, controlCtx context.Context, uid string) error {
	r, e := m.store.LoadContext(controlCtx, uid)
	if e != nil {
		return e
	}
	if m.projects {
		lane := m.forRun(r)
		if lane == nil || lane.projectScheduler() == nil {
			return nil
		}
		if lane != m {
			return lane.continueResume(ctx, controlCtx, uid)
		}
	}
	if r.Resume == nil || r.Inspect != "" {
		return nil
	}
	if _, busy := m.executions.Load(uid); busy {
		return nil
	}
	if r.Phase == Held && r.Resume.Pending {
		if m.q == nil {
			return errors.New("resume requires the write queue")
		}
		op, e := findOp(r, resumeBarrierID(r))
		if e != nil {
			return e
		}
		if !op.Landed() {
			m.q.Kick(uid)
			return nil
		}
		e = m.activateResume(controlCtx, uid)
		if e != nil {
			if errors.Is(e, errControlRefused) || errors.Is(e, errResumePending) {
				return nil
			}
			return e
		}
	} else if r.Phase != Admitted || r.Resume.Pending || r.EndedAt != nil {
		return nil
	}
	if e = m.startExecution(ctx, controlCtx, uid); e != nil {
		if errors.Is(e, errControlRefused) || errors.Is(e, errResumePending) {
			return nil
		}
		return e
	}
	return nil
}

// A preflight refusal has no attempt to retire or retry to consume.
func unstartedHold(r *Run, index int) bool {
	s := r.Steps[index]
	return (r.Reason == "secret_unavailable" || r.Reason == "herdr_env_unclean") && s.Submission == None && s.Status == "" && s.Result == nil && s.Receipt == nil
}
