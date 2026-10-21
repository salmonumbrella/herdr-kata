package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

// Execute reserves one runner per UID and returns before execution starts.
// The worker cancels its context and joins Wait before releasing worker.lock.
func (m *Manager) Execute(ctx context.Context, uid string) error {
	return m.startExecution(ctx, ctx, uid)
}

// A control budget ends after preparation; the runner keeps its caller's lifetime.
func (m *Manager) startExecution(ctx, controlCtx context.Context, uid string) error {
	if e := controlCtx.Err(); e != nil {
		return e
	}
	r, e := m.store.LoadContext(controlCtx, uid)
	if e != nil {
		return e
	}
	if m.projects {
		lane := m.forRun(r)
		if lane == nil {
			return errResumePending
		}
		if lane != m {
			if lane.projectScheduler() == nil {
				return errResumePending
			}
			return lane.startExecution(ctx, controlCtx, uid)
		}
	}
	if r.Phase != Admitted && r.Phase != Running && r.Phase != Blocked || r.EndedAt != nil {
		return nil
	}
	if r.Resume != nil {
		probeCtx, cancel := context.WithTimeout(controlCtx, 2*time.Second)
		e = m.checkResumeRoute(probeCtx, r)
		cancel()
		if e != nil {
			return e
		}
	}
	if r.Phase == Admitted {
		if e = m.admissionTail(controlCtx, uid); e != nil {
			return e
		}
	}
	if _, exists := m.executions.LoadOrStore(uid, struct{}{}); exists {
		return nil
	}
	if m.runCtx != nil {
		ctx = m.runCtx
	}
	m.runners.Go(func() {
		defer m.executions.Delete(uid)
		if e := m.execute(ctx, uid); e != nil && !errors.Is(e, context.Canceled) {
			slog.Error("execute run", "run", uid, "error", e)
			if r, loadErr := m.store.Load(uid); loadErr == nil && r.EndedAt != nil {
				return
			}
			if e = m.hold(uid, "execution_error"); e != nil {
				slog.Error("save run failure", "run", uid, "error", e)
				return
			}
			if e = m.finish(ctx, uid, nil); e != nil {
				slog.Error("settle run failure", "run", uid, "error", e)
			}
		}
	})
	return nil
}
func (m *Manager) Wait() { m.runners.Wait() }
func (m *Manager) executePrompt(ctx context.Context, uid string, index int, prepared ...map[string]string) (*Result, error) {
	waitFailures := 0
	r, e := m.store.Load(uid)
	if e != nil {
		return nil, e
	}
	if promptBudgetExhausted(r, index) {
		return nil, m.holdStopped(uid, "prompt_budget", false, r.Steps[index].Key)
	}
	if r.Steps[index].Submission != Sending && r.Steps[index].Submission != Accepted {
		if e = m.prepareFindingContext(ctx, uid, index); e != nil {
			return nil, e
		}
		r, e = m.store.Load(uid)
		if e != nil {
			return nil, e
		}
		if r.Phase == Held {
			return nil, nil
		}
		r, e = m.selectAgent(ctx, uid, index)
		if errors.Is(e, errUnproven) {
			return nil, m.hold(uid, switchUnproven(e))
		}
		if e != nil {
			return nil, e
		}
		if _, _, _, _, e := m.promptContent(r, index); e != nil {
			return nil, m.holdStopped(uid, "invalid_prompt", prelaunchStopped(r))
		}
		var secrets map[string]string
		if len(prepared) > 0 {
			secrets = prepared[0]
		}
		if e := m.launchPrepared(ctx, uid, index, secrets); e != nil {
			return nil, e
		}
		r, e = m.store.Load(uid)
		if e != nil {
			return nil, e
		}
		if r.Phase == Held {
			return nil, nil
		}
	}
	for {
		r, e := m.store.Load(uid)
		if e != nil {
			return nil, e
		}
		if r.Phase == Held {
			return nil, nil
		}
		if !m.clock.Now().Before(r.ExpiresAt) {
			if e = m.hold(uid, "timeout"); e != nil {
				return nil, e
			}
			return nil, nil
		}
		step := r.Steps[index]
		if step.Submission == None || step.Submission == Rejected {
			a, e := m.occupant(ctx, *AgentReference(r))
			if e != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if e = m.hold(uid, "agent_lost"); e != nil {
					return nil, e
				}
				return nil, nil
			}
			review, e := m.hookReview(ctx, r, index, a)
			if e != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, m.hold(uid, "submission_unproven")
			}
			if review {
				if e = m.blockedStep(uid, index, true); e != nil {
					return nil, e
				}
				if e = m.pause(ctx, min(30*time.Second, r.ExpiresAt.Sub(m.clock.Now()))); e != nil {
					return nil, e
				}
				continue
			}
			if !readyAgent(a) {
				// Waiting for idle would immediately return this same unready
				// status. Pace readiness reads outside the bounded startup poll.
				if settledAgent(a) {
					if e = m.pause(ctx, min(30*time.Second, r.ExpiresAt.Sub(m.clock.Now()))); e != nil {
						return nil, e
					}
					continue
				}
				until := []string{"idle", "done", "blocked"}
				if a.Status == "blocked" {
					if e = m.blockedStep(uid, index, true); e != nil {
						return nil, e
					}
					until = []string{"idle", "done"}
				}
				if e = m.waitAgent(ctx, r, until); e != nil {
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					if herdrCode(e, "timeout") {
						if e = m.hold(uid, "timeout"); e != nil {
							return nil, e
						}
						return nil, nil
					}
					waitFailures++
					if waitFailures > 1 {
						if e = m.pause(ctx, min(30*time.Second, r.ExpiresAt.Sub(m.clock.Now()))); e != nil {
							return nil, e
						}
					}
				} else {
					waitFailures = 0
				}
				continue
			}
			if e = m.submitStep(ctx, uid, index); e != nil {
				return nil, e
			}
			continue
		}
		res, e := m.awaitStepResult(ctx, uid, index)
		if e != nil {
			return nil, e
		}
		return res, nil
	}
}
func (m *Manager) attemptPaths(r *Run, indices ...int) (string, string, error) {
	index := 0
	if len(indices) > 0 {
		index = indices[0]
	}
	if index < 0 || index >= len(r.Steps) || !validUID(r.Steps[index].Key) || r.Steps[index].Attempt < 1 {
		return "", "", errors.New("invalid prompt attempt")
	}
	dir := m.store.layout.AttemptDir(r.UID, r.Steps[index].Key, r.Steps[index].Attempt)
	return filepath.Join(dir, "prompt.md"), filepath.Join(dir, "result.json"), nil
}
func (m *Manager) submit(ctx context.Context, uid string) error {
	return m.submitStep(ctx, uid, 0)
}
func (m *Manager) submitStep(ctx context.Context, uid string, index int) error {
	r, e := m.store.Load(uid)
	if e != nil {
		return e
	}
	s := r.Steps[index]
	if s.Submission != None && s.Submission != Rejected {
		return nil
	}
	if promptBudgetExhausted(r, index) {
		return m.holdStopped(uid, "prompt_budget", false, r.Steps[index].Key)
	}
	if e = m.prepareFindingContext(ctx, uid, index); e != nil {
		return e
	}
	r, e = m.store.Load(uid)
	if e != nil {
		return e
	}
	if r.Phase == Held {
		return nil
	}
	a, e := m.occupant(ctx, *AgentReference(r))
	if e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return m.hold(uid, "submission_unproven")
	}
	if !readyAgent(a) {
		if a.Status == "blocked" {
			return m.blockedStep(uid, index, true)
		}
		return nil
	}
	review, e := m.hookReview(ctx, r, index, a)
	if e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return m.hold(uid, "submission_unproven")
	}
	if review {
		return m.blockedStep(uid, index, true)
	}
	priorSubmission := s.Attempt > 1 || priorPrompt(r, index)
	if AgentReference(r).HasSubmitted != nil {
		priorSubmission = *AgentReference(r).HasSubmitted
	}
	if s.Submission == Rejected || priorSubmission {
		if _, e = m.proof(ctx, *AgentReference(r)); e != nil {
			return m.hold(uid, "submission_unproven")
		}
	}
	prompt, _, e := m.attemptPaths(r, index)
	if e != nil {
		return e
	}
	if e = m.writePrompt(r, index); e != nil {
		return e
	}
	if e = m.blockedStep(uid, index, false); e != nil {
		return e
	}
	remaining := r.ExpiresAt.Sub(m.clock.Now())
	if remaining <= 0 {
		return m.hold(uid, "timeout")
	}
	hadSubmission := AgentReference(r).HasSubmitted
	// Persist refreshed session and the sending barrier together before the RPC.
	_, e = m.store.Transition(uid, func(r *Run) error {
		if e := m.saveSession(r, a); e != nil {
			return e
		}
		AgentReference(r).PreSubmitSeq = a.CompletionSeq
		sent := true
		AgentReference(r).HasSubmitted = &sent
		now := m.clock.Now()
		r.Steps[index].SubmittedAt = &now
		r.Steps[index].SawActivity = false
		if r.Steps[index].Submission == None {
			r.PromptsSent++
		}
		r.Steps[index].Submission = Sending
		return nil
	})
	if e != nil {
		return e
	}
	activityTimeout := min(15*time.Second, remaining)
	callCtx, cancel := context.WithTimeout(ctx, activityTimeout)
	defer cancel()
	observed, callErr := m.h.AgentPrompt(callCtx, AgentReference(r).AgentName, "Read "+prompt+" and do exactly what it says.", []string{"working", "blocked"}, activityTimeout)
	// Cancellation after send is ambiguous too. Leave an accepted journal for recovery.
	_, e = m.store.Transition(uid, func(r *Run) error {
		r.Steps[index].Submission = Accepted
		if herdrCode(callErr, "agent_blocked") || herdrCode(callErr, "agent_not_ready") {
			r.Steps[index].Submission = Rejected
			AgentReference(r).HasSubmitted = hadSubmission
		}
		if callErr == nil && m.matches(*AgentReference(r), observed) {
			if e := m.saveSession(r, observed); e != nil {
				return e
			}
			r.Steps[index].SawActivity = observed.Status == "working" || observed.Status == "blocked"
		}
		return nil
	})
	if e != nil {
		return e
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if herdrCode(callErr, "agent_blocked") {
		return m.blockedStep(uid, index, true)
	}
	if callErr == nil {
		if e = m.observeStep(uid, index, observed); errors.Is(e, errUnproven) {
			return m.hold(uid, "agent_unproven")
		}
		if e != nil {
			return e
		}
	}
	// A short activity-gate timeout is an ambiguous acceptance, not the run
	// deadline. The result loop reconciles the occupant without resubmitting.
	return nil
}
func (m *Manager) waitAgent(ctx context.Context, r *Run, until []string) error {
	return m.waitStepAgent(ctx, r, -1, until, r.ExpiresAt.Sub(m.clock.Now()))
}

