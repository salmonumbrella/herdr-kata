package runner

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type Request struct {
	Project    string  `json:"project,omitempty"`
	Version    int     `json:"version"`
	UID        string  `json:"uid"`
	Kind       string  `json:"kind"`
	RunUID     string  `json:"run_uid"`
	Job        string  `json:"job,omitempty"`
	Input      *string `json:"input,omitempty"`
	ResetLoops bool    `json:"reset_loops,omitempty"`
}
type RequestResult struct {
	Kind    string `json:"kind,omitempty"`
	Project string `json:"project,omitempty"`
	Job     string `json:"job,omitempty"`
	Version int    `json:"version"`
	RunUID  string `json:"run_uid"`
	Error   string `json:"error,omitempty"`
	// Reason classifies a refusal; ReasonLeaseHeldElsewhere is the only one.
	Reason string `json:"reason,omitempty"`
}

var errProjectUnavailable = errors.New("waiting for project before releasing an unadmitted run")

var errControlRefused = errors.New("control request refused")

type ManualScheduler interface {
	RunNow(jobRef, runUID string, input ...*string) error
}

// SendRequest durably publishes before signalling. A failed signal is harmless:
// the worker scans this directory every 60 seconds, even when it was empty.
func SendRequest(fsys state.FS, l state.Layout, kind, ref string, input ...*string) (Request, error) {
	return SendProjectRequest(fsys, l, kind, ref, "", input...)
}

func SendResumeRequest(fsys state.FS, l state.Layout, ref string, resetLoops bool) (Request, error) {
	return sendRequest(fsys, l, "resume", ref, "", resetLoops)
}

func SendProjectRequest(fsys state.FS, l state.Layout, kind, ref, project string, input ...*string) (Request, error) {
	return sendRequest(fsys, l, kind, ref, project, false, input...)
}

func sendRequest(fsys state.FS, l state.Layout, kind, ref, project string, resetLoops bool, input ...*string) (Request, error) {
	var value *string
	if len(input) > 0 {
		value = input[0]
	}
	if value != nil {
		if kind != "run" {
			return Request{}, errors.New("input requires run")
		}
		if e := defs.ValidateInput(*value); e != nil {
			return Request{}, e
		}
	}
	var info struct {
		PID       int       `json:"pid"`
		WorkerPID int       `json:"worker_pid"`
		Version   string    `json:"version"`
		Exe       string    `json:"exe"`
		StartedAt time.Time `json:"started_at"`
	}
	if _, e := fsys.Lstat(l.WorkerLock()); missing(e) {
		return Request{}, errors.New("runtime not running; run `herdr-kata up`")
	} else if e != nil {
		return Request{}, e
	}
	held, e := state.Held(l.WorkerLock())
	if e != nil {
		return Request{}, e
	}
	if !held {
		return Request{}, errors.New("runtime not running; run `herdr-kata up`")
	}
	if e = state.ReadJSON(fsys, l.ServeInfo(), 4096, &info); e != nil {
		return Request{}, e
	}
	req := Request{Project: project, Version: 1, UID: ident.NewRunUID(time.Now(), rand.Reader), Kind: kind, RunUID: ref, Input: value, ResetLoops: resetLoops}
	switch kind {
	case "run":
		req.Job = ref
		req.RunUID = ident.NewRunUID(time.Now(), rand.Reader)
	case "release", "resume":
		if !validUID(ref) {
			return Request{}, errors.New("invalid run UID")
		}
	default:
		return Request{}, errors.New("invalid request kind")
	}
	// A lane this runtime stood down from refuses at once; the worker
	// refuses too, so this only answers sooner.
	lane := project
	if kind != "run" {
		var r Run
		if state.ReadJSON(fsys, l.RunFile(ref), 4<<20, &r) == nil {
			lane = r.Target.Identity.ProjectUID
		}
	}
	if e = savedLeaseRefusal(fsys, l, lane, info.StartedAt); e != nil {
		return Request{}, e
	}
	if e = state.WriteJSON(fsys, filepath.Join(l.RequestsDir(), req.UID+".json"), req); e != nil {
		return Request{}, e
	}
	if info.WorkerPID > 0 {
		_ = syscall.Kill(info.WorkerPID, syscall.SIGUSR1)
	}
	return req, nil
}

