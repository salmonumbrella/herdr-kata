package runner

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/inbox"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// One stable child identity owns the permanent fence, regardless of request edits.
func (m *Manager) admitIssueRequest(ctx context.Context, d sched.Due) (sched.AdmitResult, error) {
	c := d.IssueRequest
	uid, err := ident.IssueRequestUID(c.IssueUID)
	if err != nil {
		return sched.NotAdmitted, err
	}
	lock := m.admissionLock(uid)
	lock.Lock()
	defer lock.Unlock()
	if ctx.Err() != nil {
		return sched.NotAdmitted, ctx.Err()
	}
	for _, path := range []string{m.store.layout.StopFile(), m.store.layout.UpgradeFile()} {
		if _, e := m.store.fsys.Lstat(path); e == nil {
			return sched.NotAdmitted, nil
		} else if !missing(e) {
			return sched.NotAdmitted, e
		}
	}
	if m.guard == nil || m.q == nil {
		return sched.NotAdmitted, errors.New("missing admission route or queue")
	}
	if err = m.guard.Check(ctx); err != nil {
		return sched.NotAdmitted, err
	}
	if state.Quarantined(m.store.fsys, m.store.layout, uid) {
		return sched.Dropped, nil
	}
	target, err := m.pin()
	if err != nil {
		return sched.NotAdmitted, err
	}
	r, err := m.store.Load(uid)
	// Unconfirmed admissions keep their read-back-only reconciliation below.
	if err != nil || !requestAdmissionUnconfirmed(r) {
		issue, e := m.k.ShowIssue(ctx, c.IssueRef)
		if e != nil {
			return sched.NotAdmitted, e
		}
		if missing(err) && issue.Status != "open" {
			if _, receipt := issue.Metadata["herdr_kata.admission"]; receipt {
				return sched.Dropped, nil
			}
		}
		if status, reason, e := m.CheckIssueRequest(ctx, issue); e != nil {
			return sched.NotAdmitted, e
		} else if status != "" {
			return sched.Dropped, errors.New(reason)
		}
	}
	if missing(err) {
		// Any receipt, even malformed or from this executor, vetoes reconstruction.
		issue, e := m.k.ShowIssue(ctx, c.IssueRef)
		if e != nil {
			return sched.NotAdmitted, e
		}
		if _, exists := issue.Metadata["herdr_kata.admission"]; exists {
			return sched.Dropped, nil
		}
		if blocked, e := m.store.SigningBlockedFor(m.projectUID); e != nil {
			return sched.NotAdmitted, e
		} else if blocked {
			return sched.NotAdmitted, errors.New("signing_blocked")
		}
		d, e = m.requestInputs(ctx, d, target.Identity.ProjectUID)
		if e != nil {
			return sched.NotAdmitted, e
		}
		if !m.reserve(uid, "issue-request:"+c.IssueUID, false) {
			if e := m.ReconcileHeld(ctx); e != nil {
				return sched.NotAdmitted, e
			}
			if !m.reserve(uid, "issue-request:"+c.IssueUID, false) {
				return sched.NotAdmitted, nil
			}
		}
		durable := false
		defer func() {
			if !durable {
				m.unreserve(uid)
			}
		}()
		r, e = m.prepare(d, uid, "issue-request:"+c.IssueUID, target)
		if e != nil {
			return sched.NotAdmitted, e
		}
		if e = m.store.Create(r); e != nil {
			if _, loadErr := m.store.Load(uid); loadErr == nil {
				durable = true
			}
			return sched.NotAdmitted, e
		}
		durable = true
	} else if err != nil {
		return sched.NotAdmitted, err
	}
	if r.IssueRequest == nil || !sameTarget(r.Target, target) {
		return sched.NotAdmitted, errors.New("issue request target differs from journal")
	}
	if requestAdmissionUnconfirmed(r) {
		issue, e := m.k.ShowIssue(ctx, r.IssueRef)
		if e != nil {
			return sched.NotAdmitted, e
		}
		if requestReceiptMatches(r, issue) {
			// Hold capacity accounting across the journal transition. A released
			// hold must not be pruned between reserving and becoming prepared.
			m.mu.Lock()
			m.prune()
			occupied, projectOccupied := m.tallyLocked("", m.store.tryLoad)
			if occupied >= m.cfg.Limits.MaxActiveRuns || m.cfg.ProjectLimit > 0 && projectOccupied >= m.cfg.ProjectLimit {
				m.mu.Unlock()
				return sched.NotAdmitted, nil
			}
			r, err = m.store.Transition(uid, func(current *Run) error {
				if requestAdmissionUnconfirmed(current) {
					current.Phase, current.Reason, current.Inspect = Prepared, "", ""
					current.SlotReleased = false
				}
				return nil
			})
			if err == nil {
				m.reservations[uid] = gateKey(m.projectUID, "issue-request:"+r.IssueUID)
			}
			m.mu.Unlock()
			if err != nil {
				return sched.NotAdmitted, err
			}
		}
	}
	if preAdmissionHold(r) {
		return sched.Dropped, nil
	}
	if r.Phase == Prepared {
		if !m.reserve(uid, "issue-request:"+r.IssueUID, false) {
			return sched.NotAdmitted, nil
		}
		if err = m.admitPrepared(ctx, r); err != nil {
			return sched.NotAdmitted, err
		}
	}
	if err = m.Execute(ctx, uid); err != nil {
		return sched.NotAdmitted, err
	}
	return sched.Admitted, nil
}

