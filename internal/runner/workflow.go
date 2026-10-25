package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// execute consumes only the definition and attempt states frozen in run.json.
// Resolved steps are never submitted again, including after a worker restart.
func (m *Manager) execute(ctx context.Context, uid string) error {
	r, err := m.store.Load(uid)
	if err != nil {
		return err
	}
	if r.JobDef.Action.Kind == "notify" {
		return m.executeNotify(ctx, uid)
	}
	steps, err := executionSteps(r)
	if err != nil {
		return err
	}
	order, err := defs.TopoOrder(steps)
	if err != nil {
		return err
	}
restart:
	for _, key := range order {
		if err = ctx.Err(); err != nil {
			return err
		}
		r, err = m.store.Load(uid)
		if err != nil {
			return err
		}
		if r.EndedAt != nil || r.Phase == HandedOff || r.Phase == Settled {
			return nil
		}
		if r.Phase == Held {
			return m.finish(ctx, uid, nil)
		}
		index := -1
		var def defs.Step
		for _, d := range steps {
			if d.Key == key {
				def = d
				break
			}
		}
		for i, s := range r.Steps {
			if s.Key == key {
				index = i
				break
			}
		}
		if index < 0 || r.Steps[index].Kind != def.Kind {
			return errors.New("workflow step state differs from frozen definition")
		}
		step := r.Steps[index]
		if step.Status != "" {
			if err = m.collectStepUsage(ctx, uid, index); err != nil {
				return err
			}
		}
		if step.Kind == "command" && (step.Receipt != nil && !certainReceipt(r.UID, step) || step.Receipt == nil && (step.Status == "failed" || step.Status == "succeeded")) {
			if err = m.commandUnknown(uid, index, "command_outcome_unknown", boundReceipt(step.Receipt, r.UID, step) && GroupGone(step.Receipt.PGID)); err != nil {
				return err
			}
			return m.finish(ctx, uid, nil)
		}
		if step.Status == "succeeded" || step.Status == "skipped" {
			if err = m.publishFinding(ctx, uid, index); err != nil {
				return err
			}
			r, err = m.store.Load(uid)
			if err != nil {
				return err
			}
			if r.Phase == Held {
				return m.finish(ctx, uid, nil)
			}
			continue
		}
		ready := true
		for _, dependency := range def.After {
			found := false
			for _, s := range r.Steps {
				if s.Key == dependency {
					found = s.Status == "succeeded"
					break
				}
			}
			ready = ready && found
		}
		if !ready {
			_, err = m.store.Transition(uid, func(r *Run) error {
				r.Steps[index].Status = "skipped"
				r.Steps[index].Reason = "dependency_not_succeeded"
				return nil
			})
			if err != nil {
				return err
			}
			continue
		}
		for {
			r, err = m.store.Load(uid)
			if err != nil {
				return err
			}
			step = r.Steps[index]
			var retrySecrets map[string]string
			if step.Status == "needs-human" {
				return m.finish(ctx, uid, resultFromRef(step.Result))
			}
			if step.Status == "succeeded" || step.Status == "skipped" {
				break
			}
			if step.Status == "failed" {
				if edge := loopEdge(r); edge != nil && step.Key == edge.Checker {
					err = m.holdPendingLoop(ctx, uid)
					if err != nil {
						return err
					}
					return m.finish(ctx, uid, nil)
				}
				if step.Attempt-step.Uncharged > def.Retries {
					if defs.ParkFailure(r.WFDef) {
						_, err = m.store.Transition(uid, func(r *Run) error { r.ActiveStep = step.Key; loopHold(r, "step_failed"); return nil })
						if err != nil {
							return err
						}
						return m.finish(ctx, uid, nil)
					}
					break
				}
				if step.Kind == "command" && !attemptFailureProven(r.UID, step) {
					if err = m.hold(uid, "command_group_unproven"); err != nil {
						return err
					}
					return m.finish(ctx, uid, nil)
				}
				exitedPrompt := false
				if step.Kind == "prompt" {
					a, e := m.proof(ctx, *AgentReference(r))
					exitedPrompt = m.paneExited(ctx, *AgentReference(r))
					if (e != nil || !settledAgent(a) || !sequenceAdvanced(*AgentReference(r), a)) && !exitedPrompt {
						if e = m.hold(uid, switchUnproven(e)); e != nil {
							return e
						}
						return m.finish(ctx, uid, nil)
					}
				}
				// Prompt retries are authorized only by an attempt-bound reported failure.
				if step.Kind == "prompt" && !attemptFailureProven(uid, step) {
					return errors.New("prompt retry has no reported failure")
				}
				var e error
				retrySecrets, e = resolveSecrets(m.cfg, r.JobDef)
				if e != nil {
					if err = m.holdSecret(uid, index, e); err != nil {
						return err
					}
					return m.finish(ctx, uid, nil)
				}
				r, err = m.store.Transition(uid, func(r *Run) error {
					old := r.Steps[index]
					if old.Kind == "prompt" {
						// Retain even a live shared occupant before nextAttempt
						// clears its submitted state. The retry can hold before
						// another prompt is sent, while the first still counts.
						retainAgent(r, index)
					}
					if old.Herdr == nil && exitedPrompt {
						retainSharedAgentRefs(r)
						report := r.Herdr.ReportBin
						r.Herdr = HerdrRef{ReportBin: report}
					}
					r.Steps[index] = nextAttempt(old, old.Uncharged, old.LoopRerun)
					return nil
				})
				if err != nil {
					return err
				}
				step = r.Steps[index]
			}
			if !m.clock.Now().Before(r.ExpiresAt) {
				if err = m.holdStopped(uid, "timeout", false, step.Key); err != nil {
					return err
				}
				return m.finish(ctx, uid, nil)
			}
			switch step.Kind {
			case "prompt":
				res, e := m.executePrompt(ctx, uid, index, retrySecrets)
				if e != nil {
					return e
				}
				if res == nil {
					return m.finish(ctx, uid, nil)
				}
				if res.Status == "needs-human" {
					return m.finish(ctx, uid, res)
				}
			case "command":
				if err = m.runCommandStep(ctx, uid, index, def, retrySecrets); err != nil {
					return err
				}
				r, err = m.store.Load(uid)
				if err != nil {
					return err
				}
				if r.Phase == Held {
					return m.finish(ctx, uid, nil)
				}
			default:
				return fmt.Errorf("unsupported step kind %q", step.Kind)
			}
			if err = m.publishFinding(ctx, uid, index); err != nil {
				return err
			}
			r, err = m.store.Load(uid)
			if err != nil {
				return err
			}
			if r.Phase == Held {
				return m.finish(ctx, uid, nil)
			}
			if edge := loopEdge(r); edge != nil && r.Steps[index].Key == edge.Checker && r.Steps[index].Status == "failed" {
				if err = m.takeLoop(ctx, uid, index); err != nil {
					return err
				}
				r, err = m.store.Load(uid)
				if err != nil {
					return err
				}
				if r.Phase == Held {
					return m.finish(ctx, uid, nil)
				}
				goto restart
			}

		}
	}
	r, err = m.store.Load(uid)
	if err != nil {
		return err
	}
	result := &Result{Version: 1, Run: uid, Status: "succeeded"}
	// The last prompt report supplies the mixed workflow's message and evidence.
	for _, key := range order {
		for _, s := range r.Steps {
			if s.Key == key {
				if s.Result != nil {
					p := resultFromRef(s.Result)
					result.Summary = p.Summary
					result.Evidence = p.Evidence
					result.CloseReason = p.CloseReason
				}
				if s.Status == "failed" {
					result.Status = "failed"
					result.Summary = s.Reason
					if s.Result != nil {
						result.Summary = s.Result.Summary
					}
				}
			}
		}
	}
	// A later successful independent step cannot erase the failed step's reason.
	if result.Status == "failed" {
		for _, s := range r.Steps {
			if s.Status == "failed" {
				result.Summary = s.Reason
				if s.Result != nil {
					result.Summary = s.Result.Summary
				}
				break
			}
		}
	}
	return m.finish(ctx, uid, result)
}
func executionSteps(r *Run) ([]defs.Step, error) {
	if r.JobDef.Action.Kind != "execute" {
		return nil, errors.New("unsupported job action")
	}
	if r.WFDef != nil {
		if len(r.WFDef.Steps) == 0 {
			return nil, errors.New("empty workflow")
		}
		return r.WFDef.Steps, nil
	}
	if r.JobDef.Action.WorkflowUID != "" {
		return nil, errors.New("missing frozen workflow")
	}
	if len(r.Steps) != 1 || r.Steps[0].Kind != "prompt" {
		return nil, errors.New("invalid prompt job")
	}
	return []defs.Step{{Key: "main", Kind: "prompt", Prompt: r.JobDef.Action.Prompt}}, nil
}
func priorPrompt(r *Run, index int) bool {
	for i, s := range r.Steps {
		if i != index && s.Kind == "prompt" && s.Submission != None && s.Submission != "" {
			return true
		}
	}
	return false
}
func resultFromRef(r *ResultRef) *Result {
	if r == nil {
		return nil
	}
	return &Result{Version: r.Version, Run: r.Run, Step: r.Step, Attempt: r.Attempt, Status: r.Status, Summary: r.Summary, Note: r.Note, Assert: r.Assert, Target: r.Target, Evidence: r.Evidence, CloseReason: r.CloseReason}
}
func (m *Manager) runCommandStep(ctx context.Context, uid string, index int, def defs.Step, prepared ...map[string]string) error {
	r, err := m.store.Load(uid)
	if err != nil {
		return err
	}
	step := r.Steps[index]
	if e := defs.ValidateInput(r.Input); e != nil {
		return e
	}
	if e := defs.ValidateNote(previousNote(r, step.Key)); e != nil {
		return e
	}
	if step.Receipt == nil {
		if step.Submission == Sending || step.Submission == Accepted {
			return m.commandUnknown(uid, index, "command_no_receipt", false)
		}
		if step.Submission != None {
			return errors.New("invalid command submission")
		}
		var secrets map[string]string
		if len(prepared) > 0 {
			secrets = prepared[0]
		}
		if secrets == nil {
			var e error
			secrets, e = resolveSecrets(m.cfg, r.JobDef)
			if e != nil {
				return m.holdSecret(uid, index, e)
			}
		}
		r, err = m.store.Transition(uid, func(r *Run) error {
			r.Phase = Running
			r.ActiveStep = step.Key
			if r.StartedAt == nil {
				now := m.clock.Now().UTC()
				r.StartedAt = &now
			}
			r.Steps[index].Submission = Sending
			return nil
		})
		if err != nil {
			return err
		}
		report := r.Herdr.ReportBin
		if report == "" {
			report, err = os.Executable()
			if err != nil {
				return err
			}
		}
		dir := m.store.layout.AttemptDir(uid, step.Key, step.Attempt)
		receipt, callErr := RunCommand(ctx, CommandSpec{Run: uid, Step: step.Key, Attempt: step.Attempt, Command: def.Command, Cwd: r.Cwd, AttemptDir: dir, Env: os.Environ(), Secrets: secrets, CredentialEnv: m.cfg.Kata.CredentialEnv, CredentialEnvs: m.cfg.CredentialVariables(), KataEnv: m.cfg.Kata.Env, Daemon: m.cfg.Kata.Daemon, RunDir: m.store.layout.RunDir(uid), ReportBin: report, Project: m.cfg.Kata.Project, Input: r.Input, Previous: previousNote(r, step.Key), Deadline: r.ExpiresAt})
		if receipt.Version == 0 {
			return m.commandUnknown(uid, index, "command_no_receipt", true)
		}
		// A failed artifact write or drain is also uncertainty, even if Wait had an exit.
		var disk Receipt
		if e := state.ReadJSON(m.store.fsys, filepath.Join(dir, "receipt.json"), 4096, &disk); e != nil || disk != receipt {
			receipt.Uncertain = true
		}
		if callErr != nil && receipt.ExitCode == 0 && !receipt.Interrupted && !receipt.GroupAlive {
			receipt.Uncertain = true
		}
		_, err = m.store.Transition(uid, func(r *Run) error { r.Steps[index].Receipt = &receipt; return nil })
		if err != nil {
			return err
		}
		r, err = m.store.Load(uid)
		if err != nil {
			return err
		}
		step = r.Steps[index]
	}
	if !certainReceipt(r.UID, step) {
		return m.commandUnknown(uid, index, "command_outcome_unknown", boundReceipt(step.Receipt, r.UID, step) && GroupGone(step.Receipt.PGID))
	}
	note, e := m.commandNote(r, index)
	if e != nil {
		return m.commandUnknown(uid, index, "invalid_note", true)
	}
	_, err = m.store.Transition(uid, func(r *Run) error {
		s := &r.Steps[index]
		s.Note = note
		s.Submission = Accepted
		s.Status = "succeeded"
		if s.Receipt.ExitCode != 0 {
			s.Status = "failed"
			s.Reason = fmt.Sprintf("command %s exited %d", s.Key, s.Receipt.ExitCode)
		}
		return nil
	})
	return err
}
func (m *Manager) commandUnknown(uid string, index int, reason string, stopped bool) error {
	_, err := m.store.Transition(uid, func(r *Run) error {
		r.ActiveStep = r.Steps[index].Key
		r.Steps[index].Status = "unknown"
		r.Steps[index].Reason = reason
		r.Phase = Held
		r.Outcome = "unknown"
		r.Reason = reason
		r.SlotReleased = stopped
		return nil
	})
	return err
}

// Each new attempt carries only its identity and caller-selected accounting.
func nextAttempt(old StepState, uncharged int, loopRerun bool) StepState {
	return StepState{Key: old.Key, Kind: old.Kind, Attempt: old.Attempt + 1, Uncharged: uncharged, Submission: None, LoopRerun: loopRerun}
}

func reportedFailure(uid string, s StepState) bool {
	return s.Result != nil && s.Result.Run == uid && s.Result.Step == s.Key && s.Result.Attempt == s.Attempt && s.Result.Status == "failed"
}

func attemptFailureProven(uid string, s StepState) bool {
	if s.Kind == "command" {
		return certainReceipt(uid, s) && s.Receipt.ExitCode != 0 && GroupGone(s.Receipt.PGID)
	}
	return reportedFailure(uid, s)
}
