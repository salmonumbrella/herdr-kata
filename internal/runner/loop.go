package runner

import (
	"context"
	"errors"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
)

type LoopState struct {
	Used            int
	ConsumedAttempt int
	Rejection       string
	Comparable      bool
}

func loopEdge(r *Run) *defs.Loop     { edge, _ := defs.WorkflowLoop(r.WFDef); return edge }
func loopHold(r *Run, reason string) { r.Phase = Held; r.Outcome = "failed"; r.Reason = reason }
func pendingLoop(r *Run) bool {
	edge := loopEdge(r)
	if edge == nil {
		return false
	}
	for _, s := range r.Steps {
		if s.Key == edge.Checker && s.Status == "failed" {
			return true
		}
	}
	return false
}

// A saved verdict still belongs on the issue even when recovery parks before
// deciding its edge. The queue captures its UID without authorizing execution.
func (m *Manager) holdPendingLoop(ctx context.Context, uid string) error {
	current, err := m.store.Load(uid)
	if err != nil {
		return err
	}
	mode := "plain"
	edge := loopEdge(current)
	for _, step := range current.Steps {
		if edge != nil && step.Key == edge.Checker {
			if _, err := findOp(current, chainID(current, step, "checker")); err != nil {
				mode = m.findingMode(ctx, uid)
			}
			break
		}
	}

	_, err = m.store.Transition(uid, func(r *Run) error { return holdPendingLoop(r, mode) })
	return err
}
func holdPendingLoop(r *Run, mode string) error {
	edge := loopEdge(r)
	if edge == nil {
		return errors.New("missing loop edge")
	}
	for i, s := range r.Steps {
		if s.Key == edge.Checker {
			if _, e := prepareFinding(r, i, mode); e != nil {
				return e
			}
			loopHold(r, "loop_pending")
			return nil
		}
	}
	return errors.New("missing loop checker")
}

// takeLoop consumes the verdict, budget and entire span in one journal write.
// Callers that recover an undecided verdict must hold, rather than call this.
func (m *Manager) takeLoop(ctx context.Context, uid string, index int) error {
	r, e := m.store.Load(uid)
	if e != nil {
		return e
	}
	edge := loopEdge(r)
	if edge == nil || r.Steps[index].Key != edge.Checker || r.Steps[index].Status != "failed" {
		return nil
	}
	s := r.Steps[index]
	if !attemptFailureProven(uid, s) {
		if s.Kind == "command" {
			return m.hold(uid, "command_group_unproven")
		}
		return errors.New("loop requires attempt-bound verdict")
	}
	// A rewind switches agents. Prove every saved occupant stopped first.
	for _, ref := range agentReferences(r) {
		if ref.AgentName == "" && !ref.LaunchIntent {
			continue
		}
		a, err := m.proof(ctx, ref)
		if (err != nil || !settledAgent(a) || !(sequenceAdvanced(ref, a) || retainedNoActivity(r, ref))) && !m.paneExited(ctx, ref) && !m.failedStartStopped(ctx, ref) {
			return m.hold(uid, switchUnproven(err))
		}
	}
	_, e = m.store.Transition(uid, func(r *Run) error {
		current := r.Steps[index]
		if current.Attempt != s.Attempt || current.Status != "failed" || r.Loop.ConsumedAttempt >= s.Attempt {
			return nil
		}
		if r.Phase == HandedOff || r.Phase == Settled {
			return errors.New("loop run no longer active")
		}
		return applyLoop(r, index)
	})
	return e
}

// applyLoop is shared by an immediate verdict and an authorized pending edge.
func applyLoop(r *Run, index int) error {
	edge := loopEdge(r)
	current := r.Steps[index]
	normalized := strings.Join(strings.Fields(current.Note), " ")
	if r.Loop.Used >= edge.MaxLoops {
		loopHold(r, "loop_exhausted")
		return nil
	}
	if normalized != "" && r.Loop.Comparable && normalized == strings.Join(strings.Fields(r.Loop.Rejection), " ") {
		loopHold(r, "loop_stuck")
		return nil
	}
	r.Loop.ConsumedAttempt = current.Attempt
	r.Loop.Used++
	r.Loop.Rejection = current.Note
	r.Loop.Comparable = normalized != ""
	for i, old := range r.Steps {
		for _, key := range edge.Span {
			if old.Key == key {
				if old.Kind == "prompt" {
					ref := r.Herdr
					if old.Herdr != nil {
						ref = *old.Herdr
					}
					retainAgentRef(r, i, ref)
				} else {
					r.PastAttempts = append(r.PastAttempts, old)
				}
				r.Steps[i] = nextAttempt(old, old.Uncharged, true)
			}
		}
	}
	r.ActiveStep = ""
	return nil
}
