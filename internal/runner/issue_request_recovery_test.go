package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/inbox"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"strings"
	"testing"
	"time"
)

func preparedRequest(t *testing.T) (*admissionFixture, *requestKata) {
	t.Helper()
	f, k := newRequestAdmission(t)
	d, err := f.m.requestInputs(t.Context(), f.d, requestProject)
	if err != nil {
		t.Fatal(err)
	}
	target, err := f.m.pin()
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.m.prepare(d, f.uid, "issue-request:"+childUID, target)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Create(r); err != nil {
		t.Fatal(err)
	}
	f.m.executions.Store(f.uid, struct{}{})
	return f, k
}

func TestIssueRequestPreCASReadinessChange(t *testing.T) {
	f, k := preparedRequest(t)
	k.authority.ready = false
	f.start(t)
	err := f.m.admitPrepared(t.Context(), loaded(t, f.s, f.uid))
	if err == nil || k.authority.writes != 0 {
		t.Fatalf("unready child admitted: %v writes=%d", err, k.authority.writes)
	}
}

func TestIssueRequestReadinessRPCFailureRecovers(t *testing.T) {
	for _, failure := range []error{lostReply, context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			f, k := preparedRequest(t)
			if err := f.m.append(f.uid, requestAdmissionOp(loaded(t, f.s, f.uid))); err != nil {
				t.Fatal(err)
			}
			k.authority.readyErr = failure
			err := f.q.send(t.Context(), f.uid)
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			r := loaded(t, f.s, f.uid)
			if r.Phase != Prepared || r.Inspect != "" || admitted(r) || r.Ops[0].FirstSent != nil || k.authority.writes != 0 {
				t.Fatalf("read failure permanently rejected unsent admission: %+v", r)
			}
			f.q.clock = &testClock{at: time.Now().Add(time.Minute)}
			f.start(t)
			if err = f.m.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.admit(t)
			r = loaded(t, f.s, f.uid)
			if !admitted(r) || r.Phase != Admitted || k.authority.writes != 1 {
				t.Fatalf("readiness recovery duplicated or stranded execution: %+v", r)
			}
		})
	}
}

func TestIssueRequestPreparedRestart(t *testing.T) {
	f, k := preparedRequest(t)
	f.start(t)
	if err := f.m.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	r := loaded(t, f.s, f.uid)
	if !admitted(r) || r.Phase != Admitted || k.authority.writes != 1 {
		t.Fatalf("prepared restart: %+v", r)
	}
}

func TestIssueRequestRecoveryHonorsAdmissionGates(t *testing.T) {
	for _, upgrade := range []bool{false, true} {
		t.Run(fmt.Sprint(upgrade), func(t *testing.T) {
			f, k := preparedRequest(t)
			path := f.s.layout.StopFile()
			if upgrade {
				path = f.s.layout.UpgradeFile()
			}
			if err := state.WriteJSON(f.s.fsys, path, struct{}{}); err != nil {
				t.Fatal(err)
			}
			f.start(t)
			if err := f.m.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			if r := loaded(t, f.s, f.uid); r.Phase != Prepared || admitted(r) || k.authority.writes != 0 {
				t.Fatalf("admitted through stop/upgrade gate: phase=%s admitted=%v writes=%d", r.Phase, admitted(r), k.authority.writes)
			}
			if err := f.s.fsys.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := f.m.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.admit(t)
			if k.authority.writes != 1 {
				t.Fatalf("recovered admission writes=%d, want 1", k.authority.writes)
			}
		})
	}
}

func TestIssueRequestUnconfirmedCASNeverRetries(t *testing.T) {
	f, k := newRequestAdmission(t)
	k.authority.uncommitted = true
	f.start(t)
	_, err := f.m.Admit(t.Context(), f.d)
	if err == nil {
		t.Fatal("unconfirmed CAS admitted")
	}
	r := loaded(t, f.s, f.uid)
	if admitted(r) || r.Phase != Held || !r.SlotReleased || k.authority.writes != 1 {
		t.Fatalf("ambiguous admission: %+v writes=%d", r, k.authority.writes)
	}
}

