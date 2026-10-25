package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

const maxSummaryBytes = 60 << 10

// SettleOps builds frozen settlement intents, without changing run state.
// Delivery must freeze each empty Guard from a fresh issue read, including the
// close's IfMatch. A resolve-recipient read saves the current owner (else author)
// into the following notify payload before its first send. No recipient is
// guessed here. These reads do not count toward the mutation budget.
func SettleOps(r *Run, res *Result, now time.Time) []Op {
	return settleOps(r, res, now, false)
}

// noClose preserves review reporting even when command receipts provide valid evidence.
func settleOps(r *Run, res *Result, now time.Time, noClose bool) []Op {
	if r.Phase == Blocked {
		return nil
	}
	// A parked prompt still reports its diagnosis; supplying the saved result
	// here does not change the held phase or authorize execution.
	if res == nil && r.Phase == Held && r.Reason == "step_failed" {
		for i, s := range r.Steps {
			if s.Key == r.ActiveStep && s.Kind == "prompt" && s.Status == "failed" && certainVerdict(r, i) {
				res = resultFromRef(s.Result)
				break
			}
		}
	}
	outcome, reason, message, closeReason := r.Outcome, HoldReason(r), "", "done"
	var evidence []string
	if res != nil {
		message = res.Summary
		evidence = append(evidence, res.Evidence...)
		if res.CloseReason != "" {
			closeReason = res.CloseReason
		}
		if outcome == "" {
			outcome = res.Status
		}
	}
	commandOutcome, commandResults, commandOnly, commandDetails := settlementCommands(r)
	if commandOnly {
		name := r.Job.Name
		if r.Workflow != nil && r.Workflow.Name != "" {
			name = r.Workflow.Name
		}
		if outcome == "" {
			outcome = commandOutcome
		}
		if outcome == "succeeded" && commandOutcome != "succeeded" {
			outcome = commandOutcome
			if reason == "" {
				reason = "command_not_succeeded"
			}
		}
		if commandOutcome == "succeeded" && outcome == "succeeded" {
			message = CommandCloseMessage(name, r.UID, r.Occurrence, commandResults)
			closeReason = "done"
			evidence = nil
		}
		if message == "" && commandDetails != "" {
			message = fmt.Sprintf("Workflow %s: %s (run %s, occurrence %s)", name, commandDetails, r.UID, r.Occurrence)
		}
	} else if len(commandResults) > 0 && commandOutcome != "succeeded" && outcome == "succeeded" {
		outcome = commandOutcome
		if reason == "" {
			reason = "command_not_succeeded"
		}
	}
	if closeReason == "done" {
		evidence = appendCommandEvidence(evidence, CommandEvidence(commandResults))
	}
	handoff := r.Phase == HandedOff || res != nil && res.Status == "needs-human"
	if res != nil && res.Status == "needs-human" {
		outcome = "unknown"
	}
	if r.Phase == HandedOff {
		// Release preserves only an outcome already known before the handoff.
		outcome = r.Outcome
	}
	if handoff && reason == "" {
		reason = message
	}
	switch outcome {
	case "succeeded", "failed", "unknown":
	default:
		outcome = "unknown"
	}
	if reason == "" && outcome != "succeeded" {
		reason = outcome
	}
	failureNote := ""
	if outcome == "failed" {
		if step := FailedStep(r); step != nil && strings.TrimSpace(step.Note) != "" {
			failureNote = fmt.Sprintf("Step %s: %s", step.Key, step.Note)
		}
	}
	message = capText(message, maxSummaryBytes)
	var ops []Op
	identity := "herdr-kata:" + r.UID
	if r.Resume != nil {
		identity += ":resume:" + r.Resume.UID
	}
	add := func(kind, suffix, key string, p Payload, guard *kata.Cond) {
		payload, _ := json.Marshal(p)
		ops = append(ops, Op{ID: identity + ":settle:" + suffix, Kind: kind, Payload: payload, Key: key, Guard: guard, Safety: kata.SafetyOf(kind)})
	}
	closeOK := !noClose && !handoff && outcome == "succeeded" && r.IssueOwned && (closeReason == "done" || closeReason == "audit-no-change") && ValidateClose(closeReason, message, evidence) == nil
	// A fallback helps explain an empty report in comments/history, but it
	// must never supply substance for an otherwise invalid agent close.
	if message == "" {
		message = fmt.Sprintf("Run %s: %s (%s)", r.UID, outcome, reason)
	}
	if failureNote != "" {
		message = capText(failureNote+"\n\n"+message, maxSummaryBytes)
	}
	var resumed strings.Builder
	resumed.WriteString(fmt.Sprintf("Run %s resumed and succeeded.", r.UID))
	successNote := ""
	if r.Resume != nil && outcome == "succeeded" && !handoff {
		for _, step := range slices.Backward(r.Steps) {

			if step.Status != "succeeded" {
				continue
			}
			if strings.TrimSpace(step.Note) != "" && defs.ValidateNote(step.Note) == nil {
				successNote = fmt.Sprintf("Step %s: %s", step.Key, step.Note)
				resumed.WriteString("\n\n" + successNote)
			}
			break
		}
	}
	if closeOK {
		runningMessage := r.Resume != nil
		for _, op := range r.Ops {
			if op.Kind != "meta.set" {
				continue
			}
			p, err := decodePayload(op)
			if err == nil && p.Name == "work.attention_msg" && p.Value == "herdr-kata: running" {
				runningMessage = true
			}
		}
		if runningMessage {
			add("meta.unset", "clear-running", "", Payload{Ref: r.IssueRef, Name: "work.attention_msg", Owner: attentionOwner(r)}, &kata.Cond{})
		}
		if r.Resume != nil {
			add("comment", "resumed", identity+":comment:resumed-succeeded", Payload{Ref: r.IssueRef, Body: resumed.String()}, nil)
		}
		key := identity + ":close"
		add("close", "close", key, Payload{Close: &kata.CloseReq{Ref: r.IssueRef, Reason: closeReason, Message: message, Evidence: evidence, IdempotencyKey: key}}, &kata.Cond{})
	} else {
		if r.Phase == Held && reason != "" {
			message = capText("Reason: "+capText(reason, 1024)+"\n"+message, maxSummaryBytes)
		}
		if r.Resume != nil && outcome == "succeeded" && !handoff {
			message = capText(resumed.String()+"\n\n"+message, maxSummaryBytes)
		}
		// Spec §23.4: a command-only success on an existing issue stays out of
		// its thread; the observation still carries the receipt message. A run
		// that already wrote to the thread, by a finding or the hold a resume
		// follows, still closes that story with a comment.
		quiet := commandOnly && outcome == "succeeded" && !handoff && r.Phase == Settled && r.Resume == nil && !r.IssueOwned && r.Child == nil && r.IssueRequest == nil && !slices.ContainsFunc(r.Ops, func(op Op) bool { return op.Kind == "comment" })
		if !quiet {
			add("comment", "comment", identity+":comment:"+outcome, Payload{Ref: r.IssueRef, Body: message}, nil)
		}
		attention, attentionMsg := "needs-human", "herdr-kata: "+reason
		if outcome == "succeeded" && !handoff {
			attentionMsg = "herdr-kata: review run " + r.UID
			if r.IssueOwned {
				add("label.add", "review-label", "", Payload{Ref: r.IssueRef, Name: "needs-review"}, nil)
			}
		} else if outcome == "failed" && !handoff {
			attention = "stuck"
		}
		if failureNote != "" {
			attentionMsg = failureNote + "\n" + attentionMsg
		}
		if r.IssueOwned {
			// Per-run issues are created unowned. Child takeover is extended by
			// the recurring-child unit; its expected owner is the pinned actor.
			owner := ""
			if takeover(r) != nil {
				owner = r.Target.Identity.Actor
			}
			add("meta.set", "attention", "", Payload{Ref: r.IssueRef, Name: "work.attention", Value: attention, Owner: owner}, &kata.Cond{})
			add("meta.set", "attention-msg", "", Payload{Ref: r.IssueRef, Name: "work.attention_msg", Value: capText(attentionMsg, maxSummaryBytes), Owner: owner}, &kata.Cond{})
		}
		if handoff || outcome == "unknown" {
			add("resolve-recipient", "recipient", "", Payload{Ref: r.IssueRef}, nil)
			add("notify", "notify", "", Payload{Ref: r.IssueRef, Message: capText("herdr-kata: "+reason, 1024)}, nil)
		}
	}
	summary := message
	if successNote != "" && closeOK {
		summary = successNote + "\n\n" + summary
	}
	if reason != "" && (outcome != "succeeded" || handoff) && !strings.Contains(summary, reason) {
		// Reserve the beginning for the reason, so a long agent report cannot
		// push the diagnostic past the final observation's size cap.
		summary = "Reason: " + capText(reason, 1024) + "\n" + summary
	}
	ended := now.UTC()
	if r.EndedAt != nil {
		ended = *r.EndedAt
	}
	teammate := "herdr-kata"
	o := kata.Observation{JobUID: r.Job.UID, DefinitionEventUID: r.Job.DefinitionEventUID, OccurrenceKey: r.Occurrence, IssueUID: r.IssueUID, Actor: r.Target.Identity.Actor, ExecutorLabel: r.Target.Executor, Teammate: &teammate, Status: outcome, Summary: kata.Summary{Version: 1, Message: capText(summary, maxSummaryBytes)}, StartedAt: r.StartedAt, EndedAt: &ended, ExpectedRevision: 1}
	o.Transcripts, o.TranscriptsIncomplete = extractRunTranscripts(r)
	if len(o.Transcripts) > 0 {
		provenance, _ := json.Marshal(o.Transcripts)
		o.Summary.Message = capText(o.Summary.Message, maxSummaryBytes-len(provenance))
	}
	if u := r.Usage; u != nil && u.Found && !u.Partial && !handoff {
		// Kata's input count includes cached prompt tokens. Unknown totals, and a
		// handoff whose current attempt may be uncollected, stay omitted.
		o.Summary.InputTokens = u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens
		o.Summary.OutputTokens = u.OutputTokens
	}
	if r.Workflow != nil {
		o.WorkflowUID = r.Workflow.UID
		o.WorkflowDefinitionEventUID = r.Workflow.DefinitionEventUID
	}
	for _, op := range r.Ops {
		if op.Kind != "observe" || !op.Landed() {
			continue
		}
		var p Payload
		if json.Unmarshal(op.Payload, &p) == nil && p.Observation != nil {
			o.ExpectedRevision = max(o.ExpectedRevision, p.Observation.ExpectedRevision+1)
		}
	}
	if r.IssueRequest != nil {
		ops = append(ops, requestOutcomeOp(r, identity+":settle:outcome", r.Phase, outcome, reason, summary, &ended))
	} else {
		add("observe", "observe", "", Payload{Observation: &o}, nil)
	}
	if key, raw := savedNotification(r); key != "" {
		saved := string(raw)
		add("meta.unset", "ack", "", Payload{Ref: r.IssueRef, Name: key}, &kata.Cond{JSONValue: true, IfValue: &saved})
	}
	return ops
}

