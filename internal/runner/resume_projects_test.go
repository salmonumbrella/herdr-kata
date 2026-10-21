package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func projectResumeFixture(t *testing.T) (*admissionFixture, *Manager) {
	t.Helper()
	return bindProjectResumeFixture(t, heldResumeFixture(t))
}

func bindProjectResumeFixture(t *testing.T, f *admissionFixture) (*admissionFixture, *Manager) {
	t.Helper()
	f.cfg.Kata.Project = "team-project"
	f.m.EnableProjects()
	var pin kata.Installation
	if e := state.ReadJSON(f.s.fsys, f.s.layout.StateFile(), 65536, &pin); e != nil {
		t.Fatal(e)
	}
	pl := f.s.layout.Project("project")
	if e := state.WriteJSON(f.s.fsys, pl.StateFile(), pin); e != nil {
		t.Fatal(e)
	}
	lane, e := f.m.RegisterProject("project", f.cfg, f.m.k, f.g, pl)
	if e != nil {
		t.Fatal(e)
	}
	lane.environment = cleanLaunch{}
	lane.SetScheduler(&projectManualReceiver{})
	if _, e = f.m.RegisterProject("other-project", f.cfg, nil, nil, f.s.layout.Project("other-project")); e != nil {
		t.Fatal(e)
	}
	if e = state.WriteJSON(f.s.fsys, f.s.layout.StateFile(), map[string]int{"version": 1}); e != nil {
		t.Fatal(e)
	}
	f.m.k, f.m.guard = nil, nil // The dispatcher has no default project client or pin.
	if e = f.m.RestoreGates(); e != nil {
		t.Fatal(e)
	}
	return f, lane
}

