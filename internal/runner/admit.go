package runner

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/crashpoint"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// AdmissionBody depends only on the frozen journal, never the live definition
// or clock. In particular, expected revision zero is always sent.
func AdmissionBody(r *Run) kata.Observation {
	if r.Admission != nil {
		return r.Admission.Body
	}
	teammate := "herdr-kata"
	o := kata.Observation{JobUID: r.Job.UID, DefinitionEventUID: r.Job.DefinitionEventUID, OccurrenceKey: r.Occurrence, IssueUID: r.IssueUID, Actor: r.Target.Identity.Actor, Teammate: &teammate, ExecutorLabel: r.Target.Executor, Status: "running", Summary: kata.Summary{Version: 1, Message: "admitted"}, ExpectedRevision: 0}
	if r.Workflow != nil {
		o.WorkflowUID = r.Workflow.UID
		o.WorkflowDefinitionEventUID = r.Workflow.DefinitionEventUID
	}
	if r.JournalID != "" {
		o.Summary.Message += " " + r.JournalID
	}
	return o
}

// pinnedTarget reads installation state without coupling run to worker.
func pinnedTargetAt(store *Store, l state.Layout) (Target, error) {
	var s struct {
		Version     int        `json:"version"`
		Name        string     `json:"name,omitempty"`
		CreatedAt   time.Time  `json:"state_created_at"`
		Executor    string     `json:"executor"`
		InstanceUID string     `json:"instance_uid"`
		ProjectUID  string     `json:"project_uid"`
		Actor       string     `json:"actor"`
		Route       kata.Route `json:"route"`
	}
	err := state.ReadJSON(store.fsys, l.StateFile(), 64<<10, &s)
	if err != nil {
		return Target{}, err
	}
	if s.Version != 1 || s.CreatedAt.IsZero() || s.InstanceUID == "" || s.ProjectUID == "" || s.Actor == "" || s.Executor == "" {
		return Target{}, errors.New("invalid pinned installation")
	}
	return Target{Identity: kata.Identity{InstanceUID: s.InstanceUID, ProjectUID: s.ProjectUID, Actor: s.Actor}, Route: s.Route, Executor: s.Executor}, nil
}
func (m *Manager) pin() (Target, error) {
	target, err := pinnedTargetAt(m.store, m.pinLayout)
	if err == nil && (target.Identity.Actor != m.cfg.Kata.Actor || target.Executor != m.cfg.Executor) {
		err = errors.New("pinned actor or executor differs from configuration")
	}
	return target, err
}
func sameTarget(a, b Target) bool {
	return a.Identity.InstanceUID == b.Identity.InstanceUID && a.Identity.ProjectUID == b.Identity.ProjectUID && a.Identity.Actor == b.Identity.Actor && a.Executor == b.Executor && reflect.DeepEqual(a.Route, b.Route)
}
func (m *Manager) prepare(d sched.Due, uid, key string, target Target) (*Run, error) {
	var generation int64
	if lease := m.lease.Load(); lease != nil {
		held := false
		if generation, held = (*lease)(); !held {
			if refusal := m.laneRefusal(); refusal != nil {
				return nil, fmt.Errorf("lane lease not held; admission refused: %w", refusal)
			}
			return nil, errors.New("lane lease not held; admission refused")
		}
	}
	if d.Child == nil && d.IssueRequest == nil {
		owner, support := defs.Classify(d.Def, d.WFDef, m.cfg)
		if !defs.Dispatchable(owner, support, d.Job.DeletedAt) {
			return nil, fmt.Errorf("unsupported admission: %v", support.Reasons)
		}
	}
	var childInput *string
	if d.Child != nil || d.IssueRequest != nil {
		childInput = &d.Def.Issue.Body
	}
	effective, err := defs.EffectiveInput(d.Def.Options, d.ManualInput, childInput, d.WFDef != nil && d.WFDef.Input != "")
	if err != nil {
		return nil, err
	}
	profile := defs.OptionString(d.Def.Options, "agent")
	if profile == "" {
		profile = "default"
	}
	timeout := m.cfg.Limits.DefaultTimeout
	if d.Def.TimeoutSeconds > 0 {
		timeout = time.Duration(d.Def.TimeoutSeconds) * time.Second
	}
	cwd := m.store.layout.RunDir(uid)
	if d.Def.CheckoutKey != "" {
		cwd = m.cfg.Checkouts[d.Def.CheckoutKey]
	}
	proof := false
	r := &Run{ProjectName: m.cfg.Kata.Project, IssueRefs: maps.Clone(d.IssueRefs), JournalID: rand.Text(), Admitted: &proof, Version: 1, UID: uid, Source: ident.ScheduledSource(d.Job.UID, key), Target: target, Job: d.Job, JobDef: d.Def, Workflow: d.Workflow, WFDef: d.WFDef, Occurrence: key, Manual: d.Manual, Input: effective, ProfileName: profile, Profile: m.cfg.Profiles[profile], Cwd: cwd, PromptCap: defs.RunCap(m.cfg, profile, d.WFDef), Timeout: timeout, ExpiresAt: m.clock.Now().UTC().Add(timeout), Phase: Prepared, LeaseGeneration: generation}
	if d.WFDef != nil {
		for _, s := range d.WFDef.Steps {
			if p := defs.StepProfile(s, profile); s.Kind == "prompt" && p != profile {
				if r.StepProfiles == nil {
					r.StepProfiles = map[string]config.Profile{}
				}
				r.StepProfiles[p] = m.cfg.Profiles[p]
			}
		}
	}
	if d.Child != nil {
		r.Source = d.Child.IssueUID
		r.Child = &ChildRun{ChildDue: *d.Child}
		r.IssueUID, r.IssueRef = d.Child.IssueUID, d.Child.IssueRef
		r.IssueOwned = d.Child.Ownership.Decision == "own-actor"
		if d.Child.Binding.Cwd != "" {
			r.Cwd = d.Child.Binding.Cwd
		}

	}
	if c := d.IssueRequest; c != nil {
		r.Version = 2
		r.Source = "issue-request:" + c.IssueUID
		r.IssueUID, r.IssueRef = c.IssueUID, c.IssueRef
		r.IssueOwned = c.Ownership.Decision == "own-actor"
		r.IssueRequest = &IssueRequestRun{Request: c.Request, ParentUID: c.ParentUID, Binding: c.Binding, Ownership: c.Ownership, NotifyKey: c.NotifyKey, NotifyJSON: c.NotifyJSON}
		r.IssueRequest.Admission = requestReceipt(r)
		if c.Binding.Cwd != "" {
			r.Cwd = c.Binding.Cwd
		}
	}
	if d.WFDef != nil {
		order, err := defs.TopoOrder(d.WFDef.Steps)
		if err != nil {
			return nil, err
		}
		for _, key := range order {
			for _, step := range d.WFDef.Steps {
				if step.Key == key {
					r.Steps = append(r.Steps, StepState{Key: key, Kind: step.Kind, Attempt: 1, Submission: None})
				}
			}
		}
	} else if d.Def.Action.Kind == "execute" {
		r.Steps = []StepState{{Key: "main", Kind: "prompt", Attempt: 1, Submission: None}}
	}
	if d.Child != nil || d.IssueRequest != nil {
		// The hub already created this issue.
	} else if d.Def.Issue.Kind == "existing" {
		r.IssueUID = d.Def.Issue.UID
		r.IssueRef = d.IssueRefs[d.Def.Issue.UID]
		if r.IssueRef == "" {
			r.IssueRef = d.Def.Issue.UID
		}
	} else {
		r.IssueOwned = true
		req := kata.CreateReq{Parent: defs.RunParent(d.Def), Labels: []string{"herdr-kata:run"}, Title: d.Def.Issue.Title + " — " + ident.OccurrenceKey(d.Occurrence), Body: d.Def.Issue.Body, IdempotencyKey: "herdr-kata:" + uid, Meta: map[string]string{"herdr_kata.run": uid, "work.attention": "ok"}}
		r.Ops = []Op{newOp("create", "create", Payload{Create: &req}, req.IdempotencyKey)}
	}
	if d.IssueRequest == nil {
		r.Admission = &AdmissionRequests{Body: AdmissionBody(r)}
	}
	if offset := d.Def.Issue.DeadlineOffsetSeconds; offset != nil {
		r.Admission.DeadlineAt = ident.OccurrenceKey(d.Occurrence.Add(time.Duration(*offset) * time.Second))
	}
	// Detach pointers, maps and raw definitions supplied by a caller.
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	var frozen Run
	err = json.Unmarshal(b, &frozen)
	return &frozen, err
}
func newOp(id, kind string, p Payload, key string) Op {
	b, err := json.Marshal(p)
	if err != nil {
		panic(err)
	}
	return Op{ID: id, Kind: kind, Payload: b, Key: key, Safety: kata.SafetyOf(kind)}
}

