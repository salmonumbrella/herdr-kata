package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/inbox"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type requestReader interface {
	Inbox(context.Context, string) ([]kata.InboxRequest, error)
	ShowIssue(context.Context, string) (kata.IssueView, error)
}

func ownedRequestReceipt(issue kata.IssueView, target Target) (RequestAdmission, bool) {
	var a RequestAdmission
	uid, err := ident.IssueRequestUID(issue.UID)
	if err != nil || json.Unmarshal(issue.Metadata["herdr_kata.admission"], &a) != nil {
		return a, false
	}
	_, requestErr := ident.IssueRequestUID(a.RequestUID)
	raw, _ := json.Marshal(a)
	want := RequestExecutor{InstanceUID: target.Identity.InstanceUID, ProjectUID: target.Identity.ProjectUID, Actor: target.Identity.Actor, ExecutorLabel: target.Executor}
	return a, a.Version == 1 && a.RunUID == uid && a.JournalID != "" && requestErr == nil && issue.ProjectUID == target.Identity.ProjectUID && a.Executor == want && jsonEqual(issue.Metadata["herdr_kata.admission"], raw)
}

func receiptOutcome(a RequestAdmission, raw json.RawMessage) (RequestOutcome, bool) {
	var o RequestOutcome
	err := json.Unmarshal(raw, &o)
	return o, err == nil && o.Version == 1 && o.RequestUID == a.RequestUID && o.RunUID == a.RunUID && o.JournalID == a.JournalID
}

// LostIssueRequests scans outstanding runtime notifications, not cron history.
// Receipts identify loss; they never authorize reconstruction of execution.
func LostIssueRequests(ctx context.Context, k requestReader, fsys state.FS, l state.Layout, target Target, address string) ([]kata.IssueView, error) {
	if address == "" {
		return nil, nil
	}
	rows, err := k.Inbox(ctx, address)
	if err != nil {
		return nil, err
	}
	var lost []kata.IssueView
	seen := map[string]bool{}
	for _, row := range rows {
		issue, err := k.ShowIssue(ctx, row.Ref)
		if kata.NotFound(err) {
			continue
		}
		if err != nil {
			return lost, err
		}
		a, ok := ownedRequestReceipt(issue, target)
		if !ok || seen[a.RunUID] {
			continue
		}
		seen[a.RunUID] = true
		if o, ok := receiptOutcome(a, issue.Metadata["herdr_kata.outcome"]); ok && (o.Phase == Settled || o.Phase == HandedOff) {
			continue
		}
		if _, err := fsys.Lstat(l.RunDir(a.RunUID)); errors.Is(err, fs.ErrNotExist) {
			lost = append(lost, issue)
		} else if err != nil {
			return lost, err
		}
	}
	return lost, nil
}