// settlementCommands requires an attempt-bound zero-exit receipt for every
// frozen command definition. Missing state is uncertainty, never success.
func settlementCommands(r *Run) (outcome string, results []CommandStepResult, only bool, details string) {
	outcome, only = "succeeded", r.WFDef != nil && len(r.WFDef.Steps) > 0
	if r.WFDef == nil {
		return outcome, nil, false, ""
	}
	var problems []string
	for _, def := range r.WFDef.Steps {
		if def.Kind != "command" {
			only = false
			continue
		}
		results = append(results, CommandStepResult{Command: def.Command})
		var step *StepState
		for i := range r.Steps {
			if r.Steps[i].Key == def.Key {
				step = &r.Steps[i]
				break
			}
		}
		if step == nil || step.Kind != "command" || step.Receipt == nil || step.Receipt.Version != 1 || step.Receipt.Run != r.UID || step.Receipt.Step != def.Key || step.Receipt.Attempt != step.Attempt || step.Attempt < 1 {
			detail := "no valid command receipt"
			if step != nil && step.Kind == "command" && (step.Submission == None || step.Submission == "") && step.SubmittedAt == nil && step.Receipt == nil && step.Status == "" {
				detail = "not started"
			}
			problems = append(problems, def.Key+": "+detail)
			if outcome != "failed" {
				outcome = "unknown"
			}
			continue
		}
		if !step.Receipt.definite() {
			problems = append(problems, def.Key+": command outcome unknown")
			if outcome != "failed" {
				outcome = "unknown"
			}
			continue
		}
		if step.Receipt.ExitCode != 0 {
			problems = append(problems, fmt.Sprintf("%s exited %d", def.Key, step.Receipt.ExitCode))
			outcome = "failed"
			continue
		}
		if step.Status != "succeeded" {
			problems = append(problems, def.Key+": "+step.Status)
			if outcome != "failed" {
				outcome = "unknown"
			}
			continue
		}
		results[len(results)-1].Receipt = step.Receipt
	}
	return outcome, results, only, strings.Join(problems, "; ")
}