func TestIssueRequestOutcomeDoesNotOverwriteForeignValue(t *testing.T) {
	f, k := newRequestAdmission(t)
	f.start(t)
	f.admit(t)
	_, err := f.s.Transition(f.uid, func(r *Run) error {
		r.Ops = append(r.Ops, requestOutcomeOp(r, "outcome", Held, "unknown", "uncertain", "inspect attempt", nil))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	k.authority.mu.Lock()
	k.authority.issue.Metadata["herdr_kata.outcome"] = json.RawMessage(`{"summary":"human changed this"}`)
	k.authority.mu.Unlock()
	if err = f.q.wait(t.Context(), f.uid, "outcome"); err == nil {
		t.Fatal("replaced foreign outcome")
	}
	issue, err := k.ShowIssue(t.Context(), "project#child")
	if err != nil {
		t.Fatal(err)
	}
	if string(issue.Metadata["herdr_kata.outcome"]) != `{"summary":"human changed this"}` {
		t.Fatal("human outcome overwritten")
	}
}

func TestIssueRequestJournalRequiresExplicitAdmissionProof(t *testing.T) {
	f, _ := preparedRequest(t)
	_, err := f.s.Transition(f.uid, func(r *Run) error { r.Admitted = nil; return nil })
	if err == nil {
		t.Fatal("version-2 journal lost its explicit admission proof")
	}
}

func TestIssueRequestHandoffAndChangedAcknowledgement(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			f, k := newRequestAdmission(t)
			f.k.close = func(context.Context, kata.CloseReq) error {
				k.authority.mu.Lock()
				defer k.authority.mu.Unlock()
				k.authority.issue.Status = "closed"
				return nil
			}
			f.start(t)
			f.admit(t)
			if changed {
				k.authority.mu.Lock()
				k.authority.issue.Metadata["notify.d29ya2VyL2luYm94"] = json.RawMessage(`{"message":"replacement"}`)
				k.authority.mu.Unlock()
			}
			if err := f.m.settle(t.Context(), f.uid, &Result{Status: "needs-human", Summary: "operator input required"}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			r := loaded(t, f.s, f.uid)
			if err := f.q.wait(ctx, f.uid, r.Ops[len(r.Ops)-1].ID); err != nil {
				t.Fatal(err)
			}
			f.m.executions.Delete(f.uid)
			if err := f.m.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			issue, err := k.ShowIssue(ctx, "project#child")
			if err != nil {
				t.Fatal(err)
			}
			var outcome RequestOutcome
			if err = json.Unmarshal(issue.Metadata["herdr_kata.outcome"], &outcome); err != nil {
				t.Fatal(err)
			}
			r = loaded(t, f.s, f.uid)
			if issue.Status != "open" || outcome.Phase != HandedOff || outcome.Outcome != "unknown" || r.Phase != HandedOff || issue.Metadata["notify.aHVtYW4"] == nil {
				t.Fatalf("handoff damaged issue: %+v", issue)
			}
			notification := issue.Metadata["notify.d29ya2VyL2luYm94"]
			if changed && string(notification) != `{"message":"replacement"}` || !changed && notification != nil {
				t.Fatalf("incorrect runtime acknowledgement: %s", notification)
			}
		})
	}
}

func TestIssueRequestReceiptWithoutJournalNeverReconstructs(t *testing.T) {
	f, k := newRequestAdmission(t)
	f.k.run = func(context.Context, string) (kata.Run, error) {
		return kata.Run{}, errors.New("issue requests must not read cron history")
	}
	k.authority.issue.Metadata["herdr_kata.admission"] = json.RawMessage(`{"version":1}`)
	result, err := f.m.Admit(t.Context(), f.d)
	if err == nil || !strings.Contains(err.Error(), "already admitted") || result != sched.Dropped {
		t.Fatalf("replacement admission: %v %v", result, err)
	}
	ids, err := f.s.ScanActive()
	if err != nil || len(ids) != 0 {
		t.Fatalf("replacement journals: %v %v", ids, err)
	}
}

