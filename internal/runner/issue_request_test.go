package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

const requestProject = "01K00000000000000000000009"
const requestParent = "01K00000000000000000000008"

type requestAuthority struct {
	mu              sync.Mutex
	issue           kata.IssueView
	ready           bool
	lost            bool
	writes          int
	uncommitted     bool
	readyErr        error
	lostOutcome     bool
	outcomeWrites   int
	changeAfterRead string
	changeReadAt    int
	showCalls       int
	unsetBehavior   string
	unsetCalls      int
}
type requestKata struct {
	*admissionKata
	authority *requestAuthority
}

func (k *requestKata) ShowIssue(_ context.Context, ref string) (kata.IssueView, error) {
	k.authority.mu.Lock()
	defer k.authority.mu.Unlock()
	if ref == requestParent {
		return kata.IssueView{UID: requestParent, ProjectUID: requestProject, Status: "open"}, nil
	}
	b, _ := json.Marshal(k.authority.issue)
	var issue kata.IssueView
	_ = json.Unmarshal(b, &issue)
	k.authority.showCalls++
	if k.authority.changeReadAt == 0 || k.authority.showCalls == k.authority.changeReadAt {
		switch k.authority.changeAfterRead {
		case "receipt":
			var a RequestAdmission
			_ = json.Unmarshal(k.authority.issue.Metadata["herdr_kata.admission"], &a)
			a.JournalID = "foreign-journal"
			k.authority.issue.Metadata["herdr_kata.admission"], _ = json.Marshal(a)
			k.authority.issue.Revision++
		case "receipt-shape":
			var a map[string]any
			_ = json.Unmarshal(k.authority.issue.Metadata["herdr_kata.admission"], &a)
			a["unexpected"] = true
			k.authority.issue.Metadata["herdr_kata.admission"], _ = json.Marshal(a)
			k.authority.issue.Revision++
		case "owner":
			k.authority.issue.Owner = "other-worker"
			k.authority.issue.Revision++
		}
		k.authority.changeAfterRead = ""
	}
	return issue, nil
}
func (k *requestKata) Ready(context.Context) ([]kata.IssueView, error) {
	k.authority.mu.Lock()
	defer k.authority.mu.Unlock()
	if err := k.authority.readyErr; err != nil {
		k.authority.readyErr = nil
		return nil, err
	}
	if !k.authority.ready {
		return nil, nil
	}
	return []kata.IssueView{{UID: childUID, ProjectUID: requestProject, Status: "open"}}, nil
}
func (k *requestKata) ListWorkflows(context.Context) ([]kata.Workflow, error) { return nil, nil }
func (k *requestKata) ShowWorkflow(context.Context, string) (kata.Workflow, error) {
	return kata.Workflow{}, errors.New("unexpected workflow read")
}
func (k *requestKata) MetaSet(_ context.Context, ref, key, value string, c kata.Cond) (int64, error) {
	a := k.authority
	a.mu.Lock()
	defer a.mu.Unlock()
	if ref != "project#child" || c.IfMatch == "" {
		return 0, errors.New("missing revision guard")
	}
	raw, exists := a.issue.Metadata[key]
	if c.IfMatch != strconv.FormatInt(a.issue.Revision, 10) || c.IfAbsent && exists || c.IfValue != nil && !jsonEqual(raw, []byte(*c.IfValue)) {
		return 0, &kata.Error{Code: "conflict"}
	}
	if key == "herdr_kata.admission" {
		if !c.IfAbsent || !c.JSONValue {
			return 0, errors.New("missing permanent admission fence")
		}
		a.writes++
		if a.uncommitted {
			return 0, lostReply
		}
	}
	if c.JSONValue {
		a.issue.Metadata[key] = json.RawMessage(value)
	} else {
		a.issue.Metadata[key], _ = json.Marshal(value)
	}
	a.issue.Revision++
	if key == "herdr_kata.outcome" {
		a.outcomeWrites++
		if a.lostOutcome {
			return 0, lostReply
		}
	}
	if key == "herdr_kata.admission" && a.lost {
		return 0, lostReply
	}
	return a.issue.Revision, nil
}
func (k *requestKata) MetaUnset(_ context.Context, ref, key string, c kata.Cond) (int64, error) {
	a := k.authority
	a.mu.Lock()
	defer a.mu.Unlock()
	a.unsetCalls++
	if a.unsetBehavior == "replace" {
		a.issue.Metadata[key] = json.RawMessage(`{"message":"replacement"}`)
		a.issue.Revision++
	}
	if ref != "project#child" || c.IfMatch != strconv.FormatInt(a.issue.Revision, 10) || c.IfValue != nil && !jsonEqual(a.issue.Metadata[key], []byte(*c.IfValue)) {
		return 0, &kata.Error{Code: "conflict"}
	}
	delete(a.issue.Metadata, key)
	a.issue.Revision++
	if a.unsetBehavior == "canceled" {
		return 0, context.Canceled
	}
	if a.unsetBehavior == "lost" {
		return 0, lostReply
	}
	return a.issue.Revision, nil
}
func newRequestAdmission(t *testing.T) (*admissionFixture, *requestKata) {
	t.Helper()
	f := newAdmission(t)
	f.cfg.Kata.Inbox = "worker/inbox"
	f.m.cfg = f.cfg
	request := sched.ExecuteRequest{Version: 1, Kind: "execute", RequestUID: "01K00000000000000000000004", RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	raw, _ := json.Marshal(request)
	a := &requestAuthority{ready: true, issue: kata.IssueView{UID: childUID, ProjectUID: requestProject, ParentUID: requestParent, QualifiedID: "project#child", Status: "open", Owner: "worker", Revision: 7, Title: "Inspect task", Body: "Check the input", Metadata: map[string]json.RawMessage{"herdr_kata.request": raw, "notify.d29ya2VyL2luYm94": json.RawMessage(`{"from":"system","message":"execute"}`), "notify.aHVtYW4": json.RawMessage(`{"message":"human note"}`)}}}
	k := &requestKata{admissionKata: f.k, authority: a}
	f.m.k = k
	f.q.k = k
	f.k.run = func(context.Context, string) (kata.Run, error) {
		return kata.Run{}, errors.New("unexpected cron history read")
	}
	f.k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) {
		return kata.Run{}, false, errors.New("unexpected cron observation")
	}
	f.k.comment = func(context.Context, string, string, string) error { return nil }
	f.k.notify = func(context.Context, string, string, string) error { return nil }
	if err := state.WriteJSON(f.s.fsys, f.s.layout.StateFile(), kata.Installation{Version: 1, CreatedAt: request.RequestedAt, Executor: f.cfg.Executor, InstanceUID: "instance", ProjectUID: requestProject, Actor: "worker", Route: kata.Route{Bin: "/bin/kata", Endpoint: "local", DiagnoseState: "ready"}}); err != nil {
		t.Fatal(err)
	}
	f.d = sched.Due{IssueRequest: &sched.IssueRequestDue{IssueUID: childUID, IssueRef: "project#child", ParentUID: requestParent, Request: request}}
	f.uid, _ = ident.IssueRequestUID(childUID)
	return f, k
}

