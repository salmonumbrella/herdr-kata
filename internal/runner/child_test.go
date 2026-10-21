package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

const childUID = "01K00000000000000000000001"
const deliveryUID = "01K00000000000000000000002"
const deliveryEvent = "01K00000000000000000000003"

func newChildAdmission(t *testing.T) *admissionFixture {
	t.Helper()
	f := newAdmission(t)
	f.cfg.Kata.Inbox = "worker/inbox"
	f.m.cfg = f.cfg
	f.d.Child = &sched.ChildDue{IssueUID: childUID, IssueRef: "project#ebrn", Scheduled: f.d.Occurrence, DeliveryJobUID: deliveryUID, DeliveryEventUID: deliveryEvent, Binding: sched.Binding{WorkflowUID: "wf", WorkflowEventUID: "wf-event", WFDef: f.d.WFDef, Profile: "default"}, NotifyKey: "notify.d29ya2VyL2luYm94", NotifyJSON: json.RawMessage(`{"from":"system","message":"due"}`), Ownership: sched.Ownership{Decision: "own-actor", Revision: 7}}
	f.uid = ident.RunUID(f.d.Occurrence, childUID)
	f.k.create = func(context.Context, kata.CreateReq) (kata.IssueView, error) {
		t.Error("child created an issue")
		return kata.IssueView{}, nil
	}
	f.k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{UID: childUID, ProjectUID: "project", QualifiedID: "project#ebrn", Owner: "worker", Status: "open", Title: "Child title", Body: "Child context", Revision: 7}, nil
	}
	f.k.meta = func(_ context.Context, ref, key, value string, cond kata.Cond) (int64, error) {
		if ref != "project#ebrn" || key != "work.attention" || value != "ok" || cond.IfMatch != "7" {
			t.Errorf("tracked write: %s %s %s %+v", ref, key, value, cond)
		}
		return 8, nil
	}
	return f
}
func TestChildUsesHistoricalDeliveryPair(t *testing.T) {
	f := newChildAdmission(t)
	// An unrelated current delivery definition is deliberately unusable.
	f.d.Job = kata.Job{UID: "edited", DefinitionEventUID: "new-event"}
	f.d.Def = defs.JobDef{Action: defs.Action{Kind: "notify"}}
	f.k.observe = func(_ context.Context, uid string, o kata.Observation) (kata.Run, bool, error) {
		if uid != f.uid || o.JobUID != deliveryUID || o.DefinitionEventUID != deliveryEvent || o.WorkflowUID != "wf" || o.WorkflowDefinitionEventUID != "wf-event" || o.IssueUID != childUID || o.OccurrenceKey != "2026-01-02T03:04:05.000000006Z" {
			t.Errorf("historical observation: %+v", o)
		}
		return kata.Run{}, false, nil
	}
	f.start(t)
	r := f.admit(t)
	if r.Child == nil || !r.IssueOwned || r.Source != childUID || r.JobDef.Action.Kind != "execute" {
		t.Fatalf("child journal: %+v", r)
	}
}

// Admission must build executable inputs from the binding and a fresh issue read.
func TestChildAdmissionBuildsPayloadFromBinding(t *testing.T) {
	f := newChildAdmission(t)
	f.d.Workflow, f.d.WFDef = nil, nil
	f.d.Child.Ownership = sched.Ownership{}
	f.d.Child.Binding.WorkflowName = "Check child"
	f.d.Child.Binding.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "check", Kind: "command", Command: "printf checked"}}}
	f.start(t)
	r := f.admit(t)
	if r.Workflow == nil || r.Workflow.Name != "Check child" || r.Workflow.UID != "wf" || r.Workflow.DefinitionEventUID != "wf-event" {
		t.Fatalf("bound workflow missing from journal: %+v", r.Workflow)
	}
	if r.WFDef == nil || r.WFDef.Version != 1 || len(r.WFDef.Steps) != 1 || r.WFDef.Steps[0].Key != "check" || r.WFDef.Steps[0].Command != "printf checked" {
		t.Fatalf("bound execution missing from journal: %+v", r.WFDef)
	}
	if r.Child == nil || r.Child.Ownership.Decision != "own-actor" || r.Child.Ownership.Revision != 7 || !r.IssueOwned {
		t.Fatalf("fresh ownership missing from journal: %+v", r.Child)
	}
}