// Release is one transition including reporting intents. It joins the issue
// delivery lane so an unsent close cannot race the handoff. Sent intents remain
// immutable and are reconciled by the queue before the new handoff report.
func (m *Manager) Release(ctx context.Context, uid string) error {
	if e := ctx.Err(); e != nil {
		return e
	}

	project := m.projectUID
	if m.projects && project == "" {
		r, e := m.store.LoadContext(ctx, uid)
		if e != nil {
			return e
		}
		if lane := m.forRun(r); lane != nil {
			return lane.Release(ctx, uid)
		}
		project = r.Target.Identity.ProjectUID
	}
	recovery := m.recoveryLock(project)
	if e := lockControl(ctx, recovery); e != nil {
		return e
	}
	defer recovery.Unlock()
	admission := m.admissionLock(uid)
	if e := lockControl(ctx, admission); e != nil {
		return e
	}
	defer admission.Unlock()
	current, e := m.store.LoadContext(ctx, uid)
	if e != nil {
		return e
	}
	if current.Resume != nil && current.Resume.Pending && current.Inspect != "" {
		return fmt.Errorf("%w: %s", errControlRefused, current.Inspect)
	}
	if current.Phase == Held && current.Reason == "create_rejected" {
		_, e := m.store.TransitionContext(ctx, uid, func(r *Run) error {
			r.Phase, r.Reason, r.Outcome = HandedOff, "released by operator", "unknown"
			r.SlotReleased = true
			now := m.clock.Now().UTC()
			r.EndedAt = &now
			return nil
		})
		if e == nil {
			m.unreserve(uid)
		}
		return e
	}
	if preAdmissionHold(current) {
		return fmt.Errorf("%w: use abandon --local for %s", errControlRefused, current.Reason)
	}
	if m.projects && !admitted(current) && m.forRun(current) == nil {
		return errProjectUnavailable
	}
	if (current.Phase == Prepared || current.Phase == Admitted) && (!m.projects || m.forRun(current) != nil) {
		if current.Inspect != "" {
			return fmt.Errorf("%w: %s", errControlRefused, current.Inspect)
		}
		if current.Phase == Prepared {
			if current.Admission == nil && current.IssueRequest == nil || current.JobDef.Issue == nil {
				return fmt.Errorf("%w: missing frozen admission", errControlRefused)
			}
			e = m.admitPrepared(ctx, current)
		} else {
			e = m.admissionTail(ctx, uid)
		}
		if e != nil {
			latest, loadErr := m.store.LoadContext(ctx, uid)
			if loadErr == nil && latest.Inspect != "" {
				return fmt.Errorf("%w: %s", errControlRefused, latest.Inspect)
			}
			return e
		}
	}
	if m.q != nil {
		if e := lockControl(ctx, m.q.issueMu); e != nil {
			return e
		}
		defer m.q.issueMu.Unlock()
	}
	_, e = m.store.TransitionContext(ctx, uid, func(r *Run) error {
		if (r.Phase == Admitted || r.Resume != nil && r.Resume.Pending) && r.Inspect != "" {
			return fmt.Errorf("%w: %s", errControlRefused, r.Inspect)
		}
		if r.Phase == Settled {
			return fmt.Errorf("%w: release requires an unfinished run", errControlRefused)
		}
		if r.Phase == HandedOff {
			return nil
		}
		r.Phase = HandedOff
		r.SlotReleased = true
		r.Reason = "released by operator"
		if r.Outcome == "" {
			r.Outcome = "unknown"
		}
		now := m.clock.Now().UTC()
		r.EndedAt = &now
		// Cancel unsent settlement and resume operations. Sent intents remain immutable.
		for i := range r.Ops {
			op := &r.Ops[i]
			cancelResume := r.Resume != nil && r.Resume.Pending && strings.HasPrefix(op.ID, resumePrefix(r)+":")
			if !op.Done && op.FirstSent == nil && (strings.Contains(op.ID, ":settle:") || cancelResume) {
				op.retire("cancelled by release")
			}
		}
		if r.Resume != nil {
			r.Resume.Pending = false
		}
		snapshot := *r
		for _, op := range r.Ops {
			// A sent close can still land after a transient reply or retry.
			// Avoid ownership writes that would then strand the handoff report.
			if op.Kind == "close" && (op.Landed() || op.FirstSent != nil) {
				snapshot.IssueOwned = false
			}
		}
		ops := SettleOps(&snapshot, nil, now)
		// A sent observation must resolve before the handoff; reserve its resulting
		// revision even if its reply has not been saved yet.
		for i := range ops {
			if ops[i].Kind != "observe" {
				continue
			}
			p, _ := decodePayload(ops[i])
			for _, prior := range r.Ops {
				if prior.Kind == "observe" && prior.FirstSent != nil && !prior.Done {
					body, e := decodePayload(prior)
					if e != nil {
						return e
					}
					if body.Observation != nil {
						p.Observation.ExpectedRevision = max(p.Observation.ExpectedRevision, body.Observation.ExpectedRevision+1)
					}
				}
			}
			ops[i].Payload, _ = json.Marshal(p)
		}
		for i := range ops {
			if ops[i].Kind == "comment" {
				p, _ := decodePayload(ops[i])
				intro := "Handed to the operator. Automatic execution stopped."
				if AgentReference(r).TabID != "" {
					intro = "Handed to the operator. Automatic execution stopped; tab retained."
				}
				p.Body = capText(intro+"\n\n"+p.Body, maxSummaryBytes)
				ops[i].Payload, _ = json.Marshal(p)
			}
			ops[i].ID = strings.Replace(ops[i].ID, ":settle:", ":release:", 1)
			if ops[i].Key != "" {
				ops[i].Key += "-release"
			}
		}
		r.Ops = append(r.Ops, ops...)
		return nil
	})
	if e == nil {
		if m.q != nil {
			m.q.Kick(uid)
		}
		m.WakeProjects()
	}
	return e
}

