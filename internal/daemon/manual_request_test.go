package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestWorkerAcknowledgesManualJournalWithoutPeriodicScan(t *testing.T) {
	f := workerFixtureFor(t)
	if err := os.Remove(f.l.Project("project").JobCheckpoint(f.job.UID)); err != nil {
		t.Fatal(err)
	}
	// Keep the signal safe even if the worker has not registered its listener yet.
	trap := make(chan os.Signal, 1)
	signal.Notify(trap, syscall.SIGUSR1)
	defer signal.Stop(trap)
	stop := startWorkerFixture(t, f)
	waitWorker(t, func() bool { return f.lists.Load() > 0 })
	const uid = "01K00000000000000000000002"
	path := filepath.Join(f.l.RequestsDir(), uid+".json")
	req := runner.Request{Version: 1, UID: uid, RunUID: uid, Kind: "run", Project: "project", Job: f.job.UID}
	if err := state.WriteJSON(state.OSFS{}, path, req); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-f.settled:
		if status != "succeeded" {
			t.Fatal(status)
		}
	case err := <-f.failures:
		t.Fatal(err)
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	// The acknowledgement should follow journal durability, not the 60-second scan.
	// This bounds only the control handoff; it does not shorten the run's expiry.
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var result runner.RequestResult
		if err := state.ReadJSON(state.OSFS{}, runner.RequestResultPath(f.l, uid), 4096, &result); err == nil {
			if result.Error != "" || result.RunUID != uid {
				t.Fatal(result)
			}
			if _, err = os.Stat(path); os.IsNotExist(err) {
				break
			}
		}
		select {
		case <-deadline.C:
			t.Fatal("settled run still has an unacknowledged request; worker was not woken by its durable journal")
		case <-tick.C:
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
	stop()
	var saved runner.Run
	if err := state.ReadJSON(state.OSFS{}, f.l.RunFile(uid), 4<<20, &saved); err != nil || saved.Phase != runner.Settled || saved.Outcome != "succeeded" || f.creates.Load() != 1 {
		t.Fatal(saved.Phase, saved.Outcome, f.creates.Load(), err)
	}
}

// Terminal refusals must acknowledge the durable request before the
// fallback scan, preserve the actual reason, and leave a healthy peer runnable.
func TestWorkerAcknowledgesManualRefusalWithoutPeriodicScan(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		edit         func(*defs.JobDef)
		configure    func(*config.Config)
	}{
		{"disabled", "job is disabled", func(d *defs.JobDef) { d.Enabled = false }, nil},
		{"excluded-profile", "unmapped profile", func(d *defs.JobDef) {
			d.Action = defs.Action{Kind: "execute", Prompt: "Review"}
			d.Options = json.RawMessage(`{"agent":"excluded"}`)
		}, nil},
		{"missing-input", "workflow requires input", func(d *defs.JobDef) { d.Action.WorkflowUID = "needs-input" }, nil},
		{"max-timeout", "timeout_seconds 1900 exceeds limits.max_timeout 30m0s", func(d *defs.JobDef) { d.TimeoutSeconds = 1900 }, func(c *config.Config) {
			c.Limits.DefaultTimeout = 30 * time.Minute
			c.Limits.MaxTimeout = 30 * time.Minute
		}},
		{"commands-disabled", `commands are disabled for project "test" (commands=false)`, func(*defs.JobDef) {}, func(c *config.Config) {
			c.Projects = []config.Project{{Name: c.Kata.Project, Actor: c.Kata.Actor, Commands: new(false)}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := workerFixtureFor(t)
			if err := os.Remove(f.l.Project("project").JobCheckpoint(f.job.UID)); err != nil {
				t.Fatal(err)
			}
			d, err := defs.DecodeJob(f.job.Definition)
			if err != nil {
				t.Fatal(err)
			}
			tc.edit(&d)
			if tc.configure != nil {
				tc.configure(&f.cfg)
			}
			raw, err := json.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			const badJob = "01K00000000000000000000003"
			f.extraJobs = []kata.Job{{UID: badJob, Name: "refused-job", DefinitionEventUID: "refused-event", CreatedAt: f.at, Definition: raw}}
			f.extraWorkflows = []kata.Workflow{{UID: "needs-input", DefinitionEventUID: "input-event", Definition: json.RawMessage(`{"version":1,"input":"task","steps":[{"key":"main","kind":"command","command":"true"}]}`)}}
			trap := make(chan os.Signal, 1)
			signal.Notify(trap, syscall.SIGUSR1)
			defer signal.Stop(trap)
			stop := startWorkerFixture(t, f)
			waitWorker(t, func() bool { return f.lists.Load() > 0 })
			const refused = "01K00000000000000000000004"
			// Only the initial submission signals the worker. No signal or periodic
			// tick may be needed after the scheduler has computed the refusal.
			submit := func(uid, job string) string {
				path := filepath.Join(f.l.RequestsDir(), uid+".json")
				req := runner.Request{Version: 1, UID: uid, RunUID: uid, Kind: "run", Project: "project", Job: job}
				if err := state.WriteJSON(state.OSFS{}, path, req); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
					t.Fatal(err)
				}
				return path
			}
			await := func(uid, path string) runner.RequestResult {
				deadline := time.NewTimer(3 * time.Second)
				defer deadline.Stop()
				tick := time.NewTicker(time.Millisecond)
				defer tick.Stop()
				for {
					var result runner.RequestResult
					if err := state.ReadJSON(state.OSFS{}, runner.RequestResultPath(f.l, uid), 4096, &result); err == nil {
						if _, err := os.Stat(path); os.IsNotExist(err) {
							return result
						}
					}
					select {
					case <-deadline.C:
						t.Fatal("terminal manual result did not wake durable request handling")
					case err := <-f.failures:
						t.Fatal(err)
					case <-f.workerDone:
						t.Fatalf("worker stopped: %v", f.workerErr)
					case <-tick.C:
					case <-t.Context().Done():
						t.Fatal(t.Context().Err())
					}
				}
			}
			result := await(refused, submit(refused, badJob))
			for _, part := range []string{"project", "test", badJob, tc.reason, "herdr-kata status --json"} {
				if !strings.Contains(result.Error, part) {
					t.Errorf("refusal %q missing %q", result.Error, part)
				}
			}
			if result.RunUID != refused || f.creates.Load() != 0 {
				t.Fatal(result, f.creates.Load())
			}
			if _, err := os.Stat(f.l.RunFile(refused)); !os.IsNotExist(err) {
				t.Fatal("refused run has a journal", err)
			}
			// The command peer below requires command execution in this lane.
			if tc.name == "commands-disabled" {
				stop()
				return
			}
			const peer = "01K00000000000000000000005"
			result = await(peer, submit(peer, f.job.UID))
			if result.RunUID != peer || result.Error != "" {
				t.Fatal(result)
			}
			select {
			case outcome := <-f.settled:
				if outcome != "succeeded" {
					t.Fatal(outcome)
				}
			case err := <-f.failures:
				t.Fatal(err)
			case <-f.workerDone:
				t.Fatalf("worker stopped before peer settled: %v", f.workerErr)
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			}
			stop()
			var saved runner.Run
			if err := state.ReadJSON(state.OSFS{}, f.l.RunFile(peer), 4<<20, &saved); err != nil || saved.Phase != runner.Settled || saved.Outcome != "succeeded" || f.creates.Load() != 1 {
				t.Fatal(saved.Phase, saved.Outcome, f.creates.Load(), err)
			}
		})
	}
}

