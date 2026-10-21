package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

type Reader interface {
	BindingReader
	Inbox(context.Context, string) ([]kata.InboxRequest, error)
	Ready(context.Context) ([]kata.IssueView, error)
	ShowIssue(context.Context, string) (kata.IssueView, error)
	PlanningDates(context.Context, string) (kata.PlanningDates, error)
}

var _ Reader = (*kata.CLI)(nil)

// Scanner has one Run owner. Poke is safe from other goroutines.
// The callback owns durable admission, capacity and duplicate-run detection.
type Scanner struct {
	cfg        config.Config
	reader     Reader
	projectUID string
	enabled    bool
	dispatch   func(context.Context, sched.Due)
	clock      sched.Clock
	poke       chan struct{}
	snapshot   Snapshot
	// waiting is set while the last scan left a request that a later issue
	// event may make runnable; PokeWaiting skips the scan otherwise.
	waiting atomic.Bool
	// scheduled is the earliest future scheduled_on instant of a waiting
	// explicit request, from the last scan.
	scheduled time.Time
	// dates remembers each waiting request child's scheduled_on instant by
	// child revision, so scans re-read it only after the child changes.
	dates, nextDates map[string]scheduledDate
	// SaveSnapshot persists the bounded read model; set before Run.
	SaveSnapshot func(Snapshot) error
	// CheckRequest classifies durable admission evidence before readiness.
	CheckRequest func(context.Context, kata.IssueView) (status, reason string, err error)
}

func New(cfg config.Config, reader Reader, caps kata.Capabilities, dispatch func(context.Context, sched.Due), clock sched.Clock) *Scanner {
	if clock == nil {
		clock = sched.RealClock{}
	}
	enabled := false
	for _, feature := range caps.EventFeatures {
		if feature == "cron_v1" {
			enabled = true
		}
	}
	return &Scanner{cfg: cfg, reader: reader, projectUID: caps.ProjectUID, enabled: enabled && strings.TrimSpace(cfg.Kata.Inbox) != "", dispatch: dispatch, clock: clock, poke: make(chan struct{}, 1)}
}
func (s *Scanner) Enabled() bool { return s.enabled }
func (s *Scanner) Poke() {
	if !s.enabled {
		return
	}
	select {
	case s.poke <- struct{}{}:
	default:
	}
}

// PokeWaiting wakes the scanner for an issue event only when the last scan
// left a request waiting, so unrelated issue activity costs no inbox read.
func (s *Scanner) PokeWaiting() {
	if s.waiting.Load() {
		s.Poke()
	}
}

// FallbackDelay spaces fallback scans ten to twelve minutes apart, so lanes
// started together do not scan together.
func FallbackDelay() time.Duration {
	return 10*time.Minute + rand.N(2*time.Minute)
}

func (s *Scanner) Run(ctx context.Context) error {
	if !s.enabled {
		return nil
	}
	if s.reader == nil || s.dispatch == nil || !validUID(s.projectUID) {
		return fmt.Errorf("enabled inbox scanner requires a reader, callback and project UID")
	}
	now := s.clock.Now()
	dirty, debounce, slow := true, now.Add(2*time.Second), now.Add(FallbackDelay())
	for {
		wake := slow
		// Kata's ready set stays the gate; the extra second absorbs clock
		// differences at the instant itself.
		if at := s.scheduled.Add(time.Second); at.After(s.clock.Now()) && at.Before(wake) {
			wake = at
		}
		if dirty && debounce.Before(wake) {
			wake = debounce
		}
		delay := max(wake.Sub(s.clock.Now()), 0)
		timer := s.clock.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-s.poke:
			timer.Stop()
			dirty, debounce = true, s.clock.Now().Add(2*time.Second)
		case <-timer.C():
			timer.Stop()
			if ctx.Err() != nil {
				return nil
			}
			if err := s.scan(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("inbox scan failed", "error", err)
			}
			dirty, slow = false, s.clock.Now().Add(FallbackDelay())
		}
	}
}