// U1 and M2: the saved project owns quota resume, including its retry credit.
func TestProjectAgentLimitResumePreservesRetryBudget(t *testing.T) {
	x, f, clock, _ := resumableLimit(t, "usage_limit_exceeded")
	f, lane := bindProjectResumeFixture(t, f)
	clock.advance(time.Minute)
	f.start(t)
	if e := f.m.Resume(t.Context(), Request{UID: "resume-limit", RunUID: f.uid}); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	if r.Target.Identity.ProjectUID != "project" || r.Steps[0].Attempt != 2 || r.Steps[0].Uncharged != 1 || r.Steps[0].Limit != nil || r.Loop.Used != 0 {
		t.Fatal("project quota resume lost its frozen target or retry credit", r.Target, r.Steps[0], r.Loop)
	}
	x.m = lane
	if e := lane.launch(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	x.submit(t)
	if got := loaded(t, f.s, f.uid).Steps[0].Submission; got != Accepted || x.count("agent.prompt") != 2 {
		t.Fatal("quota resume did not submit exactly one new project attempt", got, x.count("agent.prompt"))
	}
}

func TestProjectResumeRoutesFromFrozenRun(t *testing.T) {
	for _, saved := range []bool{false, true} {
		t.Run(map[bool]string{false: "request", true: "saved acceptance"}[saved], func(t *testing.T) {
			f, lane := projectResumeFixture(t)
			if _, e := f.s.Transition(f.uid, func(r *Run) error {
				r.WFDef.Steps[0].Command = `printf '%s' "$KATA_PROJECT" > lane.txt`
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			req := Request{Version: 1, UID: "resume-request", Kind: "resume", RunUID: f.uid}
			if saved {
				if e := lane.acceptResume(t.Context(), req); e != nil {
					t.Fatal(e)
				}
				for head(loaded(t, f.s, f.uid)) != nil {
					deliver(t, f.q, f.uid)
				}
			} else {
				publishResume(t, f, req)
			}
			f.start(t)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			var r *Run
			// A bounded control scan may save acceptance before reports land.
			// Drive later scans as the worker does until the request completes.
			for {
				if e := f.m.HandleRequests(ctx, nil); e != nil {
					t.Fatal(e)
				}
				f.m.Wait()
				r = loaded(t, f.s, f.uid)
				if r.Phase == Settled {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("owning lane did not finish resume", r.Phase, r.Reason, r.Resume, ctx.Err())
				case <-tick.C:
				}
			}
			if r.Phase != Settled || r.Outcome != "succeeded" || r.Steps[0].Attempt != 2 || r.Target.Identity.ProjectUID != "project" {
				t.Fatalf("owning lane did not resume: phase=%s outcome=%s attempt=%d project=%s", r.Phase, r.Outcome, r.Steps[0].Attempt, r.Target.Identity.ProjectUID)
			}
			if data, e := os.ReadFile(filepath.Join(r.Cwd, "lane.txt")); e != nil || string(data) != "team-project" {
				t.Fatal("resumed command used another lane's environment", string(data), e)
			}
		})
	}
}

func TestProjectResumeEnforcesLaneCapacityAndOverlap(t *testing.T) {
	for _, scenario := range []string{"other project same job", "same project same job", "same project cap"} {
		t.Run(scenario, func(t *testing.T) {
			f, lane := projectResumeFixture(t)
			lane.cfg.ProjectLimit = 1
			peer := *loaded(t, f.s, f.uid)
			peer.UID, peer.SlotReleased = "peer", false
			if scenario == "other project same job" {
				peer.Target.Identity.ProjectUID = "other-project"
			}
			if scenario == "same project cap" {
				peer.Job.UID = "another-job"
			}
			if e := f.s.Create(&peer); e != nil {
				t.Fatal(e)
			}
			if e := f.m.RestoreGates(); e != nil {
				t.Fatal(e)
			}
			req := Request{Version: 1, UID: "resume-request", Kind: "resume", RunUID: f.uid}
			e := lane.acceptResume(t.Context(), req)
			r := loaded(t, f.s, f.uid)
			if scenario != "other project same job" {
				if e != errResumePending || r.Resume != nil || r.Steps[0].Attempt != 1 {
					t.Fatal("lane gate ignored", e, r.Resume, r.Steps[0].Attempt)
				}
			} else if e != nil || r.Resume == nil || r.Steps[0].Attempt != 2 {
				t.Fatal("another project's gate blocked resume", e, r.Resume, r.Steps[0].Attempt)
			} else if !lane.GateHeld("job") {
				t.Fatal("accepted resume lost its project overlap gate")
			}
		})
	}
}

func TestResumeWaitsForMissingLane(t *testing.T) {
	f, _ := projectResumeFixture(t)
	if _, e := f.s.Transition(f.uid, func(r *Run) error { r.Target.Identity.ProjectUID = "removed-project"; return nil }); e != nil {
		t.Fatal(e)
	}
	req := Request{Version: 1, UID: "resume-request", Kind: "resume", RunUID: f.uid}
	publishResume(t, f, req)
	if e := f.m.HandleRequests(t.Context(), nil); e != nil {
		t.Fatal(e)
	}
	var retained Request
	if e := state.ReadJSON(f.s.fsys, filepath.Join(f.s.layout.RequestsDir(), "resume-request.json"), 65536, &retained); e != nil {
		t.Fatal("missing lane discarded resume", e)
	}
	r := loaded(t, f.s, f.uid)
	if r.Resume != nil || r.Steps[0].Attempt != 1 || r.Target.Identity.ProjectUID != "removed-project" {
		t.Fatal("missing lane changed run", r)
	}
}

func TestSavedProjectResumeRechecksRouteAndFrozenTarget(t *testing.T) {
	for _, failure := range []string{"refused route", "changed pin"} {
		for _, mode := range []string{"without request", "pending replay", "activated replay", "activated without request"} {
			t.Run(failure+" "+mode, func(t *testing.T) {
				f, lane := projectResumeFixture(t)
				if _, e := f.s.Transition(f.uid, func(r *Run) error {
					r.WFDef.Steps[0].Command = `printf ran > "$HERDR_KATA_RUN_DIR/resume-marker"`
					return nil
				}); e != nil {
					t.Fatal(e)
				}
				req := Request{Version: 1, UID: "resume-request", Kind: "resume", RunUID: f.uid}
				if e := lane.acceptResume(t.Context(), req); e != nil {
					t.Fatal(e)
				}
				for head(loaded(t, f.s, f.uid)) != nil {
					deliver(t, f.q, f.uid)
				}
				if mode == "activated replay" || mode == "activated without request" {
					if e := lane.activateResume(t.Context(), f.uid); e != nil {
						t.Fatal(e)
					}
				}
				var pin kata.Installation
				if e := state.ReadJSON(f.s.fsys, lane.pinLayout.StateFile(), 65536, &pin); e != nil {
					t.Fatal(e)
				}
				if failure == "refused route" {
					f.g.blocked.Store(true)
				} else {
					changed := pin
					changed.InstanceUID = "different-instance"
					if e := state.WriteJSON(f.s.fsys, lane.pinLayout.StateFile(), changed); e != nil {
						t.Fatal(e)
					}
				}
				if mode == "pending replay" || mode == "activated replay" {
					publishResume(t, f, req)
				}
				f.start(t)
				t.Cleanup(f.m.Wait)
				if e := f.m.HandleRequests(t.Context(), nil); e != nil {
					t.Fatal(e)
				}
				r := loaded(t, f.s, f.uid)
				wantPhase, wantPending := Held, true
				if mode == "activated replay" || mode == "activated without request" {
					wantPhase, wantPending = Admitted, false
				}
				if r.Phase != wantPhase || r.Resume == nil || r.Resume.Pending != wantPending || r.Steps[0].Attempt != 2 || r.Steps[0].Submission != None {
					t.Fatal("saved resume activated through refused or changed route", r.Phase, r.Resume, r.Steps[0])
				}
				if _, e := os.Stat(filepath.Join(r.Cwd, "resume-marker")); !os.IsNotExist(e) {
					t.Fatal("refused route executed command", e)
				}
				f.g.blocked.Store(false)
				if e := state.WriteJSON(f.s.fsys, lane.pinLayout.StateFile(), pin); e != nil {
					t.Fatal(e)
				}
				if e := f.m.HandleRequests(t.Context(), nil); e != nil {
					t.Fatal(e)
				}
				f.m.Wait()
				r = loaded(t, f.s, f.uid)
				if r.Phase != Settled || r.Outcome != "succeeded" || r.Steps[0].Attempt != 2 {
					t.Fatal("accepted resume did not recover", r.Phase, r.Outcome, r.Steps[0].Attempt)
				}
			})
		}
	}
}

type resumeRouteFunc func(context.Context) error

func (f resumeRouteFunc) Check(ctx context.Context) error { return f(ctx) }

func TestSavedResumeBoundsRouteProbes(t *testing.T) {
	for _, activated := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "activated"}[activated], func(t *testing.T) {
			f, lane := projectResumeFixture(t)
			req := Request{Version: 1, UID: "resume-request", Kind: "resume", RunUID: f.uid}
			if e := lane.acceptResume(t.Context(), req); e != nil {
				t.Fatal(e)
			}
			for head(loaded(t, f.s, f.uid)) != nil {
				deliver(t, f.q, f.uid)
			}
			if activated {
				if e := lane.activateResume(t.Context(), f.uid); e != nil {
					t.Fatal(e)
				}
			}
			calls := 0
			lane.guard = resumeRouteFunc(func(ctx context.Context) error {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || deadline.After(time.Now().Add(2100*time.Millisecond)) {
					t.Error("saved continuation has no bounded control deadline")
				}
				return errors.New("endpoint unavailable")
			})
			if e := f.m.HandleRequests(t.Context(), nil); e != nil {
				t.Fatal(e)
			}
			if calls != 1 {
				t.Fatal("saved continuation did not probe its route", calls)
			}
		})
	}
}