// PendingRequests reports local control requests waiting for the worker.
func (m *Manager) PendingRequests() bool {
	entries, _ := m.store.fsys.ReadDir(m.store.layout.RequestsDir())
	return slices.ContainsFunc(entries, func(e fs.DirEntry) bool {
		return !e.IsDir() && !strings.HasPrefix(e.Name(), ".") && strings.HasSuffix(e.Name(), ".json")
	})
}

// HandleRequests keeps manual requests until a journal exists, since RunNow's
// in-memory queue does not survive a worker crash. Enqueue once per worker and
// reuse the request's run UID after every restart.
func (m *Manager) HandleRequests(ctx context.Context, scheduler ManualScheduler) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, e := m.store.fsys.ReadDir(m.store.layout.RequestsDir())
	if missing(e) {
		e = nil
	}
	if e != nil {
		return e
	}
	loadRequest := func(uid string) (*Run, error) {
		readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return m.store.LoadContext(readCtx, uid)
	}
	type pendingRequest struct {
		path string
		req  Request
	}
	var releases, others []pendingRequest
	releaseRequested := map[string]bool{}
	// Validate and read each request once before processing releases first.
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(m.store.layout.RequestsDir(), entry.Name())
		var req Request
		if e = state.ReadJSON(m.store.fsys, path, 64<<10, &req); e != nil {
			if missing(e) {
				continue
			}
			if e = m.rejectRequest(path, req, e); e != nil {
				return e
			}
			continue
		}
		if req.Input != nil {
			if e = defs.ValidateInput(*req.Input); e != nil || req.Kind != "run" {
				if e == nil {
					e = errors.New("input requires run")
				}
				if e = m.rejectRequest(path, req, e); e != nil {
					return e
				}
				continue
			}
		}
		if req.Version != 1 || !validUID(req.UID) || !validUID(req.RunUID) || entry.Name() != req.UID+".json" {
			if e = m.rejectRequest(path, req, errors.New("invalid control request")); e != nil {
				return e
			}
			continue
		}
		if req.ResetLoops && req.Kind != "resume" {
			if e = m.rejectRequest(path, req, errors.New("reset-loops requires resume")); e != nil {
				return e
			}
			continue
		}
		pending := pendingRequest{path, req}
		if req.Kind == "release" {
			releases = append(releases, pending)
		} else {
			others = append(others, pending)
		}
	}
	for _, pending := range append(releases, others...) {
		if err := ctx.Err(); err != nil {
			return err
		}
		path, req := pending.path, pending.req
		var e error
		if _, err := m.store.fsys.Lstat(path); missing(err) {
			continue
		} else if err != nil {
			return err
		}
		if req.Kind == "release" {
			releaseRequested[req.RunUID] = true
		}
		if req.Kind == "resume" && releaseRequested[req.RunUID] {
			continue
		}
		handler := m
		requestScheduler := scheduler
		if req.Kind == "run" && m.projects {
			_, loadErr := loadRequest(req.RunUID)
			local := loadErr == nil || state.Quarantined(m.store.fsys, m.store.layout, req.RunUID)
			if errors.Is(loadErr, context.DeadlineExceeded) && ctx.Err() == nil {
				continue
			}
			if loadErr != nil && !missing(loadErr) && !local {
				return loadErr
			}
			if !local {
				if req.Project == "" {
					project, job, resolveErr := ResolveJob(m.cfg, m.store.fsys, m.store.layout, req.Job, "")
					if resolveErr != nil {
						if errors.Is(resolveErr, ErrAmbiguousProject) {
							if err := m.rejectRequest(path, req, resolveErr); err != nil {
								return err
							}
						}
						continue
					}
					req.Project, req.Job = project, job
					if err := state.WriteJSON(m.store.fsys, path, req); err != nil {
						return err
					}
				}
				handler = m.ForProject(req.Project)
				if handler == nil {
					continue
				}
				requestScheduler = handler.projectScheduler()
				if requestScheduler == nil && handler.laneRefusal() == nil {
					continue
				}
			}
		}
		var handled bool
		var refused error
		switch req.Kind {
		case "resume":
			timeout := 2 * time.Second
			r, loadErr := loadRequest(req.RunUID)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(loadErr, context.DeadlineExceeded) {
				continue
			}
			if loadErr == nil {
				if refusal := m.forRun(r).laneRefusal(); refusal != nil {
					handled, refused = true, refusal
					break
				}
			}
			if loadErr == nil && r.Reason == "herdr_env_unclean" {
				timeout = 20 * time.Second
			}
			resumeCtx, cancel := context.WithTimeout(ctx, timeout)
			e = m.Resume(resumeCtx, req)
			if e == nil {
				e = m.startExecution(ctx, resumeCtx, req.RunUID)
			}
			cancel()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(e, errResumePending) || errors.Is(e, context.DeadlineExceeded) || e != nil && kata.Class(e) == kata.Retry {
				continue
			}
			// One failed route or read must not starve other controls.
			handled = true
			refused = e
			e = nil
		case "release":
			r, loadErr := loadRequest(req.RunUID)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if loadErr == nil {
				if refusal := m.forRun(r).laneRefusal(); refusal != nil {
					handled, refused = true, refusal
					break
				}
			}
			releaseCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			e = m.Release(releaseCtx, req.RunUID)
			cancel()
			if (errors.Is(e, context.DeadlineExceeded) || errors.Is(e, errProjectUnavailable)) && ctx.Err() == nil {
				// Keep this request for a later scan, without starving controls
				// whose journals are already ready for handoff.
				continue
			}
			handled = e == nil
			if missing(e) || errors.Is(e, errControlRefused) {
				refused = e
				handled = true
				e = nil
			}
		case "run":
			if _, loadErr := loadRequest(req.RunUID); loadErr == nil || state.Quarantined(m.store.fsys, m.store.layout, req.RunUID) {
				handled = true
				if loadErr != nil {
					refused = errors.New("run quarantined; local state unavailable")
				}
			} else if errors.Is(loadErr, context.DeadlineExceeded) && ctx.Err() == nil {
				continue
			} else if !missing(loadErr) {
				return loadErr
			} else if completed, ok := requestScheduler.(interface{ ManualResult(string) (bool, error) }); ok {
				done, refusal := completed.ManualResult(req.RunUID)
				if done {
					handled = true
					refused = refusal
				}
			}
			// A stood-down lane refuses the run at intake, and settles one it
			// queued before the takeover: its scheduler admits nothing more.
			if refusal := handler.laneRefusal(); !handled && refusal != nil {
				if _, loadErr := loadRequest(req.RunUID); loadErr == nil {
					handled = true
				} else if missing(loadErr) {
					handled, refused = true, refusal
				} else {
					continue
				}
			}
			if !handled {
				if _, queued := m.requests.Load(req.UID); !queued {
					if !m.projects {
						refused = handler.validateManual(ctx, req.Job)
					}
					if refused != nil {
						if kata.NotFound(refused) {
							kata.ProbeAccess(ctx, handler.guard)
							continue
						}
						if !errors.Is(refused, errManualUnsupported) && !errors.Is(refused, errManualPaused) {
							return refused
						}
						handled = true
					} else if requestScheduler == nil {
						return errors.New("manual scheduler unavailable")
					} else if e = requestScheduler.RunNow(req.Job, req.RunUID, req.Input); e == nil {
						m.requests.Store(req.UID, struct{}{})
					} else {
						continue // Backpressure must not starve later release requests.
					}
				}
			}
		default:
			refused = errors.New("unknown control request kind")
			handled = true
		}
		if e != nil {
			return e
		}
		if handled {
			if req.Kind == "release" && refused == nil {
				delete(releaseRequested, req.RunUID)
			}
			result := RequestResult{Kind: req.Kind, Project: req.Project, Job: req.Job, Version: 1, RunUID: req.RunUID}
			if refused != nil {
				result.Error = refused.Error()
				result.Reason = requestRefusal(refused)
			}
			if e = state.WriteJSON(m.store.fsys, RequestResultPath(m.store.layout, req.UID), result); e != nil {
				return e
			}
			if e = m.store.fsys.Remove(path); e != nil && !missing(e) {
				return e
			}
			if e = m.store.fsys.SyncDir(m.store.layout.RequestsDir()); e != nil {
				return e
			}
			m.requests.Delete(req.UID)
			if finished, ok := requestScheduler.(interface{ ForgetManual(string) }); ok {
				finished.ForgetManual(req.RunUID)
			}
		}
	}
	// A saved acceptance remains runnable without its original request file.
	ids, e := m.store.candidates()
	if e != nil {
		return e
	}
	for _, uid := range ids {
		if releaseRequested[uid] {
			continue
		}
		if e = m.finishReadyResume(ctx, uid); e != nil && !missing(e) && !m.store.quarantinedError(uid, e) {
			return e
		}
	}
	return nil
}
func RequestResultPath(l state.Layout, uid string) string {
	return filepath.Join(l.Root, "request-results", uid+".json")
}

