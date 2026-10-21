package runner

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type ackKata struct {
	*fakeKata
	unset func(context.Context, string, string, kata.Cond) (int64, error)
}

func (k *ackKata) MetaUnset(ctx context.Context, ref, key string, c kata.Cond) (int64, error) {
	return k.unset(ctx, ref, key, c)
}

func TestChildAckAfterReportFreshGuard(t *testing.T) {
	f := newChildAdmission(t)
	r := &Run{Version: 1, UID: f.uid, Phase: Settled, IssueUID: childUID, IssueRef: "project#ebrn", Child: &ChildRun{}, Job: kata.Job{UID: deliveryUID, DefinitionEventUID: deliveryEvent}, Outcome: "failed"}
	// Failure settlement also acknowledges after its final report.
	r.Child.ChildDue = *f.d.Child
	ops := SettleOps(r, &Result{Status: "failed", Summary: "Task failed after checking the command result."}, time.Now())
	if len(ops) < 2 || ops[len(ops)-2].Kind != "observe" || ops[len(ops)-1].Kind != "meta.unset" {
		t.Fatalf("settlement order: %+v", ops)
	}
	q, s, uid, k, _, _ := queueFixture(t, ops[len(ops)-1])
	k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{Revision: 21, Metadata: map[string]json.RawMessage{f.d.Child.NotifyKey: f.d.Child.NotifyJSON}}, nil
	}
	called := false
	q.k = &ackKata{fakeKata: k, unset: func(_ context.Context, ref, key string, c kata.Cond) (int64, error) {
		called = true
		if ref != "project#ebrn" || key != f.d.Child.NotifyKey || c.IfMatch != "21" || !c.JSONValue || c.IfValue == nil || *c.IfValue != string(f.d.Child.NotifyJSON) {
			t.Error(ref, key, c)
		}
		return 22, nil
	}}
	deliver(t, q, uid)
	if !called || !loaded(t, s, uid).Ops[0].Done {
		t.Fatal("ack not delivered")
	}
}
func TestChildAckLeavesReplaced(t *testing.T) {
	saved := `{"from":"system","message":"due"}`
	op := operation("child-ack", "meta.unset", Payload{Ref: "issue", Name: "notify.inbox"})
	op.Guard = &kata.Cond{JSONValue: true, IfValue: &saved}
	q, s, uid, k, _, _ := queueFixture(t, op)
	_, err := s.Transition(uid, func(r *Run) error {
		r.Child = &ChildRun{NotifyKey: "notify.inbox", NotifyJSON: json.RawMessage(saved)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{Revision: 23, Metadata: map[string]json.RawMessage{"notify.inbox": json.RawMessage(`{"message":"replacement"}`)}}, nil
	}
	q.k = &ackKata{fakeKata: k, unset: func(context.Context, string, string, kata.Cond) (int64, error) {
		return 0, &kata.Error{Code: "conflict"}
	}}
	deliver(t, q, uid)
	r := loaded(t, s, uid)
	if !r.Ops[0].Done || r.Inspect != "" {
		t.Fatal("changed request should be left alone", r)
	}
}

func TestChildMovedProjectStopsAttention(t *testing.T) {
	op := operation("attention", "meta.set", Payload{Ref: "project#child", Name: "work.attention", Value: "ok", Owner: "worker"})
	q, s, uid, k, _, _ := queueFixture(t, op)
	_, err := s.Transition(uid, func(r *Run) error {
		r.IssueOwned = true
		r.Target.Identity.ProjectUID = "project"
		r.Child = &ChildRun{IssueUID: childUID}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{UID: childUID, ProjectUID: "other", Owner: "worker", Status: "open", Revision: 8}, nil
	}
	k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) {
		t.Error("attention written after project move")
		return 9, nil
	}
	deliver(t, q, uid)
	r := loaded(t, s, uid)
	if r.Inspect == "" || r.Ops[0].FirstSent != nil {
		t.Fatal("moved child write not stopped", r)
	}
}