func appendCommandEvidence(items, commands []string) []string {
	seen := map[string]bool{}
	for _, e := range items {
		seen[e] = true
	}
	for _, e := range commands {
		if !seen[e] {
			seen[e] = true
			items = append(items, e)
		}
	}
	return items
}

// Bound serialized JSON text too: escaping control characters or HTML can
// otherwise turn a 60 KiB string into a summary exceeding Kata's 64 KiB cap.
func capText(s string, budget int) string {
	if len(s) > budget {
		s = s[:budget]
		for !utf8.ValidString(s) && len(s) > 0 {
			s = s[:len(s)-1]
		}
	}
	if data, _ := json.Marshal(s); len(data) <= budget {
		return s
	}
	lo, hi := 0, len(s)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		data, _ := json.Marshal(s[:mid])
		if len(data) <= budget {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	for lo > 0 && !utf8.ValidString(s[:lo]) {
		lo--
	}
	return s[:lo]
}

// settle records the terminal phase, evidence and pending operations atomically.
// Held runs retain their overlap gate and any possibly-live execution slot.
func (m *Manager) settle(ctx context.Context, uid string, res *Result) error {
	if err := m.collectRunUsage(ctx, uid); err != nil && ctx.Err() == nil {
		return err
	}
	_, err := m.store.Transition(uid, func(r *Run) error {
		if r.EndedAt != nil {
			return nil
		}
		now := m.clock.Now().UTC()
		if res != nil {
			r.Outcome = res.Status
			if res.Status == "needs-human" {
				r.Phase = HandedOff
				r.Outcome = "unknown"
				r.Reason = res.Summary
			} else {
				r.Phase = Settled
				if res.Status == "failed" {
					r.Reason = res.Summary
				}
			}
		} else if r.Phase != Held && r.Phase != HandedOff {
			return nil
		}
		r.EndedAt = &now
		r.Ops = append(r.Ops, SettleOps(r, res, now)...)
		return nil
	})
	if err == nil {
		if m.q != nil {
			m.q.Kick(uid)
		}
		m.WakeProjects()
	}
	return err
}

// FailedStep returns the active failed step, or the first failed dependency
// when a non-parking workflow has already advanced to later steps.
func FailedStep(r *Run) *StepState {
	for i := range r.Steps {
		if r.Steps[i].Key == r.ActiveStep && r.Steps[i].Status == "failed" {
			return &r.Steps[i]
		}
	}
	for i := range r.Steps {
		if r.Steps[i].Status == "failed" {
			return &r.Steps[i]
		}
	}
	return nil
}