var errManualUnsupported = errors.New("manual run refused: job is not a supported adapter job")
var errManualPaused = errors.New("manual run refused: job is paused; resume it to run it manually")

func (m *Manager) validateManual(ctx context.Context, ref string) error {
	k, ok := m.k.(interface {
		ShowJob(context.Context, string) (kata.Job, error)
		ShowWorkflow(context.Context, string) (kata.Workflow, error)
	})
	if !ok {
		return errors.New("job reader unavailable")
	}
	job, e := k.ShowJob(ctx, ref)
	if e != nil {
		if kata.NotFound(e) {
			if lister, ok := m.k.(interface {
				ListJobs(context.Context) ([]kata.Job, error)
			}); ok {
				rows, proofErr := lister.ListJobs(ctx)
				if proofErr == nil {
					absent := true
					for _, row := range rows {
						if row.UID == ref || row.Name == ref {
							absent = false
						}
					}
					if absent {
						return errManualUnsupported
					}
				}
			}
		}
		return e
	}
	d, e := defs.DecodeJob(job.Definition)
	if e != nil {
		return errManualUnsupported
	}
	if !d.Enabled {
		return errManualPaused
	}
	var wf *defs.WorkflowDef
	if d.Action.WorkflowUID != "" {
		w, e := k.ShowWorkflow(ctx, d.Action.WorkflowUID)
		if e != nil {
			if kata.NotFound(e) {
				if lister, ok := m.k.(interface {
					ListWorkflows(context.Context) ([]kata.Workflow, error)
				}); ok {
					rows, proofErr := lister.ListWorkflows(ctx)
					if proofErr == nil {
						absent := true
						for _, row := range rows {
							if row.UID == d.Action.WorkflowUID {
								absent = false
							}
						}
						if absent {
							return errManualUnsupported
						}
					}
				}
			}
			return e
		}
		def, e := defs.DecodeWorkflow(w.Definition)
		if e != nil {
			return errManualUnsupported
		}
		wf = &def
	}
	owner, support := defs.Classify(d, wf, m.cfg)
	if !defs.Dispatchable(owner, support, job.DeletedAt) {
		if len(support.Reasons) > 0 {
			return fmt.Errorf("%w: %s", errManualUnsupported, strings.Join(support.Reasons, "; "))
		}
		return errManualUnsupported
	}
	return nil
}

