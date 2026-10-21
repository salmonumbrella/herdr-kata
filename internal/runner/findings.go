package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type Findings struct {
	RootUID, HeadUID, MakerUID string
	Assertions                 map[string][]string
}
type ChainComment struct {
	Step                                          string
	Attempt                                       int
	Purpose, Target, Mode, Participant, Assertion string
}
type FindingContext struct {
	RootUID, HeadUID, MakerUID, Text string
	Truncated                        bool
	// Operator notes: the run actor's own replies to the chain, context only.
	Notes          string `json:",omitempty"`
	NotesTruncated bool   `json:",omitempty"`
}

var errChainPending = errors.New("finding comment has no saved UID")

// First assertions retain the claim as target; all general continuations use
// the head.
func findingTarget(f Findings, assertion string) (string, string) {
	if assertion != "" && f.MakerUID != "" && !slices.Contains(f.Assertions[f.MakerUID], assertion) {
		return f.MakerUID, assertion
	}
	return f.HeadUID, ""
}
func pendingChain(r *Run) *Op {
	for i := range r.Ops {
		op := &r.Ops[i]
		p, e := decodePayload(*op)
		if e == nil && p.Chain != nil && (!op.Done || op.CommentUID == "") {
			return op
		}
	}
	return nil
}
func chainID(r *Run, s StepState, purpose string) string {
	return fmt.Sprintf("herdr-kata:%s:step:%s:attempt:%d:%s", r.UID, s.Key, s.Attempt, purpose)
}

func findingPurpose(r *Run, index int) string {
	edge := loopEdge(r)
	if edge == nil {
		return ""
	}
	s := r.Steps[index]
	if s.Status != "failed" && s.Status != "succeeded" {
		return ""
	}
	if s.Key == edge.Checker && (s.Status == "failed" || r.Findings.RootUID != "") {
		return "checker"
	}
	if s.Key == edge.Goto && r.Findings.RootUID != "" {
		return "maker"
	}
	return ""
}
func prepareFinding(r *Run, index int, modes ...string) (string, error) {
	purpose := findingPurpose(r, index)
	if purpose == "" {
		return "", nil
	}
	s := r.Steps[index]
	id := chainID(r, s, purpose)
	if _, e := findOp(r, id); e == nil {
		return id, nil
	}
	if pendingChain(r) != nil {
		return "", errChainPending
	}
	mode := "plain"
	if len(modes) > 0 {
		mode = modes[0]
	}
	body := s.Reason
	if s.Result != nil {
		body = s.Result.Summary
	}
	body = strings.TrimSpace(body)
	prefix := ""
	if s.Note != "" {
		prefix = "Note: " + s.Note + "\n\n"
	}
	body = capText(prefix+body, 16<<10)
	evidence := ""
	if after, ok := strings.CutPrefix(body, prefix); ok {
		evidence = after
	}
	if strings.TrimSpace(evidence) == "" {
		body = prefix + fmt.Sprintf("%s: %s", s.Key, s.Status)
	}
	assertion, downgrade := "", ""
	if s.Result != nil && s.Result.Assert != "" {
		switch {
		case purpose != "checker":
			downgrade = "only the loop checker can assert"
		case s.FindingContext == nil || s.FindingContext.MakerUID == "" || s.Result.Target != s.FindingContext.MakerUID || s.Result.Target != r.Findings.MakerUID:
			downgrade = "target is not the saved maker claim"
		case slices.Contains(r.Findings.Assertions[r.Findings.MakerUID], s.Result.Assert):
			downgrade = "kind already published for maker claim"
		case utf8.RuneCountInString(strings.Join(strings.Fields(evidence), " ")) < 40:
			downgrade = "evidence is too short (requires 40 normalized Unicode characters)"
		default:
			assertion = s.Result.Assert
		}
	}
	target, assertion := findingTarget(r.Findings, assertion)
	kind := ""
	if target != "" {
		kind = "reply"
	}
	if assertion != "" {
		kind = assertion
	}
	header := fmt.Sprintf("Run %s · step %s · attempt %d · %s", r.UID, s.Key, s.Attempt, s.Status)
	if target != "" {
		header += "\nAnswers c:" + target
	}
	if assertion != "" {
		verb := "confirms"
		if assertion == "refute" {
			verb = "refutes"
		}
		header += "\n" + verb + " c:" + target
	}
	if downgrade != "" {
		header += "\nAssertion " + s.Result.Assert + " c:" + s.Result.Target + " downgraded to general reply: " + downgrade
	}
	p := Payload{Mode: mode, ReplyTo: target, ReplyKind: kind, Ref: r.IssueRef, Body: header + "\n\n" + body, Chain: &ChainComment{Step: s.Key, Attempt: s.Attempt, Purpose: purpose, Target: target, Mode: mode, Assertion: assertion, Participant: r.Target.Identity.Actor}}
	r.Ops = append(r.Ops, newOp(id, "comment", p, id))
	return id, nil
}
func (m *Manager) waitFinding(ctx context.Context, uid, id string) error {
	if m.q == nil {
		return errors.New("findings require the write queue")
	}
	r, e := m.store.Load(uid)
	if e != nil {
		return e
	}
	remaining := r.ExpiresAt.Sub(m.clock.Now())
	if remaining <= 0 {
		return m.hold(uid, "timeout")
	}
	waitCtx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	e = m.q.wait(waitCtx, uid, id)
	if errors.Is(e, context.DeadlineExceeded) && ctx.Err() == nil {
		return m.hold(uid, "timeout")
	}
	return e
}
func (m *Manager) publishFinding(ctx context.Context, uid string, index int) error {
	current, e := m.store.Load(uid)
	if e != nil {
		return e
	}
	purpose := findingPurpose(current, index)
	if purpose == "" {
		return nil
	}
	if op, e := findOp(current, chainID(current, current.Steps[index], purpose)); e == nil {
		if op.Done && op.CommentUID != "" {
			return nil
		}
		return m.waitFinding(ctx, uid, op.ID)
	}
	mode := m.findingMode(ctx, uid)
	for {
		var id string
		var r *Run
		_, e = m.store.Transition(uid, func(r *Run) error { var e error; id, e = prepareFinding(r, index, mode); return e })
		if errors.Is(e, errChainPending) {
			r, e = m.store.Load(uid)
			if e != nil {
				return e
			}
			if op := pendingChain(r); op != nil {
				id = op.ID
			} else {
				continue
			}
		} else if e != nil {
			return e
		} else if id == "" {
			return nil
		}
		if e = m.waitFinding(ctx, uid, id); e != nil {
			return e
		}
		r, e = m.store.Load(uid)
		if e != nil {
			return e
		}
		if r.Phase == Held {
			return nil
		}
		if op, e := findOp(r, chainID(r, r.Steps[index], "maker")); e == nil && op.Done {
			return nil
		}
		if op, e := findOp(r, chainID(r, r.Steps[index], "checker")); e == nil && op.Done {
			return nil
		}
	}
}

