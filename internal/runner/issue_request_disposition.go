package runner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/inbox"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// CheckIssueRequest overlays durable admission evidence on inbox discovery.
// It does not infer execution authority from a receipt without a journal.
func (m *Manager) CheckIssueRequest(ctx context.Context, issue kata.IssueView) (status, reason string, err error) {
	if m.guard == nil {
		return "", "", errors.New("missing admission route")
	}
	// The scanner supplies a route-fenced view; admission and acknowledgement
	// writes perform their own fresh checks. Pure classification uses the
	// established lane evidence when available; other checkers retain a live
	// check.
	if guard, ok := m.guard.(interface{ Allowed() bool }); ok {
		if !guard.Allowed() {
			return "", "", errors.New("project route refused")
		}
	} else if err := m.guard.Check(ctx); err != nil {
		return "", "", err
	}
	target, err := m.pin()
	if err != nil {
		return "", "", err
	}
	uid, err := ident.IssueRequestUID(issue.UID)
	if err != nil {
		return "", "", err
	}
	refuse := func(reason string) (string, string, error) {
		err := m.ackRefusedRequest(ctx, issue, target, reason)
		return inbox.Unsupported, reason, err
	}
	if state.Quarantined(m.store.fsys, m.store.layout, uid) {
		return inbox.IgnoredRequest, "request journal quarantined; inspect herdr-kata doctor", nil
	}
	r, err := m.store.Load(uid)
	if missing(err) {
		if _, exists := issue.Metadata["herdr_kata.admission"]; !exists {
			return "", "", nil
		}
		if a, ok := ownedRequestReceipt(issue, target); ok {
			if o, ok := receiptOutcome(a, issue.Metadata["herdr_kata.outcome"]); !ok || o.Phase != Settled && o.Phase != HandedOff {
				return inbox.IgnoredRequest, requestLossReason(uid, m.cfg.Kata.Project), nil
			}
		}
		return refuse("child already admitted; existing admission receipt vetoes execution without its journal")
	}
	if err != nil {
		return "", "", err
	}
	if r.IssueRequest == nil || !sameTarget(r.Target, target) {
		return inbox.IgnoredRequest, "request journal identity differs; inspect herdr-kata doctor", nil
	}
	request, err := inbox.ValidateRequest(issue, target.Identity.ProjectUID)
	if err != nil {
		return inbox.Unsupported, err.Error(), nil
	}
	if r.Phase == Settled || r.Phase == HandedOff || request != r.IssueRequest.Request {
		return refuse("child already admitted; automatic execution cannot repeat")
	}
	if preAdmissionHold(r) {
		if requestAdmissionUnconfirmed(r) {
			if requestReceiptMatches(r, issue) {
				return "", "", nil
			}
			if _, exists := issue.Metadata["herdr_kata.admission"]; !exists {
				return inbox.IgnoredRequest, r.Reason + ": " + r.Inspect, nil
			}
		}
		return refuse(r.Reason + ": " + r.Inspect)
	}
	return "", "", nil
}

// The existing reporting-only store keeps an acknowledgement's sending marker
// across scans and restarts. It cannot reserve capacity or execute a prompt.
func (m *Manager) ackRefusedRequest(ctx context.Context, issue kata.IssueView, target Target, reason string) error {
	request, err := inbox.ValidateRequest(issue, target.Identity.ProjectUID)
	if err != nil {
		return err
	}
	key, raw, err := inbox.AckOp(issue, m.cfg.Kata.Inbox)
	if err != nil {
		return err
	}
	uid := ident.RunUID(request.RequestedAt, "request-refusal:"+issue.UID+":"+request.RequestUID+":"+string(raw))
	reports := NewStore(m.store.fsys, abandonReportsLayout(m.store.layout))
	if err := reports.fsys.MkdirAll(filepath.Join(m.store.layout.Root, "abandoned"), 0700); err != nil {
		return err
	}
	lock, ok, err := state.TryLock(filepath.Join(m.store.layout.Root, "abandoned", uid+".lock"))
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("request acknowledgement already in progress")
	}
	defer lock.Unlock() //nolint:errcheck // Lock cleanup remains best-effort.
	r, err := reports.Load(uid)
	if missing(err) {
		ref := issue.QualifiedID
		if ref == "" {
			ref = issue.UID
		}
		value := string(raw)
		op := newOp("request-refusal:ack", "meta.unset", Payload{Ref: ref, Name: key}, "")
		op.Guard = &kata.Cond{JSONValue: true, IfValue: &value}
		yes := true
		r = &Run{Version: 1, UID: uid, Source: "request-refusal", Target: target, Admitted: &yes, Phase: Settled, Reason: reason, IssueUID: issue.UID, IssueRef: ref, SlotReleased: true, Ops: []Op{op}}
		if err := reports.Create(r); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if r.Source != "request-refusal" || !sameTarget(r.Target, target) || r.IssueUID != issue.UID {
		return errors.New("request acknowledgement identity differs")
	}
	q := NewQueue(m.k, reports, m.guard, m.clock)
	q.pinLayout = m.pinLayout
	if m.q != nil {
		q.issueMu = m.q.issueMu
		q.writeSlots = m.q.writeSlots
	}
	for {
		r, err = reports.Load(uid)
		if err != nil {
			return err
		}
		op := head(r)
		if op == nil {
			return nil
		}
		if r.Inspect != "" {
			return fmt.Errorf("request acknowledgement requires inspection: %s", r.Inspect)
		}
		if op.NextAt.After(q.clock.Now()) {
			return errors.New("request acknowledgement pending; next inbox scan retries after backoff")
		}
		if err := m.guard.Check(ctx); err != nil {
			return err
		}
		if err := q.send(ctx, uid); err != nil {
			return err
		}
	}
}