// Admit waits for the queued admission sequence to resolve before returning an
// admitted decision. Retry backoff pauses scheduler dispatch; it never discards
// a pending occurrence or releases a durable preparation's reservation.
func (m *Manager) Admit(ctx context.Context, d sched.Due) (sched.AdmitResult, error) {
	return m.AdmitWithPreparationGuard(ctx, d, nil)
}

// AdmitWithPreparationGuard locks new journal creation against a caller's
// freshness updates. An allowed guard returns an unlock function, held only
// through local preparation and publication. Existing journals bypass it.
func (m *Manager) AdmitWithPreparationGuard(ctx context.Context, d sched.Due, guard func() (unlock func(), allowed bool)) (sched.AdmitResult, error) {
	if d.IssueRequest != nil {
		return m.admitIssueRequest(ctx, d)
	}
	if err := ctx.Err(); err != nil {
		return sched.NotAdmitted, err
	}
	key := ident.OccurrenceKey(d.Occurrence)
	uid := ident.RunUID(d.Occurrence, ident.ScheduledSource(d.Job.UID, key))
	if d.Child != nil {
		d.Occurrence = d.Child.Scheduled
		key = ident.OccurrenceKey(d.Occurrence)
		uid = ident.RunUID(d.Occurrence, d.Child.IssueUID)
	} else if d.Manual {
		uid = d.ManualRunUID
		key = "manual:" + uid
	}
	if !validUID(uid) || d.Occurrence.IsZero() {
		return sched.NotAdmitted, errors.New("invalid occurrence or run UID")
	}
	lock := m.admissionLock(uid)
	lock.Lock()
	defer lock.Unlock()
	for _, path := range []string{m.store.layout.StopFile(), m.store.layout.UpgradeFile()} {
		if _, err := m.store.fsys.Lstat(path); err == nil {
			return sched.NotAdmitted, nil
		} else if !missing(err) {
			return sched.NotAdmitted, err
		}
	}
	if m.guard == nil {
		return sched.NotAdmitted, errors.New("missing route checker")
	}
	if err := m.guard.Check(ctx); err != nil {
		return sched.NotAdmitted, err
	}
	if state.Quarantined(m.store.fsys, m.store.layout, uid) {
		return sched.Dropped, nil
	}
	target, err := m.pin()
	if err != nil {
		return sched.NotAdmitted, err
	}
	if m.cfg.Kata.Project == "" {
		return sched.NotAdmitted, errors.New("project name cannot be resolved; admission refused")
	}
	d.IssueRefs = maps.Clone(d.IssueRefs)
	if d.IssueRefs == nil {
		d.IssueRefs = map[string]string{}
	}
	r, err := m.store.Load(uid)
	if missing(err) {
		if blocked, e := m.store.SigningBlockedFor(m.projectUID); e != nil {
			return sched.NotAdmitted, e
		} else if blocked {
			return sched.NotAdmitted, errors.New("signing_blocked: repair root signing before new admission")
		}
		jobUID, forbid := d.Job.UID, d.Def.Overlap == "forbid"
		if d.Child != nil {
			jobUID, forbid = d.Child.DeliveryJobUID, false
		}
		if !m.reserve(uid, jobUID, forbid) {
			// Inbox admissions share capacity with scheduled dispatch. They
			// must also reconcile held slots when the pool is full.
			if m.FreeSlots() > 0 {
				return sched.NotAdmitted, nil
			}
			if err := m.ReconcileHeld(ctx); err != nil {
				return sched.NotAdmitted, err
			}
			if !m.reserve(uid, jobUID, forbid) {
				return sched.NotAdmitted, nil
			}
		}
		durable := false
		defer func() {
			if !durable {
				m.unreserve(uid)
			}
		}()
		if d.Child == nil && d.Def.Issue != nil && d.Def.Issue.Kind == "existing" && d.IssueRefs[d.Def.Issue.UID] == "" {
			issue, e := m.k.ShowIssue(ctx, d.Def.Issue.UID)
			if e != nil {
				return sched.NotAdmitted, e
			}
			if issue.UID != d.Def.Issue.UID || issue.ProjectUID != "" && issue.ProjectUID != target.Identity.ProjectUID {
				return sched.NotAdmitted, errors.New("issue differs from pinned project")
			}
			ref := kata.IssueReference(m.cfg.Kata.Project, issue)
			if ref == "" {
				return sched.NotAdmitted, errors.New("issue reference cannot be resolved; admission refused")
			}
			d.IssueRefs[issue.UID] = ref
		}
		// A hidden or genuine not-found permits preparation only; the observation fences execution.
		crashpoint.Hit("authority.before")
		_, err = m.k.ShowRun(ctx, uid)
		crashpoint.Hit("authority.after")
		if err == nil {
			return sched.Dropped, nil
		}
		var ke *kata.Error
		if !errors.As(err, &ke) || ke.Code != "not_found" {
			return sched.NotAdmitted, err
		}
		if d.Child == nil {
			result, err := m.runParentCheck(ctx, d, target.Identity.ProjectUID, d.IssueRefs)
			if result != sched.Admitted || err != nil {
				return result, err
			}
		}
		if d.Child != nil {
			// childInputs owns the child's workflow and fresh ownership inputs.
			d, err = m.childInputs(ctx, d, target.Identity.ProjectUID)
			if err != nil {
				return sched.NotAdmitted, err
			}
		}
		unlock := func() {}
		if guard != nil {
			var allowed bool
			unlock, allowed = guard()
			if !allowed {
				return sched.NotAdmitted, nil
			}
		}
		r, err = m.prepare(d, uid, key, target)
		if err != nil {
			unlock()
			return sched.NotAdmitted, err
		}
		err = m.store.Create(r)
		unlock()
		if err != nil {
			if _, loadErr := m.store.Load(uid); loadErr == nil {
				durable = true
			}
			return sched.NotAdmitted, err
		}
		durable = true
		if r.Manual && m.OnManualJournal != nil {
			m.OnManualJournal()
		}
	} else if err != nil {
		return sched.NotAdmitted, err
	} else {
		// Explicit installation reconciliation still cannot retarget an old journal.
		if !sameTarget(r.Target, target) {
			return sched.NotAdmitted, errors.New("run target differs from pinned installation")
		}
		if preAdmissionHold(r) {
			return sched.Dropped, nil
		}
		if r.Phase != Prepared {
			if r.Phase == Admitted {
				if err = m.Execute(ctx, uid); err != nil {
					return sched.NotAdmitted, err
				}
			}
			return sched.Admitted, nil
		}
		if !m.reserve(uid, r.Job.UID, false) {
			return sched.NotAdmitted, nil
		}
	}
	if err = m.admitPrepared(ctx, r); err != nil {
		return sched.NotAdmitted, err
	}
	if err = m.Execute(ctx, uid); err != nil {
		return sched.NotAdmitted, err
	}
	return sched.Admitted, nil
}
func (m *Manager) append(uid string, op Op) error {
	_, err := m.store.Transition(uid, func(r *Run) error {
		for _, existing := range r.Ops {
			if existing.ID == op.ID {
				return nil
			}
		}
		r.Ops = append(r.Ops, op)
		return nil
	})
	if err == nil {
		m.q.Kick(uid)
	}
	return err
}

