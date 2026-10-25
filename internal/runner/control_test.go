package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"go.kenn.io/kit/secretref"
)

func TestFirstRequestIntoEmptyDirHandled(t *testing.T) {
	f := newRecovery(t)
	if e := f.m.HandleRequests(t.Context(), nil); e != nil {
		t.Fatal(e)
	}
	req := Request{Version: 1, UID: "request", Kind: "release", RunUID: f.uid}
	if e := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.RequestsDir(), "request.json"), req); e != nil {
		t.Fatal(e)
	}
	if e := f.m.HandleRequests(t.Context(), nil); e != nil {
		t.Fatal(e)
	}
	if loaded(t, f.s, f.uid).Phase != HandedOff {
		t.Fatal("not released")
	}
	if _, e := os.Stat(filepath.Join(f.s.layout.RequestsDir(), "request.json")); !os.IsNotExist(e) {
		t.Fatal("request retained", e)
	}
}
func TestReleaseKeepsTab(t *testing.T) {
	f := newRecovery(t)
	f.launch(t)
	if e := f.m.Release(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	if e := f.m.hold(f.uid, "timeout"); e == nil {
		t.Fatal("runner overwrote handoff")
	}
	if e := f.m.cleanup(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	if r.Phase != HandedOff || !r.SlotReleased || f.count("tab.close") != 0 || len(r.Ops) == 0 {
		t.Fatal(r)
	}
}

// Gap 4: releasing a held command must explain the handoff even when its
// command outcome repeats the earlier report. Saved reports remain immutable.
func TestReleaseCommentHandoffSurvivesRecovery(t *testing.T) {
	for _, delivery := range []string{"pending", "sent", "done"} {
		t.Run(delivery, func(t *testing.T) {
			f := newAdmission(t)
			r := settleFixture()
			r.Phase, r.Outcome, r.Reason = Held, "unknown", "command_outcome_unknown"
			r.Target, r.IssueOwned = Target{}, false
			r.Workflow = &kata.Workflow{Name: "Example workflow"}
			r.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "main", Kind: "command", Command: "false"}}}
			r.Steps = []StepState{{Key: "main", Kind: "command", Attempt: 1, Status: "unknown", Receipt: &Receipt{Version: 1, Run: "run-1", Step: "main", Attempt: 1, Interrupted: true}}}
			r.Ops = SettleOps(r, nil, settleTime)
			if delivery != "pending" {
				r.Ops[0].FirstSent = &settleTime
			}
			r.Ops[0].Done = delivery == "done"
			before, original := settlePayload(t, r.Ops, "comment")
			const outcome = "Workflow Example workflow: main: command outcome unknown (run run-1, occurrence 2026-01-02T03:04:05Z)"
			if original.Body != "Reason: command_outcome_unknown\n"+outcome {
				t.Fatalf("hold report = %q", original.Body)
			}
			if err := f.s.Create(r); err != nil {
				t.Fatal(err)
			}
			if err := f.m.Release(t.Context(), r.UID); err != nil {
				t.Fatal(err)
			}
			// A replacement manager recovers the same journal, then receives a
			// duplicate release. Neither event may replace or duplicate reports.
			restarted := NewManager(f.cfg, f.k, nil, f.s, nil, f.g, RealClock{})
			if err := restarted.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := restarted.Release(t.Context(), r.UID); err != nil {
				t.Fatal(err)
			}
			saved := loaded(t, f.s, r.UID)
			comments := 0
			for _, op := range saved.Ops {
				if op.Kind != "comment" {
					continue
				}
				comments++
				if op.ID == before.ID {
					if op.Key != before.Key || string(op.Payload) != string(before.Payload) {
						t.Fatal("release rewrote the saved hold report", op)
					}
					continue
				}
				p, err := decodePayload(op)
				if err != nil || p.Body != "Handed to the operator. Automatic execution stopped.\n\n"+outcome || op.Key != "herdr-kata:run-1:comment:unknown-release" {
					t.Fatalf("handoff report = %+v payload = %+v error = %v", op, p, err)
				}
			}
			if saved.Phase != HandedOff || !saved.SlotReleased || comments != 2 {
				t.Fatalf("release recovery: phase=%s slot_released=%t comments=%d", saved.Phase, saved.SlotReleased, comments)
			}
		})
	}
}
func TestAbandonRefusesWithLocalState(t *testing.T) {
	f := newRecovery(t)
	if e := Abandon(t.Context(), f.m.cfg, f.m.k, f.m.guard, f.s.fsys, f.s.layout, f.uid); e == nil {
		t.Fatal("abandoned local state")
	}
}
func TestRetentionKeepsHeldHandedOffAndBinaries(t *testing.T) {
	f := newRecovery(t)
	now := time.Now()
	old := now.Add(-15 * 24 * time.Hour)
	for _, phase := range []Phase{Held, HandedOff, Settled} {
		uid := string(phase)
		r := &Run{Version: 1, UID: uid, Phase: phase, Outcome: "succeeded", EndedAt: &old, Herdr: HerdrRef{ReportBin: filepath.Join(f.s.layout.BinDir(), uid)}}
		if e := f.s.Create(r); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.m.Retain(now); e != nil {
		t.Fatal(e)
	}
	for _, p := range []Phase{Held, HandedOff} {
		if _, e := os.Stat(f.s.layout.RunFile(string(p))); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := os.Stat(f.s.layout.RunDir(string(Settled))); !os.IsNotExist(e) {
		t.Fatal("settled retained", e)
	}
}

func TestRetentionContinuesAfterQuarantine(t *testing.T) {
	for _, fault := range []string{"", "destination", "source"} {
		t.Run("fault="+fault, func(t *testing.T) {
			f := newAdmission(t)
			now := time.Now()
			old := now.Add(-15 * 24 * time.Hour)
			const corrupt, expired = "aa-corrupt", "zz-expired"
			report := newOp("report", "comment", Payload{Ref: "issue", Body: "done"}, "report-key")
			report.Done = true
			for _, uid := range []string{corrupt, expired} {
				if err := f.s.Create(&Run{Version: 1, UID: uid, Phase: Settled, Outcome: "succeeded", EndedAt: &old, Ops: []Op{report}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(f.s.layout.RunFile(corrupt), []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
			disk := &quarantineFault{FS: f.s.fsys, point: fault, src: f.s.layout.RunDir(corrupt), dst: f.s.layout.QuarantineRunDir(corrupt)}
			f.s.fsys = disk
			err := f.m.Retain(now)
			if fault != "" {
				if err == nil || !disk.hit {
					t.Fatalf("quarantine fault not surfaced: hit=%v err=%v", disk.hit, err)
				}
				if _, joined := err.(interface{ Unwrap() []error }); !joined {
					t.Fatalf("lost quarantine failure: %v", err)
				}
				if _, err := os.Stat(f.s.layout.RunFile(expired)); err != nil {
					t.Fatal("retention continued after failed quarantine", err)
				}
				return
			}
			if err != nil {
				t.Fatal("successful quarantine stopped retention", err)
			}
			if !state.Quarantined(disk, f.s.layout, corrupt) {
				t.Fatal("corrupt journal not quarantined")
			}
			if _, err := os.Stat(f.s.layout.RunDir(expired)); !os.IsNotExist(err) {
				t.Fatal("expired journal retained", err)
			}
		})
	}
}

type manualReceiver struct {
	uid   string
	calls int
	err   error
}

func (r *manualReceiver) RunNow(_ string, uid string, input ...*string) error {
	r.uid = uid
	r.calls++
	return r.err
}

type jobKata struct {
	*admissionKata
	job kata.Job
}

func (k *jobKata) ShowJob(context.Context, string) (kata.Job, error) { return k.job, nil }
func (k *jobKata) ShowWorkflow(context.Context, string) (kata.Workflow, error) {
	return kata.Workflow{}, nil
}
func TestRunRequestRoundTripSameUID(t *testing.T) {
	f := newAdmission(t)
	f.d.Def.Action.WorkflowUID = ""
	f.d.WFDef = nil
	k := &jobKata{admissionKata: f.k, job: kata.Job{UID: "job", Definition: []byte(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Inspect"},"issue":{"kind":"existing","uid":"issue"},"overlap":"forbid","catchup":"skip"}`)}}
	f.m.k = k
	r := &manualReceiver{}
	req := Request{Version: 1, UID: "request", Kind: "run", Job: "job", RunUID: "manual-uid"}
	p := filepath.Join(f.s.layout.RequestsDir(), "request.json")
	state.WriteJSON(f.s.fsys, p, req)
	for range 2 {
		if e := f.m.HandleRequests(t.Context(), r); e != nil {
			t.Fatal(e)
		}
	}
	if r.uid != "manual-uid" || r.calls != 1 {
		t.Fatal(r)
	}
	// Keep the request until admission has a durable journal: scheduler queues are volatile.
	if _, e := os.Stat(p); e != nil {
		t.Fatal(e)
	}
}

func TestReleaseDuringSettlementSerialized(t *testing.T) {
	f := newAdmission(t)
	now := time.Now()
	r := settleFixture()
	r.UID = f.uid
	r.Target = Target{}
	r.EndedAt = &now
	r.Outcome = "succeeded"
	r.Ops = SettleOps(r, resultFixture(), now)
	if e := f.s.Create(r); e != nil {
		t.Fatal(e)
	}
	entered := make(chan struct{})
	resume := make(chan struct{})
	f.k.close = func(context.Context, kata.CloseReq) error { close(entered); <-resume; return nil }
	f.k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{UID: "issue", Status: "open", Revision: 1}, nil
	}
	// Final cron evidence is independent of the issue close. Deliver it first,
	// then exercise release's serialization with the in-flight issue mutation.
	if err := f.q.send(t.Context(), f.uid); err != nil {
		t.Fatal(err)
	}
	sent := make(chan error, 1)
	go func() { sent <- f.q.send(t.Context(), f.uid) }()
	<-entered
	released := make(chan error, 1)
	go func() { released <- f.m.Release(t.Context(), f.uid) }()
	select {
	case e := <-released:
		t.Fatal("release raced in-flight close", e)
	case <-time.After(20 * time.Millisecond):
	}
	close(resume)
	if e := <-sent; e != nil {
		t.Fatal(e)
	}
	if e := <-released; !errors.Is(e, errControlRefused) {
		t.Fatal(e)
	}
	r = loaded(t, f.s, f.uid)
	if r.Phase != Settled || !r.Ops[0].Done {
		t.Fatal(r)
	}
	// Releasing twice neither resurrects settlement nor duplicates handoff ops.
	n := len(r.Ops)
	if e := f.m.Release(t.Context(), f.uid); !errors.Is(e, errControlRefused) {
		t.Fatal(e)
	}
	if len(loaded(t, f.s, f.uid).Ops) != n {
		t.Fatal("duplicate release")
	}
}
func TestAbandonLostRunReplaysFrozenObservation(t *testing.T) {
	f := newAdmission(t)
	var seen []kata.Observation
	links := []kata.Transcript{{Agent: "codex", SessionID: "00000000-0000-4000-8000-000000000001"}}
	f.k.run = func(context.Context, string) (kata.Run, error) {
		return kata.Run{UID: f.uid, JobUID: "job", DefinitionEventUID: "event", OccurrenceKey: "manual:" + f.uid, Actor: "worker", ExecutorLabel: f.cfg.Executor, Status: "running", Revision: 3, Transcripts: links}, nil
	}
	f.k.observe = func(_ context.Context, _ string, o kata.Observation) (kata.Run, bool, error) {
		if len(o.Transcripts) != 1 || o.Transcripts[0] != links[0] || !o.TranscriptsIncomplete {
			t.Fatalf("lost-state abandonment dropped sessions: %+v", o)
		}
		seen = append(seen, o)
		if len(seen) == 1 {
			return kata.Run{}, false, lostReply
		}
		return kata.Run{}, true, nil
	}
	if e := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); e == nil {
		t.Fatal("lost reply not returned")
	}
	if e := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); e != nil {
		t.Fatal(e)
	}
	a, _ := json.Marshal(seen[0])
	b, _ := json.Marshal(seen[1])
	if string(a) != string(b) || seen[0].Status != "unknown" || seen[0].ExpectedRevision != 3 {
		t.Fatal(seen)
	}
}
func TestManualRequestRefusesNativeJob(t *testing.T) {
	f := newAdmission(t)
	k := &jobKata{admissionKata: f.k, job: kata.Job{UID: "native", Definition: []byte(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"notify","recipient":"actor"},"issue":{"kind":"per-run","title":"Child"},"overlap":"allow","catchup":"skip"}`)}}
	f.m.k = k
	req := Request{Version: 1, UID: "request", Kind: "run", Job: "native", RunUID: "manual"}
	p := filepath.Join(f.s.layout.RequestsDir(), "request.json")
	state.WriteJSON(f.s.fsys, p, req)
	scheduler := &manualReceiver{}
	if e := f.m.HandleRequests(t.Context(), scheduler); e != nil {
		t.Fatal(e)
	}
	var result RequestResult
	if e := state.ReadJSON(f.s.fsys, RequestResultPath(f.s.layout, "request"), 4096, &result); e != nil || result.Error == "" || scheduler.calls != 0 {
		t.Fatal(result, e, scheduler)
	}
}

func TestReleaseRefusesUnsentSettledObservation(t *testing.T) {
	f := newRecovery(t)
	f.accepted(t)
	f.result(t, "succeeded")
	res := f.await(t)
	if e := f.m.settle(t.Context(), f.uid, res); e != nil {
		t.Fatal(e)
	}
	if e := f.m.Release(t.Context(), f.uid); !errors.Is(e, errControlRefused) {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	for _, op := range r.Ops {
		if op.Kind == "observe" && !op.Done {
			p, e := decodePayload(op)
			if e != nil {
				t.Fatal(e)
			}
			if p.Observation.ExpectedRevision != 1 {
				t.Fatalf("cancelled observation advanced revision: %d", p.Observation.ExpectedRevision)
			}
		}
	}
}
func TestReleaseRefusesPendingSettledObservation(t *testing.T) {
	f := newRecovery(t)
	f.accepted(t)
	f.result(t, "succeeded")
	res := f.await(t)
	f.m.settle(t.Context(), f.uid, res)
	f.s.Transition(f.uid, func(r *Run) error {
		now := time.Now()
		for i := range r.Ops {
			if r.Ops[i].Kind == "observe" {
				r.Ops[i].FirstSent = &now
			}
		}
		return nil
	})
	if e := f.m.Release(t.Context(), f.uid); !errors.Is(e, errControlRefused) {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	for _, op := range r.Ops {
		if op.Kind == "observe" {
			p, _ := decodePayload(op)
			if p.Observation.ExpectedRevision != 1 {
				t.Fatal("ignored pending observation", p.Observation)
			}
		}
	}
}

func TestAbandonNeverRetargetsPendingObservation(t *testing.T) {
	f := newAdmission(t)
	f.k.run = func(context.Context, string) (kata.Run, error) {
		return kata.Run{UID: f.uid, JobUID: "job", DefinitionEventUID: "event", OccurrenceKey: "manual:" + f.uid, Actor: "worker", ExecutorLabel: f.cfg.Executor, Status: "running", Revision: 1}, nil
	}
	f.k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) {
		return kata.Run{}, false, lostReply
	}
	if e := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); e == nil {
		t.Fatal("expected ambiguity")
	}
	var pin kata.Installation
	if e := state.ReadJSON(f.s.fsys, f.s.layout.StateFile(), 64<<10, &pin); e != nil {
		t.Fatal(e)
	}
	pin.InstanceUID = "different-authority"
	state.WriteJSON(f.s.fsys, f.s.layout.StateFile(), pin)
	var writes int
	f.k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) {
		writes++
		return kata.Run{}, true, nil
	}
	if e := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); e == nil || writes != 0 {
		t.Fatal("pending abandon retargeted", e, writes)
	}
}
func TestReleaseAfterCloseDoesNotWriteWorkOnClosedIssue(t *testing.T) {
	f := newAdmission(t)
	r := settleFixture()
	r.UID = f.uid
	r.Outcome = "succeeded"
	r.Ops = SettleOps(r, resultFixture(), time.Now())
	r.Ops[0].Done = true
	if e := f.s.Create(r); e != nil {
		t.Fatal(e)
	}
	if e := f.m.Release(t.Context(), f.uid); !errors.Is(e, errControlRefused) {
		t.Fatal(e)
	}
	r = loaded(t, f.s, f.uid)
	for _, op := range r.Ops {
		if strings.Contains(op.ID, ":release:") && op.Kind == "meta.set" {
			t.Fatal("queued work write after close", op)
		}
	}
}

type finishedManual struct{ manualReceiver }

func (*finishedManual) ManualResult(string) (bool, error) {
	return true, errors.New("manual job disabled")
}
func (*finishedManual) ForgetManual(string) {}
func TestQueuedManualRequestReportsSchedulerRefusal(t *testing.T) {
	f := newAdmission(t)
	req := Request{Version: 1, UID: "request", RunUID: "manual", Kind: "run", Job: "job"}
	p := filepath.Join(f.s.layout.RequestsDir(), "request.json")
	state.WriteJSON(f.s.fsys, p, req)
	f.m.requests.Store(req.UID, struct{}{})
	if e := f.m.HandleRequests(t.Context(), &finishedManual{}); e != nil {
		t.Fatal(e)
	}
	var result RequestResult
	if e := state.ReadJSON(f.s.fsys, RequestResultPath(f.s.layout, req.UID), 4096, &result); e != nil || result.Error == "" {
		t.Fatal("queued refusal missing", result, e)
	}
	if _, e := os.Stat(p); !os.IsNotExist(e) {
		t.Fatal("handled request remains", e)
	}
}
func TestReleasePreparedResolvesIdentityBeforeReporting(t *testing.T) {
	f := newAdmission(t)
	target, e := f.m.pin()
	if e != nil {
		t.Fatal(e)
	}
	r, e := f.m.prepare(f.d, f.uid, ident.OccurrenceKey(f.d.Occurrence), target)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.Create(r); e != nil {
		t.Fatal(e)
	}
	f.k.comment = func(context.Context, string, string, string) error { return nil }
	f.k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) { return 1, nil }
	f.k.notify = func(context.Context, string, string, string) error { return nil }
	f.k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{UID: "issue", QualifiedID: "project#task", Status: "open", Author: "operator", Revision: 1}, nil
	}
	f.start(t)
	if e = f.m.Release(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	r = loaded(t, f.s, f.uid)
	admission, admitErr := findOp(r, "admission")
	if admitErr != nil || !admission.Done {
		t.Fatal("release before admission resolved", admitErr)
	}
	if r.Phase != HandedOff || r.IssueUID == "" {
		t.Fatal(r)
	}
	for _, op := range r.Ops {
		if strings.Contains(op.ID, ":release:") {
			p, e := decodePayload(op)
			if e != nil {
				t.Fatal(e)
			}
			if op.Kind == "observe" {
				if p.Observation.IssueUID == "" || p.Observation.ExpectedRevision != 1 {
					t.Fatal(p)
				}
			} else if p.Ref == "" {
				t.Fatal("blank handoff target", op)
			}
		}
	}
}
func TestRetentionFreesDeletedReservation(t *testing.T) {
	f := newRecovery(t)
	f.m.cfg.Limits.MaxActiveRuns = 1
	old := time.Now().Add(-15 * 24 * time.Hour)
	f.s.Transition(f.uid, func(r *Run) error { r.Phase = Settled; r.Outcome = "succeeded"; r.EndedAt = &old; return nil })
	f.m.reservations[f.uid] = "job"
	if e := f.m.Retain(time.Now()); e != nil {
		t.Fatal(e)
	}
	if f.m.FreeSlots() != 1 || f.m.GateHeld("job") {
		t.Fatal("deleted run still reserves capacity")
	}
}
func TestCorruptRequestDoesNotStarveRelease(t *testing.T) {
	for _, bad := range []string{`broken`, `{"version":2,"uid":"a-bad","kind":"release","run_uid":"run"}`, `{"version":1,"uid":"other-name","kind":"release","run_uid":"run"}`, `{"version":1,"uid":"a-bad","kind":"release","run_uid":"run","unexpected":true}`} {
		t.Run(bad, func(t *testing.T) {
			f := newRecovery(t)
			os.MkdirAll(f.s.layout.RequestsDir(), 0700)
			path := filepath.Join(f.s.layout.RequestsDir(), "a-bad.json")
			os.WriteFile(path, []byte(bad), 0600)
			req := Request{Version: 1, UID: "z-release", Kind: "release", RunUID: f.uid}
			state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.RequestsDir(), req.UID+".json"), req)
			if e := f.m.HandleRequests(t.Context(), nil); e != nil {
				t.Fatal(e)
			}
			if loaded(t, f.s, f.uid).Phase != HandedOff {
				t.Fatal("corrupt request starved release")
			}
			var result RequestResult
			if e := state.ReadJSON(f.s.fsys, RequestResultPath(f.s.layout, "a-bad"), 64<<10, &result); e != nil || result.Error == "" || strings.Contains(result.Error, f.s.layout.Root) {
				t.Fatal(result, e)
			}
			if _, e := os.Stat(path); !os.IsNotExist(e) {
				t.Fatal("bad request not moved", e)
			}
		})
	}
}
func TestPoisonedPreparedReleaseDoesNotStarveControls(t *testing.T) {
	f := newRecovery(t)
	if e := f.s.Create(&Run{Version: 1, UID: "poison", Phase: Prepared, Inspect: "creation requires inspection"}); e != nil {
		t.Fatal(e)
	}
	for _, req := range []Request{{Version: 1, UID: "a-poison", Kind: "release", RunUID: "poison"}, {Version: 1, UID: "z-valid", Kind: "release", RunUID: f.uid}} {
		if e := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.RequestsDir(), req.UID+".json"), req); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.m.HandleRequests(t.Context(), nil); e != nil {
		t.Fatal(e)
	}
	if loaded(t, f.s, f.uid).Phase != HandedOff {
		t.Fatal("poisoned release starved control")
	}
	var result RequestResult
	if e := state.ReadJSON(f.s.fsys, RequestResultPath(f.s.layout, "a-poison"), 4096, &result); e != nil || result.Error == "" {
		t.Fatal("missing permanent refusal", result, e)
	}
	if _, e := os.Stat(filepath.Join(f.s.layout.RequestsDir(), "a-poison.json")); !os.IsNotExist(e) {
		t.Fatal("refused request retained", e)
	}
}

func TestReleaseRefusalPreservesCloseRetry(t *testing.T) {
	f := newAdmission(t)
	now := time.Now()
	r := settleFixture()
	r.UID, r.Target, r.Outcome, r.EndedAt = f.uid, Target{}, "succeeded", &now
	r.Ops = SettleOps(r, resultFixture(), now)
	if e := f.s.Create(r); e != nil {
		t.Fatal(e)
	}
	closed, closes, notifications, observations := false, 0, 0, 0
	f.k.close = func(context.Context, kata.CloseReq) error {
		closes++
		if closes == 1 {
			return &kata.Error{Code: "daemon_unavailable"}
		}
		closed = true
		return nil
	}
	f.k.show = func(context.Context, string) (kata.IssueView, error) {
		status := "open"
		if closed {
			status = "closed"
		}
		return kata.IssueView{UID: "issue", Status: status, Author: "operator", Revision: 1}, nil
	}
	f.k.comment = func(context.Context, string, string, string) error { return nil }
	f.k.notify = func(context.Context, string, string, string) error { notifications++; return nil }
	f.k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) {
		observations++
		return kata.Run{}, false, nil
	}
	if e := f.q.send(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	if observations != 1 || closes != 0 {
		t.Fatal("final observation did not precede the issue retry", observations, closes)
	}
	if e := f.q.send(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	pending := loaded(t, f.s, f.uid).Ops[0]
	if pending.FirstSent == nil || pending.Done {
		t.Fatal("close was not left in retry", pending)
	}
	if e := f.m.Release(t.Context(), f.uid); !errors.Is(e, errControlRefused) {
		t.Fatal(e)
	}
	// Retry the same frozen close immediately, without waiting for backoff.
	f.s.Transition(f.uid, func(r *Run) error { r.Ops[0].NextAt = time.Time{}; return nil })
	for range 10 {
		r = loaded(t, f.s, f.uid)
		next := head(r)
		if next == nil {
			break
		}
		if next.Inspect != "" {
			t.Fatal("handoff report stopped", next)
		}
		if e := f.q.send(t.Context(), f.uid); e != nil {
			t.Fatal(e)
		}
	}
	r = loaded(t, f.s, f.uid)
	if head(r) != nil || r.Inspect != "" || closes != 2 || notifications != 0 || observations != 1 {
		t.Fatal(r, closes, notifications, observations)
	}
}

func TestPreparedReleaseTimeoutDoesNotStarveHealthyRequest(t *testing.T) {
	f := newAdmission(t)
	target, e := f.m.pin()
	if e != nil {
		t.Fatal(e)
	}
	prepared, e := f.m.prepare(f.d, f.uid, "occurrence", target)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.Create(prepared); e != nil {
		t.Fatal(e)
	}
	if e = f.s.Create(&Run{Version: 1, UID: "healthy", Phase: Held, Outcome: "unknown", IssueRef: "project#task"}); e != nil {
		t.Fatal(e)
	}
	for _, req := range []Request{{Version: 1, UID: "a", Kind: "release", RunUID: f.uid}, {Version: 1, UID: "b", Kind: "release", RunUID: "healthy"}} {
		if e = state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.RequestsDir(), req.UID+".json"), req); e != nil {
			t.Fatal(e)
		}
	}
	// No write queue is available: preparation cannot complete during this scan.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if e = f.m.HandleRequests(ctx, nil); e != nil {
		t.Fatal(e)
	}
	if r := loaded(t, f.s, "healthy"); r.Phase != HandedOff {
		t.Fatal("healthy release starved", r)
	}
	if _, e = os.Stat(filepath.Join(f.s.layout.RequestsDir(), "a.json")); e != nil {
		t.Fatal("pending release lost", e)
	}
}

func TestReleaseHonorsContextWhileAdmissionLocked(t *testing.T) {
	f := newRecovery(t)
	lock := f.m.admissionLock(f.uid)
	lock.Lock()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.m.Release(ctx, f.uid) }()
	select {
	case e := <-done:
		lock.Unlock()
		if !errors.Is(e, context.DeadlineExceeded) {
			t.Fatal(e)
		}
	case <-time.After(300 * time.Millisecond):
		lock.Unlock()
		<-done
		t.Fatal("release ignored cancellation while admission was locked")
	}
}

func TestAbandonAppliedLostReplyConverges(t *testing.T) {
	f := newAdmission(t)
	current := kata.Run{UID: f.uid, JobUID: "job", DefinitionEventUID: "event", OccurrenceKey: "manual:" + f.uid, Actor: "worker", ExecutorLabel: f.cfg.Executor, Status: "running", Revision: 1}
	f.k.run = func(context.Context, string) (kata.Run, error) { return current, nil }
	writes := 0
	f.k.observe = func(_ context.Context, _ string, o kata.Observation) (kata.Run, bool, error) {
		writes++
		if o.ExpectedRevision != current.Revision {
			return kata.Run{}, false, &kata.Error{Code: "revision_conflict"}
		}
		current.Revision++
		current.Status, current.Summary, current.StartedAt, current.EndedAt, current.Teammate = o.Status, o.Summary, o.StartedAt, o.EndedAt, o.Teammate
		current.Transcripts, current.TranscriptsIncomplete = o.Transcripts, o.TranscriptsIncomplete
		return kata.Run{}, false, lostReply
	}
	if e := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); e == nil {
		t.Fatal("lost reply missing")
	}
	for range 2 {
		if e := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); e != nil {
			t.Fatal("applied abandon did not converge", e)
		}
	}
	if writes != 1 {
		t.Fatal("re-sent an applied observation", writes)
	}
	if _, e := os.Stat(filepath.Join(f.s.layout.Root, "abandoned", f.uid+".json")); !os.IsNotExist(e) {
		t.Fatal("pending abandon remains", e)
	}
}

func TestAbandonDoesNotReplaceChangedReport(t *testing.T) {
	f := newAdmission(t)
	current := kata.Run{UID: f.uid, JobUID: "job", DefinitionEventUID: "event", OccurrenceKey: "manual:" + f.uid, Actor: "worker", ExecutorLabel: f.cfg.Executor, Status: "running", Revision: 1}
	f.k.run = func(context.Context, string) (kata.Run, error) { return current, nil }
	writes := 0
	f.k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) {
		writes++
		return kata.Run{}, false, lostReply
	}
	if e := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); e == nil {
		t.Fatal("lost reply missing")
	}
	current.Status, current.Revision = "succeeded", 2
	if e := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); e == nil || writes != 1 {
		t.Fatal("changed report overwritten", e, writes)
	}
}

func TestSendRequestToOrphanWorker(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	lock, ok, err := state.TryLock(l.WorkerLock())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer lock.Unlock()
	if err := state.WriteJSON(state.OSFS{}, l.ServeInfo(), map[string]any{"worker_pid": 0}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"run", "release"} {
		req, err := SendRequest(state.OSFS{}, l, kind, "target")
		if err != nil {
			t.Fatal(err)
		}
		var saved Request
		if err := state.ReadJSON(state.OSFS{}, filepath.Join(l.RequestsDir(), req.UID+".json"), 4096, &saved); err != nil || saved != req {
			t.Fatal(saved, err)
		}
	}
}

func TestManualRequestQuarantinedReportsRefusal(t *testing.T) {
	f := newAdmission(t)
	req := Request{Version: 1, UID: "request", Kind: "run", Job: "job", RunUID: "manual"}
	if err := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.RequestsDir(), "request.json"), req); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.s.layout.QuarantineRunDir(req.RunUID), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.s.layout.QuarantineRunDir(req.RunUID), "corrupt-run.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.m.HandleRequests(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	var result RequestResult
	if err := state.ReadJSON(f.s.fsys, RequestResultPath(f.s.layout, req.UID), 4096, &result); err != nil || !strings.Contains(result.Error, "quarantined") {
		t.Fatal(result, err)
	}
}

func TestFullManualQueueStillHandlesRelease(t *testing.T) {
	f := newAdmission(t)
	r := settleFixture()
	r.UID, r.Phase = f.uid, Held
	if err := f.s.Create(r); err != nil {
		t.Fatal(err)
	}
	f.m.k = &jobKata{admissionKata: f.k, job: kata.Job{UID: "job", Definition: []byte(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Inspect"},"issue":{"kind":"existing","uid":"issue"},"overlap":"forbid","catchup":"skip"}`)}}
	requests := []Request{{Version: 1, UID: "a-run", Kind: "run", Job: "job", RunUID: "manual"}, {Version: 1, UID: "b-release", Kind: "release", RunUID: f.uid}}
	for _, req := range requests {
		if err := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.RequestsDir(), req.UID+".json"), req); err != nil {
			t.Fatal(err)
		}
	}
	scheduler := &manualReceiver{err: errors.New("manual queue full")}
	if err := f.m.HandleRequests(t.Context(), scheduler); err != nil {
		t.Fatal(err)
	}
	if loaded(t, f.s, f.uid).Phase != HandedOff {
		t.Fatal("release starved by manual queue")
	}
	if _, err := os.Stat(filepath.Join(f.s.layout.RequestsDir(), "a-run.json")); err != nil {
		t.Fatal("backpressured request lost", err)
	}
	if _, err := os.Stat(filepath.Join(f.s.layout.RequestsDir(), "b-release.json")); !os.IsNotExist(err) {
		t.Fatal("release not acknowledged", err)
	}
	scheduler.err = nil
	if err := f.m.HandleRequests(t.Context(), scheduler); err != nil {
		t.Fatal(err)
	}
	if scheduler.calls != 2 {
		t.Fatal("manual request not retried", scheduler.calls)
	}
}

func TestManualRequestRefusesPausedJob(t *testing.T) {
	f := newAdmission(t)
	f.m.k = &jobKata{admissionKata: f.k, job: kata.Job{UID: "job", Definition: []byte(`{"version":1,"kind":"job","enabled":false,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Inspect"},"issue":{"kind":"existing","uid":"issue"},"overlap":"forbid","catchup":"skip"}`)}}
	req := Request{Version: 1, UID: "request", Kind: "run", Job: "job", RunUID: "manual"}
	if err := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.RequestsDir(), "request.json"), req); err != nil {
		t.Fatal(err)
	}
	scheduler := &manualReceiver{}
	if err := f.m.HandleRequests(t.Context(), scheduler); err != nil {
		t.Fatal(err)
	}
	var result RequestResult
	if err := state.ReadJSON(f.s.fsys, RequestResultPath(f.s.layout, req.UID), 4096, &result); err != nil || !strings.Contains(result.Error, "paused") || !strings.Contains(result.Error, "resume") || scheduler.calls != 0 {
		t.Fatal(result, err, scheduler)
	}
}