func TestBindingEditedAfterAdmissionIgnored(t *testing.T) {
	f := newChildAdmission(t)
	f.start(t)
	f.admit(t)
	f.d.Child.Binding.WFDef.Steps[0].Command = "false"
	f.d.Child.Binding.Profile = "missing"
	r := f.admit(t)
	if r.WFDef.Steps[0].Command != "true" || r.ProfileName != "default" {
		t.Fatal("binding changed", r)
	}
}
func TestChildDefaultPromptContext(t *testing.T) {
	f := newChildAdmission(t)
	f.d.Child.Binding = sched.Binding{Profile: "default"}
	f.start(t)
	r := f.admit(t)
	prompt := BuildPrompt(r, r.Steps[0], "result", "report", true)
	if r.WFDef != nil || !strings.Contains(prompt, "Work Kata issue project#ebrn") || !strings.Contains(prompt, "Child title") || !strings.Contains(prompt, "Child context") {
		t.Fatal(prompt)
	}
}
func TestChildTakeoverUnownedClaims(t *testing.T) {
	f := newChildAdmission(t)
	f.d.Child.Ownership = sched.Ownership{Decision: "claimed", Revision: 7}
	var claimed bool
	f.k.show = func(context.Context, string) (kata.IssueView, error) {
		owner := ""
		rev := int64(7)
		if claimed {
			owner = "worker"
			rev = 9
		}
		return kata.IssueView{UID: childUID, ProjectUID: "project", QualifiedID: "project#ebrn", Status: "open", Owner: owner, Revision: rev}, nil
	}
	f.k.claim = func(_ context.Context, ref string, unowned bool) (kata.IssueView, error) {
		r := loaded(t, f.s, f.uid)
		if r.Phase != Admitted || !unowned || ref != "project#ebrn" {
			t.Error("claim not prepared/guarded", r, ref, unowned)
		}
		claimed = true
		return f.k.show(t.Context(), ref)
	}
	f.k.meta = func(_ context.Context, _, _, _ string, c kata.Cond) (int64, error) {
		if !claimed || c.IfMatch != "9" {
			t.Error("not claim revision", c)
		}
		return 10, nil
	}
	f.start(t)
	r := f.admit(t)
	if !claimed || !r.IssueOwned {
		t.Fatal("not taken over")
	}
}
func TestChildTakeoverForeignUnsupported(t *testing.T) {
	f := newChildAdmission(t)
	f.k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{UID: childUID, ProjectUID: "project", Owner: "other", Status: "open", Revision: 8}, nil
	}
	result, err := f.m.Admit(t.Context(), f.d)
	if err == nil || result != sched.NotAdmitted || f.m.FreeSlots() != 2 {
		t.Fatal(result, err)
	}
	if _, err = f.s.Load(f.uid); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("foreign child prepared", err)
	}
}

func TestNativeChildCannotReplaceExplicitRequestAdmission(t *testing.T) {
	f := newChildAdmission(t)
	f.k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{UID: childUID, ProjectUID: "project", QualifiedID: "project#ebrn", Owner: "worker", Status: "open", Revision: 7, Metadata: map[string]json.RawMessage{"herdr_kata.admission": json.RawMessage(`{"version":1}`)}}, nil
	}
	f.m.executions.Store(f.uid, struct{}{})
	f.start(t)
	result, err := f.m.Admit(t.Context(), f.d)
	if err == nil || result == sched.Admitted {
		t.Fatalf("native source replaced explicit admission: %v %v", result, err)
	}
	if _, err = f.s.Load(f.uid); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("native replacement journal: %v", err)
	}
}