type blockedRecoveryPin struct {
	state.FS
	path             string
	blocked          atomic.Bool
	entered, release chan struct{}
}

func (f *blockedRecoveryPin) Open(path string) (state.File, error) {
	if path == f.path && f.blocked.CompareAndSwap(false, true) {
		close(f.entered)
		<-f.release
	}
	return f.FS.Open(path)
}

func TestRecoveryPreservesConcurrentProjectRelease(t *testing.T) {
	f, lane := projectResumeFixture(t)
	if _, e := f.s.Transition(f.uid, func(r *Run) error {
		r.Phase, r.Outcome, r.Reason = Running, "", ""
		r.EndedAt = nil
		r.Steps[0].Status, r.Steps[0].Reason = "", ""
		r.Steps[0].Submission, r.Steps[0].Receipt = Sending, nil
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	lane.SetScheduler(nil)
	gate := &blockedRecoveryPin{FS: f.s.fsys, path: lane.pinLayout.StateFile(), entered: make(chan struct{}), release: make(chan struct{})}
	f.s.fsys = gate
	done := make(chan error, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(gate.release) }) }
	joined := false
	defer func() {
		unblock()
		if !joined {
			<-done
		}
	}()
	go func() { done <- lane.Recover(t.Context()) }()
	<-gate.entered // Recovery has loaded the running attempt but not reconciled it.
	releaseDone := make(chan error, 1)
	go func() { releaseDone <- f.m.Release(t.Context(), f.uid) }()
	select {
	case e := <-releaseDone:
		t.Error("release overtook startup reconciliation", e)
		releaseDone <- e
	case <-time.After(50 * time.Millisecond):
	}
	unblock()
	e := <-done
	joined = true
	if e != nil {
		t.Error(e)
	}
	if e = <-releaseDone; e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	if r.Phase != HandedOff || !r.SlotReleased || r.Reason != "released by operator" || r.EndedAt == nil {
		t.Fatal("recovery undid the operator handoff", r.Phase, r.Reason, r.Outcome, r.SlotReleased)
	}
}

