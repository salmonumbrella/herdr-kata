// Package sched owns occurrence checkpoints; callers own execution and event wiring.
package sched

import (
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type KataReader interface {
	ShowJob(context.Context, string) (kata.Job, error)
	ShowWorkflow(context.Context, string) (kata.Workflow, error)
	Ready(context.Context) ([]kata.IssueView, error)
	ListJobs(context.Context) ([]kata.Job, error)
	ListWorkflows(context.Context) ([]kata.Workflow, error)
}

var _ KataReader = (*kata.CLI)(nil)

type Due struct {
	IssueRefs    map[string]string
	Job          kata.Job
	Def          defs.JobDef
	Workflow     *kata.Workflow
	WFDef        *defs.WorkflowDef
	Occurrence   time.Time
	Manual       bool
	ManualRunUID string
	ManualInput  *string
	IssueRequest *IssueRequestDue
	Child        *ChildDue
}
type ExecuteRequest struct {
	Version     int       `json:"version"`
	Kind        string    `json:"kind"`
	RequestUID  string    `json:"request_uid"`
	RequestedAt time.Time `json:"requested_at"`
}
type IssueRequestDue struct {
	IssueUID, IssueRef string
	Request            ExecuteRequest
	ParentUID          string
	Binding            Binding
	Ownership          Ownership
	NotifyKey          string
	NotifyJSON         json.RawMessage
}
type ChildDue struct {
	IssueUID, IssueRef               string
	Scheduled                        time.Time
	DeliveryJobUID, DeliveryEventUID string
	Binding                          Binding
	NotifyKey                        string
	NotifyJSON                       json.RawMessage
	Ownership                        Ownership
}

// Binding and Ownership are shared with inbox without a package import cycle.
type Binding struct {
	WorkflowName                  string `json:",omitempty"`
	ParentRef                     string
	WorkflowUID, WorkflowEventUID string
	WFDef                         *defs.WorkflowDef
	Profile                       string
	Cwd                           string
}
type Ownership struct {
	Decision string // claimed|own-actor
	Revision int64
}
type AdmitResult int

const (
	NotAdmitted AdmitResult = iota
	Admitted
	Dropped
)

// AdmissionDecision names a skip or pending admission without losing its reason.
type AdmissionDecision struct {
	Reason string
	Cause  error
}

func (d *AdmissionDecision) Error() string {
	if d.Cause != nil {
		return d.Reason + ": " + d.Cause.Error()
	}
	return d.Reason
}
func (d *AdmissionDecision) Unwrap() error { return d.Cause }

type Admitter interface {
	// Admit may wait for durable admission writes, including queue retries.
	// Dispatch pauses during that wait; pending occurrences remain journaled.
	// Callers must propagate cancellation and remove a pending occurrence only
	// after an Admitted or Dropped result.
	Admit(context.Context, Due) (AdmitResult, error)
	GateHeld(string) bool
	FreeSlots() int
}
type LogAdmitter struct{}

func (LogAdmitter) Admit(_ context.Context, d Due) (AdmitResult, error) {
	slog.Info("pending preview", "job", d.Job.UID, "occurrence", d.Occurrence)
	return NotAdmitted, nil
}
func (LogAdmitter) GateHeld(string) bool { return false }
func (LogAdmitter) FreeSlots() int       { return 1 }

type Timer interface {
	C() <-chan time.Time
	Stop()
}
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}
type RealClock struct{}
type realTimer struct{ *time.Timer }

func (RealClock) Now() time.Time                 { return time.Now().UTC() }
func (RealClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }
func (t realTimer) C() <-chan time.Time          { return t.Timer.C }
func (t realTimer) Stop()                        { t.Timer.Stop() }

type entry struct {
	job      kata.Job
	def      defs.JobDef
	schedule Schedule
	cp       *Checkpoint
	next     time.Time
	catchup  bool
	outage   bool
}
type fires []*entry

func (h fires) Len() int { return len(h) }
func (h fires) Less(i, j int) bool {
	if h[i].next.Equal(h[j].next) {
		return h[i].job.UID < h[j].job.UID
	}
	return h[i].next.Before(h[j].next)
}
func (h fires) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *fires) Push(v any)   { *h = append(*h, v.(*entry)) }
func (h *fires) Pop() any     { old := *h; v := old[len(old)-1]; *h = old[:len(old)-1]; return v }

