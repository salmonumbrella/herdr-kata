package runner

import (
	"context"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type quarantineFault struct {
	state.FS
	point, src, dst string
	hit             bool
	renames         int
	sourceSyncs     int
}

func (f *quarantineFault) Rename(a, b string) error {
	wholeRun := a == f.src && b == f.dst
	if wholeRun {
		f.renames++
	}
	err := f.FS.Rename(a, b)
	if err == nil && wholeRun && f.point == "rename" && !f.hit {
		f.hit = true
		return errors.New("interrupted after rename")
	}
	return err
}
func (f *quarantineFault) SyncDir(p string) error {
	if p == filepath.Dir(f.src) {
		f.sourceSyncs++
	}
	err := f.FS.SyncDir(p)
	if err == nil && !f.hit && (f.point == "destination" && p == filepath.Dir(f.dst) || f.point == "source" && p == filepath.Dir(f.src)) {
		f.hit = true
		return errors.New("interrupted after fsync")
	}
	return err
}
func TestLocalAbandonInterruptedRenameAndSyncRetry(t *testing.T) {
	for _, point := range []string{"rename", "destination", "source"} {
		t.Run(point, func(t *testing.T) {
			f := newAdmission(t)
			no := false
			r := &Run{Version: 1, UID: f.uid, Phase: Held, Reason: "admission_rejected", Admitted: &no, Ops: []Op{newOp("admission", "observe", Payload{}, "")}}
			if err := f.s.Create(r); err != nil {
				t.Fatal(err)
			}
			artifact := filepath.Join(f.s.layout.RunDir(f.uid), "output.log")
			if err := os.WriteFile(artifact, []byte("evidence"), 0600); err != nil {
				t.Fatal(err)
			}
			disk := &quarantineFault{FS: f.s.fsys, point: point, src: f.s.layout.RunDir(f.uid), dst: f.s.layout.QuarantineRunDir(f.uid)}
			if err := AbandonLocal(t.Context(), disk, f.s.layout, f.uid, nil); err == nil || !disk.hit {
				t.Fatal("fault not surfaced", err)
			}
			for range 2 {
				if err := AbandonLocal(t.Context(), disk, f.s.layout, f.uid, nil); err != nil {
					t.Fatal(err)
				}
			}
			if point == "source" && disk.sourceSyncs < 2 {
				t.Fatal("retry skipped uncertain source-directory sync")
			}
			if disk.renames != 1 {
				t.Fatal("retry renamed again", disk.renames)
			}
			got, err := os.ReadFile(filepath.Join(disk.dst, "output.log"))
			if err != nil || string(got) != "evidence" {
				t.Fatal(string(got), err)
			}
			if _, err = os.Stat(disk.src); !os.IsNotExist(err) {
				t.Fatal("source remains", err)
			}
			if !state.Quarantined(disk, f.s.layout, f.uid) {
				t.Fatal("veto lost")
			}
			// Plain abandon now reaches its remote checks, unchanged.
			checked := false
			f.k.run = func(context.Context, string) (kata.Run, error) {
				checked = true
				return kata.Run{}, &kata.Error{Code: "not_found"}
			}
			if err = Abandon(t.Context(), f.cfg, f.k, f.g, disk, f.s.layout, f.uid); err == nil || !checked {
				t.Fatal("plain abandon refused local directory", err)
			}
		})
	}
}
func TestLocalSigningResolutionPreservesPendingOperationsAndVeto(t *testing.T) {
	for _, kind := range []string{"create", "claim", "admission", "maker-comment", "report"} {
		t.Run(kind, func(t *testing.T) {
			f := newAdmission(t)
			no := false
			now := time.Now().UTC()
			op := newOp(kind, map[string]string{"create": "create", "claim": "claim", "admission": "observe", "maker-comment": "comment", "report": "comment"}[kind], Payload{Ref: "issue", Body: "pending"}, "original-key")
			op.SigningRefused = true
			op.FirstSent = &now
			op.Attempts = 1
			phase := Prepared
			if kind == "claim" {
				phase = Admitted
			}
			if kind == "maker-comment" {
				phase = Running
			}
			if kind == "report" {
				phase = Settled
			}
			proof := !no && phase != Prepared
			if err := f.s.Create(&Run{Version: 1, UID: f.uid, Phase: phase, Admitted: &proof, Ops: []Op{op}}); err != nil {
				t.Fatal(err)
			}
			if err := AbandonLocal(t.Context(), f.s.fsys, f.s.layout, f.uid, nil); err != nil {
				t.Fatal(err)
			}
			var archived Run
			if err := state.ReadJSON(f.s.fsys, filepath.Join(f.s.layout.QuarantineRunDir(f.uid), "run.json"), 4<<20, &archived); err != nil {
				t.Fatal(err)
			}
			if len(archived.Ops) != 1 || intent(archived.Ops[0]) != intent(op) || !archived.Ops[0].SigningRefused || archived.Ops[0].Done {
				t.Fatal("uncertain operation changed", archived.Ops)
			}
			f.s = NewStore(f.s.fsys, f.s.layout)
			f.q = NewQueue(f.k, f.s, f.g, RealClock{})
			f.m = NewManager(f.cfg, f.k, nil, f.s, f.q, f.g, RealClock{})
			f.k.run = func(context.Context, string) (kata.Run, error) {
				t.Error("quarantine reached hidden authority")
				return kata.Run{}, &kata.Error{Code: "not_found"}
			}
			if err := f.m.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			result, err := f.m.Admit(t.Context(), f.d)
			if err != nil || result != sched.Dropped {
				t.Fatal("quarantine admitted", result, err)
			}
			runQueueBatch(t, f)
		})
	}
}
func TestLocalAbandonRefusesOrdinaryLiveAndDuplicateDestination(t *testing.T) {
	for _, scenario := range []string{"ordinary", "live", "both"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAdmission(t)
			r := &Run{Version: 1, UID: f.uid, Phase: Held, Reason: "admission_rejected"}
			if scenario == "ordinary" {
				r.Reason = "timeout"
			}
			if scenario == "live" {
				r.Herdr.LaunchIntent = true
			}
			if err := f.s.Create(r); err != nil {
				t.Fatal(err)
			}
			if scenario == "both" {
				if err := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.QuarantineRunDir(f.uid), "old.json"), map[string]string{}); err != nil {
					t.Fatal(err)
				}
			}
			if err := AbandonLocal(t.Context(), f.s.fsys, f.s.layout, f.uid, nil); err == nil {
				t.Fatal("unsafe local resolution")
			}
			if _, err := os.Stat(f.s.layout.RunFile(f.uid)); err != nil {
				t.Fatal("journal moved on refusal", err)
			}
		})
	}
}

