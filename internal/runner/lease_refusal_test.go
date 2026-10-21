package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/lease"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

const elsewhereRefusal = "another installation (elsewhere) owns this lane: lease generation 2"

func writeRequest(t *testing.T, s *Store, req Request) string {
	t.Helper()
	p := filepath.Join(s.layout.RequestsDir(), req.UID+".json")
	if err := state.WriteJSON(s.fsys, p, req); err != nil {
		t.Fatal(err)
	}
	return p
}

// requireLeaseRefusal checks that the request settled with the lease refusal
// and left no request behind.
func requireLeaseRefusal(t *testing.T, s *Store, path string, req Request) {
	t.Helper()
	var result RequestResult
	if err := state.ReadJSON(s.fsys, RequestResultPath(s.layout, req.UID), 4096, &result); err != nil {
		t.Fatal("no request result:", err)
	}
	if result.Error != elsewhereRefusal || result.Reason != ReasonLeaseHeldElsewhere || result.RunUID != req.RunUID || result.Kind != req.Kind {
		t.Fatalf("result %+v", result)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("refused request remains", err)
	}
}

func manualJobKata(f *admissionFixture) {
	f.m.k = &jobKata{admissionKata: f.k, job: kata.Job{UID: "job", Definition: []byte(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Inspect"},"issue":{"kind":"existing","uid":"issue"},"overlap":"forbid","catchup":"skip"}`)}}
}

// A lane another installation took over refuses a new run at intake, without
// queuing it for a later admission.
func TestRunRefusedWhenLaneLeaseHeldElsewhere(t *testing.T) {
	f := newAdmission(t)
	manualJobKata(f)
	f.m.SetLeaseRefusal(&LeaseRefusal{Owner: "elsewhere", Generation: 2})
	req := Request{Version: 1, UID: "request", Kind: "run", Job: "job", RunUID: "manual-uid"}
	p := writeRequest(t, f.s, req)
	r := &manualReceiver{}
	if err := f.m.HandleRequests(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if r.calls != 0 {
		t.Fatal("refused run reached the scheduler")
	}
	requireLeaseRefusal(t, f.s, p, req)
	if _, err := f.s.Load(req.RunUID); !missing(err) {
		t.Fatal("refused run has a journal", err)
	}
}

// A run queued while the lane held its lease, at generation 1, settles as
// refused once another installation takes the lane at generation 2.
func TestStoodDownLaneRefusesQueuedRun(t *testing.T) {
	f := newAdmission(t)
	manualJobKata(f)
	woken := 0
	f.m.OnManualJournal = func() { woken++ }
	f.m.SetLease(func() (int64, bool) { return 1, true })
	req := Request{Version: 1, UID: "request", Kind: "run", Job: "job", RunUID: "manual-uid"}
	p := writeRequest(t, f.s, req)
	r := &manualReceiver{}
	if err := f.m.HandleRequests(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if r.calls != 1 {
		t.Fatal("run not queued", r.calls)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("queued request missing", err)
	}
	// The takeover: the lane stands down and records the new owner.
	f.m.SetLease(func() (int64, bool) { return 1, false })
	f.m.SetLeaseRefusal(&LeaseRefusal{Owner: "elsewhere", Generation: 2})
	if woken != 1 {
		t.Fatal("stand-down did not wake the control loop", woken)
	}
	if err := f.m.HandleRequests(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	requireLeaseRefusal(t, f.s, p, req)
	if _, queued := f.m.requests.Load(req.UID); queued {
		t.Fatal("refused request still marked queued")
	}
	// A lane that holds its lease again accepts new runs.
	f.m.SetLeaseRefusal(nil)
	next := Request{Version: 1, UID: "request-2", Kind: "run", Job: "job", RunUID: "manual-uid-2"}
	writeRequest(t, f.s, next)
	if err := f.m.HandleRequests(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if r.calls != 2 || r.uid != next.RunUID {
		t.Fatal("reclaimed lane refused a run", r.calls, r.uid)
	}
}

// Resume and release of a run on a stood-down lane are refused at once and
// leave the run as it was.
func TestStoodDownLaneRefusesRunControls(t *testing.T) {
	for _, kind := range []string{"resume", "release"} {
		t.Run(kind, func(t *testing.T) {
			f := newRecovery(t)
			before := loaded(t, f.s, f.uid)
			f.m.SetLeaseRefusal(&LeaseRefusal{Owner: "elsewhere", Generation: 2})
			req := Request{Version: 1, UID: "request", Kind: kind, RunUID: f.uid}
			p := writeRequest(t, f.s, req)
			if err := f.m.HandleRequests(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			requireLeaseRefusal(t, f.s, p, req)
			if after := loaded(t, f.s, f.uid); after.Phase != before.Phase || after.Reason != before.Reason || len(after.Ops) != len(before.Ops) {
				t.Fatalf("refused %s changed the run: %s/%s -> %s/%s", kind, before.Phase, before.Reason, after.Phase, after.Reason)
			}
		})
	}
}

// The CLI refuses before writing a request when the running worker saved a
// takeover of the lane. An outcome saved before the runtime started is stale
// and defers to the worker.
func TestSendRequestRefusedWhenLaneLeaseHeldElsewhere(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	lock, ok, err := state.TryLock(l.WorkerLock())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer lock.Unlock()
	started := time.Now().UTC().Add(-time.Minute)
	if err := state.WriteJSON(state.OSFS{}, l.ServeInfo(), map[string]any{"worker_pid": 0, "started_at": started}); err != nil {
		t.Fatal(err)
	}
	const project, run = "project", "01K00000000000000000000009"
	if err := state.WriteJSON(state.OSFS{}, l.RunFile(run), Run{Version: 1, UID: run, Target: Target{Identity: kata.Identity{ProjectUID: project}}}); err != nil {
		t.Fatal(err)
	}
	saveLease := func(checked time.Time) {
		t.Helper()
		if err := state.WriteJSON(state.OSFS{}, filepath.Join(l.Project(project).Root, "lease.json"), map[string]any{"held": false, "owner": "elsewhere", "generation": 2, "heartbeat": checked, "error": "another installation (elsewhere) took this lane over at lease generation 2", "checked_at": checked}); err != nil {
			t.Fatal(err)
		}
	}
	saveLease(started.Add(time.Second))
	send := map[string]func() (Request, error){
		"run":     func() (Request, error) { return SendProjectRequest(state.OSFS{}, l, "run", "job", project) },
		"release": func() (Request, error) { return SendProjectRequest(state.OSFS{}, l, "release", run, "") },
		"resume":  func() (Request, error) { return SendResumeRequest(state.OSFS{}, l, run, false) },
	}
	for kind, call := range send {
		if _, err := call(); err == nil || err.Error() != elsewhereRefusal || !errors.Is(err, ErrLeaseHeldElsewhere) {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	if entries, _ := os.ReadDir(l.RequestsDir()); len(entries) != 0 {
		t.Fatal("refused requests were written", len(entries))
	}
	saveLease(started.Add(-time.Second))
	for kind, call := range send {
		if _, err := call(); err != nil {
			t.Fatalf("%s with a stale lease outcome: %v", kind, err)
		}
	}
}

// leasedKata adds the lane lease issue to a fake Kata.
type leasedKata struct {
	*admissionKata
	lease *lease.Lease
}

func (k *leasedKata) OldestLabeled(context.Context, string) (kata.IssueView, bool, error) {
	if k.lease == nil {
		return kata.IssueView{}, false, nil
	}
	b, _ := json.Marshal(k.lease)
	return kata.IssueView{UID: "lease-issue", Status: "closed", Revision: 4, Metadata: map[string]json.RawMessage{lease.Key: b}}, true, nil
}

// Abandon writes to Kata without the runtime, so it reads the lane lease
// itself: a live lease of another installation refuses it before any write.
func TestAbandonRefusedWhenLaneLeaseHeldElsewhere(t *testing.T) {
	f := newAdmission(t)
	observed := 0
	f.k.run = func(context.Context, string) (kata.Run, error) {
		return kata.Run{UID: f.uid, JobUID: "job", DefinitionEventUID: "event", OccurrenceKey: "manual:" + f.uid, Actor: "worker", ExecutorLabel: f.cfg.Executor, Status: "running", Revision: 3}, nil
	}
	f.k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) {
		observed++
		return kata.Run{}, true, nil
	}
	k := &leasedKata{admissionKata: f.k, lease: &lease.Lease{Version: 1, Owner: "elsewhere", Generation: 2, Heartbeat: time.Now()}}
	err := Abandon(t.Context(), f.cfg, k, f.g, f.s.fsys, f.s.layout, f.uid)
	if err == nil || err.Error() != elsewhereRefusal || !errors.Is(err, ErrLeaseHeldElsewhere) || observed != 0 {
		t.Fatalf("abandon on a lane held elsewhere: %v, %d observations", err, observed)
	}
	// An expired lease, or this installation's own, refuses nothing.
	k.lease.Heartbeat = time.Now().Add(-lease.TTL - time.Minute)
	if err := Abandon(t.Context(), f.cfg, k, f.g, f.s.fsys, f.s.layout, f.uid); err != nil || observed != 1 {
		t.Fatalf("abandon with an expired lease: %v, %d observations", err, observed)
	}
}
