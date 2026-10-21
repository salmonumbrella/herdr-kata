package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// Lost execution state cannot be reconstructed. This separate reporting-only
// store reuses the guarded queue without making the UID executable locally.
func reportAbandoned(ctx context.Context, k Kata, guard RouteChecker, fsys state.FS, l, pinLayout state.Layout, target Target, remote kata.Run) (issueGone bool, err error) {
	if remote.IssueUID == "" {
		return false, nil
	}
	reports := NewStore(fsys, abandonReportsLayout(l))
	r, err := reports.Load(remote.UID)
	if missing(err) {
		if err := guard.Check(ctx); err != nil {
			return false, err
		}
		issue, err := k.ShowIssue(ctx, remote.IssueUID)
		if kata.NotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if issue.UID != remote.IssueUID || issue.ProjectUID != "" && issue.ProjectUID != target.Identity.ProjectUID {
			return false, errors.New("abandoned issue identity differs")
		}
		var marker string
		owned := issue.Status == "open" && issue.Owner == "" && json.Unmarshal(issue.Metadata["herdr_kata.run"], &marker) == nil && marker == remote.UID
		yes := true
		r = &Run{Version: 1, UID: remote.UID, Source: "abandon-report", Target: target, Admitted: &yes, Phase: Settled, Outcome: "unknown", Reason: "local state lost", IssueUID: remote.IssueUID, IssueRef: remote.IssueUID, IssueOwned: owned, SlotReleased: true}
		r.Ops = []Op{newOp("abandon:comment", "comment", Payload{Ref: remote.IssueUID, Body: "Run " + remote.UID + ": local state lost. Automatic execution stopped; inspect the issue before continuing."}, "herdr-kata:"+remote.UID+":abandon:comment")}
		if owned {
			r.Ops = append(r.Ops, newOp("abandon:attention", "meta.set", Payload{Ref: remote.IssueUID, Name: "work.attention", Value: "needs-human"}, ""), newOp("abandon:message", "meta.set", Payload{Ref: remote.IssueUID, Name: "work.attention_msg", Value: "herdr-kata: local state lost (run " + remote.UID + ")"}, ""))
		}
		if err := reports.Create(r); err != nil {
			return false, err
		}
	} else if err != nil {
		return false, err
	}
	if !sameTarget(r.Target, target) || r.Source != "abandon-report" || r.IssueUID != remote.IssueUID {
		return false, errors.New("abandon report identity differs")
	}
	return drainAbandonReport(ctx, k, guard, reports, pinLayout, remote.UID, remote.IssueUID)
}

func drainAbandonReport(ctx context.Context, k Kata, guard RouteChecker, reports *Store, pinLayout state.Layout, uid, issueUID string) (bool, error) {
	q := NewQueue(k, reports, guard, RealClock{})
	q.pinLayout = pinLayout
	for {
		r, err := reports.Load(uid)
		if err != nil {
			return false, err
		}
		op := head(r)
		if op == nil {
			return r.Reason == "local state lost; issue gone", nil
		}
		if err := guard.Check(ctx); err != nil {
			return false, err
		}
		if _, err := k.ShowIssue(ctx, issueUID); kata.NotFound(err) {
			_, err = reports.Transition(uid, finishAbandonGone)
			return err == nil, err
		} else if err != nil {
			return false, err
		}
		if r.Inspect != "" {
			if _, err := reports.Transition(uid, finishAbandonAttention); err != nil {
				return false, fmt.Errorf("abandon report requires inspection: %s: %w", r.Inspect, err)
			}
			continue
		}
		if op.NextAt.After(q.clock.Now()) {
			return false, errors.New("abandon report pending; run herdr-kata abandon again after retry backoff")
		}
		if err := guard.Check(ctx); err != nil {
			return false, err
		}
		if err := q.send(ctx, uid); err != nil {
			return false, err
		}
	}
}

// The loss comment is the required report. Concurrent issue changes may veto
// attention without trapping the explicit abandonment command indefinitely.
func finishAbandonAttention(r *Run) error {
	commentDone := false
	for _, op := range r.Ops {
		if op.SigningRefused {
			return errors.New("signing refusal evidence must be resolved")
		}
		if op.ID == "abandon:comment" && op.Done {
			commentDone = true
		}
		if !op.Done && (op.Kind != "meta.set" || op.ID != "abandon:attention" && op.ID != "abandon:message") {
			return errors.New("loss comment is not complete")
		}
	}
	if !commentDone {
		return errors.New("loss comment is not complete")
	}
	for i := range r.Ops {
		op := &r.Ops[i]
		if !op.Done {
			op.retire("loss comment retained; attention stopped: " + r.Inspect)
		}
	}
	r.Inspect = ""
	return nil
}

func abandonReportsLayout(l state.Layout) state.Layout {
	return state.Layout{Root: filepath.Join(l.Root, "abandoned", "reports")}
}

// A missing issue has no report destination. Keep every frozen intent as
// evidence, but do not leave an undeliverable report pending forever.
func finishAbandonGone(r *Run) error {
	for _, op := range r.Ops {
		if op.SigningRefused {
			return errors.New("signing refusal evidence must be resolved")
		}
	}
	for i := range r.Ops {
		op := &r.Ops[i]
		if !op.Done {
			op.retire(op.Inspect + "; issue gone; no issue to report to")
		}
	}
	r.Inspect = ""
	r.Reason = "local state lost; issue gone"
	return nil
}
