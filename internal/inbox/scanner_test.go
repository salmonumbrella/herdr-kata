package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

type scanReader struct {
	workflowReader
	requests   []kata.InboxRequest
	issues     map[string]kata.IssueView
	ready      []kata.IssueView
	readyCalls int
	inboxErr   error
	readyErr   error
	showErrors map[string]error
	onInbox    func()
	planning   map[string]kata.PlanningDates
	planReads  []string
	planErr    error
}

func (r *scanReader) Inbox(_ context.Context, address string) ([]kata.InboxRequest, error) {
	if address != "worker/inbox" {
		return nil, errors.New("wrong inbox")
	}
	if r.onInbox != nil {
		r.onInbox()
	}
	return r.requests, r.inboxErr
}
func (r *scanReader) Ready(context.Context) ([]kata.IssueView, error) {
	r.readyCalls++
	return r.ready, r.readyErr
}
func (r *scanReader) ShowIssue(_ context.Context, ref string) (kata.IssueView, error) {
	if err := r.showErrors[ref]; err != nil {
		return kata.IssueView{}, err
	}
	if i, ok := r.issues[ref]; ok {
		return i, nil
	}
	return kata.IssueView{}, &kata.Error{Code: "not_found"}
}

type scanClock struct {
	mu     sync.Mutex
	now    time.Time
	timers chan *scanTimer
}
type scanTimer struct {
	at time.Time
	c  chan time.Time
}

func (c *scanClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *scanClock) NewTimer(d time.Duration) sched.Timer {
	t := &scanTimer{at: c.Now().Add(d), c: make(chan time.Time, 1)}
	c.timers <- t
	return t
}
func (t *scanTimer) C() <-chan time.Time { return t.c }
func (t *scanTimer) Stop()               {}
func (c *scanClock) fire(t *scanTimer)   { c.mu.Lock(); c.now = t.at; c.mu.Unlock(); t.c <- t.at }
func nextTimer(t *testing.T, c *scanClock) *scanTimer {
	t.Helper()
	select {
	case timer := <-c.timers:
		return timer
	case <-time.After(3 * time.Second):
		t.Fatal("scanner did not schedule a wake")
		return nil
	}
}
func scanFixture(t *testing.T) (*Scanner, *scanReader, *scanClock, chan sched.Due) {
	t.Helper()
	c := validChild()
	c.Metadata["notify.d29ya2VyL2luYm94"] = json.RawMessage(`{"from":"system","teammate":"cron","message":"due"}`)
	r := &scanReader{requests: []kata.InboxRequest{{Ref: c.QualifiedID, Message: "untrusted prose"}}, issues: map[string]kata.IssueView{c.QualifiedID: c, parentUID: {UID: parentUID, ProjectUID: projectUID}}, ready: []kata.IssueView{c}}
	clock := &scanClock{now: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC), timers: make(chan *scanTimer, 30)}
	got := make(chan sched.Due, 10)
	cfg := bindingConfig()
	cfg.Kata.Actor = "worker"
	cfg.Kata.Inbox = "worker/inbox"
	s := New(cfg, r, kata.Capabilities{ProjectUID: projectUID, EventFeatures: []string{"cron_v1"}}, func(_ context.Context, d sched.Due) { got <- d }, clock)
	return s, r, clock, got
}
func startScanner(t *testing.T, s *Scanner) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("scanner failed to stop")
		}
	})
}
func takeDue(t *testing.T, got chan sched.Due) sched.Due {
	t.Helper()
	select {
	case d := <-got:
		return d
	case <-time.After(3 * time.Second):
		t.Fatal("validated child was not dispatched")
		return sched.Due{}
	}
}
func TestScannerDebounceAndSlowScan(t *testing.T) {
	s, _, clock, got := scanFixture(t)
	for range 20 {
		s.Poke()
	}
	startScanner(t, s)
	initial := nextTimer(t, clock)
	if initial.at.Sub(clock.Now()) != 2*time.Second {
		t.Fatal("initial scan not debounced")
	}
	debounce := nextTimer(t, clock)
	clock.fire(debounce)
	takeDue(t, got)
	slow := nextTimer(t, clock)
	if !fallbackWake(clock, slow) {
		t.Fatal("wrong slow scan deadline", slow.at)
	}
	clock.fire(slow)
	takeDue(t, got)
}
func TestScannerPokeDuringScanSurvives(t *testing.T) {
	s, r, clock, got := scanFixture(t)
	r.onInbox = s.Poke
	startScanner(t, s)
	clock.fire(nextTimer(t, clock))
	takeDue(t, got)
	// The loop creates a slow timer, then handles the poke received in Inbox.
	nextTimer(t, clock)
	debounce := nextTimer(t, clock)
	if debounce.at.Sub(clock.Now()) != 2*time.Second {
		t.Fatal("poke during scan was lost")
	}
	clock.fire(debounce)
	takeDue(t, got)
}
func TestScannerCronV1DispatchesRecurringChild(t *testing.T) {
	s, r, clock, got := scanFixture(t)
	s = New(s.cfg, r, kata.Capabilities{ProjectUID: projectUID, EventFeatures: []string{"cron_v1"}}, s.dispatch, clock)
	if !s.Enabled() {
		t.Fatal("cron_v1-only scanner is inactive")
	}
	startScanner(t, s)
	clock.fire(nextTimer(t, clock))
	due := takeDue(t, got)
	if due.Child == nil || due.Child.IssueUID != childUID || due.Child.Binding.Profile != "default" || due.Child.NotifyKey != "notify.d29ya2VyL2luYm94" {
		t.Fatalf("cron_v1 recurring dispatch lost delivery inputs: %+v", due.Child)
	}
}

