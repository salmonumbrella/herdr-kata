//go:build kata

package runner

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"github.com/salmonumbrella/herdr-kata/internal/testenv"
)

// Both installations see not-found before either is allowed to create/admit.
// Subsequent conflict reads use the real authority without the barrier.
type racingKata struct {
	*kata.CLI
	once    sync.Once
	arrived chan<- struct{}
	release <-chan struct{}
}

func (k *racingKata) ShowRun(ctx context.Context, uid string) (kata.Run, error) {
	r, e := k.CLI.ShowRun(ctx, uid)
	k.once.Do(func() {
		k.arrived <- struct{}{}
		select {
		case <-k.release:
		case <-ctx.Done():
		}
	})
	return r, e
}
func TestIntegSecondExecutorLoses(t *testing.T) {
	integrationAdmissionRace(t, []string{"first-installation", "second-installation"})
}
func TestIntegLostJournalSameIdentityLoses(t *testing.T) {
	integrationAdmissionRace(t, []string{"installation", "installation"})
}
func integrationAdmissionRace(t *testing.T, labels []string) {
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	home := t.TempDir()
	cfg := config.Config{Executor: "first-installation", Kata: config.Kata{Bin: bin, Project: "admission-test", Actor: "worker", Env: map[string]string{"KATA_HOME": home}}, Limits: config.Limits{MaxActiveRuns: 2, DefaultTimeout: time.Hour}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}
	admin := kata.New(cfg.Kata, home, nil, nil)
	raw := startKataDaemon(t, bin, home, admin.Env(), cfg.Kata.Project)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	workflow := createWorkflow(t, raw, home, "Admission commands", defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "main", Kind: "command", Command: "true"}}})
	wfdef, e := defs.DecodeWorkflow(workflow.Definition)
	if e != nil {
		t.Fatal(e)
	}
	job := createJob(t, raw, home, "Admission race", defs.JobDef{Version: 1, Kind: "job", Enabled: true, Trigger: defs.Trigger{Kind: "manual"}, Action: defs.Action{Kind: "execute", WorkflowUID: workflow.UID}, Issue: &defs.IssuePolicy{Kind: "per-run", Title: "Admission race task"}, Overlap: "forbid", Catchup: "skip"})
	def, e := defs.DecodeJob(job.Definition)
	if e != nil {
		t.Fatal(e)
	}
	at := time.Now().UTC()
	due := sched.Due{Job: job, Def: def, Occurrence: at, Workflow: &workflow, WFDef: &wfdef}
	uid := ident.RunUID(at, ident.ScheduledSource(job.UID, ident.OccurrenceKey(at)))
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	type installation struct {
		m *Manager
		s *Store
		q *Queue
		k *racingKata
	}
	var installations []installation
	for _, label := range labels {
		c := cfg
		c.Executor = label
		l := state.Layout{Root: t.TempDir()}
		cli := kata.New(c.Kata, l.Root, nil, nil)
		guard := kata.NewRouteGuard(c, cli, state.OSFS{}, l)
		if e := guard.Initialize(ctx); e != nil {
			t.Fatal(e)
		}
		k := &racingKata{CLI: cli, arrived: arrived, release: release}
		s := NewStore(state.OSFS{}, l)
		q := NewQueue(k, s, guard, RealClock{})
		m := NewManager(c, k, nil, s, q, guard, RealClock{})
		m.executions.Store(uid, struct{}{})
		installations = append(installations, installation{m, s, q, k})
	}
	queueCtx, queueCancel := context.WithCancel(ctx)
	var queues sync.WaitGroup
	for _, i := range installations {
		queues.Go(func() {
			if e := i.q.Run(queueCtx); e != nil && !errors.Is(e, context.Canceled) {
				t.Error(e)
			}
		})
	}
	defer func() { queueCancel(); queues.Wait() }()
	type result struct {
		index int
		r     sched.AdmitResult
		err   error
	}
	results := make(chan result, 2)
	for n, i := range installations {
		go func() { r, e := i.m.Admit(ctx, due); results <- result{n, r, e} }()
	}
	for range 2 {
		select {
		case <-arrived:
		case <-ctx.Done():
			t.Fatal("authority barrier", ctx.Err())
		}
	}
	close(release)
	winners := 0
	for range 2 {
		select {
		case got := <-results:
			local, e := installations[got.index].s.Load(uid)
			if e != nil {
				t.Fatal(e)
			}
			if got.r == sched.Admitted {
				winners++
				if got.err != nil || local.Phase != Admitted {
					t.Fatal(got, local)
				}
			} else {
				if got.err == nil || local.Phase != Held || local.Reason != "admission_rejected" {
					t.Fatal("loser executed or not inspected", got, local)
				}
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if winners != 1 {
		t.Fatal("admitted installations", winners)
	}
	remote, e := admin.ShowRun(ctx, uid)
	if e != nil || remote.Status != "running" {
		t.Fatal(remote, e)
	}
}
