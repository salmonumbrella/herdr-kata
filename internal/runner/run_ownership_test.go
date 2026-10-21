package runner

import (
	"context"
	"encoding/json"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"testing"
)

func TestRecoverUnsentSameLaneCloseRefusal(t *testing.T) {
	op := operation("close", "close", Payload{Close: &kata.CloseReq{Ref: "example-project#task", Reason: "done"}})
	op.Inspect = "close ownership changed"
	q, s, uid, k, c, g := queueFixture(t, op)
	if _, err := s.Transition(uid, func(r *Run) error {
		r.IssueOwned, r.IssueUID, r.Target.Identity.ProjectUID, r.Target.Identity.Actor = true, "issue", "project", "worker"
		r.Phase, r.Outcome, r.EndedAt, r.Inspect = Settled, "failed", new(c.Now()), op.Inspect
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reads, closes := 0, 0
	k.show = func(context.Context, string) (kata.IssueView, error) {
		reads++
		return kata.IssueView{UID: "issue", ProjectUID: "project", Status: "open", Owner: "worker", Revision: 7, Metadata: map[string]json.RawMessage{"herdr_kata.run": json.RawMessage(`"` + uid + `"`)}}, nil
	}
	k.close = func(_ context.Context, req kata.CloseReq) error {
		if reads == 0 || req.IfMatch != "7" {
			t.Fatal(req)
		}
		closes++
		return nil
	}
	m := NewManager(config.Config{}, k, nil, s, q, g, c)
	if err := m.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := q.send(t.Context(), uid); err != nil {
		t.Fatal(err)
	}
	r := loaded(t, s, uid)
	if closes != 1 || r.Inspect != "" || !r.Ops[0].Landed() {
		t.Fatalf("close still stranded: closes=%d inspect=%s", closes, r.Inspect)
	}
}

func TestRuntimeRunIssueClaimKeepsConditionalReporting(t *testing.T) {
	for _, kind := range []string{"close", "meta.set"} {
		for _, owner := range []string{"", "worker", "other-worker"} {
			t.Run(kind+"/"+owner, func(t *testing.T) {
				p := Payload{Ref: "example-project#task", Name: "work.attention", Value: "ok"}
				if kind == "close" {
					p.Close = &kata.CloseReq{Ref: p.Ref, Reason: "done"}
				}
				q, s, uid, k, _, _ := queueFixture(t, operation("report", kind, p))
				if _, err := s.Transition(uid, func(r *Run) error {
					r.IssueOwned, r.IssueUID, r.Target.Identity.ProjectUID, r.Target.Identity.Actor = true, "issue", "project", "worker"
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				k.show = func(context.Context, string) (kata.IssueView, error) {
					return kata.IssueView{UID: "issue", ProjectUID: "project", Status: "open", Owner: owner, Revision: 7, Metadata: map[string]json.RawMessage{"herdr_kata.run": json.RawMessage(`"` + uid + `"`)}}, nil
				}
				sends := 0
				k.close = func(_ context.Context, req kata.CloseReq) error {
					if req.IfMatch != "7" {
						t.Fatal("missing close guard", req)
					}
					sends++
					return nil
				}
				k.meta = func(_ context.Context, _, _, _ string, cond kata.Cond) (int64, error) {
					if cond.IfMatch != "7" {
						t.Fatal("missing attention guard", cond)
					}
					sends++
					return 8, nil
				}
				if err := q.send(t.Context(), uid); err != nil {
					t.Fatal(err)
				}
				want := 1
				if owner == "other-worker" {
					want = 0
				}
				if sends != want {
					t.Fatalf("owner %q: sends=%d want=%d inspect=%s", owner, sends, want, loaded(t, s, uid).Inspect)
				}
			})
		}
	}
}