func (s *Scanner) scan(ctx context.Context) (scanErr error) {
	snapshot := s.snapshot
	previous := s.scheduled
	s.scheduled = time.Time{}
	waiting := false
	s.nextDates = map[string]scheduledDate{}
	defer func() {
		// A scan forgets children it no longer saw; a failed one forgets nothing.
		if scanErr == nil || s.dates == nil {
			s.dates = s.nextDates
		} else {
			maps.Copy(s.dates, s.nextDates)
		}
		s.nextDates = nil
		s.waiting.Store(waiting || scanErr != nil)
		if scanErr != nil {
			// A failed scan keeps the earlier wake; an extra scan is harmless.
			if !previous.IsZero() && (s.scheduled.IsZero() || previous.Before(s.scheduled)) {
				s.scheduled = previous
			}
			snapshot.Error = snapshotText(scanErr.Error())
		} else {
			snapshot.Error = ""
			snapshot.LastSuccessfulScan = s.clock.Now().UTC()
		}
		s.snapshot = snapshot
		if s.SaveSnapshot != nil {
			scanErr = errors.Join(scanErr, s.SaveSnapshot(snapshot))
		}
	}()
	if snapshot.Version == 0 {
		snapshot.Version = 1
		snapshot.Requests = []Request{}
	}

	requests, err := s.reader.Inbox(ctx, s.cfg.Kata.Inbox)
	if err != nil {
		return err
	}
	if len(requests) == 0 {
		snapshot.Requests = []Request{}
		return nil
	}
	rows, err := s.reader.Ready(ctx)
	if err != nil {
		return err
	}
	snapshot.Requests = []Request{}
	snapshotBytes := 0
	record := func(r Request) {
		waiting = waiting || r.Status != Accepted
		r.Title, r.Reason = snapshotText(r.Title), snapshotText(r.Reason)
		b, err := json.Marshal(r)
		if err == nil && len(snapshot.Requests) < MaxSnapshotRequests && snapshotBytes+len(b) <= 3<<20 {
			snapshot.Requests = append(snapshot.Requests, r)
			snapshotBytes += len(b) + 1
		}
	}
	ready := make(map[string]bool, len(rows))
	for _, i := range rows {
		if i.Status == "open" && i.ProjectUID == s.projectUID {
			ready[i.UID] = true
		}
	}
	seenRefs, seenUIDs := map[string]bool{}, map[string]bool{}
	for _, request := range requests {
		if request.Ref == "" {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if seenRefs[request.Ref] {
			continue
		}
		seenRefs[request.Ref] = true
		due, status, err := s.child(ctx, request.Ref, ready)
		if err != nil {
			record(Request{Ref: request.Ref, Title: request.Title, Status: status, Reason: err.Error()})
			continue
		}
		uid := due.ChildUID()
		if seenUIDs[uid] {
			continue
		}
		seenUIDs[uid] = true
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.dispatch(ctx, due)
		record(Request{Ref: request.Ref, Title: request.Title, Status: status})
	}
	return nil
}

func (s *Scanner) child(ctx context.Context, ref string, ready map[string]bool) (sched.Due, string, error) {
	// Reads and admission checks can return either unsupported requests or
	// transient failures; only validation and readiness have special statuses.
	reject := func(err error) (sched.Due, string, error) {
		if errors.Is(err, ErrUnsupported) {
			return sched.Due{}, Unsupported, err
		}
		return sched.Due{}, IgnoredRequest, err
	}
	child, err := s.reader.ShowIssue(ctx, ref)
	if err != nil {
		return reject(err)
	}
	var request *sched.ExecuteRequest
	var o Occurrence
	if _, explicit := child.Metadata["herdr_kata.request"]; explicit {
		parsed, e := ValidateRequest(child, s.projectUID)
		err = e
		request = &parsed
	} else {
		o, err = Validate(child, s.projectUID)
	}
	if err != nil {
		return sched.Due{}, IgnoredRequest, err
	}
	if request != nil && s.CheckRequest != nil {
		status, reason, err := s.CheckRequest(ctx, child)
		if err != nil {
			return sched.Due{}, IgnoredRequest, errors.Join(errors.New(reason), err)
		}
		if status != "" {
			return sched.Due{}, status, errors.New(reason)
		}
	}
	if !ready[child.UID] {
		if request != nil {
			if at, ok := s.scheduledAt(ctx, ref, child); ok {
				return sched.Due{}, WaitingReady, fmt.Errorf("scheduled for %s", at.Format(time.RFC3339))
			}
		}
		return sched.Due{}, WaitingReady, errors.New("child is not ready")
	}
	decision, _, err := Takeover(child, s.cfg.Kata.Actor)
	if err != nil {
		return reject(err)
	}
	key, saved, err := AckOp(child, s.cfg.Kata.Inbox)
	if err != nil {
		return reject(err)
	}
	parentUID, err := parentOf(child)
	if err != nil {
		return reject(err)
	}
	parent, err := s.reader.ShowIssue(ctx, parentUID)
	if err != nil {
		return reject(err)
	}
	if parent.UID != parentUID || parent.ProjectUID != s.projectUID || request != nil && parent.Status != "open" {
		return sched.Due{}, Unsupported, fmt.Errorf("%w: parent must be in the configured project", ErrUnsupported)
	}
	binding, err := ResolveBinding(ctx, s.reader, parent, s.cfg)
	if err != nil {
		return reject(err)
	}
	if _, e := defs.EffectiveInput(nil, nil, &child.Body, binding.WFDef != nil && binding.WFDef.Input != ""); e != nil {
		return sched.Due{}, Unsupported, fmt.Errorf("%w: %v", ErrUnsupported, e)
	}
	issueRef := child.QualifiedID
	if issueRef == "" {
		issueRef = child.UID
	}
	refs := map[string]string{}
	for _, issue := range []kata.IssueView{child, parent} {
		if ref := kata.IssueReference(s.cfg.Kata.Project, issue); ref != "" {
			refs[issue.UID] = ref
		}
	}
	due := sched.Due{IssueRefs: refs, Occurrence: o.ScheduledAt, Child: &sched.ChildDue{IssueUID: child.UID, IssueRef: issueRef, Scheduled: o.ScheduledAt, DeliveryJobUID: o.JobUID, DeliveryEventUID: o.DefinitionEventUID, Binding: binding, NotifyKey: key, NotifyJSON: saved}}
	if request != nil {
		due.Child = nil
		due.IssueRequest = &sched.IssueRequestDue{IssueUID: child.UID, IssueRef: issueRef, Request: *request, ParentUID: parentUID, Binding: binding, Ownership: Ownership{Decision: decision, Revision: child.Revision}, NotifyKey: key, NotifyJSON: saved}
	}
	return due, Accepted, nil
}

type scheduledDate struct {
	revision int64
	value    string
	at       time.Time // zero when the child has no scheduled instant
}

// scheduledAt returns a request child's future scheduled_on instant and keeps
// the earliest one as the next wake. The instant is read once per child
// revision and scheduled_on value. A reschedule changes both, so the next
// scan re-reads it, replaces the wake, and the old date never fires.
func (s *Scanner) scheduledAt(ctx context.Context, ref string, child kata.IssueView) (time.Time, bool) {
	raw, ok := child.Metadata["scheduled_on"]
	if !ok {
		return time.Time{}, false
	}
	date, ok := s.dates[child.UID]
	if !ok || date.revision != child.Revision || date.value != string(raw) {
		dates, err := s.reader.PlanningDates(ctx, ref)
		if err != nil {
			slog.Warn("scheduled request planning dates", "issue", ref, "error", err)
			return time.Time{}, false
		}
		date = scheduledDate{revision: child.Revision, value: string(raw)}
		if dates.Scheduled != nil {
			date.at = dates.Scheduled.Instant.UTC()
		}
	}
	if s.nextDates != nil {
		s.nextDates[child.UID] = date
	}
	if date.at.IsZero() || !date.at.After(s.clock.Now()) {
		return time.Time{}, false
	}
	at := date.at
	if s.scheduled.IsZero() || at.Before(s.scheduled) {
		s.scheduled = at
	}
	return at, true
}

// External display text is bounded independently of executable input and guards.
func snapshotText(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) > 1024 {
		s = s[:1024]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}