// A restart may read before an already-sent CAS lands. Only the exact frozen
// receipt can release that hold; the original mutation must never be resent.
func TestIssueRequestLateAdmissionReceiptRecovers(t *testing.T) {
	for _, receipt := range []string{"exact", "foreign", "absent"} {
		t.Run(receipt, func(t *testing.T) {
			f, k := preparedRequest(t)
			r := loaded(t, f.s, f.uid)
			op := requestAdmissionOp(r)
			now := time.Now().UTC()
			op.FirstSent = &now
			if err := f.m.append(f.uid, op); err != nil {
				t.Fatal(err)
			}
			if err := f.q.send(t.Context(), f.uid); err != nil {
				t.Fatal(err)
			}
			r = loaded(t, f.s, f.uid)
			if r.Phase != Held || admitted(r) {
				t.Fatalf("unproven admission ran: %+v", r)
			}
			if receipt != "absent" {
				value := r.IssueRequest.Admission
				if receipt == "foreign" {
					value.JournalID = "another-journal"
				}
				raw, _ := json.Marshal(value)
				k.authority.mu.Lock()
				k.authority.issue.Metadata["herdr_kata.admission"] = raw
				k.authority.issue.Revision++
				k.authority.mu.Unlock()
			}
			f.start(t)
			if err := f.m.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			r = loaded(t, f.s, f.uid)
			if receipt == "exact" {
				if !admitted(r) || r.Phase != Admitted || r.SlotReleased {
					t.Fatalf("late receipt stranded: phase=%s admitted=%v inspect=%s", r.Phase, admitted(r), r.Inspect)
				}
			} else if admitted(r) || r.Phase != Held {
				t.Fatalf("unproven receipt released hold: %+v", r)
			}
			if k.authority.writes != 0 {
				t.Fatal("ambiguous CAS was resent")
			}
		})
	}
}