func TestPreparedNativeChildCannotReplaceExplicitRequestAdmission(t *testing.T) {
	f := preparedChild(t)
	f.k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{UID: childUID, ProjectUID: "project", QualifiedID: "project#ebrn", Owner: "worker", Status: "open", Revision: 7, Metadata: map[string]json.RawMessage{"herdr_kata.admission": json.RawMessage(`{"version":1}`)}}, nil
	}
	f.start(t)
	if err := f.m.admitPrepared(t.Context(), loaded(t, f.s, f.uid)); err == nil {
		t.Fatal("prepared native child replaced an explicit admission")
	}
	if r := loaded(t, f.s, f.uid); admitted(r) || r.Phase != Held {
		t.Fatalf("source switch reached native admission: %+v", r)
	}
}
func TestChildSharesCapacity(t *testing.T) {
	f := newChildAdmission(t)
	f.m.cfg.Limits.MaxActiveRuns = 1
	f.start(t)
	f.admit(t)
	d := newAdmission(t).d
	result, err := f.m.Admit(t.Context(), d)
	if result != sched.NotAdmitted || err != nil || f.m.FreeSlots() != 0 {
		t.Fatal(result, err)
	}
}
func TestDeletedLocalStateNoRelaunchChild(t *testing.T) {
	f := newChildAdmission(t)
	f.start(t)
	f.admit(t)
	if err := os.RemoveAll(f.s.layout.RunDir(f.uid)); err != nil {
		t.Fatal(err)
	}
	f.k.run = func(context.Context, string) (kata.Run, error) { return kata.Run{UID: f.uid}, nil }
	result, err := f.m.Admit(t.Context(), f.d)
	if result != sched.Dropped || err != nil {
		t.Fatal(result, err)
	}
	if _, err = f.s.Load(f.uid); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("replacement journal", err)
	}
}
func TestChildTrackedConflictStopsWrites(t *testing.T) {
	f := newChildAdmission(t)
	var calls atomic.Int32
	f.k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) {
		calls.Add(1)
		return 0, &kata.Error{Code: "conflict"}
	}
	f.start(t)
	result, err := f.m.Admit(t.Context(), f.d)
	if result != sched.NotAdmitted || err == nil || calls.Load() != 1 {
		t.Fatal(result, err, calls.Load())
	}
	if r := loaded(t, f.s, f.uid); r.Inspect == "" || r.Phase != Admitted {
		t.Fatal(r)
	}
}

func TestChildMovedProjectNotTakenOver(t *testing.T) {
	f := newChildAdmission(t)
	f.k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{UID: childUID, ProjectUID: "other-project", QualifiedID: "other#child", Status: "open", Owner: "worker", Revision: 7}, nil
	}
	f.start(t)
	f.m.executions.Store(f.uid, struct{}{})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := f.m.Admit(ctx, f.d)
	if result != sched.NotAdmitted || err == nil || f.m.FreeSlots() != 2 {
		t.Fatal("moved child admitted", result, err)
	}
	if _, err = f.s.Load(f.uid); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("moved child prepared", err)
	}
}

func preparedChild(t *testing.T) *admissionFixture {
	t.Helper()
	f := newChildAdmission(t)
	f.m.cfg.Limits.MaxActiveRuns = 1
	target, err := f.m.pin()
	if err != nil {
		t.Fatal(err)
	}
	d, err := f.m.childInputs(t.Context(), f.d, target.Identity.ProjectUID)
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.m.prepare(d, f.uid, ident.OccurrenceKey(d.Occurrence), target)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Create(r); err != nil {
		t.Fatal(err)
	}
	f.m.executions.Store(f.uid, struct{}{})
	return f
}
func TestRecoverPreparedChildKeepsIdentity(t *testing.T) {
	f := preparedChild(t)
	f.k.run = func(context.Context, string) (kata.Run, error) {
		t.Error("recovery checked a replacement UID")
		return kata.Run{}, &kata.Error{Code: "not_found"}
	}
	f.start(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := f.m.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	r := loaded(t, f.s, f.uid)
	if r.Phase != Admitted || r.Child == nil || len(r.Ops) != 2 || r.Ops[0].ID != "admission" || AdmissionBody(r).JobUID != deliveryUID {
		t.Fatal("prepared child was not resumed", r)
	}
	ids, err := f.s.ScanActive()
	if err != nil || len(ids) != 1 || ids[0] != f.uid {
		t.Fatal("replacement journal", ids, err)
	}
}
func TestReleasePreparedChildTracksAfterAdmission(t *testing.T) {
	f := preparedChild(t)
	var tracked atomic.Bool
	f.k.meta = func(_ context.Context, _, name, value string, _ kata.Cond) (int64, error) {
		if name == "work.attention" && value == "ok" {
			tracked.Store(true)
		}
		return 8, nil
	}
	f.k.observe = func(_ context.Context, _ string, o kata.Observation) (kata.Run, bool, error) {
		if o.Status == "running" && tracked.Load() {
			t.Error("child tracked before admission")
		}
		return kata.Run{}, false, nil
	}
	f.k.comment = func(context.Context, string, string, string) error { return nil }
	f.k.notify = func(context.Context, string, string, string) error { return nil }
	f.q.k = &ackKata{fakeKata: f.k.fakeKata, unset: func(context.Context, string, string, kata.Cond) (int64, error) { return 8, nil }}
	f.start(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := f.m.Release(ctx, f.uid); err != nil {
		t.Fatal(err)
	}
	r := loaded(t, f.s, f.uid)
	if !tracked.Load() || r.Phase != HandedOff || !r.SlotReleased {
		t.Fatal("prepared child not handed off", r)
	}
}