func (m *Manager) requestInputs(ctx context.Context, d sched.Due, project string) (sched.Due, error) {
	c := *d.IssueRequest
	issue, err := m.k.ShowIssue(ctx, c.IssueRef)
	if err != nil {
		return d, err
	}
	request, err := inbox.ValidateRequest(issue, project)
	if err != nil {
		return d, err
	}
	if request != c.Request || issue.UID != c.IssueUID || issue.ParentUID != c.ParentUID {
		return d, errors.New("issue request changed before preparation")
	}
	decision, _, err := inbox.Takeover(issue, m.cfg.Kata.Actor)
	if err != nil {
		return d, err
	}
	if err := requestReady(ctx, m.k, issue.UID, project); err != nil {
		return d, err
	}
	parent, err := m.k.ShowIssue(ctx, c.ParentUID)
	if err != nil {
		return d, err
	}
	if parent.UID != c.ParentUID || parent.ProjectUID != project || parent.Status != "open" {
		return d, errors.New("issue request parent unavailable")
	}
	bindings, ok := m.k.(inbox.BindingReader)
	if !ok {
		return d, errors.New("binding reader unavailable")
	}
	c.Binding, err = inbox.ResolveBinding(ctx, bindings, parent, m.cfg)
	if err != nil {
		return d, err
	}
	c.NotifyKey, c.NotifyJSON, err = inbox.AckOp(issue, m.cfg.Kata.Inbox)
	if err != nil {
		return d, err
	}
	c.Ownership = sched.Ownership{Decision: decision, Revision: issue.Revision}
	options, _ := json.Marshal(map[string]string{"agent": c.Binding.Profile})
	d.Job = kata.Job{Name: issue.Title}
	d.Def = defs.JobDef{Version: 1, Action: defs.Action{Kind: "execute", Prompt: "Work Kata issue " + c.IssueRef}, Issue: &defs.IssuePolicy{Kind: "existing", UID: issue.UID, Title: issue.Title, Body: issue.Body}, Options: options, Overlap: "allow"}
	d.WFDef = c.Binding.WFDef
	d.Workflow = nil
	if d.WFDef != nil {
		d.Workflow = &kata.Workflow{UID: c.Binding.WorkflowUID, DefinitionEventUID: c.Binding.WorkflowEventUID, Name: c.Binding.WorkflowName}
	}
	d.IssueRequest = &c
	return d, nil
}

func requestAdmissionOp(r *Run) Op {
	b, _ := json.Marshal(r.IssueRequest.Admission)
	op := newOp("admission", "meta.set", Payload{Ref: r.IssueRef, Name: "herdr_kata.admission", Value: string(b)}, "")
	op.Guard = &kata.Cond{IfAbsent: true, JSONValue: true, IfMatch: strconv.FormatInt(r.IssueRequest.Ownership.Revision, 10)}
	return op
}

