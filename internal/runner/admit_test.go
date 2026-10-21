package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type admissionKata struct {
	*fakeKata
	run      func(context.Context, string) (kata.Run, error)
	deadline func(context.Context, string, string, kata.Cond) error
}

func (k *admissionKata) ShowRun(c context.Context, u string) (kata.Run, error) { return k.run(c, u) }
func (k *admissionKata) Deadline(c context.Context, r, at string, cond kata.Cond) error {
	return k.deadline(c, r, at, cond)
}

type admissionGuard struct{ blocked atomic.Bool }

func (g *admissionGuard) Check(context.Context) error {
	if g.blocked.Load() {
		return errors.New("route changed")
	}
	return nil
}

type admissionFixture struct {
	m   *Manager
	q   *Queue
	s   *Store
	k   *admissionKata
	g   *admissionGuard
	d   sched.Due
	uid string
	cfg config.Config
}

func newAdmission(t *testing.T) *admissionFixture {
	t.Helper()
	s := testStore(t)
	cfg := config.Config{Executor: "installation", Kata: config.Kata{Actor: "worker", Project: "project"}, Limits: config.Limits{MaxActiveRuns: 2, DefaultTimeout: time.Hour}, Profiles: map[string]config.Profile{"default": {Kind: "codex", Args: []string{"--test"}}}}
	at := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	d := sched.Due{Job: kata.Job{UID: "job", DefinitionEventUID: "job-event"}, Occurrence: at, Def: defs.JobDef{Version: 1, Kind: "job", Enabled: true, Trigger: defs.Trigger{Kind: "manual"}, Action: defs.Action{Kind: "execute", WorkflowUID: "wf"}, Issue: &defs.IssuePolicy{Kind: "per-run", Title: "Inspect task", Body: "Details"}, Overlap: "forbid", Catchup: "skip"}}
	d.Workflow = &kata.Workflow{UID: "wf", DefinitionEventUID: "wf-event"}
	d.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "main", Kind: "command", Command: "true"}}}
	uid := ident.RunUID(at, ident.ScheduledSource(d.Job.UID, ident.OccurrenceKey(at)))
	g := &admissionGuard{}
	k := &admissionKata{fakeKata: &fakeKata{}}
	k.run = func(context.Context, string) (kata.Run, error) { return kata.Run{}, &kata.Error{Code: "not_found"} }
	k.create = func(context.Context, kata.CreateReq) (kata.IssueView, error) {
		return kata.IssueView{UID: "issue", QualifiedID: "project#task", Revision: 7}, nil
	}
	k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) { return kata.Run{}, false, nil }
	k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{UID: "issue", QualifiedID: "project#task", Revision: 7}, nil
	}
	if err := state.WriteJSON(s.fsys, s.layout.StateFile(), kata.Installation{Version: 1, CreatedAt: at.Add(-time.Hour), Executor: cfg.Executor, InstanceUID: "instance", ProjectUID: "project", Actor: "worker", Route: kata.Route{Bin: "/bin/kata", Endpoint: "local", DiagnoseState: "ready"}}); err != nil {
		t.Fatal(err)
	}
	q := NewQueue(k, s, g, RealClock{})
	m := NewManager(cfg, k, nil, s, q, g, RealClock{})
	m.environment = cleanLaunch{}
	return &admissionFixture{m, q, s, k, g, d, uid, cfg}
}
func (f *admissionFixture) start(t *testing.T) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- f.q.Run(ctx) }()
	stop := sync.OnceFunc(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	})
	t.Cleanup(stop)
	return stop
}
func (f *admissionFixture) admit(t *testing.T) *Run {
	t.Helper()
	// Admission tests reserve the executor separately; workflow tests exercise it.
	f.m.executions.Store(f.uid, struct{}{})
	result, err := f.m.Admit(t.Context(), f.d)
	if err != nil || result != sched.Admitted {
		t.Fatalf("admit: %v %v", result, err)
	}
	return loaded(t, f.s, f.uid)
}
func TestAdmitSavesPreparedBeforeCreate(t *testing.T) {
	f := newAdmission(t)
	f.k.create = func(_ context.Context, req kata.CreateReq) (kata.IssueView, error) {
		r := loaded(t, f.s, f.uid)
		if r.Phase != Prepared || len(r.Ops) != 1 || r.Ops[0].FirstSent == nil || req.Title != "Inspect task — "+ident.OccurrenceKey(f.d.Occurrence) || req.IdempotencyKey != "herdr-kata:"+f.uid || req.Meta["herdr_kata.run"] != f.uid || req.Meta["work.attention"] != "ok" {
			t.Errorf("not prepared: %+v %+v", r, req)
		}
		return kata.IssueView{UID: "issue", QualifiedID: "project#task"}, nil
	}
	f.k.observe = func(_ context.Context, u string, o kata.Observation) (kata.Run, bool, error) {
		r := loaded(t, f.s, u)
		if r.IssueUID != "issue" || o.IssueUID != "issue" || o.ExpectedRevision != 0 || o.StartedAt != nil || o.Summary.Message != "admitted "+r.JournalID {
			t.Errorf("observation: %+v %+v", r, o)
		}
		return kata.Run{}, false, nil
	}
	f.start(t)
	r := f.admit(t)
	if r.Phase != Admitted || f.m.FreeSlots() != 1 || !f.m.GateHeld("job") {
		t.Fatalf("admission/capacity: %+v", r)
	}
	if r.Profile.Kind != "codex" || r.Cwd != f.s.layout.RunDir(f.uid) || r.ExpiresAt.IsZero() {
		t.Fatalf("inputs not frozen: %+v", r)
	}
}
func TestAdmitExistingEvidenceNoLocalRefuses(t *testing.T) {
	f := newAdmission(t)
	f.k.run = func(context.Context, string) (kata.Run, error) { return kata.Run{UID: f.uid}, nil }
	result, err := f.m.Admit(t.Context(), f.d)
	if err != nil || result != sched.Dropped {
		t.Fatalf("%v %v", result, err)
	}
	if _, err := f.s.Load(f.uid); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("replacement journal: %v", err)
	}
	if f.m.FreeSlots() != 2 {
		t.Fatal("capacity leaked")
	}
}
func TestAdmitAuthorityErrorNoLocalRefuses(t *testing.T) {
	f := newAdmission(t)
	f.k.run = func(context.Context, string) (kata.Run, error) { return kata.Run{}, lostReply }
	r, e := f.m.Admit(t.Context(), f.d)
	if e == nil || r != sched.NotAdmitted || f.m.FreeSlots() != 2 {
		t.Fatal(r, e)
	}
	if _, e := f.s.Load(f.uid); !errors.Is(e, fs.ErrNotExist) {
		t.Fatal(e)
	}
}
func TestAdmitQuarantineVetoes(t *testing.T) {
	f := newAdmission(t)
	if err := state.WriteJSON(f.s.fsys, f.s.layout.QuarantineRunDir(f.uid)+"/bad.json", map[string]string{}); err != nil {
		t.Fatal(err)
	}
	f.k.run = func(context.Context, string) (kata.Run, error) {
		t.Error("authority read after veto")
		return kata.Run{}, nil
	}
	r, e := f.m.Admit(t.Context(), f.d)
	if r != sched.Dropped || e != nil || f.m.FreeSlots() != 2 {
		t.Fatal(r, e)
	}
}
func TestAdmitLookAlikeSameRunReuses(t *testing.T) {
	f := newAdmission(t)
	f.k.create = func(context.Context, kata.CreateReq) (kata.IssueView, error) {
		return kata.IssueView{}, &kata.Error{Code: "duplicate_candidates", CandidateUIDs: []string{"candidate"}}
	}
	f.k.show = func(context.Context, string) (kata.IssueView, error) {
		b, _ := json.Marshal(f.uid)
		return kata.IssueView{UID: "same", Metadata: map[string]json.RawMessage{"herdr_kata.run": b}}, nil
	}
	f.start(t)
	if r := f.admit(t); r.IssueUID != "same" {
		t.Fatal(r)
	}
}
func TestAdmitLookAlikeOtherForceNew(t *testing.T) {
	f := newAdmission(t)
	var calls atomic.Int32
	f.k.create = func(_ context.Context, r kata.CreateReq) (kata.IssueView, error) {
		if calls.Add(1) == 1 {
			return kata.IssueView{}, &kata.Error{Code: "duplicate_candidates", CandidateUIDs: []string{"other"}}
		}
		if !r.ForceNew || r.IdempotencyKey != "herdr-kata:"+f.uid {
			t.Error("force-new request", r)
		}
		return kata.IssueView{UID: "fresh"}, nil
	}
	f.start(t)
	r := f.admit(t)
	if r.IssueUID != "fresh" || calls.Load() != 2 {
		t.Fatal(r, calls.Load())
	}
}
func TestAdmitReplayContinues(t *testing.T) {
	f := newAdmission(t)
	f.k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) { return kata.Run{}, true, nil }
	f.start(t)
	r := f.admit(t)
	before := len(r.Ops)
	r = f.admit(t)
	if r.Phase != Admitted || len(r.Ops) != before {
		t.Fatal(r)
	}
}
func TestAdmitConflictIdentityInspects(t *testing.T) {
	f := newAdmission(t)
	f.k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) {
		return kata.Run{}, false, &kata.Error{Code: "cron_conflict"}
	}
	var n atomic.Int32
	f.k.run = func(context.Context, string) (kata.Run, error) {
		if n.Add(1) == 1 {
			return kata.Run{}, &kata.Error{Code: "not_found"}
		}
		return kata.Run{UID: f.uid, ExecutorLabel: "other"}, nil
	}
	f.start(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	r, e := f.m.Admit(ctx, f.d)
	if r != sched.NotAdmitted || e == nil {
		t.Fatal(r, e)
	}
	saved := loaded(t, f.s, f.uid)
	if saved.Phase != Held || saved.Reason != "admission_rejected" {
		t.Fatal(saved)
	}
}
func TestAdmitRouteMismatchRefused(t *testing.T) {
	f := newAdmission(t)
	f.g.blocked.Store(true)
	f.k.run = func(context.Context, string) (kata.Run, error) {
		t.Error("authority touched on mismatch")
		return kata.Run{}, nil
	}
	r, e := f.m.Admit(t.Context(), f.d)
	if r != sched.NotAdmitted || e == nil || f.m.FreeSlots() != 2 {
		t.Fatal(r, e)
	}
}
func TestAdmitDeadlineGuarded(t *testing.T) {
	f := newAdmission(t)
	offset := int64(90)
	f.d.Def.Issue.DeadlineOffsetSeconds = &offset
	var called atomic.Bool
	f.k.deadline = func(_ context.Context, r, at string, c kata.Cond) error {
		called.Store(true)
		if r != "project#task" || at != ident.OccurrenceKey(f.d.Occurrence.Add(90*time.Second)) || c.IfMatch != "7" {
			t.Error(r, at, c)
		}
		return nil
	}
	f.start(t)
	f.admit(t)
	if !called.Load() {
		t.Fatal("deadline missing")
	}
}
func TestCapacityReservedAtomically(t *testing.T) {
	f := newAdmission(t)
	f.d.Def.Overlap = "allow"
	f.k.create = func(_ context.Context, r kata.CreateReq) (kata.IssueView, error) {
		return kata.IssueView{UID: r.IdempotencyKey}, nil
	}
	f.start(t)
	var wg sync.WaitGroup
	var admitted atomic.Int32
	for n := range 20 {
		wg.Go(func() {
			d := f.d
			d.Occurrence = d.Occurrence.Add(time.Duration(n) * time.Second)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			f.m.executions.Store(ident.RunUID(d.Occurrence, ident.ScheduledSource(d.Job.UID, ident.OccurrenceKey(d.Occurrence))), struct{}{})
			r, e := f.m.Admit(ctx, d)
			if e != nil {
				t.Error(e)
			}
			if r == sched.Admitted {
				admitted.Add(1)
			}
		})
	}
	wg.Wait()
	if admitted.Load() != 2 || f.m.FreeSlots() != 0 {
		t.Fatal(admitted.Load(), f.m.FreeSlots())
	}
}
func TestAdmissionBodyFrozenWorkflowAndManual(t *testing.T) {
	f := newAdmission(t)
	f.d.Manual = true
	f.d.ManualRunUID = ident.RunUID(f.d.Occurrence, "manual")
	f.uid = f.d.ManualRunUID
	f.d.Workflow = &kata.Workflow{UID: "wf", DefinitionEventUID: "wf-event"}
	f.d.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "step", Kind: "prompt", Prompt: "Inspect"}}}
	f.d.Def.Action = defs.Action{Kind: "execute", WorkflowUID: "wf"}
	f.start(t)
	r := f.admit(t)
	o := AdmissionBody(r)
	if o.OccurrenceKey != "manual:"+f.uid || o.WorkflowUID != "wf" || o.WorkflowDefinitionEventUID != "wf-event" || o.Actor != "worker" || o.Teammate == nil || *o.Teammate != "herdr-kata" || o.ExecutorLabel != f.cfg.Executor {
		t.Fatal(o)
	}
	if !reflect.DeepEqual(r.Steps, []StepState{{Key: "step", Kind: "prompt", Attempt: 1, Submission: None}}) {
		t.Fatal(r.Steps)
	}
}
func TestRestoreGatesFromLocalPhases(t *testing.T) {
	f := newAdmission(t)
	for n, p := range []Phase{Prepared, Admitted, Running, Blocked, Held, HandedOff, Settled} {
		r := &Run{Version: 1, UID: fmt.Sprint(n), Job: kata.Job{UID: fmt.Sprint(n)}, Phase: p}
		if e := f.s.Create(r); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.m.RestoreGates(); e != nil {
		t.Fatal(e)
	}
	if f.m.FreeSlots() != 0 {
		t.Fatal("restored capacity exceeded")
	}
	for n := range 7 {
		if f.m.GateHeld(fmt.Sprint(n)) != (n < 5) {
			t.Fatal("gate", n)
		}
	}
}

// A manual retry must use the scheduled instant captured before a crash.
func TestAdmitManualRetryFreezesDeadline(t *testing.T) {
	f := newAdmission(t)
	f.d.Manual = true
	f.d.ManualRunUID = f.uid
	offset := int64(90)
	f.d.Def.Issue.DeadlineOffsetSeconds = &offset
	ctx, cancel := context.WithCancel(t.Context())
	f.k.run = func(context.Context, string) (kata.Run, error) { return kata.Run{}, &kata.Error{Code: "not_found"} }
	done := make(chan error, 1)
	go func() { _, e := f.m.Admit(ctx, f.d); done <- e }()
	for {
		if _, e := f.s.Load(f.uid); e == nil {
			break
		}
		select {
		case <-time.After(time.Millisecond):
		case <-t.Context().Done():
			t.Fatal("prepare timeout")
		}
	}
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	f.d.Occurrence = f.d.Occurrence.Add(time.Hour)
	f.k.deadline = func(_ context.Context, _ string, at string, _ kata.Cond) error {
		if at != "2026-01-02T03:05:35.000000006Z" {
			t.Error("regenerated deadline", at)
		}
		return nil
	}
	f.start(t)
	f.admit(t)
}

func TestAdmitHandedOffNotExecuted(t *testing.T) {
	f := newAdmission(t)
	f.start(t)
	f.admit(t)
	_, e := f.s.Transition(f.uid, func(r *Run) error { r.Phase = HandedOff; r.Reason = "human handoff"; return nil })
	if e != nil {
		t.Fatal(e)
	}
	r := f.admit(t)
	if r.Phase != HandedOff || r.Reason != "human handoff" || f.m.FreeSlots() != 2 || f.m.GateHeld("job") {
		t.Fatal(r)
	}
}

type liveAdmissionKata struct {
	*admissionKata
	changed atomic.Bool
}

func (k *liveAdmissionKata) Health(context.Context) (kata.Health, error) {
	return kata.Health{SchemaVersion: 33}, nil
}
func (k *liveAdmissionKata) Capabilities(context.Context) (kata.Capabilities, error) {
	return kata.Capabilities{ProjectUID: "project", EventFeatures: []string{"cron_v1"}}, nil
}
func (k *liveAdmissionKata) FederationIdentity(context.Context) (kata.Identity, error) {
	return kata.Identity{InstanceUID: "instance", Actor: "worker"}, nil
}
func (k *liveAdmissionKata) DaemonDiagnose(context.Context, string) (kata.Diagnosis, error) {
	r := k.ResolveRoute()
	return kata.Diagnosis{State: "ready", Endpoint: r.Endpoint, Authoritative: true}, nil
}
func (k *liveAdmissionKata) ResolveRoute() kata.Route {
	endpoint := "local"
	if k.changed.Load() {
		endpoint = "changed"
	}
	return kata.Route{Bin: "/bin/kata", Endpoint: endpoint, DiagnoseState: "ready"}
}
func TestAdmitLiveRouteGuardCatalogEditRefuses(t *testing.T) {
	f := newAdmission(t)
	k := &liveAdmissionKata{admissionKata: f.k}
	g := kata.NewRouteGuard(f.cfg, k, f.s.fsys, f.s.layout)
	if e := g.Check(t.Context()); e != nil {
		t.Fatal(e)
	}
	k.changed.Store(true)
	m := NewManager(f.cfg, k, nil, f.s, f.q, g, RealClock{})
	r, e := m.Admit(t.Context(), f.d)
	if e == nil || r != sched.NotAdmitted {
		t.Fatal(r, e)
	}
	if _, e := f.s.Load(f.uid); !errors.Is(e, fs.ErrNotExist) {
		t.Fatal("route edit prepared a journal", e)
	}
}

func TestAdmitStopOrUpgradeRefusesNewWork(t *testing.T) {
	for _, file := range []string{"stop", "upgrade"} {
		t.Run(file, func(t *testing.T) {
			f := newAdmission(t)
			if e := state.WriteJSON(f.s.fsys, f.s.layout.Root+"/"+file, map[string]string{}); e != nil {
				t.Fatal(e)
			}
			r, e := f.m.Admit(t.Context(), f.d)
			if e != nil || r != sched.NotAdmitted || f.m.FreeSlots() != 2 {
				t.Fatal(r, e)
			}
			if _, e := f.s.Load(f.uid); !errors.Is(e, fs.ErrNotExist) {
				t.Fatal("prepared while draining", e)
			}
		})
	}
}
func TestAdmitCreateEmptyUIDInspects(t *testing.T) {
	f := newAdmission(t)
	var calls atomic.Int32
	f.k.create = func(context.Context, kata.CreateReq) (kata.IssueView, error) {
		calls.Add(1)
		return kata.IssueView{}, nil
	}
	f.start(t)
	r, e := f.m.Admit(t.Context(), f.d)
	if e == nil || r != sched.NotAdmitted || calls.Load() != 1 {
		t.Fatal(r, e, calls.Load())
	}
	if r := loaded(t, f.s, f.uid); r.Inspect == "" {
		t.Fatal("empty create reply not inspected")
	}
}

func TestQueueAdmissionTargetNeverRetargeted(t *testing.T) {
	f := newAdmission(t)
	target, e := f.m.pin()
	if e != nil {
		t.Fatal(e)
	}
	r := &Run{Version: 1, UID: f.uid, Target: target, Phase: Held, Ops: []Op{operation("report", "comment", Payload{Ref: "issue", Body: "pending report"})}}
	if e := f.s.Create(r); e != nil {
		t.Fatal(e)
	}
	var install kata.Installation
	if e := state.ReadJSON(f.s.fsys, f.s.layout.StateFile(), 64<<10, &install); e != nil {
		t.Fatal(e)
	}
	install.InstanceUID = "replacement"
	if e := state.WriteJSON(f.s.fsys, f.s.layout.StateFile(), install); e != nil {
		t.Fatal(e)
	}
	f.k.comment = func(context.Context, string, string, string) error { t.Error("old run retargeted"); return nil }
	deliver(t, f.q, f.uid)
	if saved := loaded(t, f.s, f.uid); saved.Inspect == "" || saved.Ops[0].Done {
		t.Fatal("retargeted report was not held")
	}
}

// Ignored agent spellings must not choose an unvalidated launch profile.
func TestAdmitUsesExactAgentOption(t *testing.T) {
	for _, tc := range []struct {
		name, options, profile, kind, arg string
	}{
		{"variant only", `{"Agent":"other"}`, "default", "codex", "--test"},
		{"canonical then variant", `{"agent":"default","AGENT":"other"}`, "default", "codex", "--test"},
		{"canonical", `{"agent":"other"}`, "other", "claude", "--other"},
		{"duplicate canonical", `{"agent":"default","agent":"other"}`, "other", "claude", "--other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdmission(t)
			f.cfg.Profiles["other"] = config.Profile{Kind: "claude", Args: []string{"--other"}}
			f.m.cfg = f.cfg
			f.d.Def.Action = defs.Action{Kind: "execute", Prompt: "work"}
			f.d.Def.Options = json.RawMessage(tc.options)
			f.d.Workflow, f.d.WFDef = nil, nil
			f.start(t)
			r := f.admit(t)
			if r.ProfileName != tc.profile || r.Profile.Kind != tc.kind || len(r.Profile.Args) != 1 || r.Profile.Args[0] != tc.arg {
				t.Fatalf("journal selected profile %q: %+v", r.ProfileName, r.Profile)
			}
		})
	}
}