// admitPrepared resolves frozen issue/admission intents without launching work.
// The caller holds the per-run admission lock.
func (m *Manager) admitPrepared(ctx context.Context, r *Run) error {
	uid := r.UID
	var err error
	if m.q == nil {
		return errors.New("write queue unavailable")
	}
	if r.Inspect != "" {
		return errors.New(r.Inspect)
	}
	if r.JobDef.Issue.Kind == "per-run" && r.IssueUID == "" {
		if err = m.q.wait(ctx, uid, "create"); err != nil {
			return err
		}
		// A force-new is a separate frozen operation, after the original conflict.
		r, err = m.store.Load(uid)
		if err != nil {
			return err
		}
		if _, forceErr := findOp(r, "create-force-new"); r.IssueUID == "" && forceErr == nil {
			if err = m.q.wait(ctx, uid, "create-force-new"); err != nil {
				return err
			}
			r, err = m.store.Load(uid)
			if err != nil {
				return err
			}
		}
	}
	if r.IssueUID == "" {
		_, err := m.store.Transition(uid, func(r *Run) error {
			r.Inspect = "create did not resolve an issue UID"
			return nil
		})
		return errors.Join(errors.New("create did not resolve an issue UID"), err)
	}
	body := AdmissionBody(r)
	op := newOp("admission", "observe", Payload{Observation: &body}, "")
	if r.IssueRequest != nil {
		op = requestAdmissionOp(r)
	}
	if err = m.append(uid, op); err != nil {
		return err
	}
	if err = m.q.wait(ctx, uid, op.ID); err != nil {
		return err
	}
	return m.admissionTail(ctx, uid)
}