// Guard preparation is persisted by Queue.send before any mutation. A restarted
// ambiguous CAS is read back, never sent a second time.
func (q *Queue) requestPreflight(ctx context.Context, r *Run, op Op, p *Payload) (bool, error) {
	if op.Kind == "observe" {
		return false, q.inspect(r.UID, op.ID, "issue request cannot write cron history")
	}
	if p.Name != "herdr_kata.admission" && p.Name != "herdr_kata.outcome" {
		return true, nil
	}
	if op.FirstSent != nil && !op.RetryIdentical {
		return false, q.resolve(ctx, r.UID, op, *p, nil)
	}
	issue, err := q.showIssue(ctx, r.UID, r.IssueRef)
	if err != nil {
		return false, q.failedRead(ctx, r.UID, op, err, true)
	}
	if p.Name == "herdr_kata.admission" {
		request, err := inbox.ValidateRequest(issue, r.Target.Identity.ProjectUID)
		if err != nil || request != r.IssueRequest.Request || issue.UID != r.IssueUID || issue.ParentUID != r.IssueRequest.ParentUID || issue.Owner != "" && issue.Owner != r.Target.Identity.Actor {
			return false, q.admissionHold(r.UID, "issue request changed before admission")
		}
		if err := requestReady(ctx, q.k, r.IssueUID, r.Target.Identity.ProjectUID); err != nil {
			if errors.Is(err, errRequestNotReady) {
				return false, q.admissionHold(r.UID, err.Error())
			}
			return false, q.failedRead(ctx, r.UID, op, err, true)
		}
		parent, err := q.k.ShowIssue(ctx, r.IssueRequest.ParentUID)
		if err != nil {
			return false, q.failedRead(ctx, r.UID, op, err, true)
		}
		if parent.UID != r.IssueRequest.ParentUID || parent.ProjectUID != r.Target.Identity.ProjectUID || parent.Status != "open" {
			return false, q.admissionHold(r.UID, "issue request parent unavailable")
		}
		return true, nil
	}
	if !requestReceiptMatches(r, issue) || issue.Owner != r.Target.Identity.Actor {
		return false, q.inspect(r.UID, op.ID, "issue request reporting identity changed")
	}
	if op.Guard.IfMatch != "" {
		return true, nil
	}
	// Only the prior delivered runtime intent may be replaced, never a value
	// merely discovered in Kata after preparation.
	previous := ""
	for _, prior := range r.Ops {
		if prior.ID == op.ID {
			break
		}
		if prior.Done && prior.Inspect == "" && prior.Kind == "meta.set" {
			body, e := decodePayload(prior)
			if e == nil && body.Name == p.Name {
				previous = body.Value
			}
		}
	}
	op.Guard.JSONValue = true
	if previous == "" {
		op.Guard.IfAbsent = true
		if _, exists := issue.Metadata[p.Name]; exists {
			return false, q.inspect(r.UID, op.ID, "outcome changed before reporting")
		}
	} else {
		if !jsonEqual(issue.Metadata[p.Name], []byte(previous)) {
			return false, q.inspect(r.UID, op.ID, "outcome changed before reporting")
		}
		op.Guard.IfValue = &previous
	}
	op.Guard.IfMatch = strconv.FormatInt(issue.Revision, 10)
	// The caller shares this Guard pointer with its frozen operation copy.
	return true, nil
}

var errRequestNotReady = errors.New("issue request is not ready")

func requestReady(ctx context.Context, k Kata, uid, project string) error {
	reader, ok := k.(interface {
		Ready(context.Context) ([]kata.IssueView, error)
	})
	if !ok {
		return errors.New("readiness reader unavailable")
	}
	rows, err := reader.Ready(ctx)
	if err != nil {
		return err
	}
	for _, i := range rows {
		if i.UID == uid && i.ProjectUID == project && i.Status == "open" {
			return nil
		}
	}
	return errRequestNotReady
}

// An absent first read-back is not proof that the in-flight CAS cannot land.
// Only this unresolved hold can return to preparation, and only with the exact
// receipt. The saved FirstSent marker still makes delivery read-back-only.
func requestAdmissionUnconfirmed(r *Run) bool {
	return r.IssueRequest != nil && preAdmissionHold(r) && r.Inspect == "issue request admission not confirmed"
}