func TestResumeWaitsForLaneRecovery(t *testing.T) {
	f, lane := projectResumeFixture(t)
	if _, e := f.s.Transition(f.uid, func(r *Run) error {
		r.WFDef.Steps[0].Command = `printf once >> "$HERDR_KATA_RUN_DIR/recovered-marker"; while [ ! -f "$HERDR_KATA_RUN_DIR/continue" ]; do sleep 0.01; done`
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	req := Request{Version: 1, UID: "resume-request", Kind: "resume", RunUID: f.uid}
	if e := lane.acceptResume(t.Context(), req); e != nil {
		t.Fatal(e)
	}
	for head(loaded(t, f.s, f.uid)) != nil {
		deliver(t, f.q, f.uid)
	}
	if e := lane.activateResume(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	lane.SetScheduler(nil)
	var probes atomic.Int32
	lane.guard = resumeRouteFunc(func(ctx context.Context) error { probes.Add(1); return f.g.Check(ctx) })
	gate := &blockedRecoveryPin{FS: f.s.fsys, path: lane.pinLayout.StateFile(), entered: make(chan struct{}), release: make(chan struct{})}
	f.s.fsys = gate
	f.start(t)
	ctx, cancel := context.WithCancel(t.Context())
	var once sync.Once
	unblock := func() { once.Do(func() { close(gate.release) }) }
	done := make(chan error, 1)
	joined := false
	defer func() {
		unblock()
		cancel()
		if !joined {
			<-done
		}
		f.m.Wait()
	}()
	go func() { done <- lane.Recover(ctx) }()
	<-gate.entered // Recovery passed the executor check and is reading the pin.
	if e := f.m.HandleRequests(ctx, nil); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	marker := filepath.Join(r.Cwd, "recovered-marker")
	if probes.Load() != 1 {
		t.Error("controls dispatched into a lane while recovery was reconciling")
		// Reproduce the old interleaving: the new command is Sending before
		// recovery reaches its journal transition.
		until := time.Now().Add(5 * time.Second)
		for {
			if _, e := os.Stat(marker); e == nil {
				break
			}
			if time.Now().After(until) {
				t.Fatal("command did not start")
			}
			time.Sleep(time.Millisecond)
		}
	}
	unblock()
	e := <-done
	joined = true
	if e != nil {
		t.Fatal(e)
	}
	lane.SetScheduler(&projectManualReceiver{})
	if e := os.WriteFile(filepath.Join(r.Cwd, "continue"), nil, 0600); e != nil {
		t.Fatal(e)
	}
	f.m.Wait()
	r = loaded(t, f.s, f.uid)
	if r.Phase != Settled || r.Outcome != "succeeded" || r.Steps[0].Attempt != 2 {
		t.Fatal("recovery interfered with resume", r.Phase, r.Reason, r.Steps[0])
	}
	if data, e := os.ReadFile(marker); e != nil || string(data) != "once" {
		t.Fatal("resumed command did not execute exactly once", string(data), e)
	}
}

func TestProjectControlCancellationDoesNotWaitForCapacity(t *testing.T) {
	for _, kind := range []string{"release", "resume"} {
		t.Run(kind, func(t *testing.T) {
			f, _ := projectResumeFixture(t)
			gate := &blockedRecoveryPin{FS: f.s.fsys, path: f.s.layout.RunFile(f.uid), entered: make(chan struct{}), release: make(chan struct{})}
			f.s.fsys = gate
			f.m.mu.Lock() // Capacity work can wait here on another journal's stop proof.
			locked := true
			defer func() {
				if locked {
					f.m.mu.Unlock()
				}
			}()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if kind == "release" {
					done <- f.m.Release(ctx, f.uid)
				} else {
					done <- f.m.Resume(ctx, Request{Version: 1, UID: "resume-request", Kind: "resume", RunUID: f.uid})
				}
			}()
			<-gate.entered // The control passed its entry check before cancellation.
			cancel()
			close(gate.release)
			var result error
			select {
			case result = <-done:
			case <-time.After(5 * time.Second):
				t.Error("canceled control waited for unrelated capacity work")
				f.m.mu.Unlock()
				locked = false
				result = <-done
			}
			if !errors.Is(result, context.Canceled) {
				t.Fatal("control ignored cancellation", result)
			}
		})
	}
}

func TestControlScanSkipsBusyJournalAfterItsBudget(t *testing.T) {
	f, _ := projectResumeFixture(t)
	peer := *loaded(t, f.s, f.uid)
	peer.UID = "other-run"
	peer.Target.Identity.ProjectUID = "other-project"
	if e := f.s.Create(&peer); e != nil {
		t.Fatal(e)
	}
	req := Request{Version: 1, UID: "release-request", Kind: "release", RunUID: peer.UID}
	if e := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.RequestsDir(), req.UID+".json"), req); e != nil {
		t.Fatal(e)
	}
	journal := f.s.lock(f.uid)
	journal.Lock() // A held occupant check owns this run's journal, not the peer's.
	locked := true
	defer func() {
		if locked {
			journal.Unlock()
		}
	}()
	done := make(chan error, 1)
	go func() { done <- f.m.HandleRequests(t.Context(), nil) }()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Error("control scan waited indefinitely for another project's journal")
		journal.Unlock()
		locked = false
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
	if r := loaded(t, f.s, peer.UID); r.Phase != HandedOff || !r.SlotReleased {
		t.Fatal("healthy project's release did not complete", r.Phase, r.SlotReleased)
	}
}