func admitted(r *Run) bool {
	if r.Admitted != nil {
		return *r.Admitted
	}
	return r.Phase != Prepared
}
func preAdmissionOp(op Op) bool {
	return (op.ID == "create" || op.ID == "create-force-new") && op.Kind == "create" || op.ID == "admission" && (op.Kind == "observe" || op.Kind == "meta.set")
}

func eligible(r *Run, op *Op) bool {
	return op != nil && (admitted(r) || preAdmissionOp(*op)) && !preAdmissionHold(r)
}
func preAdmissionHold(r *Run) bool {
	return r.Phase == Held && (r.Reason == "admission_rejected" || r.Reason == "create_rejected")
}
func (m *Manager) admissionTail(ctx context.Context, uid string) error {
	for _, id := range []string{"child-claim", "child-tracked", "deadline"} {
		r, err := m.store.Load(uid)
		if err != nil {
			return err
		}
		if _, err = findOp(r, id); err != nil {
			continue
		}
		if err = m.q.wait(ctx, uid, id); err != nil {
			return err
		}
	}
	return nil
}

// Check only a fresh preparation. Recovery replays the saved create unchanged.
func (m *Manager) runParentCheck(ctx context.Context, d sched.Due, project string, refs map[string]string) (sched.AdmitResult, error) {
	parent := defs.RunParent(d.Def)
	if parent == "" {
		return sched.Admitted, nil
	}
	i, err := m.k.ShowIssue(ctx, parent)
	if err != nil {
		if kata.NotFound(err) {
			kata.ProbeAccess(ctx, m.guard)
			return sched.NotAdmitted, &sched.AdmissionDecision{Reason: "parent_unavailable", Cause: err}
		}
		return sched.NotAdmitted, err
	}
	if i.UID != parent {
		return sched.NotAdmitted, errors.New("parent_unavailable: parent UID differs")
	}
	if i.ProjectUID != project {
		return sched.Dropped, &sched.AdmissionDecision{Reason: "parent_outside_project"}
	}
	if i.Status == "closed" {
		return sched.Dropped, &sched.AdmissionDecision{Reason: "parent_closed"}
	}
	if i.Status != "open" {
		return sched.NotAdmitted, errors.New("parent_unavailable: unknown parent status")
	}
	if ref := kata.IssueReference(m.cfg.Kata.Project, i); ref != "" {
		refs[i.UID] = ref
	}
	return sched.Admitted, nil
}