type manual struct {
	ref, uid string
	at       time.Time
	waiting  bool
	input    *string
}
type Scheduler struct {
	// OnManualComplete is configured before Run and must not block.
	// It wakes request handling after a terminal refusal is available.
	OnManualComplete func()
	// RecordDecision publishes admission reasons before a checkpoint drops work.
	RecordDecision func(job, reason string, dropped bool) error
	// Definitions, when set, supplies the listings to load instead of
	// listing jobs and workflows again.
	Definitions   func(context.Context) ([]kata.Job, []kata.Workflow, error)
	cfg           config.Config
	k             KataReader
	admitter      Admitter
	fsys          state.FS
	layout        state.Layout
	clock         Clock
	entries       map[string]*entry
	fires         fires
	poke, refresh chan struct{}
	refreshMu     sync.Mutex
	refreshDone   func()
	requests      chan manual
	manuals       []manual
	manualResults sync.Map
	waiting       map[string]bool
	recheck       bool
	slow          time.Time
	announced     map[string]string
}

func New(cfg config.Config, k KataReader, a Admitter, fsys state.FS, l state.Layout, clock Clock) *Scheduler {
	if clock == nil {
		clock = RealClock{}
	}
	return &Scheduler{cfg: cfg, k: k, admitter: a, fsys: fsys, layout: l, clock: clock, entries: map[string]*entry{}, announced: map[string]string{}, waiting: map[string]bool{}, recheck: true, slow: clock.Now().UTC().Add(10 * time.Minute), poke: make(chan struct{}, 1), refresh: make(chan struct{}, 1), requests: make(chan manual, 100)}
}

// Poke and RefreshDefs are safe during a snapshot: requests coalesce but survive it.
func (s *Scheduler) Poke() {
	select {
	case s.poke <- struct{}{}:
	default:
	}
}

// An optional acknowledgement runs after the scheduler applies this refresh.
// Coalescing retains the latest acknowledgement; an in-flight load cannot
// acknowledge a refresh requested after that load began.
func (s *Scheduler) RefreshDefs(after ...func()) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if len(after) > 0 {
		s.refreshDone = after[0]
	}
	select {
	case s.refresh <- struct{}{}:
	default:
	}
}

func (s *Scheduler) refreshDefinitions(ctx context.Context) error {
	s.refreshMu.Lock()
	done := s.refreshDone
	s.refreshDone = nil
	s.refreshMu.Unlock()
	if err := s.load(ctx); err != nil {
		// Run may retry after a remote failure. Retain its acknowledgement
		// unless a newer refresh has replaced it in the meantime.
		s.refreshMu.Lock()
		if s.refreshDone == nil {
			s.refreshDone = done
		}
		s.refreshMu.Unlock()
		return err
	}
	if done != nil {
		done()
	}
	return nil
}

// RunNow queues a bounded request; its UID remains stable while admission waits.
func (s *Scheduler) RunNow(jobRef, runUID string, input ...*string) error {
	var value *string
	if len(input) > 0 && input[0] != nil {
		v := *input[0]
		if e := defs.ValidateInput(v); e != nil {
			return e
		}
		value = &v
	}
	if jobRef == "" || !validUID(runUID) {
		return fmt.Errorf("invalid manual request")
	}
	select {
	case s.requests <- manual{ref: jobRef, uid: runUID, at: s.clock.Now().UTC(), input: value}:
		return nil
	default:
		return fmt.Errorf("manual queue full")
	}
}
func (s *Scheduler) drainManual() {
	for len(s.manuals) < 100 {
		select {
		case m := <-s.requests:
			s.manuals = append(s.manuals, m)
		default:
			return
		}
	}
}