func TestLocalAbandonStoppedMakerBeforeRunOutcome(t *testing.T) {
	for _, status := range []string{"idle", "done", "working"} {
		t.Run(status, func(t *testing.T) {
			f := newRecovery(t)
			f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Working}})
			f.accepted(t)
			r := loaded(t, f.s, f.uid)
			a, err := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
			if err != nil {
				t.Fatal(err)
			}
			a.Status = status
			seq := uint64(1)
			a.CompletionSeq = &seq
			f.h.SetAgent(a)
			if _, err = f.s.Transition(f.uid, func(r *Run) error {
				r.Steps[0].Status = "succeeded"
				r.Steps[0].Result = &ResultRef{Version: 1, Run: r.UID, Step: "main", Attempt: 1, Status: "succeeded", Summary: "Maker result ready for reporting."}
				op := newOp("maker-comment", "comment", Payload{Ref: "issue", Body: "Maker result ready for reporting."}, "maker-key")
				op.SigningRefused = true
				r.Ops = []Op{op}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			err = AbandonLocal(t.Context(), f.s.fsys, f.s.layout, f.uid, f.m.h)
			if status == "working" {
				if err == nil {
					t.Fatal("live maker quarantined")
				}
				return
			}
			if err != nil {
				t.Fatal("stopped maker requires a final run outcome", err)
			}
			if !state.Quarantined(f.s.fsys, f.s.layout, f.uid) {
				t.Fatal("stopped maker not quarantined")
			}
		})
	}
}

