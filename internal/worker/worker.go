// Package worker coordinates durable local work behind fresh project reads.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/salmonumbrella/herdr-kata/internal/attempt"
	"github.com/salmonumbrella/herdr-kata/internal/clock"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/flow"
	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/prompt"
	"github.com/salmonumbrella/herdr-kata/internal/report"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/session"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type Worker struct {
	caps         map[string]capsEntry
	Config       config.Config
	Store        *journal.Store
	HC           hc.HC
	Clients      map[string]kata.Kata
	Bin          string
	Unsupported  map[string][]string
	installed    time.Time
	StartedAt    time.Time
	SeenRequests map[string]bool
}

func New(c config.Config, st *journal.Store, h hc.HC, clients map[string]kata.Kata, bin string) *Worker {
	return &Worker{Config: c, Store: st, HC: h, Clients: clients, Bin: bin, Unsupported: make(map[string][]string), SeenRequests: make(map[string]bool), caps: make(map[string]capsEntry)}
}
func (w *Worker) init(now time.Time) error {
	if !w.installed.IsZero() {
		return nil
	}
	path := filepath.Join(w.Store.Layout.Root, "installation.json")
	var v struct {
		Version int
		At      time.Time
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		runs, e := w.Store.All()
		if e != nil {
			return e
		}
		if len(runs) > 0 {
			return errors.New("installation state missing with existing run journals")
		}
		v.Version = 1
		v.At = now.UTC()
		if err = state.WriteJSON(path, v); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if err = json.Unmarshal(b, &v); err != nil {
		return err
	}
	if v.Version != 1 || v.At.IsZero() {
		return errors.New("invalid installation state")
	}
	w.installed = v.At
	return nil
}
func (w *Worker) capacity(project config.Project, jobUID, overlap string) (bool, error) {
	can, _, err := w.capacityState(project, jobUID, overlap)
	return can, err
}
func (w *Worker) capacityState(project config.Project, jobUID, overlap string) (bool, bool, error) {
	runs, err := w.Store.Active()
	if err != nil {
		return false, false, err
	}
	total, local := 0, 0
	for _, r := range runs {
		if r.Phase == "settled" {
			continue
		}
		total++
		if r.Project == project.Name {
			local++
			if overlap == "forbid" && r.JobUID == jobUID {
				return false, true, nil
			}
		}
	}
	limit := project.MaxActiveRuns
	if limit == 0 {
		limit = 1
	}
	return total < w.Config.Limits.MaxActiveRuns && local < limit, false, nil
}
func (w *Worker) prepare(ctx context.Context, k kata.Kata, project config.Project, p defs.Plan, key, occurrence, requestID string, now time.Time, deadline ...time.Time) error {
	title := strings.TrimSpace(p.Issue.Title + " " + occurrence)
	if p.Issue.Kind == "existing" {
		var err error
		title, err = k.IssueTitle(p.Issue.UID)
		if err != nil {
			return err
		}
	}
	if len(deadline) > 0 && !clock.Current(ctx, now).Before(deadline[0]) {
		return nil
	}
	r := journal.Run{Version: 1, Key: key, RunUID: ulid.Make().String(), Project: project.Name, Actor: project.Actor, Executor: w.Config.Executor, JobUID: p.JobUID, Occurrence: occurrence, RequestID: requestID, Phase: "prepared", Plan: p, IssueTitle: title, StartedAt: new(now.UTC()), ExpiresAt: now.Add(p.Timeout).UTC()}
	if p.Issue.Kind == "existing" {
		r.IssueUID = p.Issue.UID
		r.IssueRef = p.Issue.UID
	}
	err := w.Store.Create(r)
	if errors.Is(err, journal.ErrExists) {
		return nil
	}
	return err
}

func (w *Worker) acknowledgePreparation(q journal.Requests, req journal.Request, key string, prepareErr error) (bool, error) {
	if prepareErr != nil {
		if !w.Store.Exists(key) {
			return false, prepareErr
		}
		return true, errors.Join(prepareErr, w.acknowledgeExisting(q, req, key))
	}
	return true, q.Done(req.ID)
}

func (w *Worker) acknowledgeExisting(q journal.Requests, req journal.Request, key string) error {
	if _, err := w.Store.Load(key); err != nil {
		return errors.Join(err, q.Reject(req.ID, "existing run journal is unreadable"))
	}
	return q.Done(req.ID)
}
func (w *Worker) checkpoint(project config.Project, job kata.CronJob, p defs.Plan, occupied bool, now time.Time) (sched.Checkpoint, sched.Dropped, error) {
	var dropped sched.Dropped
	c := sched.Checkpoint{Version: 1, JobUID: job.UID, Through: w.installed}
	if job.CreatedAt.After(c.Through) {
		c.Through = job.CreatedAt
	}
	b, err := os.ReadFile(w.Store.Layout.Job(job.UID))
	if err == nil {
		if err = json.Unmarshal(b, &c); err != nil {
			return c, dropped, err
		}
		if c.Version != 1 || c.JobUID != job.UID {
			return c, dropped, errors.New("invalid checkpoint identity or version")
		}
	} else if !os.IsNotExist(err) {
		return c, dropped, err
	}
	records, err := (report.Outbox{Layout: w.Store.Layout, Dir: w.Store.Layout.Dropped()}).All()
	if err != nil {
		return c, dropped, err
	}
	for _, r := range records {
		saved := r.Checkpoint
		if r.Project != project.Name || saved.JobUID != job.UID || saved.Through.Before(c.Through) {
			continue
		}
		if saved.Through.After(c.Through) {
			if saved.EventUID == job.DefinitionEventUID || saved.EventUID == c.EventUID || c.EventUID == "" {
				c = *saved
			} else {
				c.Through = saved.Through
			}
		} else if saved.EventUID == c.EventUID {
			c.Pending = slices.DeleteFunc(c.Pending, func(o sched.Occurrence) bool {
				return !slices.ContainsFunc(saved.Pending, func(keep sched.Occurrence) bool { return keep.Key == o.Key })
			})
		}
	}
	c.Pending = slices.DeleteFunc(c.Pending, func(o sched.Occurrence) bool {
		return w.Store.Exists(journal.RunKey(w.Config.Executor, project.Name, job.UID, o.Key))
	})
	if c.EventUID != "" && c.EventUID != job.DefinitionEventUID {
		c.Pending = nil
	}
	policy := p.Catchup
	cutoff := c.Through
	if w.StartedAt.After(cutoff) {
		cutoff = w.StartedAt
	}
	cutoff = cutoff.Add(-p.Grace)
	if policy == "skip" {
		policy = "latest"
	}
	due, dropped := sched.Occurrences(job.Definition.Trigger, job.CreatedAt, c.Through, now, policy, p.Grace)
	dropped.Add(c.Merge(job.DefinitionEventUID, due, now))
	if p.Catchup != "all" && len(c.Pending) > 1 {
		for _, o := range c.Pending[:len(c.Pending)-1] {
			dropped.Include(o.At)
		}
		c.Pending = c.Pending[len(c.Pending)-1:]
	}
	if p.Catchup == "skip" && len(c.Pending) > 0 && (!c.Pending[0].At.After(cutoff) || now.Sub(c.Pending[0].At) > p.Grace+2*time.Minute) {
		dropped.Include(c.Pending[0].At)
		c.Pending = nil
	}
	if occupied && p.Grace > 0 {
		c.Pending = slices.DeleteFunc(c.Pending, func(o sched.Occurrence) bool {
			if now.Sub(o.At) <= p.Grace {
				return false
			}
			dropped.Include(o.At)
			return true
		})
	}
	return c, dropped, nil
}
func (w *Worker) commitCheckpoint(project config.Project, p defs.Plan, c sched.Checkpoint, dropped sched.Dropped, occupied bool, now time.Time) error {
	if dropped.Count > 0 {
		code, detail := "catchup_missed", fmt.Sprintf("catchup %s, grace %ds", p.Catchup, int64(p.Grace/time.Second))
		if occupied {
			code, detail = "overlap_skipped", fmt.Sprintf("overlap forbid, grace %ds", int64(p.Grace/time.Second))
		}
		detail = fmt.Sprintf("%d occurrences %s..%s not launched (%s)", dropped.Count, dropped.First.UTC().Format(time.RFC3339Nano), dropped.Last.UTC().Format(time.RFC3339Nano), detail)
		if dropped.Clipped {
			detail += "; earlier occurrences beyond the 7-day scan not counted"
		}
		if err := (report.Outbox{Layout: w.Store.Layout, Dir: w.Store.Layout.Dropped()}).Drop(p, project.Name, project.Actor, w.Config.Executor, code, detail, dropped.First.UTC().Format(time.RFC3339Nano), c, now); err != nil {
			return err
		}
	}
	return state.WriteJSON(w.Store.Layout.Job(c.JobUID), c)
}
func (w *Worker) release(q journal.Requests, req journal.Request) error {
	r, err := w.Store.Find(req.Run)
	if err != nil {
		return w.releaseLookupFailure(q, req, err)
	}
	if r.Phase == "released" {
		return q.Done(req.ID)
	}
	if r.Phase != "held" {
		return errors.New("release requires a held run")
	}
	if !r.ReportDone && !r.ReportRefused {
		return nil
	}
	if err = w.Store.Update(r.Key, func(r *journal.Run) error { r.Phase = "released"; return nil }); err != nil {
		return err
	}
	return q.Done(req.ID)
}

func (w *Worker) releaseLookupFailure(q journal.Requests, req journal.Request, err error) error {
	if temporary, ok := errors.AsType[interface {
		error
		Temporary() bool
	}](err); ok && temporary.Temporary() {
		return err
	}
	return errors.Join(err, q.Reject(req.ID, err.Error()))
}
func (w *Worker) Tick(ctx context.Context, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx = clock.Progress(ctx, now)
	if err := w.init(now); err != nil {
		return err
	}
	if w.StartedAt.IsZero() {
		w.StartedAt = now.UTC()
	}
	var errs []error
	q := journal.Requests{Dir: w.Store.Layout.Requests()}
	requests, err := q.Pending()
	for _, req := range requests {
		w.SeenRequests[req.ID] = true
	}
	if err != nil {
		errs = append(errs, err)
	}
	blocked := make(map[string]bool)
	protected := func(r journal.Run) bool {
		return blocked[r.Project] || blocked[r.Project+":"+r.JobUID]
	}
	consumed := make(map[string]bool)
	matched := make(map[string]string)
	allRead := true
	reasons := make(map[string]string)
	permanent := make(map[string]string)
	for _, req := range requests {
		reasons[req.ID] = "waiting for project definitions"
	}
	setReason := func(destination map[string]string, project, job, reason string) {
		for _, req := range requests {
			if req.Kind == "run" && (req.Project == "" || req.Project == project) && (job == "" || req.JobUID == job) && (matched[req.ID] == "" || matched[req.ID] == project) {
				destination[req.ID] = reason
			}
		}
	}
	rejectReason := func(project, job, reason string) { setReason(permanent, project, job, reason) }
	pendingReason := func(project, job, reason string) { setReason(reasons, project, job, reason) }
	for _, req := range requests {
		if req.Kind != "run" {
			continue
		}
		for _, project := range w.Config.Projects {
			if req.Project != "" && req.Project != project.Name {
				continue
			}
			key := journal.ManualKey(w.Config.Executor, project.Name, req.ID)
			if !w.Store.Exists(key) {
				continue
			}
			consumed[req.ID] = true
			if e := w.acknowledgeExisting(q, req, key); e != nil {
				errs = append(errs, e)
			}
			break
		}
	}
	admission := make(map[string]bool)
	w.Unsupported = make(map[string][]string)
	for _, project := range w.Config.Projects {
		k := w.Clients[project.Name]
		if k == nil {
			allRead = false
			blocked[project.Name] = true
			pendingReason(project.Name, "", "project client is unavailable")
			errs = append(errs, fmt.Errorf("client missing for %s", project.Name))
			continue
		}
		caps, known, capErr := w.capsFor(project.Name, clock.Current(ctx, now))
		if capErr != nil {
			errs = append(errs, capErr)
		}
		if !known {
			allRead = false
			pendingReason(project.Name, "", "project capabilities are unavailable")
			continue
		}
		remote, e := w.controls(ctx, k, project.Name, caps, clock.Current(ctx, now))
		if e != nil {
			errs = append(errs, e)
		}
		list, e := k.Jobs()
		if e != nil {
			allRead = false
			blocked[project.Name] = true
			pendingReason(project.Name, "", "project definitions are unavailable")
			errs = append(errs, fmt.Errorf("%s definitions: %w", project.Name, e))
			continue
		}
		jobs := list.Jobs
		for _, job := range jobs {
			for _, req := range requests {
				if req.Kind == "run" && req.JobUID == job.UID && (req.Project == "" || req.Project == project.Name) {
					matched[req.ID] = project.Name
				}
			}
		}
		if list.Paused() {
			allRead = false
			pendingReason(project.Name, "", "project cron is paused")
			continue
		}
		admission[project.Name] = true
		ownedJobs := make([]kata.CronJob, 0, len(jobs))
		for _, job := range jobs {
			owned, reason := defs.Owned(job, project.Name, w.Config)
			if owned {
				reason = caps.Excludes(job.Author)
				owned = reason == ""
			}
			if !owned {
				rejectReason(project.Name, job.UID, reason)
			} else {
				ownedJobs = append(ownedJobs, job)
			}
		}
		jobs = ownedJobs
		protectJob := func(job kata.CronJob) {
			d := job.Definition
			blocked[project.Name+":"+job.UID] = d.Overlap == "forbid" && d.GraceSeconds != nil && *d.GraceSeconds > 0 && d.Trigger.Kind != "manual" && d.Trigger.Kind != "issue-scheduled"
		}
		protectJobs := func() {
			for _, job := range jobs {
				protectJob(job)
			}
		}
		workflows, e := k.Workflows()
		if e != nil {
			allRead = false
			protectJobs()
			pendingReason(project.Name, "", "project workflows are unavailable")
			errs = append(errs, e)
			continue
		}
		var ready map[string]bool
		if defs.NeedsReadiness(jobs) {
			if ready, e = k.Ready(); e != nil {
				allRead = false
				protectJobs()
				pendingReason(project.Name, "", "project readiness is unavailable")
				errs = append(errs, e)
				continue
			}
		}
		wfs := make(map[string]kata.CronWorkflow)
		for _, wf := range workflows {
			wfs[wf.UID] = wf
		}
		for _, job := range jobs {
			p, reasons := defs.Freeze(job, wfs, w.Config, project.Name, exec.LookPath)
			prepare := func(plan defs.Plan, key, occurrence, requestID, remoteUID string, now time.Time, deadline ...time.Time) error {
				return w.prepareInputs(ctx, k, project, job, wfs, plan, key, occurrence, requestID, remoteUID, now, deadline...)
			}
			if len(reasons) > 0 {
				protectJob(job)
				w.Unsupported[job.UID] = reasons
				if e := (report.Outbox{Layout: w.Store.Layout}).Discover(p, project.Name, project.Actor, w.Config.Executor, reasons, now); e != nil {
					errs = append(errs, e)
				}
				rejectReason(project.Name, job.UID, "unsupported job: "+strings.Join(reasons, ", "))
				continue
			}
			if p.Issue.Kind == "existing" && !ready[p.Issue.UID] {
				protectJob(job)
				rejectReason(project.Name, job.UID, "existing issue is not ready")
				continue
			}
			for _, req := range remote {
				if req.Kind != "run" || !slices.Contains(caps.Controls, "run_request") || req.JobUID != job.UID || req.DefinitionEventUID != job.DefinitionEventUID || !clock.Current(ctx, now).Before(req.ExpiresAt) {
					continue
				}
				key := journal.RunKey(w.Config.Executor, project.Name, job.UID, "request:"+req.UID)
				if w.Store.Exists(key) {
					continue
				}
				can, err := w.capacity(project, job.UID, p.Overlap)
				if err != nil {
					errs = append(errs, err)
					continue
				}
				if can {
					remoteUID := ""
					if len(req.Inputs) > 0 {
						remoteUID = req.UID
					}
					if err = prepare(p, key, "request:"+req.UID, "", remoteUID, now, req.ExpiresAt); err != nil {
						errs = append(errs, err)
					}
				}
			}

			for _, req := range requests {
				if consumed[req.ID] || req.Kind != "run" || req.JobUID != job.UID || req.Project != "" && req.Project != project.Name {
					continue
				}
				key := journal.ManualKey(w.Config.Executor, project.Name, req.ID)
				if w.Store.Exists(key) {
					consumed[req.ID] = true
					if e = w.acknowledgeExisting(q, req, key); e != nil && !os.IsNotExist(e) {
						errs = append(errs, e)
					}
					continue
				}
				can, e := w.capacity(project, job.UID, p.Overlap)
				if e != nil {
					pendingReason(project.Name, job.UID, "run capacity could not be checked")
					errs = append(errs, e)
					continue
				}
				if !can {
					pendingReason(project.Name, job.UID, "run capacity or overlap gate is unavailable")
					continue
				}
				manual := p
				if req.InputSet || req.Input != "" {
					manual.Input = req.Input
				}
				prepared, e := w.acknowledgePreparation(q, req, key, prepare(manual, key, "manual:"+req.ID, req.ID, "", now))
				if e != nil {
					pendingReason(project.Name, job.UID, "could not prepare run: "+e.Error())
					errs = append(errs, e)
				}
				if !prepared {
					continue
				}
				consumed[req.ID] = true
			}
			trig := job.Definition.Trigger
			if trig.Kind == "manual" {
				continue
			}
			if trig.Kind == "issue-scheduled" {
				if trig.IssueUID == nil {
					continue
				}
				date, e := k.PlanningDates(*trig.IssueUID)
				if e != nil {
					errs = append(errs, e)
					continue
				}
				o := sched.DateOccurrence(*trig.IssueUID, date)
				if o == nil || o.At.After(now) || o.At.Before(job.CreatedAt) || !ready[*trig.IssueUID] {
					continue
				}
				key := journal.RunKey(w.Config.Executor, project.Name, job.UID, o.Key)
				if w.Store.Exists(key) {
					continue
				}
				can, e := w.capacity(project, job.UID, p.Overlap)
				if e != nil {
					errs = append(errs, e)
					continue
				}
				if can {
					if e = prepare(p, key, o.Key, "", "", now); e != nil {
						errs = append(errs, e)
					}
				}
				continue
			}
			_, occupied, e := w.capacityState(project, job.UID, p.Overlap)
			var c sched.Checkpoint
			var dropped sched.Dropped
			if e == nil {
				c, dropped, e = w.checkpoint(project, job, p, occupied, now)
			}
			if e == nil {
				e = w.commitCheckpoint(project, p, c, dropped, occupied, now)
			}
			if e != nil {
				protectJob(job)
				errs = append(errs, e)
				continue
			}
			for len(c.Pending) > 0 {
				o := c.Pending[0]
				if o.At.After(now) {
					break
				}
				key := journal.RunKey(w.Config.Executor, project.Name, job.UID, o.Key)
				if !w.Store.Exists(key) {
					can, e := w.capacity(project, job.UID, p.Overlap)
					if e != nil {
						errs = append(errs, e)
						break
					}
					if !can {
						break
					}
					if e = prepare(p, key, o.Key, "", "", now); e != nil {
						errs = append(errs, e)
						break
					}
				}
				c.Pending = c.Pending[1:]
				if e = state.WriteJSON(w.Store.Layout.Job(job.UID), c); e != nil {
					errs = append(errs, e)
					break
				}
			}
		}
	}
	for _, req := range requests {
		if req.Kind == "release" {
			if r, e := w.Store.Find(req.Run); e == nil && protected(r) {
				errs = append(errs, q.Block(req.ID, "scheduled expiry decision is not durable"))
				continue
			}
			if err = w.release(q, req); err != nil {
				errs = append(errs, err)
			}
		}
	}
	for _, req := range requests {
		if req.Kind == "run" && !consumed[req.ID] {
			var e error
			if reason := permanent[req.ID]; reason != "" {
				e = q.Reject(req.ID, reason)
			} else if matched[req.ID] == "" && allRead {
				e = q.Reject(req.ID, "job is unknown")
			} else {
				e = q.Block(req.ID, reasons[req.ID])
			}
			if e != nil {
				errs = append(errs, e)
			}
		}
	}
	runs, err := w.Store.Active()
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, r := range runs {
		if r.Phase == "prepared" && !admission[r.Project] {
			continue
		}
		if k := w.Clients[r.Project]; k != nil {
			if err = w.advance(report.WithSummaryV2(ctx, slices.Contains(w.caps[r.Project].caps.EventFeatures, "cron_run_summary_v2")), k, r, now, protected(r)); err != nil {
				errs = append(errs, fmt.Errorf("run %s: %w", r.RunUID, err))
			}
		}
	}
	clients := make(map[string]kata.Kata)
	for _, project := range w.Config.Projects {
		clients[project.Name] = w.Clients[project.Name]
		if k := clients[project.Name]; k != nil {
			if err := w.beat(ctx, k, project.Name, now); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if err := (report.Outbox{Layout: w.Store.Layout}).Deliver(clients, now); err != nil {
		errs = append(errs, err)
	}
	if err := (report.Outbox{Layout: w.Store.Layout, Dir: w.Store.Layout.Dropped()}).Deliver(clients, now); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
func states(r journal.Run) map[string]flow.StepState {
	out := make(map[string]flow.StepState)
	for _, a := range r.Attempts {
		if out[a.Step].Attempts <= a.N {
			out[a.Step] = flow.StepState{Status: a.Status, Attempts: a.N}
		}
	}
	return out
}
func (w *Worker) held(ctx context.Context, k kata.Kata, r journal.Run, reason string, now time.Time) error {
	outcome := "unknown"
	current := states(r)
	for _, s := range r.Plan.Steps {
		if st := current[s.Key]; st.Status == "failed" && st.Attempts > s.Retries {
			outcome = "failed"
		}
	}
	if err := w.Store.Update(r.Key, func(r *journal.Run) error {
		r.Phase = "held"
		r.Hold = reason
		r.Outcome = outcome
		r.EndedAt = new(now.UTC())
		return nil
	}); err != nil {
		return err
	}
	return report.Hold(ctx, k, w.Store, r.Key)
}
func (w *Worker) advance(ctx context.Context, k kata.Kata, r journal.Run, now time.Time, protectGate bool) error {
	if r.Phase == "prepared" {
		if err := report.Admit(ctx, k, w.Store, r.Key); err != nil {
			if saved, e := w.Store.Load(r.Key); e != nil {
				return errors.Join(err, e)
			} else if saved.Phase == "held" {
				return errors.Join(err, report.Hold(ctx, k, w.Store, r.Key))
			}
			return err
		}
		var err error
		r, err = w.Store.Load(r.Key)
		if err != nil {
			return err
		}
	}
	switch r.Phase {
	case "held":
		return report.Hold(ctx, k, w.Store, r.Key)
	case "settled":
		return report.Final(ctx, k, w.Store, r.Key)
	case "running":
	default:
		return nil
	}
	if cancelled(r) {
		return w.cancel(ctx, k, r, now)
	}
	for idx, a := range r.Attempts {
		if a.Hold != "" {
			return w.held(ctx, k, r, a.Hold, now)
		}
		if slices.Contains([]string{"succeeded", "failed", "skipped", "needs-human"}, a.Status) {
			continue
		}
		if a.Address == "" {
			var err error
			if a.Nonce == "" {
				if !now.Before(r.ExpiresAt) {
					return w.held(ctx, k, r, "timeout", now)
				}
				err = attempt.Launch(ctx, w.HC, w.Store, r.Key, idx, a.Spec)
			} else {
				err = attempt.Recover(ctx, w.HC, w.Store, r.Key, idx)
			}
			if err != nil {
				return err
			}
			r, err = w.Store.Load(r.Key)
			if err != nil {
				return err
			}
			a = r.Attempts[idx]
			if a.Hold != "" {
				return w.held(ctx, k, r, a.Hold, now)
			}
		}
		out, err := attempt.Settle(ctx, w.HC, w.Store, r.Key, idx, now)
		if err != nil {
			return err
		}
		switch o := out.(type) {
		case attempt.Held:
			return w.held(ctx, k, r, o.Reason, now)
		case attempt.Notify:
			return report.NotifyOnce(ctx, k, w.Store, r.Key, o.ID, o.Message+"; hc "+a.Address)
		case attempt.Pending:
			return nil
		}
	}
	var err error
	r, err = w.Store.Load(r.Key)
	if err != nil {
		return err
	}
	for _, a := range r.Attempts {
		if a.Status == "failed" && a.StopReason != "" {
			if err = w.Store.Update(r.Key, func(r *journal.Run) error {
				r.Phase = "settled"
				r.Outcome = "failed"
				r.EndedAt = new(now.UTC())
				return nil
			}); err != nil {
				return err
			}
			return report.Final(ctx, k, w.Store, r.Key)
		}
	}
	switch next := flow.Next(r.Plan, states(r)).(type) {
	case flow.Invalid:
		return w.held(ctx, k, r, "invalid_workflow", now)
	case flow.Hold:
		return w.held(ctx, k, r, "launch_ambiguous", now)
	case flow.Wait:
		return nil
	case flow.Finish:
		if protectGate {
			return nil
		}
		if err = w.Store.Update(r.Key, func(r *journal.Run) error {
			r.Phase = "settled"
			r.Outcome = next.Outcome
			r.EndedAt = new(now.UTC())
			return nil
		}); err != nil {
			return err
		}
		return report.Final(ctx, k, w.Store, r.Key)
	case flow.Start:
		if !now.Before(r.ExpiresAt) {
			return w.held(ctx, k, r, "timeout", now)
		}
		var step defs.Step
		for _, s := range r.Plan.Steps {
			if s.Key == next.Step {
				step = s
				break
			}
		}
		spec, e := w.spec(r, step, next.N)
		if e != nil {
			if !errors.Is(e, prompt.ErrInvalidPrompt) {
				return e
			}
			r.Detail = e.Error()
			_ = w.Store.Update(r.Key, func(saved *journal.Run) error { saved.Detail = r.Detail; return nil })
			return w.held(ctx, k, r, "invalid_prompt", now)
		}
		idx := len(r.Attempts)
		if err = w.Store.Update(r.Key, func(r *journal.Run) error {
			r.Attempts = append(r.Attempts, journal.Attempt{Step: step.Key, N: next.N, Status: "launching", Spec: spec})
			return nil
		}); err != nil {
			return err
		}
		return attempt.Launch(ctx, w.HC, w.Store, r.Key, idx, spec)
	}
	return nil
}
func (w *Worker) spec(r journal.Run, s defs.Step, n int) (hc.RunSpec, error) {
	dir := w.Store.Layout.Attempt(r.Key, s.Key, n)
	if err := state.EnsureDir(dir); err != nil {
		return hc.RunSpec{}, err
	}
	previous := ""
	if len(r.Attempts) > 0 {
		previous = r.Attempts[len(r.Attempts)-1].Note
	}
	extra := maps.Clone(w.Config.Kata.Env)
	if extra == nil {
		extra = make(map[string]string)
	}
	if s.Kind == "prompt" && len(w.Config.Sessions.Env) > 0 {
		mapping, err := json.Marshal(w.Config.Sessions.Env)
		if err != nil {
			return hc.RunSpec{}, err
		}
		extra[session.MappingEnv] = string(mapping)
	}
	extra["KATA_PROJECT"] = r.Project
	extra["KATA_CRON_RUN"] = r.RunUID
	extra["KATA_CRON_RUN_DIR"] = w.Store.Layout.Run(r.Key)
	extra["KATA_CRON_STEP"] = s.Key
	extra["KATA_CRON_ATTEMPT"] = fmt.Sprint(n)
	argv := slices.Clone(s.Argv)
	if s.Kind == "command" {
		if r.Plan.InputValues != nil {
			var values map[string]any
			decoder := json.NewDecoder(strings.NewReader(string(r.Plan.InputValues)))
			decoder.UseNumber()
			if err := decoder.Decode(&values); err != nil {
				return hc.RunSpec{}, err
			}
			for name, value := range values {
				extra["KATA_INPUT_"+strings.ToUpper(name)] = fmt.Sprint(value)
			}
		}
		extra["KATA_CRON_INPUT"] = r.Plan.Input
		extra["KATA_CRON_PREVIOUS"] = previous
		extra["KATA_CRON_NOTE_FILE"] = filepath.Join(dir, "note.txt")
		argv = []string{w.Bin, "exec-step", dir, "--", "/bin/sh", "-c", s.Text}
	} else {
		prefix := prompt.Quote(w.Config.Kata.Bin) + " --project " + prompt.Quote(r.Project) + " --daemon " + prompt.Quote(w.Config.Kata.Daemon) + " --as " + prompt.Quote(r.Actor)
		text, err := prompt.Build(r, s, n, r.IssueRef, r.IssueTitle, previous, filepath.Join(dir, "result.json"), w.Bin, prefix)
		if err != nil {
			return hc.RunSpec{}, err
		}
		path := filepath.Join(dir, "prompt.md")
		if err = state.WriteFile(path, []byte(text)); err != nil {
			return hc.RunSpec{}, err
		}
		argv = append(argv, prompt.Line(path))
	}
	unset := w.Config.Limits.UnsetEnv
	if s.Kind == "prompt" {
		unset = append(slices.Clone(unset), session.EnvNames(w.Config.Sessions.Env)...)
	}
	prefix := attempt.LaunchEnv(unset, extra)
	return hc.RunSpec{Cwd: r.Plan.CheckoutPath, Labels: map[string]string{"kata.executor": r.Executor}, Prefix: prefix, Argv: argv}, nil
}