func abandonIssueRequest(ctx context.Context, cfg config.Config, k Kata, guard RouteChecker, fsys state.FS, l, pinLayout state.Layout, target Target, uid string) (bool, error) {
	reader, ok := k.(requestReader)
	if !ok {
		return false, errors.New("issue request reader unavailable")
	}
	var issue kata.IssueView
	var a RequestAdmission
	// An existing report may replay after its inbox notification was acknowledged.
	// Otherwise, establish a matching request before creating any local artifacts.
	if _, err := fsys.Lstat(abandonReportsLayout(l).RunFile(uid)); missing(err) {
		lost, err := LostIssueRequests(ctx, reader, fsys, l, target, cfg.Kata.Inbox)
		if err != nil {
			return false, err
		}
		for _, candidate := range lost {
			receipt, _ := ownedRequestReceipt(candidate, target)
			if receipt.RunUID == uid {
				issue, a = candidate, receipt
				break
			}
		}
		if issue.UID == "" {
			return false, &kata.Error{Code: "not_found", Message: "run unavailable"}
		}
	} else if err != nil {
		return false, err
	}
	if err := fsys.MkdirAll(filepath.Join(l.Root, "abandoned"), 0700); err != nil {
		return false, err
	}
	lock, ok, err := state.TryLock(filepath.Join(l.Root, "abandoned", uid+".lock"))
	if err != nil {
		return false, err
	}
	if !ok {
		return false, errors.New("abandon already in progress")
	}
	defer lock.Unlock() //nolint:errcheck // Lock cleanup remains best-effort.
	reports := NewStore(fsys, abandonReportsLayout(l))
	r, err := reports.Load(uid)
	if missing(err) {
		if issue.UID == "" {
			return false, errors.New("abandon request report disappeared; inspect local state")
		}
		if issue.Status != "open" || issue.Owner != target.Identity.Actor {
			return false, errors.New("lost issue request ownership changed; inspect the issue")
		}
		key, notification, err := inbox.AckOp(issue, cfg.Kata.Inbox)
		if err != nil {
			return false, err
		}
		ref := issue.QualifiedID
		if ref == "" {
			ref = issue.UID
		}
		now := time.Now().UTC()
		outcome := RequestOutcome{Version: 1, RequestUID: a.RequestUID, RunUID: uid, JournalID: a.JournalID, Phase: Settled, Outcome: "unknown", Reason: "local state lost", Summary: "local state lost", EndedAt: &now}
		raw, _ := json.Marshal(outcome)
		op := newOp("abandon:outcome", "meta.set", Payload{Ref: ref, Name: "herdr_kata.outcome", Value: string(raw)}, "")
		op.Guard = &kata.Cond{JSONValue: true, IfAbsent: true}
		if previous, exists := issue.Metadata["herdr_kata.outcome"]; exists {
			if _, ok := receiptOutcome(a, previous); !ok {
				return false, errors.New("issue request outcome identity changed")
			}
			value := string(previous)
			op.Guard.IfAbsent, op.Guard.IfValue = false, &value
		}
		yes := true
		r = &Run{Version: 1, UID: uid, Source: "abandon-request-report", Target: target, Admitted: &yes, Phase: Settled, Outcome: "unknown", Reason: "local state lost", IssueUID: issue.UID, IssueRef: ref, IssueOwned: true, SlotReleased: true}
		saved := string(notification)
		ack := newOp("abandon:ack", "meta.unset", Payload{Ref: ref, Name: key}, "")
		ack.Guard = &kata.Cond{JSONValue: true, IfValue: &saved}
		r.Ops = []Op{op,
			newOp("abandon:comment", "comment", Payload{Ref: ref, Body: "Run " + uid + ": local state lost. Automatic execution stopped; inspect the issue before continuing."}, "herdr-kata:"+uid+":abandon:comment"),
			newOp("abandon:attention", "meta.set", Payload{Ref: ref, Name: "work.attention", Value: "needs-human", Owner: target.Identity.Actor}, ""),
			newOp("abandon:message", "meta.set", Payload{Ref: ref, Name: "work.attention_msg", Value: "herdr-kata: local state lost (run " + uid + ")", Owner: target.Identity.Actor}, ""), ack}
		if err := reports.Create(r); err != nil {
			return false, err
		}
	} else if err != nil {
		return false, err
	}
	if r.Source != "abandon-request-report" || !sameTarget(r.Target, target) {
		return false, errors.New("abandon request report identity differs")
	}
	return drainAbandonReport(ctx, k, guard, reports, pinLayout, uid, r.IssueRef)
}

func lossReceiptMatches(r *Run, issue kata.IssueView) bool {
	a, ok := ownedRequestReceipt(issue, r.Target)
	if !ok || issue.UID != r.IssueUID {
		return false
	}
	for _, op := range r.Ops {
		if op.ID != "abandon:outcome" {
			continue
		}
		p, err := decodePayload(op)
		if err != nil {
			return false
		}
		_, matches := receiptOutcome(a, json.RawMessage(p.Value))
		return matches
	}
	return false
}

func (q *Queue) lossReportPreflight(ctx context.Context, r *Run, op *Op, p Payload) (bool, error) {
	issue, err := q.showIssue(ctx, r.UID, r.IssueRef)
	if err != nil {
		return false, q.failedRead(ctx, r.UID, *op, err, true)
	}
	if !lossReceiptMatches(r, issue) {
		return false, q.inspect(r.UID, op.ID, "lost issue request receipt changed")
	}
	if (p.Name == "herdr_kata.outcome" || p.Name == "work.attention" || p.Name == "work.attention_msg") && (issue.Owner != r.Target.Identity.Actor || issue.Status != "open") {
		return false, q.inspect(r.UID, op.ID, "lost issue request ownership changed")
	}
	if op.Safety == kata.Guarded {
		if op.Guard == nil {
			op.Guard = &kata.Cond{}
		}
		if op.Guard.IfMatch == "" {
			op.Guard.IfMatch = strconv.FormatInt(issue.Revision, 10)
		}
	}
	return true, nil
}

func requestLossReason(uid, project string) string {
	return fmt.Sprintf("local state lost; inspect original occupants, then herdr-kata abandon %s --project %s", uid, project)
}

func requestReportReadBack(r *Run, op Op) bool {
	return op.Safety == kata.Guarded && (r.Source == "abandon-request-report" || r.Source == "request-refusal")
}