// Run is the only scheduler writer. The worker supplies state_created_at and
// calls RefreshDefs on definition events and its 15-minute fallback refresh.
func (s *Scheduler) Run(ctx context.Context) error {
	if e := s.refreshDefinitions(ctx); e != nil {
		return e
	}
	s.slow = s.clock.Now().UTC().Add(10 * time.Minute)
	for {
		s.drainManual()
		if e := s.tick(ctx); e != nil {
			return e
		}
		now := s.clock.Now().UTC()
		delay := min(s.slow.Sub(now), 10*time.Minute)
		reconcile := true
		if len(s.fires) > 0 {
			if fireDelay := s.fires[0].next.Sub(now); fireDelay < delay {
				delay = fireDelay
				reconcile = false
			}
		}
		if delay < 0 {
			delay = 0
		}
		timer := s.clock.NewTimer(delay)
		requests := s.requests
		if len(s.manuals) >= 100 {
			requests = nil
		}
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-s.refresh:
			timer.Stop()
			if e := s.refreshDefinitions(ctx); e != nil {
				return e
			}
		case <-s.poke:
			timer.Stop()
			s.recheck = true
		case m := <-requests:
			timer.Stop()
			s.manuals = append(s.manuals, m)
		case <-timer.C():
			timer.Stop()
			if reconcile {
				s.recheck = true
				s.slow = s.clock.Now().UTC().Add(10 * time.Minute)
			}
		}
	}
}
func (s *Scheduler) save(c *Checkpoint) error { return c.Save(s.fsys, s.layout) }
func dropLog(uid, reason string, keys []string) {
	if len(keys) > 0 {
		slog.Info("occurrences skipped", "job", uid, "reason", reason, "from", keys[0], "to", keys[len(keys)-1], "count", len(keys))
	}
}

// The route guard verifies the configured lane actor against the authenticated
// pin. Provenance gates only automatic adapter work, never manual or child runs.
func automaticJobAllowed(job kata.Job, actor string) bool {
	return actor != "" && job.Author == actor
}

func (s *Scheduler) definitions(ctx context.Context) ([]kata.Job, []kata.Workflow, error) {
	if s.Definitions != nil {
		return s.Definitions(ctx)
	}
	jobs, e := s.k.ListJobs(ctx)
	if e != nil {
		return nil, nil, e
	}
	workflows, e := s.k.ListWorkflows(ctx)
	return jobs, workflows, e
}