// Abandon resolves explicit local-state loss. The frozen observation survives
// ambiguous replies; subsequent calls replay it without changing timestamps.
func Abandon(ctx context.Context, cfg config.Config, k Kata, guard RouteChecker, fsys state.FS, l state.Layout, uid string) error {
	_, err := AbandonProject(ctx, cfg, k, guard, fsys, l, l, uid)
	return err
}
func AbandonProject(ctx context.Context, cfg config.Config, k Kata, guard RouteChecker, fsys state.FS, l, pinLayout state.Layout, uid string) (issueGone bool, err error) {
	if !validUID(uid) {
		return false, errors.New("invalid run UID")
	}
	if _, e := fsys.Lstat(l.RunDir(uid)); e == nil {
		return false, errors.New("abandon refuses a run with a local directory")
	} else if !errors.Is(e, fs.ErrNotExist) {
		return false, e
	}
	if guard == nil {
		return false, errors.New("missing route checker")
	}
	check := guard.Check
	if inspector, ok := guard.(interface{ CheckReadOnly(context.Context) error }); ok {
		check = inspector.CheckReadOnly
	}
	if e := check(ctx); e != nil {
		return false, e
	}
	if e := laneLeaseElsewhere(ctx, k, cfg.Executor); e != nil {
		return false, e
	}
	target, e := pinnedTargetAt(NewStore(fsys, l), pinLayout)
	if e != nil {
		return false, e
	}
	if target.Executor != cfg.Executor || target.Identity.Actor != cfg.Kata.Actor {
		return false, errors.New("installation identity mismatch")
	}
	current, e := k.ShowRun(ctx, uid)
	if kata.NotFound(e) && cfg.Kata.Inbox != "" {
		return abandonIssueRequest(ctx, cfg, k, guard, fsys, l, pinLayout, target, uid)
	}
	if e != nil {
		return false, e
	}
	if current.UID != uid || current.ExecutorLabel != target.Executor || current.Actor != target.Identity.Actor || current.JobUID == "" || current.DefinitionEventUID == "" || current.OccurrenceKey == "" || current.Revision < 1 {
		return false, errors.New("lost run identity does not match installation")
	}
	if e = fsys.MkdirAll(filepath.Join(l.Root, "abandoned"), 0700); e != nil {
		return false, e
	}
	lock, ok, e := state.TryLock(filepath.Join(l.Root, "abandoned", uid+".lock"))
	if e != nil {
		return false, e
	}
	if !ok {
		return false, errors.New("abandon already in progress")
	}
	defer lock.Unlock() //nolint:errcheck // Lock cleanup remains best-effort.
	path := filepath.Join(l.Root, "abandoned", uid+".json")
	var pending struct {
		Version     int
		Target      Target
		Observation kata.Observation
	}
	e = state.ReadJSON(fsys, path, 64<<10, &pending)
	o := pending.Observation
	if missing(e) {
		if current.Status == "unknown" && current.Summary == (kata.Summary{Version: 1, Message: "local state lost"}) {
			return reportAbandoned(ctx, k, guard, fsys, l, pinLayout, target, current)
		}
		if current.Status != "running" {
			return false, errors.New("only a running lost run can be abandoned")
		}
		now := time.Now().UTC()
		o = kata.Observation{JobUID: current.JobUID, DefinitionEventUID: current.DefinitionEventUID, WorkflowUID: current.WorkflowUID, WorkflowDefinitionEventUID: current.WorkflowDefinitionEventUID, OccurrenceKey: current.OccurrenceKey, IssueUID: current.IssueUID, Actor: current.Actor, ExecutorLabel: current.ExecutorLabel, Teammate: current.Teammate, Status: "unknown", Summary: kata.Summary{Version: 1, Message: "local state lost"}, StartedAt: current.StartedAt, EndedAt: &now, ExpectedRevision: current.Revision}
		o.Transcripts = current.Transcripts
		// Local loss prevents proving whether all attempts were represented.
		o.TranscriptsIncomplete = true
		o, e = prepareObservation(ctx, k, o)
		if e != nil {
			return false, e
		}
		pending.Version = 1
		pending.Target = target
		pending.Observation = o
		if e = state.WriteJSON(fsys, path, pending); e != nil {
			return false, e
		}
	} else if e != nil {
		return false, e
	}
	if pending.Version != 1 || !sameTarget(pending.Target, target) || o.WorkflowUID != current.WorkflowUID || o.WorkflowDefinitionEventUID != current.WorkflowDefinitionEventUID || o.IssueUID != current.IssueUID || o.JobUID != current.JobUID || o.DefinitionEventUID != current.DefinitionEventUID || o.OccurrenceKey != current.OccurrenceKey || o.Actor != target.Identity.Actor || o.ExecutorLabel != target.Executor || o.Status != "unknown" || o.Summary.Message != "local state lost" {
		return false, fmt.Errorf("abandon request identity differs")
	}
	if observationProvenance(current, &o) && current.Status == o.Status && current.Summary == o.Summary && sameTime(current.StartedAt, o.StartedAt) && sameTime(current.EndedAt, o.EndedAt) && sameOptionalString(current.Teammate, o.Teammate) && current.Revision > o.ExpectedRevision {
		gone, e := reportAbandoned(ctx, k, guard, fsys, l, pinLayout, target, current)
		if e != nil {
			return false, e
		}
		return gone, clearAbandon(fsys, path)
	}
	if current.Status != "running" || current.Revision != o.ExpectedRevision || !sameOptionalString(current.Teammate, o.Teammate) {
		return false, errors.New("lost run changed since abandon was prepared")
	}
	_, _, e = k.Observe(ctx, uid, o)
	if e != nil {
		return false, e
	}
	gone, e := reportAbandoned(ctx, k, guard, fsys, l, pinLayout, target, current)
	if e != nil {
		return false, e
	}
	return gone, clearAbandon(fsys, path)
}