// Freeze a bounded canonical read once per attempt, before writing its prompt.
func (m *Manager) prepareFindingContext(ctx context.Context, uid string, index int) error {
	r, e := m.store.Load(uid)
	if e != nil {
		return e
	}
	if r.Steps[index].FindingContext != nil {
		return nil
	}
	if op := pendingChain(r); op != nil {
		if e = m.waitFinding(ctx, uid, op.ID); e != nil {
			return e
		}
		r, e = m.store.Load(uid)
		if e != nil {
			return e
		}
	}
	if r.Phase == Held || r.Findings.RootUID == "" {
		return nil
	}
	if m.q == nil {
		return errors.New("findings require the write queue")
	}
	delivery := m.q.forRun(r)
	if delivery == nil {
		return errors.New("waiting for project " + r.Target.Identity.ProjectUID)
	}
	var issue kata.IssueView
	for delay := 2 * time.Second; ; delay = min(delay*2, 30*time.Second) {
		if e = ctx.Err(); e != nil {
			return e
		}
		remaining := r.ExpiresAt.Sub(m.clock.Now())
		if remaining <= 0 {
			return m.hold(uid, "timeout")
		}
		readCtx, cancel := context.WithTimeout(ctx, min(30*time.Second, remaining))
		issue, e = delivery.showIssue(readCtx, uid, r.IssueRef)
		cancel()
		if e == nil {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if kata.Class(e) != kata.Retry {
			return e
		}
		if e = m.pause(ctx, min(delay, max(time.Duration(0), r.ExpiresAt.Sub(m.clock.Now())))); e != nil {
			return e
		}
	}
	chain := chainUIDs(r)
	wanted := maps.Clone(chain)
	fc := &FindingContext{RootUID: r.Findings.RootUID, HeadUID: r.Findings.HeadUID, MakerUID: r.Findings.MakerUID}
	var lines []string
	size, count := 0, 0
	for _, c := range issue.Comments {
		if !wanted[c.UID] {
			continue
		}
		delete(wanted, c.UID)
		line := "c:" + c.UID + "\n" + c.Body + "\n"
		extra := len(line)
		if len(lines) > 0 {
			extra++
		}
		if count >= 50 || size+extra > 16<<10 {
			fc.Truncated = true
			continue
		}
		lines = append(lines, line)
		size += extra
		count++
	}
	fc.Truncated = fc.Truncated || len(wanted) > 0
	fc.Text = strings.Join(lines, "\n")
	fc.Notes, fc.NotesTruncated = operatorNotes(r, chain, issue.Comments)
	_, e = m.store.Transition(uid, func(current *Run) error {
		if pendingChain(current) != nil || current.Findings.HeadUID != fc.HeadUID {
			return errChainPending
		}
		if current.Steps[index].FindingContext == nil {
			current.Steps[index].FindingContext = fc
		}
		return nil
	})
	return e
}

// Operator notes are the run actor's replies to this run's published chain,
// excluding the runtime's own comments by teammate and by saved UID. The newest
// 10 within 4 KiB are kept.
func operatorNotes(r *Run, chain map[string]bool, comments []kata.Comment) (string, bool) {
	own := map[string]bool{}
	for _, op := range r.Ops {
		own[op.CommentUID] = true
	}
	var notes []string
	size, truncated := 0, false
	for _, c := range slices.Backward(comments) {
		if !chain[c.ReplyToUID] || c.Author != r.Target.Identity.Actor || c.Teammate == "herdr-kata" || own[c.UID] {
			continue
		}
		note := "c:" + c.UID + " answers c:" + c.ReplyToUID + "\n" + c.Body + "\n"
		extra := len(note)
		if len(notes) > 0 {
			extra++
		}
		if len(notes) >= 10 || size+extra > 4<<10 {
			truncated = true
			continue
		}
		notes = append(notes, note)
		size += extra
	}
	slices.Reverse(notes)
	return strings.Join(notes, "\n"), truncated
}

func chainUIDs(r *Run) map[string]bool {
	uids := map[string]bool{}
	for _, op := range r.Ops {
		p, e := decodePayload(op)
		if e == nil && p.Chain != nil && op.Done && op.CommentUID != "" {
			uids[op.CommentUID] = true
		}
	}
	return uids
}

func (m *Manager) findingMode(ctx context.Context, uid string) string {
	if m.q != nil {
		typed, reason := m.q.k.TypedReplies(ctx)
		if typed {
			return "typed"
		}
		slog.Debug("plain finding replies", "run", uid, "reason", reason)
	}
	return "plain"
}