func TestAbandonLostIssueRequestWithoutCronRecord(t *testing.T) {
	f, k := newRequestAdmission(t)
	stop := f.start(t)
	r := f.admit(t)
	stop()
	f.k.run = func(context.Context, string) (kata.Run, error) { return kata.Run{}, &kata.Error{Code: "not_found"} }
	if err := f.s.fsys.Rename(f.s.layout.RunDir(f.uid), f.s.layout.RunDir(f.uid)+".backup"); err != nil {
		t.Fatal(err)
	}
	comments := 0
	f.k.comment = func(_ context.Context, ref, body, key string) error {
		comments++
		if ref != "project#child" || !strings.Contains(body, "local state lost") || key != "herdr-kata:"+f.uid+":abandon:comment" {
			t.Fatal(ref, body, key)
		}
		return nil
	}
	for range 2 {
		if err := Abandon(t.Context(), f.cfg, k, f.g, f.s.fsys, f.s.layout, f.uid); err != nil {
			t.Fatal(err)
		}
	}
	issue, err := k.ShowIssue(t.Context(), "project#child")
	if err != nil {
		t.Fatal(err)
	}
	var outcome RequestOutcome
	if err := json.Unmarshal(issue.Metadata["herdr_kata.outcome"], &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.RunUID != f.uid || outcome.JournalID != r.JournalID || outcome.Outcome != "unknown" || outcome.Reason != "local state lost" || outcome.Phase != Settled {
		t.Fatalf("loss not recorded: %+v", outcome)
	}
	if string(issue.Metadata["work.attention"]) != `"needs-human"` || comments != 1 {
		t.Fatal("missing or repeated loss report", issue.Metadata, comments)
	}
	if _, exists := issue.Metadata["notify.d29ya2VyL2luYm94"]; exists {
		t.Fatal("runtime notification not acknowledged")
	}
	if _, exists := issue.Metadata["notify.aHVtYW4"]; !exists {
		t.Fatal("human notification cleared")
	}
	if _, err := f.s.fsys.Lstat(f.s.layout.RunDir(f.uid)); !missing(err) {
		t.Fatal("execution journal reconstructed", err)
	}
}

func TestIssueRequestPermanentVetoAcknowledgesNotification(t *testing.T) {
	for _, kind := range []string{"foreign", "completed", "replacement"} {
		t.Run(kind, func(t *testing.T) {
			f, k := newRequestAdmission(t)
			if kind != "foreign" {
				stop := f.start(t)
				f.admit(t)
				stop()
				if kind == "completed" {
					if _, err := f.s.Transition(f.uid, func(r *Run) error { r.Phase = Settled; r.Outcome = "succeeded"; return nil }); err != nil {
						t.Fatal(err)
					}
				} else {
					raw := json.RawMessage(`{"version":1,"kind":"execute","request_uid":"01K00000000000000000000005","requested_at":"2026-01-02T03:04:05Z"}`)
					k.authority.issue.Metadata["herdr_kata.request"] = raw
				}
			} else {
				k.authority.issue.Metadata["herdr_kata.admission"] = json.RawMessage(`{"journal_id":"foreign"}`)
			}
			result, err := f.m.Admit(t.Context(), f.d)
			if result != sched.Dropped || err == nil || !strings.Contains(err.Error(), "already admitted") {
				t.Fatalf("veto not explained: result=%v err=%v", result, err)
			}
			issue, _ := k.ShowIssue(t.Context(), "project#child")
			if _, exists := issue.Metadata["notify.d29ya2VyL2luYm94"]; exists {
				t.Fatal("vetoed notification not acknowledged")
			}
			if _, exists := issue.Metadata["notify.aHVtYW4"]; !exists {
				t.Fatal("human notification cleared")
			}
		})
	}
}

func TestAbandonLostIssueRequestReadsBackAmbiguousOutcome(t *testing.T) {
	f, k := newRequestAdmission(t)
	stop := f.start(t)
	f.admit(t)
	stop()
	f.k.run = func(context.Context, string) (kata.Run, error) { return kata.Run{}, &kata.Error{Code: "not_found"} }
	if err := f.s.fsys.Rename(f.s.layout.RunDir(f.uid), f.s.layout.RunDir(f.uid)+".backup"); err != nil {
		t.Fatal(err)
	}
	k.authority.lostOutcome = true
	if err := Abandon(t.Context(), f.cfg, k, f.g, f.s.fsys, f.s.layout, f.uid); err != nil {
		t.Fatal(err)
	}
	if k.authority.outcomeWrites != 1 {
		t.Fatalf("outcome resent %d times", k.authority.outcomeWrites)
	}
}

func TestLostIssueRequestReportFencesReceiptAndOwnerChanges(t *testing.T) {
	for _, change := range []string{"receipt", "owner", "receipt-shape"} {
		t.Run(change, func(t *testing.T) {
			f, k := newRequestAdmission(t)
			stop := f.start(t)
			f.admit(t)
			stop()
			f.k.run = func(context.Context, string) (kata.Run, error) { return kata.Run{}, &kata.Error{Code: "not_found"} }
			if err := f.s.fsys.Rename(f.s.layout.RunDir(f.uid), f.s.layout.RunDir(f.uid)+".backup"); err != nil {
				t.Fatal(err)
			}
			k.authority.changeAfterRead = change
			err := Abandon(t.Context(), f.cfg, k, f.g, f.s.fsys, f.s.layout, f.uid)
			if err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatal("changed request report was allowed", err)
			}
			issue, _ := k.ShowIssue(t.Context(), "project#child")
			if issue.Metadata["herdr_kata.outcome"] != nil || issue.Metadata["notify.d29ya2VyL2luYm94"] == nil {
				t.Fatal("changed request mutated", issue.Metadata)
			}
		})
	}
}