func TestScannerDisabled(t *testing.T) {
	for _, tc := range []struct {
		inbox    string
		features []string
	}{{"", []string{"cron_v1"}}, {"worker/inbox", nil}, {"worker/inbox", []string{"unrelated_feature"}}} {
		cfg := bindingConfig()
		cfg.Kata.Inbox = tc.inbox
		s := New(cfg, nil, kata.Capabilities{ProjectUID: projectUID, EventFeatures: tc.features}, nil, nil)
		if s.Enabled() {
			t.Fatal("disabled scanner enabled")
		}
		s.Poke()
		if err := s.Run(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}
func TestChildUsesHistoricalDeliveryPair(t *testing.T) {
	s, r, clock, got := scanFixture(t)
	// Reader has no ShowJob: a current delivery definition cannot replace the pair.
	r.requests = append(r.requests, r.requests[0])
	startScanner(t, s)
	clock.fire(nextTimer(t, clock))
	d := takeDue(t, got)
	nextTimer(t, clock)
	if d.Child == nil || d.Child.DeliveryJobUID != jobUID || d.Child.DeliveryEventUID != eventUID || d.Child.IssueUID != childUID || d.Child.IssueRef != "test#cw1k" || d.Occurrence.Format(time.RFC3339Nano) != "2026-10-13T08:00:00.123Z" || d.Child.NotifyKey != "notify.d29ya2VyL2luYm94" || !d.Child.Scheduled.Equal(d.Occurrence) || d.Child.Binding.Profile != "default" {
		t.Fatalf("%+v", d.Child)
	}
	select {
	case <-got:
		t.Fatal("duplicate inbox row dispatched twice in a batch")
	default:
	}
}
func TestScannerRejectsUnsupportedChildren(t *testing.T) {
	for _, change := range []func(*scanReader){
		func(r *scanReader) { r.ready = nil },
		func(r *scanReader) { c := r.issues["test#cw1k"]; c.Owner = "foreign"; r.issues["test#cw1k"] = c },
		func(r *scanReader) { c := r.issues["test#cw1k"]; delete(c.Metadata, "cron.occurrence") },
		func(r *scanReader) { c := r.issues["test#cw1k"]; delete(c.Metadata, "notify.d29ya2VyL2luYm94") },
		func(r *scanReader) { p := r.issues[parentUID]; p.ProjectUID = "other"; r.issues[parentUID] = p },
		func(r *scanReader) {
			p := r.issues[parentUID]
			p.Metadata = map[string]json.RawMessage{"herdr_kata.checkout": json.RawMessage(`"unknown"`)}
			r.issues[parentUID] = p
		},
	} {
		s, r, _, got := scanFixture(t)
		change(r)
		var snapshot Snapshot
		s.SaveSnapshot = func(v Snapshot) error { snapshot = v; return nil }
		if err := s.scan(t.Context()); err != nil {
			t.Fatal(err)
		}
		select {
		case <-got:
			t.Fatal("unsupported child dispatched")
		default:
		}
		if len(snapshot.Requests) != 1 || snapshot.Requests[0].Ref != "test#cw1k" || snapshot.Requests[0].Reason == "" || snapshot.Requests[0].Status == Accepted {
			t.Fatal("missing rejected-request status", snapshot)
		}
	}
}
func TestScannerReadFailureDoesNotAuthorize(t *testing.T) {
	for _, onError := range []func(*scanReader){func(r *scanReader) { r.inboxErr = errors.New("offline") }, func(r *scanReader) { r.readyErr = errors.New("offline") }} {
		s, r, clock, got := scanFixture(t)
		onError(r)
		startScanner(t, s)
		clock.fire(nextTimer(t, clock))
		nextTimer(t, clock)
		select {
		case <-got:
			t.Fatal("read failure authorized execution")
		default:
		}
	}
}

func TestScannerPendingChildBecomesReady(t *testing.T) {
	s, r, _, got := scanFixture(t)
	r.ready = nil
	var snapshot Snapshot
	s.SaveSnapshot = func(v Snapshot) error { snapshot = v; return nil }
	if err := s.scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Requests) != 1 || snapshot.Requests[0].Status != WaitingReady {
		t.Fatal(snapshot)
	}
	select {
	case <-got:
		t.Fatal("unready child dispatched")
	default:
	}
	r.ready = []kata.IssueView{r.issues["test#cw1k"]}
	if err := s.scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	takeDue(t, got)
	if len(snapshot.Requests) != 1 || snapshot.Requests[0].Status != Accepted || snapshot.Requests[0].Reason != "" {
		t.Fatal("ready child is still waiting", snapshot)
	}
}

func TestScannerBatchIsolatesInvalidSibling(t *testing.T) {
	s, r, _, got := scanFixture(t)
	prose := kata.IssueView{UID: workflowUID, ProjectUID: projectUID, Status: "open", Metadata: map[string]json.RawMessage{"notify.d29ya2VyL2luYm94": json.RawMessage(`{"message":"please work"}`)}}
	r.issues["test#prose"] = prose
	r.requests = append([]kata.InboxRequest{{Ref: "test#prose", Message: "Work this now"}}, r.requests...)
	r.ready = append(r.ready, prose)
	var snapshot Snapshot
	s.SaveSnapshot = func(v Snapshot) error { snapshot = v; return nil }
	if err := s.scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	takeDue(t, got)
	if r.readyCalls != 1 {
		t.Fatal("readiness was not batched", r.readyCalls)
	}
	if len(snapshot.Requests) != 2 || snapshot.Requests[0].Ref != "test#prose" || snapshot.Requests[0].Status != IgnoredRequest || snapshot.Requests[1].Status != Accepted {
		t.Fatal(snapshot)
	}
}

func TestOversizedChildInputUnsupported(t *testing.T) {
	s, r, _, got := scanFixture(t)
	child := r.issues[r.requests[0].Ref]
	child.Body = strings.Repeat("x", 4097)
	r.issues[r.requests[0].Ref] = child
	if e := s.scan(t.Context()); e != nil {
		t.Fatal(e)
	}
	select {
	case <-got:
		t.Fatal("oversized child body dispatched")
	default:
	}
}

func TestScannerKeepsReferencesFromExistingReads(t *testing.T) {
	s, r, clock, got := scanFixture(t)
	s.cfg.Kata.Project = "example-project"
	child := r.ready[0]
	child.QualifiedID = "example-project#child5"
	// Keep the request lookup stable while the handled response supplies its ref.
	original := r.requests[0].Ref
	r.issues[original] = child
	parent := r.issues[parentUID]
	parent.ShortID = "parent5"
	r.issues[parentUID] = parent
	startScanner(t, s)
	clock.fire(nextTimer(t, clock))
	d := takeDue(t, got)
	if d.IssueRefs[child.UID] != "example-project#child5" || d.IssueRefs[parentUID] != "example-project#parent5" || d.Child.Binding.ParentRef != "example-project#parent5" {
		t.Fatal(d.IssueRefs)
	}
}

// fallbackWake reports a jittered ten-minute fallback scan.
func fallbackWake(clock *scanClock, timer *scanTimer) bool {
	d := timer.at.Sub(clock.Now())
	return d >= 10*time.Minute && d < 12*time.Minute
}

// Lanes started together must not run their fallback scans together.
func TestFallbackDelaysSpread(t *testing.T) {
	seen := map[time.Duration]bool{}
	for range 20 {
		d := FallbackDelay()
		if d < 10*time.Minute || d >= 12*time.Minute {
			t.Fatal(d)
		}
		seen[d] = true
	}
	if len(seen) < 2 {
		t.Fatal("fallback delay is not jittered")
	}
}

// Issue events wake the scanner only while a request waits on its child.
func TestPokeWaitingScansOnlyForWaitingRequests(t *testing.T) {
	s, r, _, got := scanFixture(t)
	if err := s.scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	takeDue(t, got)
	s.PokeWaiting()
	if len(s.poke) != 0 {
		t.Fatal("issue event scanned with nothing waiting")
	}
	r.ready = nil
	if err := s.scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.PokeWaiting()
	if len(s.poke) != 1 {
		t.Fatal("issue event ignored a waiting request")
	}
}