func TestIssueRequestAdmissionReplayAndLostReply(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprint(lost), func(t *testing.T) {
			f, k := newRequestAdmission(t)
			k.authority.lost = lost
			f.start(t)
			r := f.admit(t)
			if r.Version != 2 || r.Child != nil || r.Admission != nil || r.Source != "issue-request:"+childUID || r.Input != "Check the input" || r.IssueRequest == nil || !admitted(r) {
				t.Fatalf("incorrect frozen journal: %+v", r)
			}
			f.admit(t)
			if k.authority.writes != 1 {
				t.Fatalf("admitted %d times", k.authority.writes)
			}
			// A new request identity cannot admit this child a second time.
			f.d.IssueRequest.Request.RequestUID = "01K00000000000000000000005"
			f.admit(t)
			if k.authority.writes != 1 {
				t.Fatal("replacement request admitted again")
			}
		})
	}
}

func TestIssueRequestReconcilesHeldCapacity(t *testing.T) {
	for _, stopped := range []bool{true, false} {
		t.Run(fmt.Sprint(stopped), func(t *testing.T) {
			f, k := newRequestAdmission(t)
			f.m.cfg.Limits.MaxActiveRuns = 1
			held := &Run{Version: 1, UID: "held-peer", Phase: Held, Job: kata.Job{UID: "held-job"}, Outcome: "unknown"}
			if !stopped {
				held.Steps = []StepState{{Key: "main", Kind: "prompt", Submission: Accepted}}
			}
			if err := f.s.Create(held); err != nil {
				t.Fatal(err)
			}
			if err := f.m.RestoreGates(); err != nil {
				t.Fatal(err)
			}
			f.start(t)
			f.m.executions.Store(f.uid, struct{}{})
			result, err := f.m.Admit(t.Context(), f.d)
			want := sched.NotAdmitted
			if stopped {
				want = sched.Admitted
			}
			if err != nil || result != want {
				t.Fatalf("admission with stopped=%v: result=%v err=%v, want %v", stopped, result, err, want)
			}
			if got := loaded(t, f.s, held.UID); got.SlotReleased != stopped || got.Phase != Held || got.Outcome != "unknown" {
				t.Fatalf("held result changed or capacity proof ignored: %+v", got)
			}
			wantWrites := 0
			if stopped {
				wantWrites = 1
				f.admit(t)
			}
			if k.authority.writes != wantWrites {
				t.Fatalf("admission writes=%d, want %d", k.authority.writes, wantWrites)
			}
		})
	}
}

func TestIssueRequestAdmissionCompetingJournals(t *testing.T) {
	a, ka := newRequestAdmission(t)
	b, kb := newRequestAdmission(t)
	kb.authority = ka.authority
	for _, f := range []*admissionFixture{a, b} {
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
		f.start(t)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, f := range []*admissionFixture{a, b} {
		wg.Go(func() { r := loaded(t, f.s, f.uid); results <- f.m.admitPrepared(t.Context(), r) })
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 || ka.authority.writes != 1 {
		t.Fatalf("winners=%d admissions=%d", successes, ka.authority.writes)
	}
}

func (k *requestKata) Inbox(_ context.Context, address string) ([]kata.InboxRequest, error) {
	if address != "worker/inbox" {
		return nil, fmt.Errorf("unexpected inbox %q", address)
	}
	k.authority.mu.Lock()
	defer k.authority.mu.Unlock()
	if _, exists := k.authority.issue.Metadata["notify.d29ya2VyL2luYm94"]; !exists {
		return nil, nil
	}
	return []kata.InboxRequest{{Ref: "project#child"}}, nil
}