func TestQueueBarrierCancellationDoesNotWaitForJournal(t *testing.T) {
	f := heldResumeFixture(t)
	if _, e := f.s.Transition(f.uid, func(r *Run) error { r.Ops[0].Done = false; return nil }); e != nil {
		t.Fatal(e)
	}
	for len(f.q.wake) > 0 {
		<-f.q.wake
	}
	journal := f.s.lock(f.uid)
	journal.Lock()
	locked := true
	defer func() {
		if locked {
			journal.Unlock()
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.q.wait(ctx, f.uid, "old-observe") }()
	<-f.q.wake // The wait entered before its caller canceled.
	cancel()
	var result error
	select {
	case result = <-done:
	case <-time.After(5 * time.Second):
		t.Error("canceled report barrier waited on a busy journal")
		journal.Unlock()
		locked = false
		result = <-done
	}
	if !errors.Is(result, context.Canceled) {
		t.Fatal(result)
	}
}

func TestResumePruningDoesNotWaitForPeerJournal(t *testing.T) {
	f, _ := projectResumeFixture(t)
	peer := *loaded(t, f.s, f.uid)
	peer.UID = "other-run"
	peer.Target.Identity.ProjectUID = "other-project"
	peer.SlotReleased = false
	if e := f.s.Create(&peer); e != nil {
		t.Fatal(e)
	}
	if e := f.m.RestoreGates(); e != nil {
		t.Fatal(e)
	}
	journal := f.s.lock(peer.UID)
	journal.Lock()
	locked := true
	defer func() {
		if locked {
			journal.Unlock()
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- f.m.Resume(ctx, Request{Version: 1, UID: "resume-request", Kind: "resume", RunUID: f.uid})
	}()
	until := time.Now().Add(5 * time.Second)
	for {
		if !f.m.mu.TryLock() {
			break
		}
		f.m.mu.Unlock()
		if time.Now().After(until) {
			t.Fatal("resume did not enter capacity checks")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	var result error
	select {
	case result = <-done:
	case <-time.After(5 * time.Second):
		t.Error("resume ignored cancellation while pruning a peer journal")
		journal.Unlock()
		locked = false
		result = <-done
	}
	if !errors.Is(result, context.Canceled) {
		t.Fatal(result)
	}
	if locked {
		journal.Unlock()
		locked = false
	}
	if r := loaded(t, f.s, f.uid); r.Resume != nil || r.Steps[0].Attempt != 1 {
		t.Fatal("canceled resume allocated an attempt", r.Resume, r.Steps[0].Attempt)
	}
	if !f.m.ForProject("other-project").GateHeld("job") {
		t.Fatal("busy peer lost its reservation")
	}
}

func TestFallbackReleaseSerializesWithLateProjectRegistration(t *testing.T) {
	f, lane := projectResumeFixture(t)
	if _, e := f.s.Transition(f.uid, func(r *Run) error {
		r.Phase, r.Outcome, r.Reason = Running, "", ""
		r.EndedAt = nil
		r.Steps[0].Status, r.Steps[0].Reason = "", ""
		r.Steps[0].Submission, r.Steps[0].Receipt = Sending, nil
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	f.m.lanesMu.Lock()
	delete(f.m.lanes, "project")
	f.m.lanesMu.Unlock()
	gate := &blockedRecoveryPin{FS: f.s.fsys, path: lane.pinLayout.StateFile(), entered: make(chan struct{}), release: make(chan struct{})}
	f.s.fsys = gate
	admission := f.m.admissionLock(f.uid)
	admission.Lock()
	locked := true
	ctx, cancel := context.WithCancel(t.Context())
	releaseDone := make(chan error, 1)
	recoveryDone := make(chan error, 1)
	recoveryStarted, releaseJoined, recoveryJoined := false, false, false
	var once sync.Once
	unblock := func() { once.Do(func() { close(gate.release) }) }
	defer func() {
		unblock()
		if locked {
			admission.Unlock()
		}
		cancel()
		if !releaseJoined {
			<-releaseDone
		}
		if recoveryStarted && !recoveryJoined {
			<-recoveryDone
		}
	}()
	fallback := f.m.recoveryLock("project")
	go func() { releaseDone <- f.m.Release(ctx, f.uid) }()
	until := time.Now().Add(5 * time.Second)
	for fallback.TryLock() {
		fallback.Unlock()
		if time.Now().After(until) {
			t.Fatal("fallback release did not enter")
		}
		time.Sleep(time.Millisecond)
	}
	// The lookup already missed the lane; Release is waiting on admission.
	registered, e := f.m.RegisterProject("project", lane.cfg, lane.k, lane.guard, lane.pinLayout)
	if e != nil {
		t.Fatal(e)
	}
	recoveryStarted = true
	go func() { recoveryDone <- registered.Recover(ctx) }()
	select {
	case <-gate.entered:
		t.Error("late lane recovery overtook an in-progress fallback release")
	case <-time.After(time.Second):
	}
	admission.Unlock()
	locked = false
	e = <-releaseDone
	releaseJoined = true
	if e != nil {
		t.Fatal(e)
	}
	unblock()
	e = <-recoveryDone
	recoveryJoined = true
	if e != nil {
		t.Fatal("recovery failed after a completed handoff", e)
	}
	if r := loaded(t, f.s, f.uid); r.Phase != HandedOff || r.Reason != "released by operator" || !r.SlotReleased {
		t.Fatal("fallback handoff changed", r.Phase, r.Reason, r.SlotReleased)
	}
}

func TestProjectReconciliationAndRecoveryScopeJournalWaits(t *testing.T) {
	for _, operation := range []string{"reconcile", "recover"} {
		for _, blocked := range []string{"foreign", "own"} {
			t.Run(operation+"/"+blocked, func(t *testing.T) {
				f, lane := projectResumeFixture(t)
				h := herdrtest.New(t)
				lane.h = &herdr.Client{Path: h.Path}
				own, err := f.s.Transition(f.uid, func(r *Run) error { r.SlotReleased = false; return nil })
				if err != nil {
					t.Fatal(err)
				}
				peer := &Run{Version: 1, UID: "peer", Phase: Held, Target: own.Target, Job: own.Job}
				peer.Target.Identity.ProjectUID = "other-project"
				peer.Job.UID = "peer-job"
				if err := f.s.Create(peer); err != nil {
					t.Fatal(err)
				}
				if err := f.m.RestoreGates(); err != nil {
					t.Fatal(err)
				}
				uid, budget, wait := "peer", 10*time.Second, 5*time.Second
				if blocked == "own" {
					uid, budget, wait = f.uid, 50*time.Millisecond, 2*time.Second
				}
				journal := f.s.lock(uid)
				journal.Lock()
				locked := true
				defer func() {
					if locked {
						journal.Unlock()
					}
				}()
				ctx, cancel := context.WithTimeout(t.Context(), budget)
				defer cancel()
				done := make(chan error, 1)
				go func() {
					if operation == "reconcile" {
						done <- lane.ReconcileHeld(ctx)
					} else {
						done <- lane.Recover(ctx)
					}
				}()
				select {
				case err = <-done:
				case <-time.After(wait):
					t.Error("project scan waited on a foreign journal or ignored its cancellation")
					journal.Unlock()
					locked = false
					err = <-done
				}
				if blocked == "own" && !errors.Is(err, context.DeadlineExceeded) || blocked == "foreign" && err != nil {
					t.Fatal("project scan result", err)
				}
				if locked {
					journal.Unlock()
					locked = false
				}
				released := operation == "reconcile" && blocked == "foreign"
				if r := loaded(t, f.s, f.uid); r.SlotReleased != released || r.Phase != Held || !lane.GateHeld(r.Job.UID) {
					t.Fatal("own held run lost its capacity or overlap fence", r.SlotReleased, r.Phase)
				}
				if r := loaded(t, f.s, "peer"); r.SlotReleased || r.Phase != Held {
					t.Fatal("foreign held run changed", r.SlotReleased, r.Phase)
				}
				free := 0
				if released {
					free = 1
				}
				if f.m.FreeSlots() != free || len(h.Requests()) != 0 {
					t.Fatal("project scan freed foreign capacity or probed its occupant", f.m.FreeSlots(), h.Requests())
				}
			})
		}
	}
}