func TestIssueRequestVetoAcknowledgementReadBack(t *testing.T) {
	for _, behavior := range []string{"lost", "replace"} {
		t.Run(behavior, func(t *testing.T) {
			f, k := newRequestAdmission(t)
			k.authority.issue.Metadata["herdr_kata.admission"] = json.RawMessage(`{"journal_id":"foreign"}`)
			k.authority.unsetBehavior = behavior
			result, err := f.m.Admit(t.Context(), f.d)
			if result != sched.Dropped || err == nil || !strings.Contains(err.Error(), "already admitted") {
				t.Fatal("refusal acknowledgement did not finish", result, err)
			}
			if k.authority.unsetCalls != 1 {
				t.Fatal("acknowledgement resent", k.authority.unsetCalls)
			}
			issue, _ := k.ShowIssue(t.Context(), "project#child")
			notification := issue.Metadata["notify.d29ya2VyL2luYm94"]
			if behavior == "replace" && string(notification) != `{"message":"replacement"}` || behavior == "lost" && notification != nil {
				t.Fatal("incorrect acknowledgement", string(notification))
			}
		})
	}
}

func TestLostIssueRequestReportUsesReceiptRevision(t *testing.T) {
	for _, change := range []string{"receipt", "owner", "receipt-shape"} {
		t.Run(change, func(t *testing.T) {
			f, k := newRequestAdmission(t)
			stop := f.start(t)
			f.admit(t)
			stop()
			f.k.run = func(context.Context, string) (kata.Run, error) { return kata.Run{}, &kata.Error{Code: "not_found"} }
			if err := f.s.fsys.Rename(f.s.layout.RunDir(f.uid), f.s.layout.RunDir(f.uid)+".backup"); err != nil {
				t.Fatal(err)
			}
			k.authority.changeAfterRead = change
			// Discovery and the drain's existence read precede the queue preflight.
			// Race the issue change immediately after that identity-checked snapshot.
			k.authority.changeReadAt = k.authority.showCalls + 3
			if err := Abandon(t.Context(), f.cfg, k, f.g, f.s.fsys, f.s.layout, f.uid); err == nil {
				t.Fatal("changed request report was allowed")
			}
			issue, _ := k.ShowIssue(t.Context(), "project#child")
			if issue.Metadata["herdr_kata.outcome"] != nil {
				t.Fatal("report used a revision newer than its receipt proof", issue.Metadata)
			}
		})
	}
}

func TestIssueRequestRejectedPreparationAcknowledges(t *testing.T) {
	f, k := preparedRequest(t)
	k.authority.ready = false
	stop := f.start(t)
	if err := f.m.admitPrepared(t.Context(), loaded(t, f.s, f.uid)); err == nil {
		t.Fatal("unready admission accepted")
	}
	stop()
	issue, err := k.ShowIssue(t.Context(), "project#child")
	if err != nil {
		t.Fatal(err)
	}
	status, reason, err := f.m.CheckIssueRequest(t.Context(), issue)
	if err != nil || status != inbox.Unsupported || !strings.Contains(reason, "not ready") {
		t.Fatal("rejected preparation not classified", status, reason, err)
	}
	issue, _ = k.ShowIssue(t.Context(), "project#child")
	if issue.Metadata["notify.d29ya2VyL2luYm94"] != nil {
		t.Fatal("permanent refusal not acknowledged")
	}
}

type refusalRouteGuard struct {
	calls   int
	refusal error
}

func (g *refusalRouteGuard) Check(context.Context) error {
	g.calls++
	if g.calls >= 3 {
		return g.refusal
	}
	return nil
}