func sameOptionalString(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
func clearAbandon(fsys state.FS, path string) error {
	if e := fsys.Remove(path); e != nil && !missing(e) {
		return e
	}
	return fsys.SyncDir(filepath.Dir(path))
}

// Admissions, queued RPCs and occupant checks may own a control or journal
// lock. A request deadline also bounds acquisition, not just Queue.wait.
func lockControl(ctx context.Context, mu *sync.Mutex) error {
	for {
		if e := ctx.Err(); e != nil {
			return e
		}
		if mu.TryLock() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// Rejected inputs cannot block valid controls behind them. Keep the original
// file for inspection and return a refusal addressed by its filename identity.
func (m *Manager) rejectRequest(path string, req Request, cause error) error {
	uid := strings.TrimSuffix(filepath.Base(path), ".json")
	reason := requestRefusalReason(cause)
	if req.RunUID == "" {
		reason = "request " + filepath.Base(path) + ": " + reason
	}
	result := RequestResult{Kind: req.Kind, Project: req.Project, Job: req.Job, Version: 1, RunUID: req.RunUID, Error: capText(reason, 4096)}
	if e := state.WriteJSON(m.store.fsys, RequestResultPath(m.store.layout, uid), result); e != nil {
		return e
	}
	dir := filepath.Join(m.store.layout.Root, "quarantine", "requests")
	if e := m.store.fsys.MkdirAll(dir, 0700); e != nil {
		return e
	}
	dst := filepath.Join(dir, strconv.FormatInt(time.Now().UnixNano(), 10)+"-"+filepath.Base(path))
	if e := m.store.fsys.Rename(path, dst); e != nil {
		return e
	}
	for current := dir; ; current = filepath.Dir(current) {
		if e := m.store.fsys.SyncDir(current); e != nil {
			return e
		}
		if current == filepath.Clean(m.store.layout.Root) || filepath.Dir(current) == current {
			break
		}
	}
	m.requests.Delete(uid)
	return m.store.fsys.SyncDir(filepath.Dir(path))
}

// Keep the refusal useful to operators without including its local request path.
func requestRefusalReason(err error) string {
	if corrupt, ok := errors.AsType[*state.CorruptError](err); ok {
		return requestRefusalReason(corrupt.Err)
	}
	if path, ok := errors.AsType[*os.PathError](err); ok {
		return requestRefusalReason(path.Err)
	}
	return err.Error()
}

// AbandonLocal is an offline whole-run resolution. No Kata boundary is used.
func AbandonLocal(ctx context.Context, fsys state.FS, l state.Layout, uid string, h *herdr.Client) error {
	if !validUID(uid) {
		return errors.New("invalid run UID")
	}
	serve, ok, err := state.TryLock(l.ServeLock())
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("stop the runtime first")
	}
	defer serve.Unlock() //nolint:errcheck // Lock cleanup remains best-effort.
	worker, ok, err := state.TryLock(l.WorkerLock())
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("stop the runtime first")
	}
	defer worker.Unlock() //nolint:errcheck // Lock cleanup remains best-effort.
	if state.Quarantined(fsys, l, uid) {
		if _, err := fsys.Lstat(l.RunFile(uid)); missing(err) {
			// Finish both directory syncs if an interrupted whole-run move
			// already removed its source. Corrupt-file quarantine can retain
			// a source directory of artifacts, which acknowledgement preserves.
			if _, sourceErr := fsys.Lstat(l.RunDir(uid)); missing(sourceErr) {
				if _, parentErr := fsys.Lstat(filepath.Dir(l.RunDir(uid))); parentErr == nil {
					if err := state.QuarantineRunDirectory(fsys, l, uid); err != nil {
						return err
					}
				} else if !missing(parentErr) {
					return parentErr
				}
			} else if sourceErr != nil {
				return sourceErr
			}
			return acknowledgeQuarantine(fsys, l, uid)
		} else if err != nil {
			return err
		}
	}
	if _, err = fsys.Lstat(l.RunDir(uid)); missing(err) {
		if err := state.QuarantineRunDirectory(fsys, l, uid); err != nil {
			return err
		}
		return acknowledgeQuarantine(fsys, l, uid)
	} else if err != nil {
		return err
	}
	s := NewStore(fsys, l)
	r, err := s.Load(uid)
	if err != nil {
		return err
	}
	accepted := preAdmissionHold(r)
	for _, op := range r.Ops {
		accepted = accepted || op.SigningRefused
	}
	if !accepted {
		return errors.New("local abandon requires admission_rejected, create_rejected or signing_refused")
	}
	m := NewManager(config.Config{}, nil, h, s, nil, nil, nil)
	proofCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stopped := m.localStopped(proofCtx, r)
	if err := proofCtx.Err(); err != nil {
		return fmt.Errorf("run occupant not proven stopped: %w", err)
	}
	if !stopped {
		return errors.New("run occupant not proven stopped")
	}
	if err := state.QuarantineRunDirectory(fsys, l, uid); err != nil {
		return err
	}
	return acknowledgeQuarantine(fsys, l, uid)
}

// Local quarantine needs proof of stopped occupants, not a successful or certain
// execution outcome. Keep every original receipt and result unchanged on disk.
func (m *Manager) localStopped(ctx context.Context, r *Run) bool {
	proofRun := *r
	proofRun.Steps = nil
	for _, step := range append(append([]StepState{}, r.Steps...), r.PastAttempts...) {
		if step.Kind == "command" && (step.Submission == Sending || step.Submission == Accepted) {
			if !m.commandStopped(r, step) {
				return false
			}
		}
	}
	// The command groups have independent liveness proof. Let the established
	// provenance checks handle only prompt occupants, including retained agents.
	for _, step := range r.Steps {
		if step.Kind != "command" {
			proofRun.Steps = append(proofRun.Steps, step)
		}
	}
	if proofRun.Outcome == "" {
		proofRun.Outcome = "unknown"
	}
	if m.h == nil {
		for _, ref := range agentReferences(&proofRun) {
			if ref.PaneID != "" || ref.AgentName != "" || ref.LaunchIntent {
				return false
			}
		}
	}
	stopped, _ := m.heldStopped(ctx, &proofRun)
	return stopped
}

// This is explicit operator acknowledgement; quarantine still vetoes admission.
func acknowledgeQuarantine(fsys state.FS, l state.Layout, uid string) error {
	return state.WriteJSON(fsys, filepath.Join(l.QuarantineRunDir(uid), "acknowledged.json"), struct {
		Acknowledged bool `json:"acknowledged"`
	}{true})
}
