package runner

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/crashpoint"
	"github.com/salmonumbrella/herdr-kata/internal/inbox"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// RouteChecker must resolve and validate the live route on every Check.
type RouteChecker interface {
	Check(ctx context.Context) error
}
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

// Kata is the CLI boundary used by the write queue.
type Kata interface {
	Observe(context.Context, string, kata.Observation) (kata.Run, bool, error)
	ShowRun(context.Context, string) (kata.Run, error)
	CreateIssue(context.Context, kata.CreateReq) (kata.IssueView, error)
	ShowIssue(context.Context, string) (kata.IssueView, error)
	Close(context.Context, kata.CloseReq) error
	TypedReplies(context.Context) (bool, string)
	Comment(context.Context, kata.CommentReq) (kata.Comment, error)
	MetaSet(context.Context, string, string, string, kata.Cond) (int64, error)
	MetaUnset(context.Context, string, string, kata.Cond) (int64, error)
	Deadline(context.Context, string, string, kata.Cond) error
	Notify(context.Context, string, string, string) error
	LabelAdd(context.Context, string, string) error
	Claim(context.Context, string, bool) (kata.IssueView, error)
}

var _ Kata = (*kata.CLI)(nil)

// Payload is encoded once in Op.Payload. Guard and Key live in Op so freezing a
// revision never regenerates the request body. Observe targets the run UID.
type Payload struct {
	Ref, Name, Value, Body, To, Message, Owner, At string
	Mode, ReplyTo, ReplyKind                       string
	Chain                                          *ChainComment
	IfUnowned                                      bool
	Observation                                    *kata.Observation
	Create                                         *kata.CreateReq
	Close                                          *kata.CloseReq
}
type Queue struct {
	projectUID string
	wake       chan struct{}
	runMu      sync.Mutex
	issueMu    *sync.Mutex
	k          Kata
	store      *Store
	guard      RouteChecker
	clock      Clock
	// Owned by this lane's Run loop.
	routeRefusals int
	routeRetryAt  time.Time
	pinLayout     state.Layout
	routeMu       sync.Mutex
	routePassed   time.Time
	lease         atomic.Pointer[func() (int64, bool)]
	*queueShared
}

// RouteReuse bounds how long a lane's writes trust the lane's own latest
// passing route check instead of probing Kata again.
const RouteReuse = 5 * time.Second

// leaseFunc returns the lane's lease, if the lane has one.
func (q *Queue) leaseFunc() func() (int64, bool) {
	if f := q.lease.Load(); f != nil {
		return *f
	}
	return nil
}

// checkRoute reuses a passing check younger than RouteReuse while the guard
// still holds the route, including after admissions stopped for another
// installation. Every failed check or access probe releases the route, so a
// lost route fences the next write at once.
func (q *Queue) checkRoute(ctx context.Context) error {
	if g, ok := q.guard.(interface{ RouteHeld() bool }); ok {
		q.routeMu.Lock()
		passed := q.routePassed
		q.routeMu.Unlock()
		if age := q.clock.Now().Sub(passed); !passed.IsZero() && age >= 0 && age < RouteReuse && g.RouteHeld() {
			return nil
		}
	}
	started := q.clock.Now()
	err := q.guard.Check(ctx)
	q.routeMu.Lock()
	defer q.routeMu.Unlock()
	if err != nil {
		q.routePassed = time.Time{}
	} else if started.After(q.routePassed) {
		q.routePassed = started
	}
	return err
}

type queueShared struct {
	started time.Time // when this process's queue began; paces deferrals
	// All issue writes share a lane, including different aliases of an issue.
	// Observations may use the second CLI slot concurrently.
	changeMu   sync.Mutex
	changed    chan struct{}
	projects   bool
	lanesMu    sync.RWMutex
	lanes      map[string]*Queue
	rootWake   chan struct{}
	writeSlots chan struct{}
}

func NewQueue(k Kata, s *Store, guard RouteChecker, clock Clock) *Queue {
	if clock == nil {
		clock = RealClock{}
	}
	wake := make(chan struct{}, 1)
	return &Queue{k: k, store: s, guard: guard, clock: clock, pinLayout: s.layout, wake: wake, issueMu: &sync.Mutex{}, queueShared: &queueShared{started: clock.Now(), changed: make(chan struct{}), lanes: map[string]*Queue{}, rootWake: wake, writeSlots: make(chan struct{}, 2)}}
}