func (s *Scheduler) load(ctx context.Context) error {
	// Decode installation state through a raw map so later units can add fields.
	var raw map[string]json.RawMessage
	if e := state.ReadJSON(s.fsys, s.layout.StateFile(), 64*1024, &raw); e != nil {
		return e
	}
	var created time.Time
	if e := json.Unmarshal(raw["state_created_at"], &created); e != nil || created.IsZero() {
		return fmt.Errorf("state_created_at required")
	}
	jobs, workflows, e := s.definitions(ctx)
	if e != nil {
		return e
	}
	wf := map[string]*defs.WorkflowDef{}
	for _, w := range workflows {
		if w.DeletedAt == nil {
			if d, e := defs.DecodeWorkflow(w.Definition); e == nil {
				wf[w.UID] = &d
			}
		}
	}
	entries := map[string]*entry{}
	seen := map[string]bool{}
	now := s.clock.Now().UTC()
	for _, j := range jobs {
		if !validUID(j.UID) {
			slog.Warn("invalid job UID")
			continue
		}
		seen[j.UID] = true
		announce := s.announced[j.UID] != j.DefinitionEventUID
		s.announced[j.UID] = j.DefinitionEventUID
		d, e := defs.DecodeJob(j.Definition)
		if e != nil {
			if old := s.entries[j.UID]; old != nil {
				if e := s.drop(old.cp, "unsupported"); e != nil {
					return e
				}
			}
			continue
		}
		first := created
		if j.CreatedAt.After(first) {
			first = j.CreatedAt
		}
		c, e := LoadCheckpoint(s.fsys, s.layout, j.UID, first)
		if e != nil {
			return e
		}
		dirty := c.DefinitionEventUID == "" || c.DefinitionEventUID != j.DefinitionEventUID
		if c.DefinitionEventUID == "" {
			c.DefinitionEventUID = j.DefinitionEventUID
		} else {
			dropLog(j.UID, "superseded", c.Supersede(j.DefinitionEventUID, now))
		}
		owner, support := defs.Classify(d, wf[d.Action.WorkflowUID], s.cfg)
		if announce && owner != defs.Ignored {
			for _, warning := range support.Warnings {
				slog.Warn(warning, "job", j.UID)
			}
		}
		if !defs.Dispatchable(owner, support, j.DeletedAt) {
			dirty = dirty || len(c.Pending) > 0
			dropLog(j.UID, "disabled, native or unsupported", c.Pending)
			c.Pending = nil
			if dirty {
				if e := s.save(c); e != nil {
					return e
				}
			}
			continue
		}
		if !automaticJobAllowed(j, s.cfg.Kata.Actor) {
			dirty = dirty || len(c.Pending) > 0
			dropLog(j.UID, "original author does not match lane actor", c.Pending)
			c.Pending = nil
			if dirty {
				if e := s.save(c); e != nil {
					return e
				}
			}
			continue
		}
		schedule, e := Compile(d.Trigger, j.CreatedAt)
		if e != nil {
			return e
		}
		next := schedule.Next(c.HandledThrough)
		en := &entry{job: j, def: d, schedule: schedule, cp: c, outage: len(c.Pending) > 0, catchup: !next.IsZero() && !next.After(now)}
		if old := s.entries[j.UID]; old != nil && !dirty {
			en.catchup = old.catchup
			en.outage = old.outage
		}
		entries[j.UID] = en
		if dirty {
			if e := s.save(c); e != nil {
				return e
			}
		}
	}
	for uid, old := range s.entries {
		if !seen[uid] {
			if e := s.drop(old.cp, "deleted or unsupported"); e != nil {
				return e
			}
		}
	}
	s.entries = entries
	s.waiting = map[string]bool{}
	s.recheck = true
	s.reheap()
	return nil
}
func (s *Scheduler) reheap() {
	s.fires = nil
	for _, e := range s.entries {
		e.next = e.schedule.Next(e.cp.HandledThrough)
		if !e.next.IsZero() {
			heap.Push(&s.fires, e)
		}
	}
}
func (s *Scheduler) drop(c *Checkpoint, reason string) error {
	if len(c.Pending) == 0 {
		return nil
	}
	dropLog(c.JobUID, reason, c.Pending)
	c.Pending = nil
	return s.save(c)
}
func (s *Scheduler) tick(ctx context.Context) error {
	now := s.clock.Now().UTC()
	select {
	case <-s.poke:
		s.recheck = true
	default:
	}
	recheck := s.recheck || !now.Before(s.slow)
	s.recheck = false
	if !now.Before(s.slow) {
		s.slow = now.Add(10 * time.Minute)
	}
	for len(s.fires) > 0 && !s.fires[0].next.After(now) {
		e := heap.Pop(&s.fires).(*entry)
		policy := e.def
		if !e.catchup && !e.outage && now.Sub(e.next) < 2*time.Minute {
			policy.Catchup = "all"
		}
		e.catchup = false
		q, through, d := Plan(*e.cp, e.schedule, policy, now)
		e.cp.Pending = q
		e.cp.HandledThrough = through
		if d.Dropped {
			slog.Info("occurrences skipped", "job", e.job.UID, "reason", "policy", "ranges", d.Ranges)
		}
		if err := s.save(e.cp); err != nil {
			return err
		}
		e.next = e.schedule.Next(through)
		if !e.next.IsZero() {
			heap.Push(&s.fires, e)
		}
	}
	var ready map[string]bool
	readyRefs := map[string]string{}
	// A full dispatch batch gives stopped held occupants a chance to return
	// capacity. Preview admitters need no Herdr reconciliation.
	if s.admitter.FreeSlots() <= 0 {
		pending := len(s.manuals) > 0
		for _, en := range s.entries {
			pending = pending || len(en.cp.Pending) > 0
		}
		if r, ok := s.admitter.(interface{ ReconcileHeld(context.Context) error }); ok && pending {
			if err := r.ReconcileHeld(ctx); err != nil {
				return err
			}
		}
	}
	var readyErr error
	readReady := false
	var decisionReason string
	admit := func(d Due) (AdmitResult, error) {
		decisionReason = ""
		if d.Def.Overlap == "forbid" && s.admitter.GateHeld(d.Job.UID) {
			return NotAdmitted, nil
		}
		if d.Def.Action.Kind == "execute" && d.Def.Issue.Kind == "existing" {
			if !readReady {
				readReady = true
				var issues []kata.IssueView
				issues, readyErr = s.k.Ready(ctx)
				ready = map[string]bool{}
				for _, i := range issues {
					if i.Status == "open" {
						ready[i.UID] = true
						if ref := kata.IssueReference(s.cfg.Kata.Project, i); ref != "" {
							readyRefs[i.UID] = ref
						}
					}
				}
			}
			if readyErr != nil {
				return NotAdmitted, readyErr
			}
			if !ready[d.Def.Issue.UID] {
				s.waiting[d.Job.UID] = true
				return NotAdmitted, nil
			}
		}
		d.IssueRefs = nil
		if d.Def.Issue != nil {
			if ref := readyRefs[d.Def.Issue.UID]; ref != "" {
				d.IssueRefs = map[string]string{d.Def.Issue.UID: ref}
			}
		}
		delete(s.waiting, d.Job.UID)
		result, err := s.admitter.Admit(ctx, d)
		if decision, ok := errors.AsType[*AdmissionDecision](err); ok {
			decisionReason = decision.Reason
			if s.RecordDecision != nil {
				if saveErr := s.RecordDecision(d.Job.UID, decision.Reason, result == Dropped); saveErr != nil {
					return NotAdmitted, saveErr
				}
			}
			if result == Dropped {
				dropLog(d.Job.UID, decision.Reason, []string{d.Occurrence.UTC().Format(time.RFC3339Nano)})
				err = nil
			}
		} else if result == Admitted && err == nil && s.RecordDecision != nil {
			err = s.RecordDecision(d.Job.UID, "", false)
		}
		return result, err
	}
	keys := make([]string, 0, len(s.entries))
	for uid := range s.entries {
		keys = append(keys, uid)
	}
	sort.Strings(keys)
	for _, uid := range keys {
		en := s.entries[uid]
		if len(en.cp.Pending) == 0 || s.admitter.FreeSlots() <= 0 || (!recheck && (s.waiting[uid] || (en.def.Overlap == "forbid" && s.admitter.GateHeld(uid)))) {
			continue
		}
		for len(en.cp.Pending) > 0 && s.admitter.FreeSlots() > 0 {
			due, owner, support, e := s.current(ctx, uid)
			if e != nil {
				if errors.Is(e, errDeleted) {
					if e := s.drop(en.cp, "deleted"); e != nil {
						return e
					}
					delete(s.entries, uid)
					s.reheap()
				} else {
					en.outage = true
					slog.Warn("revalidation failed", "job", uid, "error", e)
				}
				break
			}
			if !automaticJobAllowed(due.Job, s.cfg.Kata.Actor) {
				if e := s.drop(en.cp, "original author does not match lane actor"); e != nil {
					return e
				}
				delete(s.entries, uid)
				s.reheap()
				break
			}
			if due.Job.DefinitionEventUID != en.cp.DefinitionEventUID {
				dropLog(uid, "superseded", en.cp.Supersede(due.Job.DefinitionEventUID, now))
				if e := s.save(en.cp); e != nil {
					return e
				}
				delete(s.entries, uid)
				s.reheap()
				s.RefreshDefs()
				break
			}
			if !defs.Dispatchable(owner, support, due.Job.DeletedAt) {
				if e := s.drop(en.cp, "disabled or unsupported"); e != nil {
					return e
				}
				delete(s.entries, uid)
				s.reheap()
				break
			}
			if en.outage {
				q, through, dropped := Plan(*en.cp, en.schedule, en.def, now)
				en.cp.Pending, en.cp.HandledThrough = q, through
				if dropped.Dropped {
					slog.Info("occurrences skipped", "job", uid, "reason", "outage catchup", "ranges", dropped.Ranges)
				}
				if e := s.save(en.cp); e != nil {
					return e
				}
				en.outage = false
				if len(q) == 0 {
					break
				}
			}
			due.Occurrence, _ = time.Parse(time.RFC3339Nano, en.cp.Pending[0])
			result, e := admit(due)
			if errors.Is(e, defs.ErrInput) {
				dropLog(uid, e.Error(), en.cp.Pending[:1])
				result = Dropped
				e = nil
			}
			if e != nil {
				en.outage = true
				slog.Warn("admission failed", "job", uid, "error", e)
				break
			}
			if result == NotAdmitted {
				break
			}
			if result != Admitted && result != Dropped {
				return fmt.Errorf("invalid admission result")
			}
			if result == Dropped && decisionReason == "" {
				dropLog(uid, "already_observed_or_quarantined", en.cp.Pending[:1])
			}
			en.cp.Pending = en.cp.Pending[1:]
			if e := s.save(en.cp); e != nil {
				return e
			}
		}
	}
	remaining := s.manuals[:0]
	for _, m := range s.manuals {
		if m.waiting && !recheck {
			remaining = append(remaining, m)
			continue
		}
		due, owner, support, e := s.current(ctx, m.ref)
		if e != nil {
			if errors.Is(e, errDeleted) {
				slog.Warn("manual job deleted", "job", m.ref)
				s.completeManual(m.uid, m.ref, fmt.Errorf("manual job deleted"))
			} else {
				remaining = append(remaining, m)
			}
			continue
		}
		if !defs.Dispatchable(owner, support, due.Job.DeletedAt) {
			slog.Warn("manual request unsupported", "job", m.ref)
			reasons := append([]string(nil), support.Reasons...)
			switch {
			case due.Job.DeletedAt != nil:
				reasons = append(reasons, "job is deleted")
			case due.Def.Kind == "job" && !due.Def.Enabled:
				reasons = append(reasons, "job is disabled")
			case owner == defs.Native:
				reasons = append(reasons, "job is owned by Kata's native scheduler")
			}
			s.completeManual(m.uid, m.ref, fmt.Errorf("%s", strings.Join(reasons, "; ")))
			continue
		}
		if s.admitter.FreeSlots() <= 0 {
			remaining = append(remaining, m)
			continue
		}
		due.Manual = true
		due.ManualRunUID = m.uid
		due.ManualInput = m.input
		due.Occurrence = m.at
		result, e := admit(due)
		if errors.Is(e, defs.ErrInput) {
			s.completeManual(m.uid, m.ref, e)
			continue
		}
		if e != nil || result == NotAdmitted {
			m.waiting = true
			remaining = append(remaining, m)
		}
		if e == nil && result == Dropped {
			if decisionReason != "" {
				s.completeManual(m.uid, m.ref, fmt.Errorf("manual run skipped: %s", decisionReason))
			} else {
				s.completeManual(m.uid, m.ref, fmt.Errorf("manual run already observed or quarantined; local state unavailable"))
			}
		}
	}
	s.manuals = remaining
	return nil
}