type hiddenManual struct{ *jobKata }

func (k *hiddenManual) ShowJob(context.Context, string) (kata.Job, error) {
	return kata.Job{}, &kata.Error{Code: "not_found"}
}
func TestManualRequestHidden404StaysPending(t *testing.T) {
	f := newAdmission(t)
	f.m.k = &hiddenManual{jobKata: &jobKata{admissionKata: f.k}}
	req := Request{Version: 1, UID: "request", Kind: "run", Job: "job", RunUID: "manual-uid"}
	path := filepath.Join(f.s.layout.RequestsDir(), "request.json")
	if err := state.WriteJSON(f.s.fsys, path, req); err != nil {
		t.Fatal(err)
	}
	receiver := &manualReceiver{}
	if err := f.m.HandleRequests(t.Context(), receiver); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil || receiver.calls != 0 {
		t.Fatal("hidden job request dropped or dispatched", err, receiver.calls)
	}
	if _, err := os.Stat(RequestResultPath(f.s.layout, req.UID)); !os.IsNotExist(err) {
		t.Fatal("hidden job treated as refused", err)
	}
}

func TestManualRequestExplainsSecretClassification(t *testing.T) {
	for _, tc := range []struct{ variable, secret, reason string }{
		{"KATA_SERVER", "review", "secret_refs reserved variable: KATA_SERVER"},
		{"HERDR_KATA_RUN", "review", "secret_refs reserved variable: HERDR_KATA_RUN"},
		{"GITHUB_TOKEN", "missing", "unmapped secret: missing"},
	} {
		t.Run(tc.variable, func(t *testing.T) {
			f := newAdmission(t)
			f.m.cfg.Secrets = map[string]secretref.Ref{"review": {Env: "EXAMPLE_SOURCE"}}
			d := f.d.Def
			d.Action.WorkflowUID, d.Action.Prompt = "", "Inspect"
			d.SecretRefs = map[string]string{tc.variable: tc.secret}
			definition, err := json.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			f.m.k = &jobKata{admissionKata: f.k, job: kata.Job{UID: "job", Definition: definition}}
			req := Request{Version: 1, UID: "request", Kind: "run", Job: "job", RunUID: "manual"}
			if err := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.RequestsDir(), "request.json"), req); err != nil {
				t.Fatal(err)
			}
			scheduler := &manualReceiver{}
			if err := f.m.HandleRequests(t.Context(), scheduler); err != nil {
				t.Fatal(err)
			}
			var result RequestResult
			if err := state.ReadJSON(f.s.fsys, RequestResultPath(f.s.layout, req.UID), 4096, &result); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result.Error, tc.reason) || scheduler.calls != 0 {
				t.Fatalf("manual refusal lost the specific reason: %+v calls=%d", result, scheduler.calls)
			}
		})
	}
}