// A run admitted before another installation takes the lane over still
// belongs to this installation. The stood-down lane admits nothing new, but
// it keeps supervising that run and settles it with its real result.
func TestTakenOverLaneSettlesItsRunInFlight(t *testing.T) {
	f := workerFixtureFor(t)
	if err := os.Remove(f.l.Project("project").JobCheckpoint(f.job.UID)); err != nil {
		t.Fatal(err)
	}
	marks := t.TempDir()
	started, release := filepath.Join(marks, "started"), filepath.Join(marks, "release")
	f.command = fmt.Sprintf("touch %q; while [ ! -f %q ]; do sleep 0.05; done", started, release)
	trap := make(chan os.Signal, 1)
	signal.Notify(trap, syscall.SIGUSR1)
	defer signal.Stop(trap)
	startWorkerFixture(t, f)
	waitWorker(t, func() bool { return f.lists.Load() > 0 })
	const uid = "01K00000000000000000000002"
	req := runner.Request{Version: 1, UID: uid, RunUID: uid, Kind: "run", Project: "project", Job: f.job.UID}
	if err := state.WriteJSON(state.OSFS{}, filepath.Join(f.l.RequestsDir(), uid+".json"), req); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	waitWorker(t, func() bool { _, err := os.Stat(started); return err == nil })

	// Another installation takes the lane over while the command runs.
	f.leases.takeover("taker", "project")
	f.leases.mu.Lock()
	issue := f.leases.issues["project"]
	f.events <- fmt.Sprintf(`{"event_id":900,"type":"issue.metadata_updated","issue_uid":%q,"payload":{"revision_new":%d}}`, issue.UID, issue.Revision)
	f.leases.mu.Unlock()
	waitWorker(t, func() bool {
		var s LeaseStatus
		return state.ReadJSON(state.OSFS{}, leaseFile(f.l.Project("project")), 4096, &s) == nil && !s.Held && s.Owner == "taker"
	})

	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-f.settled:
		if status != "succeeded" {
			t.Fatalf("the run in flight settled %s", status)
		}
	case err := <-f.failures:
		t.Fatal(err)
	case <-t.Context().Done():
		t.Fatal("the run in flight never settled after the takeover")
	}
}