func TestIssueRequestRefusalStopsWhenRouteCloses(t *testing.T) {
	f, k := newRequestAdmission(t)
	k.authority.issue.Metadata["herdr_kata.admission"] = json.RawMessage(`{"journal_id":"foreign"}`)
	refused := errors.New("project route refused")
	f.m.guard = &refusalRouteGuard{refusal: refused}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err := f.m.Admit(ctx, f.d)
	if !errors.Is(err, refused) {
		t.Fatal("route refusal wedged acknowledgement until deadline", err)
	}
	if k.authority.unsetCalls != 0 {
		t.Fatal("acknowledged on a refused route")
	}
}

func TestIssueRequestAcknowledgementLandedBeforeInterruptionHasRemedy(t *testing.T) {
	f, k := newRequestAdmission(t)
	k.authority.issue.Metadata["herdr_kata.admission"] = json.RawMessage(`{"journal_id":"foreign"}`)
	k.authority.unsetBehavior = "canceled"
	if _, err := f.m.Admit(t.Context(), f.d); !errors.Is(err, context.Canceled) {
		t.Fatal("interruption not retained", err)
	}
	rows, err := k.Inbox(t.Context(), f.cfg.Kata.Inbox)
	if err != nil || len(rows) != 0 {
		t.Fatal("acknowledgement did not land", rows, err)
	}
	problems, err := AbandonReportProblems(f.s.fsys, f.s.layout)
	if err != nil || len(problems) != 1 {
		t.Fatal("uncertain acknowledgement not visible", problems, err)
	}
	if !strings.Contains(problems[0].Detail, "otherwise herdr-kata stop") || !strings.Contains(problems[0].Detail, "quarantine") {
		t.Fatal("missing recovery path when no inbox row can retry", problems[0].Detail)
	}
}

type classificationRouteGuard struct {
	allowed bool
	probes  int
}

func (g *classificationRouteGuard) Allowed() bool { return g.allowed }
func (g *classificationRouteGuard) Check(context.Context) error {
	g.probes++
	if !g.allowed {
		return errors.New("project route refused")
	}
	return nil
}

func TestIssueRequestClassificationUsesEstablishedRoute(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			f, k := newRequestAdmission(t)
			stop := f.start(t)
			f.admit(t)
			stop()
			if err := f.s.fsys.Rename(f.s.layout.RunDir(f.uid), f.s.layout.RunDir(f.uid)+".backup"); err != nil {
				t.Fatal(err)
			}
			issue, err := k.ShowIssue(t.Context(), "project#child")
			if err != nil {
				t.Fatal(err)
			}
			guard := &classificationRouteGuard{allowed: allowed}
			f.m.guard = guard
			for range 2 {
				status, reason, err := f.m.CheckIssueRequest(t.Context(), issue)
				if allowed && (err != nil || status != inbox.IgnoredRequest || !strings.Contains(reason, "local state lost")) {
					t.Fatal("lost request classification", status, reason, err)
				}
				if !allowed && (err == nil || !strings.Contains(err.Error(), "route refused")) {
					t.Fatal("refused lane was treated as allowed", status, reason, err)
				}
			}
			if guard.probes != 0 {
				t.Fatalf("read-only classification repeated %d full route probes", guard.probes)
			}
		})
	}
}

func TestIssueRequestClassificationChecksPlainGuard(t *testing.T) {
	f, k := newRequestAdmission(t)
	stop := f.start(t)
	f.admit(t)
	stop()
	if err := f.s.fsys.Rename(f.s.layout.RunDir(f.uid), f.s.layout.RunDir(f.uid)+".backup"); err != nil {
		t.Fatal(err)
	}
	issue, err := k.ShowIssue(t.Context(), "project#child")
	if err != nil {
		t.Fatal(err)
	}
	f.g.blocked.Store(true)
	_, _, err = f.m.CheckIssueRequest(t.Context(), issue)
	if err == nil || !strings.Contains(err.Error(), "route changed") {
		t.Fatal("plain RouteChecker refusal was skipped", err)
	}
}