func (m *Manager) waitStepAgent(ctx context.Context, r *Run, index int, until []string, timeout time.Duration) error {
	remaining := r.ExpiresAt.Sub(m.clock.Now())
	timeout = min(timeout, remaining)
	if timeout <= 0 {
		return &herdr.Error{Code: "timeout"}
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	a, e := m.h.AgentWait(callCtx, AgentReference(r).AgentName, until, timeout)
	if e == nil {
		if index < 0 {
			e = m.observeSession(r.UID, a)
		} else {
			e = m.observeStep(r.UID, index, a)
		}
	}
	if errors.Is(e, context.DeadlineExceeded) && ctx.Err() == nil {
		return &herdr.Error{Code: "timeout"}
	}
	return e
}

func (m *Manager) blockedStep(uid string, index int, blocked bool) error {
	return m.blocked(uid, blocked, index)
}
func (m *Manager) blocked(uid string, blocked bool, indices ...int) error {
	index := 0
	if len(indices) > 0 {
		index = indices[0]
	}
	current, e := m.store.Load(uid)
	if e != nil {
		return e
	}
	if blocked && current.Phase == Blocked || !blocked && current.Phase != Blocked {
		return nil
	}
	blockedMessage := "herdr-kata: agent blocked"
	if blocked {
		if limit := attemptLimit(current, index, m.agentEnv()); limit != nil {
			blockedMessage += ": " + limitDiagnosis(limit)
		}
	}

	_, e = m.store.Transition(uid, func(r *Run) error {
		if blocked && r.Phase == Blocked || !blocked && r.Phase != Blocked {
			return nil
		}
		prefix := fmt.Sprintf("blocked:%s:%d:%t:%d", r.Steps[index].Key, r.Steps[index].Attempt, blocked, len(r.Ops))
		if blocked {
			r.Phase = Blocked
			if r.IssueRef != "" {
				r.Ops = append(r.Ops, newOp(prefix+":recipient", "resolve-recipient", Payload{Ref: r.IssueRef}, ""), newOp(prefix+":notify", "notify", Payload{Ref: r.IssueRef, Message: blockedMessage}, ""))
			}
		} else {
			r.Phase = Running
		}
		if r.IssueOwned {
			value := "ok"
			message := "herdr-kata: running"
			if blocked {
				value = "needs-human"
				message = blockedMessage
			}
			op := newOp(prefix+":attention", "meta.set", Payload{Ref: r.IssueRef, Name: "work.attention", Value: value, Owner: attentionOwner(r)}, "")
			op.Guard = &kata.Cond{}
			r.Ops = append(r.Ops, op)
			msg := newOp(prefix+":attention-msg", "meta.set", Payload{Ref: r.IssueRef, Name: "work.attention_msg", Value: message, Owner: attentionOwner(r)}, "")
			msg.Guard = &kata.Cond{}
			r.Ops = append(r.Ops, msg)
		}
		return nil
	})
	if e == nil && m.q != nil {
		m.q.Kick(uid)
	}
	return e
}

func (m *Manager) finish(ctx context.Context, uid string, res *Result) error {
	if e := m.settle(ctx, uid, res); e != nil {
		return e
	}
	if e := m.cleanup(ctx, uid); e != nil && ctx.Err() == nil {
		slog.Warn("run tab retained", "run", uid, "error", e)
	}
	return ctx.Err()
}

func (m *Manager) promptContent(r *Run, index int) (prompt, text, result, report string, err error) {
	if e := defs.ValidateInput(r.Input); e != nil {
		return "", "", "", "", e
	}
	previous := previousNote(r, r.Steps[index].Key)
	if e := defs.ValidateNote(previous); e != nil {
		return "", "", "", "", e
	}
	prompt, result, e := m.attemptPaths(r, index)
	if e != nil {
		return "", "", "", "", e
	}
	report = AgentReference(r).ReportBin
	if report == "" {
		report, e = os.Executable()
		if e != nil {
			return "", "", "", "", e
		}
	}
	text = BuildPrompt(r, r.Steps[index], result, report, false)
	if len(text) > defs.MaxPromptBytes {
		return "", "", "", "", errors.New("expanded prompt exceeds 256 KiB")
	}
	return prompt, text, result, report, nil
}

func (m *Manager) writePrompt(r *Run, index int) error {
	prompt, text, result, report, e := m.promptContent(r, index)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(prompt), 0700); e != nil {
		return e
	}

	fsys := m.store.fsys
	if info, err := fsys.Lstat(prompt); err == nil {
		if !info.Mode().IsRegular() || info.Size() > defs.MaxPromptBytes {
			return errors.New("invalid frozen prompt file")
		}
		f, err := fsys.Open(prompt)
		if err != nil {
			return err
		}
		defer f.Close()
		opened, err := f.Stat()
		if err != nil {
			return err
		}
		if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
			return errors.New("prompt file changed")
		}
		b, err := io.ReadAll(io.LimitReader(f, defs.MaxPromptBytes+1))
		if err != nil {
			return err
		}
		// The saved prompt freezes the reporter capability for this attempt.
		// Validate either complete variant without probing the executable again.
		if string(b) != text && (!checkerClaim(r, r.Steps[index]) || string(b) != BuildPrompt(r, r.Steps[index], result, report, true)) {
			return errors.New("frozen prompt differs from attempt")
		}
		return nil
	} else if !missing(err) {
		return err
	}
	if checkerClaim(r, r.Steps[index]) && reporterAssertions(report) {
		text = BuildPrompt(r, r.Steps[index], result, report, true)
		if len(text) > defs.MaxPromptBytes {
			return errors.New("expanded prompt exceeds 256 KiB")
		}
	}
	dir := filepath.Dir(prompt)
	f, e := fsys.CreateTemp(dir, ".prompt-*")
	if e != nil {
		return e
	}
	defer fsys.Remove(f.Name()) //nolint:errcheck // Temporary-file removal remains best-effort.
	if _, e = io.WriteString(f, text); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = fsys.Rename(f.Name(), prompt); e != nil {
		return e
	}
	for path := dir; ; path = filepath.Dir(path) {
		if e = fsys.SyncDir(path); e != nil {
			return e
		}
		if path == m.store.layout.RunDir(r.UID) {
			break
		}
	}
	return nil
}

// A validation failure before creating any occupant can return its slot while
// retaining the run's gate. Earlier commands need their ordinary exit proof.
func prelaunchStopped(r *Run) bool {
	for _, ref := range agentReferences(r) {
		if ref.PaneID != "" || ref.LaunchIntent {
			return false
		}
	}
	for _, s := range r.Steps {
		if s.Kind == "command" && (s.Submission == Sending || s.Submission == Accepted) {
			if !certainReceipt(r.UID, s) || !GroupGone(s.Receipt.PGID) {
				return false
			}
		}
	}
	return true
}

// Rejected input is the same counted attempt; accepted input is never resent.
func promptBudgetExhausted(r *Run, index int) bool {
	return r.PromptCap > 0 && r.PromptsSent >= r.PromptCap && r.Steps[index].Submission == None
}