// A run queued on a lane before another installation takes it over is never
// admitted there: the stood-down lane settles its request as refused, with
// up's wording, and refuses new runs at once. The run in flight still settles.
func TestStoodDownLaneRefusesQueuedRun(t *testing.T) {
	f := workerFixtureFor(t)
	f.cfg.Limits.MaxActiveRuns = 1
	if err := os.Remove(f.l.Project("project").JobCheckpoint(f.job.UID)); err != nil {
		t.Fatal(err)
	}
	marks := t.TempDir()
	started, release := filepath.Join(marks, "started"), filepath.Join(marks, "release")
	f.command = fmt.Sprintf("touch %q; while [ ! -f %q ]; do sleep 0.05; done", started, release)
	// A failed check must not leave the command looping.
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	trap := make(chan os.Signal, 1)
	signal.Notify(trap, syscall.SIGUSR1)
	defer signal.Stop(trap)
	startWorkerFixture(t, f)
	waitWorker(t, func() bool { return f.lists.Load() > 0 })
	submit := func(uid string) {
		t.Helper()
		req := runner.Request{Version: 1, UID: uid, RunUID: uid, Kind: "run", Project: "project", Job: f.job.UID}
		if err := state.WriteJSON(state.OSFS{}, filepath.Join(f.l.RequestsDir(), uid+".json"), req); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
			t.Fatal(err)
		}
	}
	result := func(uid string) (runner.RequestResult, bool) {
		var r runner.RequestResult
		return r, state.ReadJSON(state.OSFS{}, runner.RequestResultPath(f.l, uid), 4096, &r) == nil
	}
	const inFlight, queued, late = "01K00000000000000000000002", "01K00000000000000000000003", "01K00000000000000000000004"
	submit(inFlight)
	waitWorker(t, func() bool { _, err := os.Stat(started); return err == nil })
	// The only slot is taken, so this run waits in the lane's queue: the
	// scheduler reads its job, finds no free slot and keeps it.
	shows := f.shows.Load()
	submit(queued)
	waitWorker(t, func() bool { return f.shows.Load() > shows })
	select {
	case err := <-f.failures:
		t.Fatal(err)
	default:
	}
	if _, done := result(queued); done {
		t.Fatal("queued run settled before the takeover")
	}

	f.leases.takeover("taker", "project")
	f.leases.mu.Lock()
	issue := f.leases.issues["project"]
	f.events <- fmt.Sprintf(`{"event_id":900,"type":"issue.metadata_updated","issue_uid":%q,"payload":{"revision_new":%d}}`, issue.UID, issue.Revision)
	f.leases.mu.Unlock()

	const want = "another installation (taker) owns this lane: lease generation 2"
	refused := func(uid string) {
		t.Helper()
		waitWorker(t, func() bool { _, done := result(uid); return done })
		r, _ := result(uid)
		if r.Error != want || r.Reason != runner.ReasonLeaseHeldElsewhere || r.RunUID != uid {
			t.Fatalf("%s: %+v", uid, r)
		}
		if _, err := os.Stat(f.l.RunFile(uid)); !os.IsNotExist(err) {
			t.Fatalf("%s: refused run has a journal: %v", uid, err)
		}
	}
	refused(queued)
	submit(late)
	refused(late)

	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-f.settled:
		if status != "succeeded" {
			t.Fatalf("the run in flight settled %s", status)
		}
	case err := <-f.failures:
		t.Fatal(err)
	case <-t.Context().Done():
		t.Fatal("the run in flight never settled after the takeover")
	}
}