var errDeleted = errors.New("definition deleted")

func notFound(err error) bool { return kata.NotFound(err) }
func (s *Scheduler) current(ctx context.Context, ref string) (Due, defs.Owner, defs.Support, error) {
	j, e := s.k.ShowJob(ctx, ref)
	if e != nil {
		if notFound(e) {
			rows, proofErr := s.k.ListJobs(ctx)
			if proofErr == nil {
				found := false
				for _, row := range rows {
					found = found || row.UID == ref || row.Name == ref
				}
				if !found {
					return Due{}, defs.Ignored, defs.Support{}, errDeleted
				}
			}
			kata.ProbeAccess(ctx, s.k)
		}
		return Due{}, defs.Ignored, defs.Support{}, e
	}
	d, e := defs.DecodeJob(j.Definition)
	if e != nil {
		return Due{Job: j}, defs.Ignored, defs.Support{Reasons: []string{"decode job: " + e.Error()}}, nil
	}
	due := Due{Job: j, Def: d}
	if d.Action.WorkflowUID != "" {
		w, e := s.k.ShowWorkflow(ctx, d.Action.WorkflowUID)
		if e != nil {
			absent := false
			if notFound(e) {
				rows, proofErr := s.k.ListWorkflows(ctx)
				if proofErr == nil {
					absent = true
					for _, row := range rows {
						if row.UID == d.Action.WorkflowUID {
							absent = false
						}
					}
				}
				if !absent {
					kata.ProbeAccess(ctx, s.k)
				}
			}
			if !absent {
				return due, defs.Ignored, defs.Support{}, e
			}
		}
		if e == nil && w.DeletedAt == nil {
			wd, e := defs.DecodeWorkflow(w.Definition)
			if e == nil {
				due.Workflow = &w
				due.WFDef = &wd
			}
		}
	}
	owner, support := defs.Classify(d, due.WFDef, s.cfg)
	return due, owner, support, nil
}

// Store before waking the request handler: it may read the result immediately.
func (s *Scheduler) completeManual(uid, job string, err error) {
	s.manualResults.Store(uid, fmt.Errorf("manual run refused for project %q job %q: %w; inspect with herdr-kata status --json", s.cfg.Kata.Project, job, err))
	if s.OnManualComplete != nil {
		s.OnManualComplete()
	}
}

// ManualResult exposes terminal refusals without losing the durable request.
// ForgetManual is called only after the worker has saved the response.
func (s *Scheduler) ManualResult(uid string) (bool, error) {
	v, ok := s.manualResults.Load(uid)
	if !ok {
		return false, nil
	}
	return true, v.(error)
}
func (s *Scheduler) ForgetManual(uid string) { s.manualResults.Delete(uid) }