// wait observes durable completions without polling or bypassing the queue.
func (q *Queue) wait(ctx context.Context, uid, id string) error {
	q.Kick(uid)
	for {
		q.changeMu.Lock()
		changed := q.changed
		q.changeMu.Unlock()
		r, err := q.store.LoadContext(ctx, uid)
		if err != nil {
			return err
		}
		op, err := findOp(r, id)
		if err != nil {
			return err
		}
		if r.Inspect != "" {
			return errors.New(r.Inspect)
		}
		if op.Done {
			// A final observation can pass issue writes, but callers use wait
			// as the ordered reporting barrier before resume or continuation.
			complete := true
			for _, prior := range r.Ops {
				if prior.ID == id {
					break
				}
				complete = complete && prior.Done
			}
			if complete {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
func (q *Queue) signal() {
	q.changeMu.Lock()
	defer q.changeMu.Unlock()
	close(q.changed)
	q.changed = make(chan struct{})
}

// Kick is a bounded, coalesced wake. The journal remains the queue; dropped
// duplicate wakeups cannot lose an operation, including after restart.
func (q *Queue) Kick(uid string) {
	signal := func(ch chan struct{}) {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	if q.projects && q.projectUID == "" {
		if uid != "" {
			if r, e := q.store.Load(uid); e == nil {
				if lane := q.forRun(r); lane != nil {
					signal(lane.wake)
					return
				}
			}
		}
		signal(q.rootWake)
		q.lanesMu.RLock()
		defer q.lanesMu.RUnlock()
		for _, lane := range q.lanes {
			signal(lane.wake)
		}
		return
	}
	signal(q.wake)
}
func head(r *Run) *Op {
	// Execution evidence is independent of issue reporting. A refused close or
	// attention guard must not leave a completed run looking live in Kata.
	// Keep observations in revision order, including a sent pre-release result.
	if r.EndedAt != nil && admitted(r) && !preAdmissionHold(r) {
		for i := range r.Ops {
			if !r.Ops[i].Done && r.Ops[i].Kind == "observe" {
				return &r.Ops[i]
			}
		}
	}
	for i := range r.Ops {
		if !r.Ops[i].Done {
			return &r.Ops[i]
		}
	}
	return nil
}
func decodePayload(op Op) (Payload, error) {
	var p Payload
	d := json.NewDecoder(bytes.NewReader(op.Payload))
	d.DisallowUnknownFields()
	err := d.Decode(&p)
	return p, err
}

// Run drains at most two distinct runs per batch. Operations addressing the
// same issue are serialized across runs as well. Only one Run loop may own a
// queue; cancellation waits for in-flight CLI calls to return.
func (q *Queue) Run(ctx context.Context) error {
	q.runMu.Lock()
	defer q.runMu.Unlock()
	if q.projects && q.projectUID == "" {
		return q.runProjects(ctx)
	}
	for ctx.Err() == nil {
		ids, err := q.store.projectCandidates(ctx, q.projectUID)
		if err != nil {
			return err
		}
		var ready []string
		busy := map[string]bool{}
		next := time.Time{}
		now := q.clock.Now()
		checked := map[*Queue]bool{}
		for _, uid := range ids {
			readCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			r, err := q.store.LoadContext(readCtx, uid)
			cancel()
			if errors.Is(err, context.DeadlineExceeded) {
				if err := ctx.Err(); err != nil {
					return err
				}
				// A different lane may hold its journal during an occupant probe.
				retry := q.clock.Now().Add(2 * time.Second)
				if next.IsZero() || retry.Before(next) {
					next = retry
				}
				continue
			}
			if errors.Is(err, fs.ErrNotExist) || q.store.quarantinedError(uid, err) {
				continue
			}
			if err != nil {
				return err
			}
			if q.projectUID != "" && r.Target.Identity.ProjectUID != q.projectUID {
				continue
			}
			op := head(r)
			if !eligible(r, op) || op.Blocked() {
				continue
			}
			if op.NextAt.After(now) {
				if next.IsZero() || op.NextAt.Before(next) {
					next = op.NextAt
				}
				continue
			}
			lane := q.forRun(r)
			if lane == nil {
				continue
			}
			allowed, seen := checked[lane]
			if !seen {
				// A refused route is re-checked with backoff, not every pass.
				if lane.guard != nil && !now.Before(lane.routeRetryAt) {
					err := lane.checkRoute(ctx)
					if state.LocalError(err) {
						return err
					}
					allowed = err == nil
					if allowed {
						lane.routeRefusals, lane.routeRetryAt = 0, time.Time{}
					} else {
						lane.routeRetryAt = now.Add(kata.WaitDelay(2*time.Second, lane.routeRefusals))
						lane.routeRefusals++
					}
				}
				checked[lane] = allowed
			}
			if !allowed {
				retry := now.Add(2 * time.Second)
				if lane.routeRetryAt.After(retry) {
					retry = lane.routeRetryAt
				}
				if next.IsZero() || next.After(retry) {
					next = retry
				}
				continue
			}
			p, err := decodePayload(*op)
			if err != nil {
				if err = q.inspect(uid, op.ID, err.Error()); err != nil {
					return err
				}
				continue
			}
			ref := p.Ref
			if op.Kind == "close" && p.Close != nil {
				ref = p.Close.Ref
			}
			if ref != "" {
				ref = r.Target.Identity.ProjectUID + "\x00" + ref
			}
			if ref != "" && busy[ref] {
				continue
			}
			if len(ready) < 2 {
				ready = append(ready, uid)
				busy[ref] = true
			}
		}
		if len(ready) > 0 {
			results := make(chan error, len(ready))
			for _, uid := range ready {
				go func() { results <- q.send(ctx, uid) }()
			}
			var batchErr error
			for range ready {
				batchErr = errors.Join(batchErr, <-results)
			}
			if batchErr != nil {
				if ctx.Err() != nil && errors.Is(batchErr, ctx.Err()) {
					return ctx.Err()
				}
				return batchErr
			}
			continue
		}
		var timer Timer
		var tick <-chan time.Time
		if !next.IsZero() {
			timer = q.clock.NewTimer(max(time.Duration(0), next.Sub(q.clock.Now())))
			tick = timer.C()
		}
		select {
		case <-ctx.Done():
		case <-q.wake:
		case <-tick:
		}
		if timer != nil {
			timer.Stop()
		}
	}
	return ctx.Err()
}

func (q *Queue) send(ctx context.Context, uid string) error {
	defer q.signal()
	if state.Quarantined(q.store.fsys, q.store.layout, uid) {
		return nil
	}
	r, err := q.store.Load(uid)
	if err != nil {
		return err
	}
	lane := q.forRun(r)
	if lane == nil {
		return nil
	}
	q = lane

	h := head(r)
	if !eligible(r, h) || h.Blocked() || h.NextAt.After(q.clock.Now()) {
		return nil
	}
	op := *h
	if r.Target.Identity.InstanceUID != "" {
		target, err := pinnedTargetAt(q.store, q.pinLayout)
		if err != nil {
			return q.failedRead(ctx, uid, op, err, true)
		}
		if !sameTarget(r.Target, target) {
			return q.inspect(uid, op.ID, "run target differs from pinned installation")
		}
	}
	originalIntent := intent(op)
	if op.Kind != "observe" {
		q.issueMu.Lock()
		defer q.issueMu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Discovery and shared-slot waits can outlive the scan's route check.
	// Probe outside shared delivery capacity and claim an immediately free
	// slot. A send that had to wait keeps the slot it waited for and rechecks
	// the route while holding it, so a freed slot costs one probe, not one
	// per waiting lane. A check younger than RouteReuse counts as fresh.
	if q.guard == nil {
		return nil
	}
	// A run prepared under one lease generation is admitted only while the
	// lane still holds it. Before this process first claims the lane, the
	// run waits; once the lane has held or holds another generation, it is
	// held. Its later writes settle a run already admitted.
	if lease := q.leaseFunc(); lease != nil && preAdmissionOp(op) && r.LeaseGeneration != 0 {
		generation, holds := lease()
		switch {
		case generation == 0:
			return q.deferOp(uid, op)
		case !holds || generation != r.LeaseGeneration:
			return q.admissionHold(uid, "the lane lease passed to another installation before admission")
		}
	}
	held := false
	release := func() {
		if held {
			<-q.writeSlots
			held = false
		}
	}
	for {
		if err := q.checkRoute(ctx); err != nil {
			release()
			if state.LocalError(err) {
				return err
			}
			return ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			release()
			return err
		}
		if held {
			break
		}
		select {
		case q.writeSlots <- struct{}{}:
			held = true
		default:
			select {
			case q.writeSlots <- struct{}{}:
				held = true
				continue // Recheck the route after the wait.
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		break
	}
	defer release()
	p, err := decodePayload(op)
	if err != nil {
		return q.inspect(uid, op.ID, err.Error())
	}
	if op.Safety != kata.SafetyOf(op.Kind) {
		return q.inspect(uid, op.ID, "incorrect operation safety class")
	}
	if op.ID == "admission" && r.Child != nil {
		issue, err := q.showIssue(ctx, uid, r.IssueRef)
		if err != nil {
			return q.failedRead(ctx, uid, op, err, true)
		}
		if err := inbox.ValidateNativeSource(issue); err != nil {
			return q.admissionHold(uid, err.Error())
		}
	}
	if r.Source == "abandon-request-report" {
		if proceed, e := q.lossReportPreflight(ctx, r, &op, p); e != nil || !proceed {
			return e
		}
	}
	if r.IssueRequest != nil {
		if proceed, e := q.requestPreflight(ctx, r, op, &p); e != nil || !proceed {
			return e
		}
	}
	if op.Kind == "resolve-recipient" {
		crashpoint.Hit("recipient.before")
		issue, e := q.showIssue(ctx, uid, p.Ref)
		crashpoint.Hit("recipient.after")
		if e != nil {
			return q.failedRead(ctx, uid, op, e, true)
		}
		to := p.To
		if to == "" {
			to = issue.Owner
			if to == "" {
				to = issue.Author
			}
		}
		if to == "" {
			return q.inspect(uid, op.ID, "notification recipient unavailable")
		}
		_, e = q.store.Transition(uid, func(current *Run) error {
			saved, e := findOp(current, op.ID)
			if e != nil {
				return e
			}
			if intent(*saved) != originalIntent || head(current) == nil || head(current).ID != op.ID {
				return errIntentChanged
			}
			found := false
			for i := range current.Ops {
				if current.Ops[i].ID == op.ID {
					found = true
					continue
				}
				if !found {
					continue
				}
				next := &current.Ops[i]
				if next.Kind != "notify" || next.FirstSent != nil {
					return errors.New("recipient op has no unsent notify")
				}
				np, e := decodePayload(*next)
				if e != nil {
					return e
				}
				np.To = to
				next.Payload, e = json.Marshal(np)
				if e != nil {
					return e
				}
				saved.Done = true
				return nil
			}
			return errors.New("recipient op has no notify")
		})
		if errors.Is(e, errIntentChanged) {
			return nil
		}
		return e
	}
	now := q.clock.Now()
	if op.Safety == kata.ReplaySafe && op.FirstSent != nil && !now.Before(op.FirstSent.Add(7*24*time.Hour)) {
		return q.inspect(uid, op.ID, "idempotency horizon expired")
	}
	if op.FirstSent != nil && !op.RetryIdentical && (op.Safety == kata.Unguarded || op.Safety == kata.ReadBack || requestReportReadBack(r, op)) {
		updated, e := q.store.Transition(uid, func(r *Run) error {
			saved, e := findOp(r, op.ID)
			if e != nil {
				return e
			}
			saved.Attempts++
			return nil
		})
		if e != nil {
			return e
		}
		saved, _ := findOp(updated, op.ID)
		op = *saved
		return q.resolve(ctx, uid, op, p, nil)
	}
	if op.Kind == "meta.set" || op.Kind == "meta" || op.Kind == "meta.unset" || op.Kind == "deadline" {
		owned := strings.HasPrefix(p.Name, "work.")
		if op.Guard == nil || op.Guard.IfMatch == "" || owned {
			issue, e := q.showIssue(ctx, uid, p.Ref)
			if e != nil {
				return q.failedRead(ctx, uid, op, e, true)
			}
			if owned && (!r.IssueOwned || !childIssueMatches(r, issue) || issue.Status != "open" || !reportingOwner(r, issue, p.Owner)) {
				return q.inspect(uid, op.ID, "attention ownership changed")
			}
			if op.Guard == nil {
				op.Guard = &kata.Cond{}
			}
			if op.Guard.IfMatch == "" {
				op.Guard.IfMatch = strconv.FormatInt(issue.Revision, 10)
			}
		}
	}
	if op.Kind == "close" && p.Close != nil {
		if op.Guard == nil {
			op.Guard = &kata.Cond{IfMatch: p.Close.IfMatch}
		}
		if op.Guard.IfMatch == "" || r.IssueOwned {
			issue, e := q.showIssue(ctx, uid, p.Close.Ref)
			if e != nil {
				return q.failedRead(ctx, uid, op, e, true)
			}
			if op.FirstSent != nil && issue.Status == "closed" {
				return q.complete(uid, op, nil)
			}
			owner := ""
			if takeover(r) != nil {
				owner = r.Target.Identity.Actor
			}
			if r.IssueOwned && (!childIssueMatches(r, issue) || issue.Status != "open" || !reportingOwner(r, issue, owner)) {
				return q.inspect(uid, op.ID, "close ownership changed")
			}
			if op.Guard.IfMatch == "" {
				op.Guard.IfMatch = strconv.FormatInt(issue.Revision, 10)
			}
		}
	}
	if op.Kind == "observe" && p.Observation != nil && op.FirstSent == nil {
		prepared, err := prepareObservation(ctx, q.k, *p.Observation)
		if err != nil {
			return q.failedRead(ctx, uid, op, err, true)
		}
		p.Observation = &prepared
		op.Payload, err = json.Marshal(p)
		if err != nil {
			return err
		}
	}
	// Saving the sending marker must succeed, including directory fsync, before
	// any mutation. A crash afterward makes unguarded operations read-back-only.
	_, err = q.store.Transition(uid, func(current *Run) error {
		saved, e := findOp(current, op.ID)
		if e != nil {
			return e
		}
		if currentHead := head(current); currentHead == nil || currentHead.ID != op.ID || saved.Done || saved.Blocked() || saved.NextAt.After(q.clock.Now()) || intent(*saved) != originalIntent {
			return errIntentChanged
		}
		saved.Guard = op.Guard
		saved.Payload = op.Payload
		if saved.FirstSent == nil {
			saved.FirstSent = &now
		}
		saved.Attempts++
		saved.RetryIdentical = false
		op = *saved
		return nil
	})
	if err != nil {
		if errors.Is(err, errIntentChanged) {
			return nil
		}
		return err
	}
	crashpoint.Hit("kata." + op.Kind + ".before")
	var issue *kata.IssueView
	var comment kata.Comment
	var replayed bool
	var e error
	if op.Kind == "comment" {
		if op.Key == "" {
			return q.inspect(uid, op.ID, "missing comment key")
		}
		comment, e = q.k.Comment(ctx, kata.CommentReq{Ref: p.Ref, Body: p.Body, IdempotencyKey: op.Key, ReplyTo: typedTarget(p), Kind: p.ReplyKind})
	} else if op.Kind == "observe" && p.Observation != nil {
		_, replayed, e = q.k.Observe(ctx, uid, *p.Observation)
	} else {
		issue, e = q.mutate(ctx, uid, op, p)
	}
	crashpoint.Hit("kata." + op.Kind + ".after")
	if e == nil {
		if op.Kind == "comment" && comment.UID == "" {
			return q.retry(uid, op)
		}
		if op.Kind == "comment" {
			target, kind := typedTarget(p), ""
			if target != "" {
				kind = p.ReplyKind
			}
			if comment.ReplyToUID != "" && comment.ReplyToUID != target || comment.ReplyKind != "" && comment.ReplyKind != kind {
				return q.inspect(uid, op.ID, "typed reply response mismatch")
			}
			if target != "" && (comment.ReplyToUID == "" || comment.ReplyKind == "") {
				slog.Warn("typed reply response omitted edge", "run", uid, "op", op.ID)
			}
		}
		op.Replayed = replayed
		return q.complete(uid, op, issue, comment)
	}
	if kata.Class(e) == kata.SigningRefused {
		if _, err := q.store.Transition(uid, func(r *Run) error {
			saved, err := findOp(r, op.ID)
			if err != nil {
				return err
			}
			saved.SigningRefused = true
			saved.RetryIdentical = true
			return nil
		}); err != nil {
			return err
		}
		return q.retry(uid, op)
	}
	if kata.NotFound(e) {
		if op.Kind == "create" && p.Create != nil && p.Create.Parent != "" {
			if _, err := q.store.Transition(uid, func(r *Run) error { r.Reason = "parent_unavailable"; return nil }); err != nil {
				return err
			}
		}
		q.probeAccess(ctx)
		if op.FirstSent != nil && !now.Before(op.FirstSent.Add(7*24*time.Hour)) {
			return q.inspect(uid, op.ID, "not-found unresolved for seven days")
		}
	}
	if op.ID == "admission" && r.IssueRequest == nil && kata.Class(e) == kata.Conflict {
		return q.rejectAdmission(ctx, uid)
	}
	if errors.Is(e, context.Canceled) {
		return e
	}
	var ke *kata.Error
	if op.Kind == "comment" && errors.As(e, &ke) {
		switch ke.Code {
		case "duplicate_reply":
			reason := "duplicate_reply: inspection required"
			if details := strings.TrimSpace(ke.ExistingHandle + " " + ke.ExistingUID); details != "" {
				reason += "; existing " + details
			}
			attrs := []any{"run", uid, "op", op.ID}
			if ke.ExistingUID != "" {
				attrs = append(attrs, "existing_uid", ke.ExistingUID)
			}
			if ke.ExistingHandle != "" {
				attrs = append(attrs, "existing_handle", ke.ExistingHandle)
			}
			slog.Warn("duplicate comment reply", attrs...)
			return q.inspect(uid, op.ID, reason)
		case "issue_closed", "comment_not_found", "idempotency_mismatch":
			return q.inspect(uid, op.ID, e.Error())
		case "daemon_api_too_old":
			return q.inspect(uid, op.ID, "daemon downgraded after typed operation was frozen: "+e.Error())
		}
	}
	if op.Kind == "close" && errors.As(e, &ke) && ke.Code == "validation" && p.Close != nil {
		return q.closeReview(uid, op, p)
	}
	if kata.Class(e) == kata.Fatal {
		if op.Kind == "create" && preAdmissionOp(op) && r.Phase == Prepared {
			_, err := q.store.Transition(uid, func(current *Run) error {
				holdAdmission(current, "create_rejected")
				current.Inspect = e.Error()
				saved, err := findOp(current, op.ID)
				if err != nil {
					return err
				}
				saved.Inspect = e.Error()
				return nil
			})
			q.signal()
			return err
		}
		return q.inspect(uid, op.ID, e.Error())
	}
	if kata.Class(e) == kata.Conflict || requestReportReadBack(r, op) || op.Safety == kata.Unguarded || op.Safety == kata.ReadBack || r.IssueRequest != nil && (p.Name == "herdr_kata.admission" || p.Name == "herdr_kata.outcome") {
		return q.resolve(ctx, uid, op, p, e)
	}
	return q.retry(uid, op)
}

func (q *Queue) mutate(ctx context.Context, _ string, op Op, p Payload) (*kata.IssueView, error) {
	switch op.Kind {
	case "observe":
		return nil, errors.New("missing observation")
	case "create":
		if p.Create == nil || op.Key == "" {
			return nil, errors.New("missing create request or key")
		}
		req := *p.Create
		req.IdempotencyKey = op.Key
		issue, err := q.k.CreateIssue(ctx, req)
		return &issue, err
	case "close":
		if p.Close == nil || op.Key == "" {
			return nil, errors.New("missing close request or key")
		}
		req := *p.Close
		req.IdempotencyKey = op.Key
		if op.Guard != nil {
			req.IfMatch = op.Guard.IfMatch
		}
		return nil, q.k.Close(ctx, req)
	case "meta", "meta.set":
		revision, err := q.k.MetaSet(ctx, p.Ref, p.Name, p.Value, *op.Guard)
		if op.ID == "admission" {
			return &kata.IssueView{Revision: revision}, err
		}
		return nil, err
	case "meta.unset":
		_, err := q.k.MetaUnset(ctx, p.Ref, p.Name, *op.Guard)
		return nil, err
	case "deadline":
		return nil, q.k.Deadline(ctx, p.Ref, p.At, *op.Guard)
	case "notify":
		return nil, q.k.Notify(ctx, p.Ref, p.To, p.Message)
	case "label", "label.add":
		return nil, q.k.LabelAdd(ctx, p.Ref, p.Name)
	case "claim":
		issue, err := q.k.Claim(ctx, p.Ref, p.IfUnowned)
		return &issue, err
	default:
		return nil, errors.New("unsupported operation kind")
	}
}
func (q *Queue) retry(uid string, op Op) error {
	delay := 2 * time.Second
	for n := 1; n < op.Attempts && delay < 5*time.Minute; n++ {
		delay = min(delay*2, 5*time.Minute)
	}
	_, err := q.store.Transition(uid, func(r *Run) error {
		saved, e := findOp(r, op.ID)
		if e != nil {
			return e
		}
		saved.NextAt = q.clock.Now().Add(delay)
		return nil
	})
	return err
}

// deferDelay paces a write waiting for the lane's first claim: 2 seconds
// just after a restart, then a quarter of the queue's age, up to five minutes.
func (q *Queue) deferDelay() time.Duration {
	return min(5*time.Minute, max(2*time.Second, q.clock.Now().Sub(q.started)/4))
}

// deferOp retries op after deferDelay without counting an attempt.
func (q *Queue) deferOp(uid string, op Op) error {
	_, err := q.store.Transition(uid, func(r *Run) error {
		saved, e := findOp(r, op.ID)
		if e != nil {
			return e
		}
		saved.NextAt = q.clock.Now().Add(q.deferDelay())
		return nil
	})
	return err
}

func (q *Queue) inspect(uid, id, why string) error {
	_, err := q.store.Transition(uid, func(r *Run) error {
		op, e := findOp(r, id)
		if e != nil {
			return e
		}
		op.Inspect = why
		r.Inspect = why
		return nil
	})
	return err
}
func (q *Queue) complete(uid string, op Op, issue *kata.IssueView, comments ...kata.Comment) error {
	_, err := q.store.Transition(uid, func(r *Run) error {
		saved, e := findOp(r, op.ID)
		if e != nil {
			return e
		}
		if op.Kind == "comment" {
			if len(comments) == 0 || comments[0].UID == "" {
				return errors.New("comment reply omitted UID")
			}
			saved.CommentUID = comments[0].UID
			p, e := decodePayload(op)
			if e != nil {
				return e
			}
			if p.Chain != nil {
				if r.Findings.RootUID == "" {
					r.Findings.RootUID = saved.CommentUID
				}
				r.Findings.HeadUID = saved.CommentUID
				if p.Chain.Purpose == "maker" {
					r.Findings.MakerUID = saved.CommentUID
				}
				if p.Chain.Assertion != "" {
					if r.Findings.Assertions == nil {
						r.Findings.Assertions = map[string][]string{}
					}
					r.Findings.Assertions[p.Chain.Target] = append(r.Findings.Assertions[p.Chain.Target], p.Chain.Assertion)
				}
			}
		}
		if issue != nil {
			rememberReference(r, *issue)
		}
		saved.Done = true
		saved.NextAt = time.Time{}
		saved.SigningRefused = false
		saved.RetryIdentical = false
		saved.Replayed = op.Replayed
		if op.ID == "admission" && r.Phase == Prepared {
			yes := true
			r.Admitted = &yes
			r.Phase = Admitted
			if ownership := takeover(r); ownership != nil {
				if r.IssueRequest != nil && issue != nil {
					ownership.Revision = issue.Revision
				}
				if ownership.Decision == "claimed" {
					r.Ops = append(r.Ops, newOp("child-claim", "claim", Payload{Ref: r.IssueRef, Owner: r.Target.Identity.Actor, IfUnowned: true}, ""))
				} else {
					r.Ops = append(r.Ops, childTracking(r))
				}
			}
			if r.Admission != nil && r.Admission.DeadlineAt != "" {
				r.Ops = append(r.Ops, newOp("deadline", "deadline", Payload{Ref: r.IssueRef, At: r.Admission.DeadlineAt}, ""))
			}
		}
		if op.Kind == "claim" && takeover(r) != nil {
			if issue == nil || !childIssueMatches(r, *issue) || issue.Owner != r.Target.Identity.Actor || issue.Status != "open" {
				saved.Done = false
				saved.Inspect = "child claim ownership changed"
				r.Inspect = saved.Inspect
				return nil
			}
			takeover(r).Revision = issue.Revision
			r.IssueOwned = true
			// Tracking immediately follows the claim, before any later tail write.
			for i := range r.Ops {
				if r.Ops[i].ID == op.ID {
					tail := append([]Op{childTracking(r)}, r.Ops[i+1:]...)
					r.Ops = append(r.Ops[:i+1], tail...)
					break
				}
			}
		}
		if issue != nil && issue.UID != "" && (op.Kind == "create" || op.Kind == "claim") {
			if op.Kind == "create" && r.Reason == "parent_unavailable" {
				r.Reason = ""
			}
			r.IssueUID = issue.UID
			r.IssueRef = issue.QualifiedID
			if r.IssueRef == "" {
				r.IssueRef = issue.UID
			}
			if r.Admission != nil && op.Kind == "create" {
				r.Admission.Body.IssueUID = issue.UID
			}
		}
		if op.Kind == "meta.set" || op.Kind == "meta" {
			p, e := decodePayload(op)
			if e != nil {
				return e
			}
			if p.Name == "work.attention" || p.Name == "work.attention_msg" {
				// Only consecutive identical intents can coalesce; an intervening write
				// may change the issue again, even when it is for a different key.
				for i := range r.Ops {
					if r.Ops[i].ID == op.ID {
						for j := i + 1; j < len(r.Ops); j++ {
							next := &r.Ops[j]
							if next.FirstSent != nil || next.Kind != op.Kind || !bytes.Equal(next.Payload, op.Payload) {
								break
							}
							next.Done = true
						}
						break
					}
				}
			}
		}
		return nil
	})
	return err
}
func (q *Queue) failedRead(ctx context.Context, uid string, op Op, err error, advance bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	if kata.NotFound(err) {
		q.probeAccess(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if op.FirstSent != nil && !q.clock.Now().Before(op.FirstSent.Add(7*24*time.Hour)) {
			return q.inspect(uid, op.ID, "not-found unresolved for seven days")
		}
	}
	if kata.Class(err) == kata.Retry {
		if advance {
			op.Attempts++
			if _, e := q.store.Transition(uid, func(r *Run) error {
				saved, e := findOp(r, op.ID)
				if e != nil {
					return e
				}
				saved.Attempts = op.Attempts
				return nil
			}); e != nil {
				return e
			}
		}
		return q.retry(uid, op)
	}
	return q.inspect(uid, op.ID, err.Error())
}

var errIntentChanged = errors.New("operation intent changed while preparing delivery")

func (q *Queue) resolve(ctx context.Context, uid string, op Op, p Payload, cause error) error {
	if op.ID == "admission" {
		r, err := q.store.Load(uid)
		if err != nil {
			return err
		}
		if r.IssueRequest == nil {
			return q.rejectAdmission(ctx, uid)
		}
		present, issue, err := q.readBack(ctx, uid, op, p, cause)
		if err != nil {
			return q.failedRead(ctx, uid, op, err, false)
		}
		if present {
			return q.complete(uid, op, issue)
		}
		return q.admissionHold(uid, "issue request admission not confirmed")
	}
	if strings.HasPrefix(p.Name, "work.") && kata.Class(cause) == kata.Conflict {
		r, err := q.store.Load(uid)
		if err != nil {
			return err
		}
		if takeover(r) != nil {
			return q.inspect(uid, op.ID, "child attention guard conflicted")
		}
	}
	present, issue, err := q.readBack(ctx, uid, op, p, cause)
	if err != nil {
		return q.failedRead(ctx, uid, op, err, false)
	}
	if present {
		return q.complete(uid, op, issue)
	}
	// The original sent intent remains immutable. Save a distinct force-new
	// request before its first send, and only after all look-alikes were read.
	var conflict *kata.Error
	if op.ID == "create" && op.Kind == "create" && p.Create != nil && !p.Create.ForceNew && errors.As(cause, &conflict) && conflict.Code == "duplicate_candidates" && len(conflict.CandidateUIDs) > 0 {
		req := *p.Create
		req.ForceNew = true
		_, err := q.store.Transition(uid, func(r *Run) error {
			saved, err := findOp(r, op.ID)
			if err != nil {
				return err
			}
			saved.Done = true
			r.Ops = append(r.Ops, newOp("create-force-new", "create", Payload{Create: &req}, op.Key))
			return nil
		})
		return err
	}
	return q.inspect(uid, op.ID, "operation effect not confirmed")
}
func (q *Queue) readBack(ctx context.Context, uid string, op Op, p Payload, cause error) (bool, *kata.IssueView, error) {
	if op.Kind == "observe" {
		got, err := q.k.ShowRun(ctx, uid)
		if err != nil {
			return false, nil, err
		}
		if p.Observation == nil {
			return false, nil, nil
		}
		want := p.Observation
		match := got.UID == uid && got.JobUID == want.JobUID && got.DefinitionEventUID == want.DefinitionEventUID && got.WorkflowUID == want.WorkflowUID && got.WorkflowDefinitionEventUID == want.WorkflowDefinitionEventUID && got.OccurrenceKey == want.OccurrenceKey && got.IssueUID == want.IssueUID && got.Actor == want.Actor && got.ExecutorLabel == want.ExecutorLabel
		match = match && observationProvenance(got, want) && got.Status == want.Status && got.Summary == want.Summary && sameTime(got.StartedAt, want.StartedAt) && sameTime(got.EndedAt, want.EndedAt)
		if strings.Contains(op.ID, ":resume:") && strings.HasSuffix(op.ID, ":observe") && want.Status == "running" {
			match = match && got.Revision == want.ExpectedRevision+1
		}
		teammate := "herdr-kata"
		if want.Teammate != nil {
			teammate = *want.Teammate
		}
		match = match && got.Teammate != nil && *got.Teammate == teammate
		return match, nil, nil
	}
	if op.Kind == "create" {
		var e *kata.Error
		if !errors.As(cause, &e) {
			return false, nil, nil
		}
		for _, candidate := range e.CandidateUIDs {
			issue, err := q.showIssue(ctx, uid, candidate)
			if err != nil {
				return false, nil, err
			}
			var value string
			if json.Unmarshal(issue.Metadata["herdr_kata.run"], &value) == nil && value == uid {
				return true, &issue, nil
			}
		}
		return false, nil, nil
	}
	ref := p.Ref
	if op.Kind == "close" && p.Close != nil {
		ref = p.Close.Ref
	}
	issue, err := q.showIssue(ctx, uid, ref)
	if err != nil {
		return false, nil, err
	}
	if p.Name == "herdr_kata.admission" || p.Name == "herdr_kata.outcome" {
		r, err := q.store.Load(uid)
		if err != nil {
			return false, nil, err
		}
		if r.Source == "abandon-request-report" && !lossReceiptMatches(r, issue) {
			return false, nil, nil
		}
		if r.IssueRequest != nil && (!requestReceiptMatches(r, issue) || p.Name == "herdr_kata.outcome" && issue.Owner != r.Target.Identity.Actor) {
			return false, nil, nil
		}
	}
	switch op.Kind {
	case "claim":
		return issue.Owner == p.Owner && p.Owner != "", &issue, nil
	case "meta", "meta.set":
		raw, exists := issue.Metadata[p.Name]
		if !exists {
			return false, nil, nil
		}
		if op.Guard != nil && op.Guard.JSONValue {
			var got, want any
			return decodeValue(raw, &got) == nil && decodeValue([]byte(p.Value), &want) == nil && reflect.DeepEqual(got, want), &issue, nil
		}
		var got string
		return json.Unmarshal(raw, &got) == nil && got == p.Value, nil, nil
	case "meta.unset":
		raw, exists := issue.Metadata[p.Name]
		if !exists {
			return true, nil, nil
		}
		r, err := q.store.Load(uid)
		if err != nil {
			return false, nil, err
		}
		if key, _ := savedNotification(r); (key != "" && p.Name == key || strings.HasPrefix(p.Name, "notify.")) && op.Guard != nil && op.Guard.IfValue != nil {
			var got, want any
			if decodeValue(raw, &got) == nil && decodeValue([]byte(*op.Guard.IfValue), &want) == nil && !reflect.DeepEqual(got, want) {
				return true, nil, nil
			}
		}
		return false, nil, nil
	case "notify":
		var request struct {
			Message  string `json:"message"`
			From     string `json:"from"`
			Teammate string `json:"teammate"`
		}
		err := json.Unmarshal(issue.Metadata["notify."+base64.RawURLEncoding.EncodeToString([]byte(p.To))], &request)
		r, e := q.store.Load(uid)
		if e != nil {
			return false, nil, e
		}
		return err == nil && request.Message == p.Message && request.From == r.Target.Identity.Actor && request.Teammate == "herdr-kata", nil, nil
	case "deadline":
		raw, exists := issue.Metadata["deadline_on"]
		if p.At == "-" {
			return !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")), nil, nil
		}
		var got string
		return json.Unmarshal(raw, &got) == nil && got == p.At, nil, nil
	case "label", "label.add":
		if slices.Contains(issue.Labels, p.Name) {
			return true, nil, nil
		}
	case "close":
		return issue.Status == "closed", nil, nil
	}
	// Comments have no authoritative read-back surface in the adapter. A
	// conflict requires inspection rather than a fabricated new request.
	return false, nil, nil
}
func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
func decodeValue(raw []byte, v *any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	return d.Decode(v)
}

// A definitive validation refusal is an unchanged issue, so replace only the
// unsent settlement tail with a review handoff. The sent close intent remains
// immutable; successful execution and its final observation are preserved.
func (q *Queue) closeReview(uid string, op Op, p Payload) error {
	_, e := q.store.Transition(uid, func(r *Run) error {
		for i := range r.Ops {
			if r.Ops[i].ID != op.ID {
				continue
			}
			if intent(r.Ops[i]) != intent(op) {
				return errIntentChanged
			}
			review := settleOps(r, &Result{Status: "succeeded", Summary: p.Close.Message, CloseReason: p.Close.Reason, Evidence: p.Close.Evidence}, q.clock.Now(), true)
			var added []Op
			for _, next := range review {
				if next.Kind == "observe" || next.Kind == "meta.unset" {
					continue
				}
				exists := false
				for _, saved := range r.Ops {
					if saved.ID == next.ID {
						exists = true
						break
					}
				}
				if !exists {
					added = append(added, next)
				}
			}
			r.Ops[i].Done = true
			tail := append(added, r.Ops[i+1:]...)
			r.Ops = append(r.Ops[:i+1], tail...)
			return nil
		}
		return errors.New("close operation disappeared")
	})
	return e
}

func holdAdmission(r *Run, reason string) {
	no := false
	r.Admitted = &no
	r.Phase = Held
	r.Reason = reason
	r.Inspect = reason
	r.SlotReleased = true
}
func (q *Queue) admissionHold(uid, reason string) error {
	_, err := q.store.Transition(uid, func(r *Run) error {
		if r.Phase == Prepared {
			holdAdmission(r, "admission_rejected")
			r.Inspect = reason
		}
		return nil
	})
	q.signal()
	return err
}

func (q *Queue) probeAccess(ctx context.Context) {
	if g, ok := q.guard.(interface{ ProbeAccess(context.Context) error }); ok {
		_ = g.ProbeAccess(ctx)
	}
}

// SigningBlocked is derived from durable unresolved operations on each admission.
func (s *Store) SigningBlocked() (bool, error) { return s.SigningBlockedFor("") }
func (s *Store) SigningBlockedFor(project string) (bool, error) {
	for _, store := range []*Store{s, NewStore(s.fsys, abandonReportsLayout(s.layout))} {
		blocked, err := store.signingBlockedIn(project)
		if err != nil || blocked {
			return blocked, err
		}
	}
	return false, nil
}

func (s *Store) signingBlockedIn(project string) (bool, error) {
	ids, err := s.projectCandidates(context.Background(), project)
	if err != nil {
		return false, err
	}
	for _, uid := range ids {
		r, e := s.Load(uid)
		if missing(e) || s.quarantinedError(uid, e) {
			continue
		}
		if e != nil {
			return false, e
		}
		if project != "" && r.Target.Identity.ProjectUID != project {
			continue
		}
		for _, op := range r.Ops {
			if op.SigningRefused {
				return true, nil
			}
		}
	}
	return false, nil
}

func typedTarget(p Payload) string {
	if p.Mode == "typed" {
		return p.ReplyTo
	}
	return ""
}

func (q *Queue) register(uid string, k Kata, g RouteChecker, l state.Layout) *Queue {
	q.lanesMu.Lock()
	defer q.lanesMu.Unlock()
	lane := &Queue{k: k, store: q.store, guard: g, clock: q.clock, pinLayout: l, queueShared: q.queueShared, projectUID: uid, wake: make(chan struct{}, 1), issueMu: &sync.Mutex{}}
	q.lanes[uid] = lane
	lane.wake <- struct{}{}
	select {
	case q.rootWake <- struct{}{}:
	default:
	}
	return lane
}
func (q *Queue) forRun(r *Run) *Queue {
	if !q.projects {
		return q
	}
	q.lanesMu.RLock()
	defer q.lanesMu.RUnlock()
	return q.lanes[r.Target.Identity.ProjectUID]
}

// Route checks belong to their lane. A slow or unavailable endpoint cannot
// block another project's admission; all delivery shares two CLI write slots.
func (q *Queue) runProjects(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	started := map[string]bool{}
	failures := make(chan error, 8)
	for {
		q.lanesMu.RLock()
		for uid, lane := range q.lanes {
			if !started[uid] {
				started[uid] = true
				wg.Go(func() {
					err := lane.Run(ctx)
					select {
					case failures <- err:
					case <-ctx.Done():
					}
				})
			}
		}
		q.lanesMu.RUnlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-failures:
			return err
		case <-q.rootWake:
		}
	}
}

// rejectAdmission holds a run whose admission Kata refused. When another
// installation admitted the occurrence, the inspection names it; the lane
// lease, not this conflict, decides which installation serves the lane.
func (q *Queue) rejectAdmission(ctx context.Context, uid string) error {
	// Read the winner once; a failed or hidden read grants no admission authority.
	winner, readErr := q.k.ShowRun(ctx, uid)
	local, err := q.store.Load(uid)
	if err != nil {
		return err
	}
	if readErr != nil || winner.UID != uid || winner.ExecutorLabel == "" || winner.ExecutorLabel == local.Target.Executor {
		return q.admissionHold(uid, "admission_rejected")
	}
	_, err = q.store.Transition(uid, func(r *Run) error {
		if r.Phase != Prepared {
			return nil
		}
		holdAdmission(r, "admission_rejected")
		r.Inspect = "another installation (" + winner.ExecutorLabel + ") admitted this occurrence"
		return nil
	})
	q.signal()
	return err
}