func TestLocalAbandonInterruptedCommandAfterSigningRefusal(t *testing.T) {
	f := newAdmission(t)
	spec := commandSpec(t, "exec sleep 60")
	spec.Run = f.uid
	spec.Step = "main"
	spec.AttemptDir = f.s.layout.AttemptDir(f.uid, "main", 1)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	receipt, err := RunCommand(ctx, spec)
	if !errors.Is(err, context.DeadlineExceeded) || !receipt.Interrupted || !GroupGone(receipt.PGID) {
		t.Fatal("timeout did not produce a stopped interrupted command", receipt, err)
	}
	yes := true
	r := &Run{Version: 1, UID: f.uid, Phase: Held, Admitted: &yes, Outcome: "unknown", Reason: "timeout", Steps: []StepState{{Key: "main", Kind: "command", Attempt: 1, Submission: Sending, Status: "unknown", Receipt: &receipt}}, Ops: []Op{operation("report", "comment", Payload{Ref: "issue", Body: "Command timed out."})}}
	if err = f.s.Create(r); err != nil {
		t.Fatal(err)
	}
	f.k.comment = func(context.Context, string, string, string) error {
		return &kata.Error{Code: "root_signing_key_unavailable", Exit: 5}
	}
	deliver(t, f.q, f.uid)
	if !loaded(t, f.s, f.uid).Ops[0].SigningRefused {
		t.Fatal("signing refusal not reproduced")
	}
	if err = AbandonLocal(t.Context(), f.s.fsys, f.s.layout, f.uid, nil); err != nil {
		t.Fatal("stopped command required a successful outcome or Herdr", err)
	}
	var archived Run
	if err = state.ReadJSON(f.s.fsys, filepath.Join(f.s.layout.QuarantineRunDir(f.uid), "run.json"), 4<<20, &archived); err != nil {
		t.Fatal(err)
	}
	if archived.Outcome != "unknown" || !reflect.DeepEqual(archived.Steps[0].Receipt, &receipt) || !archived.Ops[0].SigningRefused || archived.Ops[0].Done {
		t.Fatal("quarantine rewrote uncertain evidence", archived)
	}
}
func TestLocalAbandonRefusesLiveCommandGroup(t *testing.T) {
	f := newAdmission(t)
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = commandProcAttr()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	now := time.Now()
	receipt := &Receipt{Version: 1, Run: f.uid, Step: "main", Attempt: 1, StartedAt: now, EndedAt: now, PGID: cmd.Process.Pid, Interrupted: true}
	if GroupGone(receipt.PGID) {
		t.Fatal("command not alive")
	}
	op := operation("report", "comment", Payload{Ref: "issue", Body: "uncertain"})
	op.SigningRefused = true
	if err := f.s.Create(&Run{Version: 1, UID: f.uid, Phase: Held, Outcome: "unknown", Steps: []StepState{{Key: "main", Kind: "command", Attempt: 1, Submission: Sending, Receipt: receipt}}, Ops: []Op{op}}); err != nil {
		t.Fatal(err)
	}
	if err := AbandonLocal(t.Context(), f.s.fsys, f.s.layout, f.uid, nil); err == nil {
		t.Fatal("live group quarantined")
	}
	if _, err := f.s.Load(f.uid); err != nil {
		t.Fatal("live journal moved", err)
	}
}

func TestLocalAbandonBoundsUnresponsiveHerdrAndReleasesLocks(t *testing.T) {
	f := newRecovery(t)
	var block atomic.Bool
	var lookups atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	f.h.BeforeRequest = func(req herdrtest.Request) {
		if req.Method == "agent.get" && block.Load() && lookups.Add(1) > 1 {
			<-release
		}
	}
	f.launch(t)
	f.h.Fail("agent.get", herdr.Error{Code: "agent_not_found"}, herdr.Error{Code: "unavailable"})
	block.Store(true)
	if _, err := f.s.Transition(f.uid, func(r *Run) error {
		op := operation("report", "comment", Payload{Ref: "issue", Body: "pending"})
		op.SigningRefused = true
		r.Ops = []Op{op}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- AbandonLocal(context.Background(), f.s.fsys, f.s.layout, f.uid, f.m.h) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unresponsive occupant quarantined")
		}
	case <-time.After(35 * time.Second):
		unblock()
		<-done
		t.Fatal("unresponsive Herdr kept offline recovery and both runtime locks indefinitely")
	}
	for _, path := range []string{f.s.layout.ServeLock(), f.s.layout.WorkerLock()} {
		if held, err := state.Held(path); err != nil || held {
			t.Fatal("proof timeout retained runtime lock", held, err)
		}
	}
	if _, err := f.s.Load(f.uid); err != nil {
		t.Fatal("proof timeout moved journal", err)
	}
}

func runQueueBatch(t *testing.T, f *admissionFixture) {
	t.Helper()
	ack := make(chan struct{}, 1)
	f.k.comment = func(context.Context, string, string, string) error { ack <- struct{}{}; return nil }
	sentinel := &Run{Version: 1, UID: "zz", Phase: Admitted, Ops: []Op{operation("sentinel", "comment", Payload{Ref: "other-issue", Body: "sentinel"})}}
	if _, err := f.s.Load("zz"); missing(err) {
		if err = f.s.Create(sentinel); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err = f.s.Transition("zz", func(r *Run) error {
			r.Ops = append(r.Ops, operation(fmt.Sprint(len(r.Ops)+1), "comment", Payload{Ref: "other-issue", Body: "sentinel"}))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- f.q.Run(ctx) }()
	select {
	case <-ack:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("queue did not deliver sentinel")
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