func TestCanceledResumeScanDoesNotWaitForJournal(t *testing.T) {
	f := heldResumeFixture(t)
	publishResume(t, f, Request{Version: 1, UID: "resume-request", Kind: "resume", RunUID: f.uid})
	journal := f.s.lock(f.uid)
	journal.Lock()
	locked := true
	defer func() {
		if locked {
			journal.Unlock()
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan error, 1)
	go func() { done <- f.m.HandleRequests(ctx, nil) }()
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Error("canceled resume scan blocked on journal")
		journal.Unlock()
		locked = false
		err = <-done
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatal("resume scan ignored cancellation", err)
	}
	if _, err = os.Stat(filepath.Join(f.s.layout.RequestsDir(), "resume-request.json")); err != nil {
		t.Fatal("canceled scan discarded resume request", err)
	}
}

func TestReleaseRefusesSettledRunWithoutChangingEvidence(t *testing.T) {
	for _, outcome := range []string{"succeeded", "failed", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			f := newAdmission(t)
			r := settleFixture()
			r.UID, r.Phase, r.Outcome, r.Ops = f.uid, Settled, outcome, nil
			if err := f.s.Create(r); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(f.s.layout.RunFile(f.uid))
			if err != nil {
				t.Fatal(err)
			}
			writes := 0
			f.k.comment = func(context.Context, string, string, string) error { writes++; return nil }
			f.k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) { writes++; return 1, nil }
			f.k.observe = func(context.Context, string, kata.Observation) (kata.Run, bool, error) {
				writes++
				return kata.Run{}, true, nil
			}
			if err := f.m.Release(t.Context(), f.uid); !errors.Is(err, errControlRefused) || !strings.Contains(err.Error(), "release requires an unfinished run") {
				t.Fatalf("release: %v", err)
			}
			after, err := os.ReadFile(f.s.layout.RunFile(f.uid))
			if err != nil || string(after) != string(before) {
				t.Fatal("settled journal changed", err)
			}
			if err := f.q.send(t.Context(), f.uid); err != nil {
				t.Fatal(err)
			}
			if writes != 0 {
				t.Fatal("release posted to Kata", writes)
			}
		})
	}
}

func TestManualRefusalCarriesRequestIdentity(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "scheduler refusal", true: "rejected request"}[invalid], func(t *testing.T) {
			f := newAdmission(t)
			req := Request{Version: 1, UID: "request", RunUID: "manual", Kind: "run", Project: "project", Job: "job"}
			if invalid {
				req.Version = 2
			}
			if err := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.RequestsDir(), "request.json"), req); err != nil {
				t.Fatal(err)
			}
			if err := f.m.HandleRequests(t.Context(), &finishedManual{}); err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			if err := state.ReadJSON(f.s.fsys, RequestResultPath(f.s.layout, "request"), 64<<10, &result); err != nil {
				t.Fatal(err)
			}
			if result["kind"] != "run" || result["project"] != "project" || result["job"] != "job" || result["run_uid"] != "manual" || result["error"] == "" {
				t.Fatal("refusal lost request identity", result)
			}
		})
	}
}
