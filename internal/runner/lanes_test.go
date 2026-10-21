package runner

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// §22.4–5: global capacity survives an unavailable lane, and saved operations
// use their frozen project even when both projects use the same issue ref.
func TestLaneCapacityAndDelivery(t *testing.T) {
	f := newAdmission(t)
	f.m.EnableProjects()
	for _, uid := range []string{"project-a", "project-b"} {
		cfg := f.cfg
		cfg.Kata.Project = uid
		cfg.ProjectLimit = 1
		l := f.s.layout.Project(uid)
		if e := state.WriteJSON(f.s.fsys, l.StateFile(), kata.Installation{Version: 1, CreatedAt: time.Now(), Executor: cfg.Executor, InstanceUID: "instance", ProjectUID: uid, Actor: "worker", Route: kata.Route{Bin: "/bin/kata"}}); e != nil {
			t.Fatal(e)
		}
		k := &fakeKata{comment: func(_ context.Context, ref, body, key string) error {
			if ref != "same-ref" || body != uid {
				t.Errorf("crossed lanes: %s %s via %s", ref, body, uid)
			}
			return nil
		}}
		lane, e := f.m.RegisterProject(uid, cfg, k, &admissionGuard{}, l)
		if e != nil {
			t.Fatal(e)
		}
		if !lane.reserve("run-"+uid, "job", true) {
			t.Fatal("different project's gate blocked admission")
		}
		if lane.reserve("extra-"+uid, "other-job", false) {
			t.Fatal("project cap ignored")
		}
		r := &Run{Version: 1, UID: "run-" + uid, Phase: Admitted, Job: kata.Job{UID: "job"}, Target: Target{Identity: kata.Identity{InstanceUID: "instance", ProjectUID: uid, Actor: "worker"}, Executor: cfg.Executor, Route: kata.Route{Bin: "/bin/kata"}}, Ops: []Op{operation("comment", "comment", Payload{Ref: "same-ref", Body: uid})}}
		if e := f.s.Create(r); e != nil {
			t.Fatal(e)
		}
	}
	if f.m.FreeSlots() != 0 {
		t.Fatal("global cap ignored")
	}
	if _, e := f.m.RegisterProject("project-a", f.cfg, f.k, f.g, f.s.layout); e == nil {
		t.Fatal("duplicate project UID accepted")
	}
	for _, uid := range []string{"project-a", "project-b"} {
		if e := f.q.send(context.Background(), "run-"+uid); e != nil {
			t.Fatal(e)
		}
		if !loaded(t, f.s, "run-"+uid).Ops[0].Done {
			t.Fatal("not delivered")
		}
	}
	// A removed project's payload must never reach any remaining route.
	if _, e := f.s.Transition("run-project-a", func(r *Run) error {
		r.Target.Identity.ProjectUID = "missing-project"
		r.Ops = append(r.Ops, operation("pending", "comment", Payload{Ref: "same-ref", Body: "never send"}))
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if e := f.q.send(t.Context(), "run-project-a"); e != nil {
		t.Fatal(e)
	}
	if loaded(t, f.s, "run-project-a").Ops[1].Done {
		t.Fatal("missing lane fell back")
	}
}
func TestRestoreUnavailableProjectReservesCapacity(t *testing.T) {
	f := newAdmission(t)
	f.m.EnableProjects()
	r := &Run{Version: 1, UID: "waiting-run", Phase: Held, Job: kata.Job{UID: "job"}, Target: Target{Identity: kata.Identity{ProjectUID: "removed-project"}}}
	if e := f.s.Create(r); e != nil {
		t.Fatal(e)
	}
	if e := f.m.RestoreGates(); e != nil {
		t.Fatal(e)
	}
	if e := f.m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	if f.m.FreeSlots() != 1 {
		t.Fatal("unconfigured project released its slot")
	}
}

func TestProjectCommandsRefusedBeforePreparation(t *testing.T) {
	f := newAdmission(t)
	denied := false
	f.m.cfg.Commands = &denied
	target, e := f.m.pin()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.m.prepare(f.d, f.uid, "occurrence", target); e == nil {
		t.Fatal("commands=false admitted command workflow")
	}
}

func TestRefusedLaneCannotStarveOtherProject(t *testing.T) {
	f := newAdmission(t)
	f.m.EnableProjects()
	bad := &admissionGuard{}
	bad.blocked.Store(true)
	calls := make(chan string, 8)
	for _, uid := range []string{"project-a", "project-b"} {
		cfg := f.cfg
		cfg.Kata.Project = uid
		pl := f.s.layout.Project(uid)
		pin := kata.Installation{Version: 1, CreatedAt: time.Now(), Executor: cfg.Executor, InstanceUID: "instance", ProjectUID: uid, Actor: "worker"}
		if e := state.WriteJSON(f.s.fsys, pl.StateFile(), pin); e != nil {
			t.Fatal(e)
		}
		k := &fakeKata{comment: func(context.Context, string, string, string) error { calls <- uid; return nil }}
		guard := &admissionGuard{}
		if uid == "project-a" {
			guard = bad
		}
		if _, e := f.m.RegisterProject(uid, cfg, k, guard, pl); e != nil {
			t.Fatal(e)
		}
	}
	for _, row := range []struct{ uid, project string }{{"aaa", "project-a"}, {"aab", "project-a"}, {"zzz", "project-b"}} {
		if e := f.s.Create(&Run{Version: 1, UID: row.uid, Phase: Admitted, Target: Target{Identity: kata.Identity{InstanceUID: "instance", ProjectUID: row.project, Actor: "worker"}, Executor: f.cfg.Executor}, Ops: []Op{operation("report", "comment", Payload{Ref: "same-ref", Body: "report"})}}); e != nil {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.q.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case got := <-calls:
		if got != "project-b" {
			t.Fatalf("refused lane delivered via %s", got)
		}
	case <-ctx.Done():
		t.Fatal("refused lane starved healthy project")
	}
	if e := f.q.wait(ctx, "zzz", "report"); e != nil {
		t.Fatal(e)
	}
	for _, uid := range []string{"aaa", "aab"} {
		if loaded(t, f.s, uid).Ops[0].FirstSent != nil {
			t.Fatal("refused lane attempted a write")
		}
	}
}

func TestBusyJournalCannotStarveOtherProjectDelivery(t *testing.T) {
	f := newAdmission(t)
	f.m.EnableProjects()
	calls := make(chan string, 8)
	for _, uid := range []string{"project-a", "project-b"} {
		cfg := f.cfg
		cfg.Kata.Project = uid
		pl := f.s.layout.Project(uid)
		pin := kata.Installation{Version: 1, CreatedAt: time.Now(), Executor: cfg.Executor, InstanceUID: "instance", ProjectUID: uid, Actor: "worker"}
		if e := state.WriteJSON(f.s.fsys, pl.StateFile(), pin); e != nil {
			t.Fatal(e)
		}
		k := &fakeKata{comment: func(context.Context, string, string, string) error { calls <- uid; return nil }}
		if _, e := f.m.RegisterProject(uid, cfg, k, &admissionGuard{}, pl); e != nil {
			t.Fatal(e)
		}
		r := &Run{Version: 1, UID: "run-" + uid, Phase: Admitted, Target: Target{Identity: kata.Identity{InstanceUID: "instance", ProjectUID: uid, Actor: "worker"}, Executor: cfg.Executor}, Ops: []Op{operation("report", "comment", Payload{Ref: "same-ref", Body: "report"})}}
		if e := f.s.Create(r); e != nil {
			t.Fatal(e)
		}
	}
	journal := f.s.lock("run-project-a")
	journal.Lock() // Model a held occupant probe under A's journal lock.
	locked := true
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	done := make(chan error, 1)
	go func() { done <- f.q.Run(ctx) }()
	defer func() {
		if locked {
			journal.Unlock()
		}
		cancel()
		<-done
	}()
	select {
	case got := <-calls:
		if got != "project-b" {
			t.Fatal("delivery crossed projects", got)
		}
	case <-ctx.Done():
		t.Fatal("busy journal starved healthy project delivery")
	}
	if e := f.q.wait(ctx, "run-project-b", "report"); e != nil {
		t.Fatal(e)
	}
	journal.Unlock()
	locked = false
	select {
	case got := <-calls:
		if got != "project-a" {
			t.Fatal("busy journal retry crossed projects", got)
		}
	case <-ctx.Done():
		t.Fatal("delivery did not retry the previously busy journal")
	}
	if e := f.q.wait(ctx, "run-project-a", "report"); e != nil {
		t.Fatal(e)
	}
}

func TestExistingJournalRefusesSameJobInAnotherProject(t *testing.T) {
	f := newAdmission(t)
	var pin kata.Installation
	if err := state.ReadJSON(f.s.fsys, f.s.layout.StateFile(), 65536, &pin); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(f.s.fsys, f.s.layout.Project("project").StateFile(), pin); err != nil {
		t.Fatal(err)
	}
	if err := f.s.fsys.Remove(f.s.layout.StateFile()); err != nil {
		t.Fatal(err)
	}
	if e := state.InitializeLayout(f.s.fsys, f.s.layout); e != nil {
		t.Fatal(e)
	}
	f.m.EnableProjects()
	a, e := f.m.RegisterProject("project", f.cfg, f.k, f.g, f.s.layout.Project("project"))
	if e != nil {
		t.Fatal(e)
	}
	f.m = a
	f.start(t)
	r := f.admit(t)
	if r.Target.Identity.ProjectUID != "project" {
		t.Fatal(r.Target)
	}
	cfg := f.cfg
	cfg.Kata.Project = "other-project"
	pl := f.s.layout.Project("other-project")
	if e = state.WriteJSON(f.s.fsys, pl.StateFile(), kata.Installation{Version: 1, CreatedAt: time.Now(), Executor: cfg.Executor, InstanceUID: "other-instance", ProjectUID: "other-project", Actor: "worker"}); e != nil {
		t.Fatal(e)
	}
	k := &admissionKata{fakeKata: &fakeKata{}, run: func(context.Context, string) (kata.Run, error) {
		t.Error("cross-project collision reached authority read")
		return kata.Run{}, nil
	}}
	b, e := f.m.RegisterProject("other-project", cfg, k, &admissionGuard{}, pl)
	if e != nil {
		t.Fatal(e)
	}
	if decision, e := b.Admit(t.Context(), f.d); e == nil || decision != sched.NotAdmitted {
		t.Fatal("journal retargeted", decision, e)
	}
	if loaded(t, f.s, f.uid).Target.Identity.ProjectUID != "project" {
		t.Fatal("original journal changed")
	}
}

type laneCheckFunc func(context.Context) error

func (f laneCheckFunc) Check(ctx context.Context) error { return f(ctx) }

func TestSlowRouteCheckDoesNotBlockAnotherLane(t *testing.T) {
	f := newAdmission(t)
	f.m.EnableProjects()
	delivered := make(chan struct{}, 1)
	for _, uid := range []string{"project-a", "project-b"} {
		cfg := f.cfg
		cfg.Kata.Project = uid
		pl := f.s.layout.Project(uid)
		if e := state.WriteJSON(f.s.fsys, pl.StateFile(), kata.Installation{Version: 1, CreatedAt: time.Now(), Executor: cfg.Executor, InstanceUID: "instance", ProjectUID: uid, Actor: "worker"}); e != nil {
			t.Fatal(e)
		}
		var guard RouteChecker = &admissionGuard{}
		if uid == "project-a" {
			guard = laneCheckFunc(func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
		}
		k := &fakeKata{comment: func(context.Context, string, string, string) error {
			if uid != "project-b" {
				t.Error("blocked route delivered")
			}
			delivered <- struct{}{}
			return nil
		}}
		if _, e := f.m.RegisterProject(uid, cfg, k, guard, pl); e != nil {
			t.Fatal(e)
		}
		runUID := "aaa"
		if uid == "project-b" {
			runUID = "zzz"
		}
		if e := f.s.Create(&Run{Version: 1, UID: runUID, Phase: Admitted, Target: Target{Identity: kata.Identity{InstanceUID: "instance", ProjectUID: uid, Actor: "worker"}, Executor: cfg.Executor}, Ops: []Op{operation("report", "comment", Payload{Ref: "same-ref", Body: "report"})}}); e != nil {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.q.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-delivered:
	case <-ctx.Done():
		t.Fatal("slow route check blocked healthy delivery")
	}
	if e := f.q.wait(ctx, "zzz", "report"); e != nil {
		t.Fatal(e)
	}
}

func TestRecoveryPropagatesRouteFailureForRetry(t *testing.T) {
	f := newAdmission(t)
	r, e := f.m.prepare(f.d, f.uid, "occurrence", Target{Identity: kata.Identity{InstanceUID: "instance", ProjectUID: "project", Actor: "worker"}, Executor: f.cfg.Executor})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.Create(r); e != nil {
		t.Fatal(e)
	}
	f.g.blocked.Store(true)
	if e = f.m.Recover(t.Context()); e == nil {
		t.Fatal("recovery silently skipped a journal on route failure")
	}
	if loaded(t, f.s, f.uid).Phase != Prepared {
		t.Fatal("route failure advanced the run")
	}
}

func TestReleaseUnadmittedRunWaitsForMissingProject(t *testing.T) {
	f := newAdmission(t)
	target, e := f.m.pin()
	if e != nil {
		t.Fatal(e)
	}
	r, e := f.m.prepare(f.d, f.uid, "occurrence", target)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.Create(r); e != nil {
		t.Fatal(e)
	}
	f.m.EnableProjects()
	if e = f.m.RestoreGates(); e != nil {
		t.Fatal(e)
	}
	if e = f.m.Release(t.Context(), f.uid); e == nil {
		t.Fatal("unadmitted run was handed off without its lane")
	}
	saved := loaded(t, f.s, f.uid)
	if saved.Phase != Prepared || admitted(saved) || len(saved.Ops) != 1 || f.m.FreeSlots() != 1 {
		t.Fatal("offline release lost admission requirements", saved)
	}
}

type wakingReceiver struct {
	projectManualReceiver
	pokes int
}

func (r *wakingReceiver) Poke() { r.pokes++ }
func TestReleaseWakesOtherProjectsSharingCapacity(t *testing.T) {
	f := newRecovery(t)
	cfg := f.m.cfg
	cfg.Limits.MaxActiveRuns = 1
	root := NewManager(cfg, nil, f.m.h, f.s, nil, nil, nil)
	root.EnableProjects()
	a, e := root.RegisterProject("project-a", cfg, nil, nil, f.s.layout.Project("project-a"))
	if e != nil {
		t.Fatal(e)
	}
	b, e := root.RegisterProject("project-b", cfg, nil, nil, f.s.layout.Project("project-b"))
	if e != nil {
		t.Fatal(e)
	}
	ar, br := &wakingReceiver{}, &wakingReceiver{}
	a.SetScheduler(ar)
	b.SetScheduler(br)
	if _, e = f.s.Transition(f.uid, func(r *Run) error { r.Target.Identity.ProjectUID = "project-a"; r.Phase = Held; return nil }); e != nil {
		t.Fatal(e)
	}
	if e = root.RestoreGates(); e != nil {
		t.Fatal(e)
	}
	if b.FreeSlots() != 0 {
		t.Fatal("fixture did not exhaust shared capacity")
	}
	if e = root.Release(t.Context(), f.uid); e != nil {
		t.Fatal(e)
	}
	if b.FreeSlots() != 1 || br.pokes == 0 {
		t.Fatal("other project was not woken when capacity was released", br.pokes)
	}
}

func TestBusyPeerJournalDoesNotBlockProjectCapacity(t *testing.T) {
	f := newAdmission(t)
	f.m.EnableProjects()
	a, e := f.m.RegisterProject("project-a", f.cfg, nil, nil, f.s.layout.Project("project-a"))
	if e != nil {
		t.Fatal(e)
	}
	b, e := f.m.RegisterProject("project-b", f.cfg, nil, nil, f.s.layout.Project("project-b"))
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.Create(&Run{Version: 1, UID: "peer", Phase: Held, Job: kata.Job{UID: "job-a"}, Target: Target{Identity: kata.Identity{ProjectUID: "project-a"}}}); e != nil {
		t.Fatal(e)
	}
	if e = f.m.RestoreGates(); e != nil {
		t.Fatal(e)
	}
	journal := f.s.lock("peer")
	journal.Lock()
	locked := true
	defer func() {
		if locked {
			journal.Unlock()
		}
	}()
	type result struct {
		free                  int
		reserved, extra, gate bool
	}
	done := make(chan result, 1)
	go func() {
		done <- result{b.FreeSlots(), b.reserve("new", "job-b", false), b.reserve("extra", "other-job", false), a.GateHeld("job-a")}
	}()
	var got result
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Error("another project's journal blocked capacity checks")
		journal.Unlock()
		locked = false
		got = <-done
	}
	if got.free != 1 || !got.reserved || got.extra || !got.gate {
		t.Fatal("busy peer was not counted conservatively", got)
	}
}

func TestBusyForeignSigningJournalDoesNotBlockAdmission(t *testing.T) {
	f := newAdmission(t)
	f.m.EnableProjects()
	lane, err := f.m.RegisterProject("project", f.cfg, f.k, f.g, f.s.layout)
	if err != nil {
		t.Fatal(err)
	}
	op := operation("report", "comment", Payload{Ref: "issue", Body: "pending report"})
	op.SigningRefused = true
	if err := f.s.Create(&Run{Version: 1, UID: "peer", Phase: Held, Target: Target{Identity: kata.Identity{ProjectUID: "project-a"}}, Ops: []Op{op}}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.RestoreGates(); err != nil {
		t.Fatal(err)
	}
	f.m.executions.Store(f.uid, struct{}{})
	f.start(t)
	journal := f.s.lock("peer")
	journal.Lock()
	locked := true
	defer func() {
		if locked {
			journal.Unlock()
		}
	}()
	type result struct {
		decision sched.AdmitResult
		err      error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() {
		decision, err := lane.Admit(ctx, f.d)
		done <- result{decision, err}
	}()
	var got result
	select {
	case got = <-done:
	case <-ctx.Done():
		t.Error("foreign signing journal blocked admission with spare capacity")
		journal.Unlock()
		locked = false
		got = <-done
	}
	if got.err != nil || got.decision != sched.Admitted {
		t.Fatal("healthy project did not admit", got.decision, got.err)
	}
	if r := loaded(t, f.s, f.uid); r.Phase != Admitted || r.Target.Identity.ProjectUID != "project" || r.IssueUID != "issue" {
		t.Fatal("admission crossed projects", r.Phase, r.Target.Identity.ProjectUID, r.IssueUID)
	}
}

func TestSlowDeliveryRouteProbeDoesNotOccupyOtherProjectSlots(t *testing.T) {
	f := newAdmission(t)
	f.m.EnableProjects()
	var checks, healthyWrites atomic.Int32
	probes := make(chan struct{}, 2)
	resume := make(chan struct{})
	delivered := make(chan string, 4)
	queues := map[string]*Queue{}
	for _, project := range []string{"project-a", "project-b"} {
		cfg := f.cfg
		cfg.Kata.Project = project
		pl := f.s.layout.Project(project)
		pin := kata.Installation{Version: 1, CreatedAt: time.Now(), Executor: cfg.Executor, InstanceUID: "instance", ProjectUID: project, Actor: "worker"}
		if err := state.WriteJSON(f.s.fsys, pl.StateFile(), pin); err != nil {
			t.Fatal(err)
		}
		var guard RouteChecker = &admissionGuard{}
		if project == "project-a" {
			guard = laneCheckFunc(func(ctx context.Context) error {
				if checks.Add(1) == 1 {
					return nil // The scan passed before this endpoint slowed.
				}
				select {
				case probes <- struct{}{}:
				default:
				}
				select {
				case <-resume:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}
		k := &fakeKata{observe: func(_ context.Context, uid string, body kata.Observation) (kata.Run, bool, error) {
			if body.Status != "running" || project == "project-b" && uid != "run-b" {
				t.Errorf("delivery crossed projects: %s %s %+v", project, uid, body)
			}
			if project == "project-b" {
				healthyWrites.Add(1)
			}
			delivered <- project
			return kata.Run{}, false, nil
		}}
		lane, err := f.m.RegisterProject(project, cfg, k, guard, pl)
		if err != nil {
			t.Fatal(err)
		}
		queues[project] = lane.q
	}
	for _, row := range []struct{ uid, project string }{{"run-a1", "project-a"}, {"run-a2", "project-a"}, {"run-b", "project-b"}} {
		yes := true
		r := &Run{Version: 1, UID: row.uid, Phase: Admitted, Admitted: &yes, Target: Target{Identity: kata.Identity{InstanceUID: "instance", ProjectUID: row.project, Actor: "worker"}, Executor: f.cfg.Executor}, Ops: []Op{operation("report", "observe", Payload{Observation: &kata.Observation{Status: "running", ExpectedRevision: 1}})}}
		if err := f.s.Create(r); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	aDone, bDone := make(chan error, 1), make(chan error, 1)
	bStarted := false
	go func() { aDone <- queues["project-a"].Run(ctx) }()
	defer func() {
		close(resume)
		cancel()
		<-aDone
		if bStarted {
			<-bDone
		}
	}()
	for range 2 {
		select {
		case <-probes:
		case <-ctx.Done():
			t.Fatal("delivery probes did not begin after the passing scan")
		}
	}
	bStarted = true
	go func() { bDone <- queues["project-b"].Run(ctx) }()
	select {
	case project := <-delivered:
		if project != "project-b" {
			t.Fatal("slow lane wrote before its route probe completed", project)
		}
	case <-ctx.Done():
		t.Fatal("slow route probes occupied the healthy project's delivery slots")
	}
	if err := queues["project-b"].wait(ctx, "run-b", "report"); err != nil || healthyWrites.Load() != 1 {
		t.Fatal("healthy delivery did not complete once", err, healthyWrites.Load())
	}
	for _, uid := range []string{"run-a1", "run-a2"} {
		if op := loaded(t, f.s, uid).Ops[0]; op.FirstSent != nil || op.Done {
			t.Fatal("blocked route probe marked a delivery", uid, op.FirstSent, op.Done)
		}
	}
}
