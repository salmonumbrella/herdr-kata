package runner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type referenceSnapshot struct {
	ProjectName string
	IssueRefs   map[string]string `json:"issue_refs"`
}

func savedReferences(t *testing.T, s *Store, uid string) referenceSnapshot {
	t.Helper()
	var got referenceSnapshot
	var raw map[string]json.RawMessage
	if err := state.ReadJSON(s.fsys, s.layout.RunFile(uid), maxRunBytes, &raw); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw["ProjectName"], &got.ProjectName)
	_ = json.Unmarshal(raw["issue_refs"], &got.IssueRefs)
	return got
}
func TestAdmissionRequiresProjectName(t *testing.T) {
	f := newAdmission(t)
	f.m.cfg.Kata.Project = ""
	f.m.executions.Store(f.uid, struct{}{})
	f.start(t)
	result, err := f.m.Admit(t.Context(), f.d)
	if result != sched.NotAdmitted || err == nil || !strings.Contains(err.Error(), "project") {
		t.Fatalf("unresolved project admitted: %v %v", result, err)
	}
	if _, err := f.s.Load(f.uid); !missing(err) {
		t.Fatalf("unresolved project created a journal: %v", err)
	}
}
func TestAdmissionSavesDisplayReferences(t *testing.T) {
	f := newAdmission(t)
	f.m.cfg.Kata.Project = "example-project"
	f.d.Def.Options = json.RawMessage(`{"run_parent":"` + runParentUID + `"}`)
	shows, creates := 0, 0
	f.k.show = func(_ context.Context, ref string) (kata.IssueView, error) {
		shows++
		if ref != runParentUID {
			t.Errorf("unexpected show %s", ref)
		}
		return kata.IssueView{UID: ref, ShortID: "00001", ProjectUID: "project", Status: "open"}, nil
	}
	f.k.create = func(_ context.Context, req kata.CreateReq) (kata.IssueView, error) {
		creates++
		if req.Parent != runParentUID {
			t.Errorf("parent changed: %s", req.Parent)
		}
		return kata.IssueView{UID: "child", ShortID: "abcd5", ProjectUID: "project"}, nil
	}
	f.start(t)
	f.admit(t)
	got := savedReferences(t, f.s, f.uid)
	if got.ProjectName != "example-project" || got.IssueRefs[runParentUID] != "example-project#00001" || got.IssueRefs["child"] != "example-project#abcd5" {
		t.Fatalf("saved refs: %+v", got)
	}
	if shows != 1 || creates != 1 {
		t.Fatalf("extra Kata calls: shows=%d creates=%d", shows, creates)
	}
}
func TestQueueClaimSavesDisplayReference(t *testing.T) {
	q, s, uid, k, _, _ := queueFixture(t, operation("claim", "claim", Payload{Ref: "issue", Owner: "worker", IfUnowned: true}))
	_, err := s.Transition(uid, func(r *Run) error { r.Target.Identity.ProjectUID = "project"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	k.claim = func(_ context.Context, ref string, unowned bool) (kata.IssueView, error) {
		calls++
		if ref != "issue" || !unowned {
			t.Errorf("claim changed: %s %v", ref, unowned)
		}
		return kata.IssueView{UID: "issue", QualifiedID: "example-project#abc45", ProjectUID: "project", Owner: "worker"}, nil
	}
	deliver(t, q, uid)
	if got := savedReferences(t, s, uid).IssueRefs["issue"]; got != "example-project#abc45" || calls != 1 {
		t.Fatalf("claim ref %q; calls=%d", got, calls)
	}
}

func TestHandledShowResponseSavesReference(t *testing.T) {
	q, s, uid, k, _, _ := queueFixture(t, operation("resolve", "resolve-recipient", Payload{Ref: "issue"}), operation("notify", "notify", Payload{Ref: "issue", Message: "help"}))
	calls := 0
	k.show = func(context.Context, string) (kata.IssueView, error) {
		calls++
		return kata.IssueView{UID: "issue", QualifiedID: "example-project#abc45", Owner: "worker"}, nil
	}
	deliver(t, q, uid)
	if got := savedReferences(t, s, uid).IssueRefs["issue"]; got != "example-project#abc45" || calls != 1 {
		t.Fatal(got, calls)
	}
}
