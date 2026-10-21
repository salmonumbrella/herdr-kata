package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type workerFixture struct {
	cfg          config.Config
	l            state.Layout
	k            *kata.CLI
	job          kata.Job
	at           time.Time
	lists        atomic.Int32
	afterJobList func(context.Context, int32)
	afterJobShow func(context.Context, int32)
	afterRunShow func(context.Context)
	remoteJob    atomic.Pointer[kata.Job]
	failJobList  atomic.Bool
	shows        atomic.Int32
	creates      atomic.Int32
	tail         atomic.Bool
	redirected   atomic.Bool
	events       chan string
	settled      chan string
	failures     chan error
	workerDone   chan struct{}
	workerErr    error

	extraJobs      []kata.Job
	extraWorkflows []kata.Workflow
	leases         *hubLeases
	// command is the wf workflow's command step; empty runs "true".
	command string
}

func (f *workerFixture) workflow() kata.Workflow {
	command := f.command
	if command == "" {
		command = "true"
	}
	step, _ := json.Marshal(map[string]any{"version": 1, "steps": []any{map[string]string{"key": "main", "kind": "command", "command": command}}})
	return kata.Workflow{UID: "wf", DefinitionEventUID: "wf-event", Definition: step}
}

// CLI envelopes carry live author provenance even though persisted Job snapshots
// omit it. Keep each fake job's explicit author instead of inferring the actor.
type workerJobEnvelope struct {
	kata.Job
	Author string `json:"author"`
}

func workerFixtureFor(t *testing.T) *workerFixture {
	t.Helper()
	h := herdrtest.New(t)
	t.Setenv("HERDR_SOCKET_PATH", h.Path)
	f := &workerFixture{l: state.Layout{Root: t.TempDir()}, at: time.Now().UTC().Add(-time.Minute), events: make(chan string, 8), settled: make(chan string, 1), failures: make(chan error, 1), workerDone: make(chan struct{})}
	t.Setenv("HERDR_PLUGIN_STATE_DIR", f.l.Root)
	f.cfg = config.Config{Executor: "test", Session: herdr.SessionFromSocket(h.Path), Kata: config.Kata{Bin: "/bin/kata", Actor: "worker", Project: "test", Env: map[string]string{"KATA_HOME": t.TempDir()}}, Limits: config.Limits{MaxActiveRuns: 2, DefaultTimeout: time.Hour}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}
	f.job = kata.Job{Author: "worker", UID: "01K00000000000000000000001", DefinitionEventUID: "event", CreatedAt: f.at.Add(-time.Hour), Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","workflow_uid":"wf"},"issue":{"kind":"per-run","title":"Inspect"},"overlap":"forbid","catchup":"all"}`)}
	f.remoteJob.Store(&f.job)
	f.leases = &hubLeases{}
	cliRunner := func(ctx context.Context, argv, _ []string, _ string, input []byte) ([]byte, []byte, int, error) {
		args := argv[8:]
		cmd := strings.Join(args, " ")
		if out, code, ok := f.leases.reply("project", args); ok {
			return out, nil, code, nil
		}
		var v any
		switch {
		case cmd == "health":
			return []byte(`{"version":"test","schema_version":33}`), nil, 0, nil
		case cmd == "cron capabilities":
			v = kata.Capabilities{ProjectUID: "project", EventFeatures: []string{"cron_v1"}}
			if f.redirected.Load() {
				v = kata.Capabilities{ProjectUID: "other", EventFeatures: []string{"cron_v1"}}
			}
		case cmd == "federation identity":
			v = map[string]any{"instance_uid": "instance", "auth": map[string]string{"actor": "worker"}}
		case strings.HasPrefix(cmd, "daemon diagnose"):
			v = map[string]string{"state": "ready", "endpoint": "local", "project_uid": "project", "federation_role": "hub"}
		case cmd == "cron job list":
			if !f.tail.Load() {
				t.Error("snapshot before tail")
			}
			if f.failJobList.Load() {
				return nil, nil, 7, errors.New("job list unavailable")
			}
			jobs := append([]kata.Job{*f.remoteJob.Load()}, f.extraJobs...)
			envelopes := make([]workerJobEnvelope, 0, len(jobs))
			for _, job := range jobs {
				envelopes = append(envelopes, workerJobEnvelope{Job: job, Author: job.Author})
			}
			v = map[string]any{"jobs": envelopes}
			if f.redirected.Load() {
				v = map[string]any{"jobs": []workerJobEnvelope{}}
			}
			// Freeze the answer before observers use its count to switch routes.
			calls := f.lists.Add(1)
			if f.afterJobList != nil {
				f.afterJobList(ctx, calls)
			}
		case cmd == "cron workflow list":
			v = map[string]any{"workflows": append([]kata.Workflow{f.workflow()}, f.extraWorkflows...)}
		case strings.HasPrefix(cmd, "cron workflow show"):
			workflow := f.workflow()
			for _, extra := range f.extraWorkflows {
				if args[3] == extra.UID {
					workflow = extra
				}
			}
			v = map[string]any{"workflow": workflow}
		case strings.HasPrefix(cmd, "cron job show"):
			job := *f.remoteJob.Load()
			for _, extra := range f.extraJobs {
				if args[3] == extra.UID {
					job = extra
				}
			}
			v = map[string]any{"job": workerJobEnvelope{Job: job, Author: job.Author}}
			calls := f.shows.Add(1)
			if f.afterJobShow != nil {
				f.afterJobShow(ctx, calls)
			}
		case strings.HasPrefix(cmd, "cron run show"):
			if f.afterRunShow != nil {
				f.afterRunShow(ctx)
			}
			return []byte(`{"error":{"code":"not_found","message":"absent"}}`), nil, 1, nil
		case args[0] == "create":
			f.creates.Add(1)
			v = map[string]any{"issue": kata.IssueView{UID: "issue", QualifiedID: "test#task", Revision: 1}}
		case args[0] == "show":
			v = map[string]any{"issue": kata.IssueView{UID: "issue", Status: "open", Revision: 1}}
		case args[0] == "close":
			v = map[string]any{}
		case strings.HasPrefix(cmd, "cron run observe"):
			var observation kata.Observation
			if err := json.Unmarshal(input, &observation); err != nil {
				t.Error(err)
			}
			if observation.Status != "running" {
				select {
				case f.settled <- observation.Status:
				default:
				}
			}
			v = map[string]any{"run": kata.Run{}, "replayed": false}
		default:
			err := fmt.Errorf("unexpected command %s", cmd)
			t.Error(err)
			select {
			case f.failures <- err:
			default:
			}
			return nil, nil, 1, err
		}
		b, e := json.Marshal(v)
		return b, nil, 0, e
	}
	starter := func(ctx context.Context, _, _ []string, _ string) (kata.Proc, error) {
		r, w := io.Pipe()
		f.tail.Store(true)
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer w.Close()
			for {
				select {
				case <-ctx.Done():
					return
				case event := <-f.events:
					if _, err := io.WriteString(w, event+"\n"); err != nil {
						return
					}
				}
			}
		}()
		return kata.Proc{Stdout: r, Stderr: strings.NewReader(""), Wait: func() error { <-done; return nil }}, nil
	}
	f.k = kata.New(f.cfg.Kata, f.l.Root, cliRunner, starter)
	// Initialization precedes snapshots even for the preview-only scheduler.
	if err := state.InitializeLayout(state.OSFS{}, f.l); err != nil {
		t.Fatal(err)
	}
	if e := kata.NewRouteGuard(f.cfg, f.k, state.OSFS{}, f.l.Project("project")).Initialize(t.Context()); e != nil {
		t.Fatal(e)
	}
	cp := sched.Checkpoint{Version: 1, JobUID: "01K00000000000000000000001", DefinitionEventUID: "event", HandledThrough: f.at, Pending: []string{ident.OccurrenceKey(f.at)}}
	if e := cp.Save(state.OSFS{}, f.l.Project("project")); e != nil {
		t.Fatal(e)
	}
	return f
}

// definitionsRefreshed reports the lane's first definition snapshot, which
// its scheduler loads as it starts.
func definitionsRefreshed(f *workerFixture) bool {
	snapshot, err := defs.ReadSnapshot(state.OSFS{}, f.l.Project("project").DefsSnapshot())
	return err == nil && len(snapshot.Jobs) == 1
}

func startWorkerFixture(t *testing.T, f *workerFixture) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		f.workerErr = workerWithCLI(ctx, f.cfg, f.l, f.k)
		close(f.workerDone)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-f.workerDone
			if e := f.workerErr; e != nil && !errors.Is(e, context.Canceled) {
				t.Error(e)
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

type previewAdmitter struct {
	sched.LogAdmitter
	previewed chan struct{}
}

func (a previewAdmitter) Admit(ctx context.Context, d sched.Due) (sched.AdmitResult, error) {
	result, err := a.LogAdmitter.Admit(ctx, d)
	select {
	case a.previewed <- struct{}{}:
	default:
	}
	return result, err
}

func waitWorker(t *testing.T, p func() bool) {
	t.Helper()
	ctx := t.Context()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !p() {
		select {
		case <-ctx.Done():
			t.Fatal("worker did not reach expected state:", ctx.Err())
		case <-tick.C:
		}
	}
}
func TestWorkerPreviewThenUpgradeKeepsPending(t *testing.T) {
	f := workerFixtureFor(t)
	f.tail.Store(true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	preview := previewAdmitter{previewed: make(chan struct{}, 1)}
	s := sched.New(f.cfg, f.k, preview, state.OSFS{}, f.l.Project("project"), nil)
	done := make(chan struct{})
	var previewErr error
	go func() {
		previewErr = s.Run(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()
	select {
	case <-preview.previewed:
	case <-done:
		t.Fatalf("preview returned before admission: %v", previewErr)
	case err := <-f.failures:
		t.Fatal(err)
	case <-t.Context().Done():
		t.Fatal("preview admission:", t.Context().Err())
	}
	cancel()
	<-done
	if previewErr != nil && !errors.Is(previewErr, context.Canceled) {
		t.Fatal(previewErr)
	}
	var cp sched.Checkpoint
	if e := state.ReadJSON(state.OSFS{}, f.l.Project("project").JobCheckpoint("01K00000000000000000000001"), 64<<10, &cp); e != nil || len(cp.Pending) != 1 {
		t.Fatal(cp, e)
	}
	f.tail.Store(false)
	stop := startWorkerFixture(t, f)
	uid := ident.RunUID(f.at, ident.ScheduledSource("01K00000000000000000000001", ident.OccurrenceKey(f.at)))
	// A terminal report proves execution finished. Join the worker before
	// reading its snapshots, so atomic replacement cannot race our assertions.
	// The test caller owns the timeout: directory fsync alone can exceed a
	// short phase watchdog while admission is still progressing normally.
	select {
	case status := <-f.settled:
		if status != "succeeded" {
			t.Fatal("terminal observation:", status)
		}
	case err := <-f.failures:
		t.Fatal(err)
	case <-f.workerDone:
		t.Fatalf("worker returned before terminal observation: %v", f.workerErr)
	case <-t.Context().Done():
		t.Fatal("terminal observation:", t.Context().Err())
	}
	stop()
	if f.creates.Load() != 1 {
		t.Fatal("creates", f.creates.Load())
	}
	if e := state.ReadJSON(state.OSFS{}, f.l.Project("project").JobCheckpoint("01K00000000000000000000001"), 64<<10, &cp); e != nil || len(cp.Pending) != 0 {
		t.Fatal("admitted occurrence remains pending", cp, e)
	}
	var r runner.Run
	e := state.ReadJSON(state.OSFS{}, f.l.RunFile(uid), 4<<20, &r)
	if e != nil || r.Phase != runner.Settled || r.Outcome != "succeeded" {
		t.Fatal(r, e)
	}
}
func TestWorkerRestoresGatesBeforeDispatch(t *testing.T) {
	for _, p := range []runner.Phase{runner.Prepared, runner.Admitted, runner.Running, runner.Blocked, runner.Held} {
		t.Run(string(p), func(t *testing.T) {
			f := workerFixtureFor(t)
			s := runner.NewStore(state.OSFS{}, f.l)
			if e := s.Create(&runner.Run{Version: 1, UID: "old", Phase: p, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project"}}, Job: f.job}); e != nil {
				t.Fatal(e)
			}
			startWorkerFixture(t, f)
			waitWorker(t, func() bool { return f.shows.Load() > 0 })
			if f.creates.Load() != 0 {
				t.Fatal("dispatch preceded gate restore")
			}
			var cp sched.Checkpoint
			if e := state.ReadJSON(state.OSFS{}, f.l.Project("project").JobCheckpoint("01K00000000000000000000001"), 64<<10, &cp); e != nil || len(cp.Pending) != 1 {
				t.Fatal(cp, e)
			}
		})
	}
}

func TestWorkerCancellationIsClean(t *testing.T) {
	for n := range 20 {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			f := workerFixtureFor(t)
			f.cfg.Limits.MaxActiveRuns = 1
			s := runner.NewStore(state.OSFS{}, f.l)
			if e := s.Create(&runner.Run{Version: 1, UID: "held", Phase: runner.Held, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project"}}, Job: f.job}); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- workerWithCLI(ctx, f.cfg, f.l, f.k) }()
			waitWorker(t, func() bool {
				var status RouteStatus
				return state.ReadJSON(state.OSFS{}, filepath.Join(f.l.Project("project").Root, "route.json"), 1024, &status) == nil && status.Allowed && definitionsRefreshed(f)
			})
			cancel()
			if e := <-done; e != nil {
				t.Fatalf("ordinary cancellation: %v", e)
			}
		})
	}
}

func TestWorkerPreparedAdmissionRecoveredBeforeDispatch(t *testing.T) {
	f := workerFixtureFor(t)
	f.cfg.Limits.MaxActiveRuns = 1
	s := runner.NewStore(state.OSFS{}, f.l)
	g := kata.NewRouteGuard(f.cfg, f.k, state.OSFS{}, f.l.Project("project"))
	q := runner.NewQueue(f.k, s, g, runner.RealClock{})
	m := runner.NewManager(f.cfg, f.k, nil, s, q, g, runner.RealClock{})
	m.EnableProjects()
	m, err := m.RegisterProject("project", f.cfg, f.k, g, f.l.Project("project"))
	if err != nil {
		t.Fatal(err)
	}
	def, e := defs.DecodeJob(f.job.Definition)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, e := m.Admit(ctx, sched.Due{Job: f.job, Def: def, Occurrence: f.at, Workflow: &kata.Workflow{UID: "wf", DefinitionEventUID: "wf-event"}, WFDef: &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "main", Kind: "command", Command: "true"}}}})
		done <- e
	}()
	uid := ident.RunUID(f.at, ident.ScheduledSource(f.job.UID, ident.OccurrenceKey(f.at)))
	waitWorker(t, func() bool { r, e := s.Load(uid); return e == nil && r.Phase == runner.Prepared })
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	startWorkerFixture(t, f)
	// Recovery completes admission from the frozen journal before dispatch.
	waitWorker(t, func() bool {
		var r runner.Run
		return state.ReadJSON(state.OSFS{}, f.l.RunFile(uid), 4<<20, &r) == nil && r.IssueUID == "issue" && r.EndedAt != nil && definitionsRefreshed(f)
	})
	var r runner.Run
	if e := state.ReadJSON(state.OSFS{}, f.l.RunFile(uid), 4<<20, &r); e != nil || r.Phase != runner.Settled || r.Outcome != "succeeded" {
		t.Fatal(r, e)
	}
	var cp sched.Checkpoint
	if e := state.ReadJSON(state.OSFS{}, f.l.Project("project").JobCheckpoint(f.job.UID), 64<<10, &cp); e != nil || len(cp.Pending) != 1 {
		t.Fatal(cp, e)
	}
}

type failingRunScan struct{ state.OSFS }

func (failingRunScan) ReadDir(string) ([]fs.DirEntry, error) { return nil, fs.ErrPermission }
func TestQueueFailureCancelsStartupRecovery(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(context.Canceled)
	l := state.Layout{Root: t.TempDir()}
	store := runner.NewStore(failingRunScan{}, l)
	queue := runner.NewQueue(nil, store, nil, nil)
	failures := make(chan error, 1)
	runQueue(ctx, queue, cancel, failures)
	if ctx.Err() != context.Canceled || !errors.Is(context.Cause(ctx), fs.ErrPermission) || !errors.Is(<-failures, fs.ErrPermission) {
		t.Fatal("queue failure left recovery waiting")
	}
}

func TestWorkerRefusedRouteSkipsDefinitionRefresh(t *testing.T) {
	f := workerFixtureFor(t)
	// Keep the initial answer in flight while refusing the route.
	release := make(chan struct{})
	f.afterJobList = func(ctx context.Context, call int32) {
		if call == 1 {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}
	f.cfg.Limits.MaxActiveRuns = 1
	if err := runner.NewStore(state.OSFS{}, f.l).Create(&runner.Run{Version: 1, UID: "held", Phase: runner.Held, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project"}}, Job: f.job}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- workerWithCLI(ctx, f.cfg, f.l, f.k) }()
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			if err := <-done; err != nil {
				t.Error(err)
			}
		}
	})
	waitWorker(t, func() bool {
		var status RouteStatus
		return f.lists.Load() >= 1 && state.ReadJSON(state.OSFS{}, filepath.Join(f.l.Project("project").Root, "route.json"), 1024, &status) == nil && status.Allowed
	})
	f.redirected.Store(true)
	close(release)
	f.events <- `{"reset_required":true,"reset_after_id":1}`
	waitWorker(t, func() bool {
		var status RouteStatus
		return state.ReadJSON(state.OSFS{}, filepath.Join(f.l.Project("project").Root, "route.json"), 1024, &status) == nil && !status.Allowed
	})
	f.events <- `{"event_id":2,"type":"cron.job.updated"}`
	// A second reset checks the route after the queued definition event. Once
	// processed, both refresh paths must have left the original snapshot alone.
	f.events <- `{"reset_required":true,"reset_after_id":3}`
	waitWorker(t, func() bool {
		var cursor kata.Cursor
		return state.ReadJSON(state.OSFS{}, f.l.Project("project").EventsCursor(), 1024, &cursor) == nil && cursor.AfterID == 3
	})
	cancel()
	err := <-done
	joined = true
	if err != nil {
		t.Fatal(err)
	}
	var cp sched.Checkpoint
	if err := state.ReadJSON(state.OSFS{}, f.l.Project("project").JobCheckpoint(f.job.UID), 64<<10, &cp); err != nil || len(cp.Pending) != 1 {
		t.Fatal("pending occurrence dropped", cp, err)
	}
	snapshot, err := defs.ReadSnapshot(state.OSFS{}, f.l.Project("project").DefsSnapshot())
	if err != nil || len(snapshot.Jobs) != 1 || f.lists.Load() != 1 {
		t.Fatal("redirected definitions refreshed", snapshot, err, f.lists.Load())
	}
}

func TestWorkerRefusedProjectDoesNotBlockHealthyLane(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	cfg := config.Config{Session: "work", Executor: "worker", Kata: config.Kata{Bin: "/bin/kata", Env: map[string]string{"KATA_HOME": t.TempDir()}}, Limits: config.Limits{MaxActiveRuns: 2, DefaultTimeout: time.Hour}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}, Projects: []config.Project{{Name: "project-a", Actor: "actor-a", Daemon: "hub-a", CredentialEnv: "TOKEN_A"}, {Name: "project-b", Actor: "actor-b", Daemon: "hub-b", CredentialEnv: "TOKEN_B"}}}
	t.Setenv("TOKEN_A", "credential-a")
	t.Setenv("TOKEN_B", "credential-b")
	var tail atomic.Int32
	factory := func(view config.Config) *kata.CLI {
		check := func(argv, env []string) {
			joined := " " + strings.Join(argv, " ") + " "
			for _, part := range []string{" --project " + view.Kata.Project + " ", " --as " + view.Kata.Actor + " ", " --daemon " + view.Kata.Daemon + " "} {
				if !strings.Contains(joined, part) {
					t.Errorf("wrong lane arguments: %v", argv)
				}
			}
			own, other := "TOKEN_A=credential-a", "TOKEN_B=credential-b"
			if view.Kata.Project == "project-b" {
				own, other = other, own
			}
			haveOwn := false
			for _, item := range env {
				if item == own {
					haveOwn = true
				}
				if item == other {
					t.Error("another lane's credential reached Kata")
				}
			}
			if !haveOwn {
				t.Error("lane credential absent")
			}
		}
		leases := &hubLeases{}
		cliRunner := func(_ context.Context, argv, env []string, _ string, _ []byte) ([]byte, []byte, int, error) {
			check(argv, env)
			if view.Kata.Project == "project-a" {
				return []byte(`{"error":{"code":"transport","message":"unavailable"}}`), nil, 7, nil
			}
			cmd := strings.Join(argv[10:], " ")
			if out, code, ok := leases.reply("project-b", argv[10:]); ok {
				return out, nil, code, nil
			}
			var v any
			switch {
			case cmd == "health":
				v = kata.Health{Version: "test", SchemaVersion: 33}
			case cmd == "cron capabilities":
				v = kata.Capabilities{ProjectUID: "project-b", EventFeatures: []string{"cron_v1"}}
			case cmd == "federation identity":
				v = map[string]any{"instance_uid": "instance-b", "auth": map[string]string{"actor": "actor-b"}}
			case strings.HasPrefix(cmd, "daemon diagnose"):
				v = map[string]string{"state": "ready", "endpoint": "hub.example", "project_uid": "project-b", "federation_role": "hub"}
			case cmd == "cron job list":
				v = map[string]any{"jobs": []kata.Job{}}
			case cmd == "cron workflow list":
				v = map[string]any{"workflows": []kata.Workflow{}}
			default:
				t.Errorf("unexpected call %s", cmd)
				return nil, nil, 1, errors.New("unexpected call")
			}
			b, e := json.Marshal(v)
			return b, nil, 0, e
		}
		starter := func(ctx context.Context, argv, env []string, _ string) (kata.Proc, error) {
			check(argv, env)
			tail.Add(1)
			r, w := io.Pipe()
			done := make(chan struct{})
			go func() { <-ctx.Done(); w.Close(); close(done) }()
			return kata.Proc{Stdout: r, Stderr: strings.NewReader(""), Wait: func() error { <-done; return nil }}, nil
		}
		return kata.New(view.Kata, l.Root, cliRunner, starter)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- workerWithFactory(ctx, cfg, l, factory) }()
	defer func() {
		cancel()
		if e := <-done; e != nil {
			t.Error(e)
		}
	}()
	waitWorker(t, func() bool { _, e := os.Stat(l.Project("project-b").DefsSnapshot()); return e == nil })
	if tail.Load() != 1 {
		t.Fatal("refused lane started a tail", tail.Load())
	}
}

func TestWorkerStopsOnLocalSnapshotWriteFailure(t *testing.T) {
	f := workerFixtureFor(t)
	if e := state.InitializeLayout(state.OSFS{}, f.l); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(f.l.Project("project").DefsSnapshot(), 0700); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	err := workerWithCLI(ctx, f.cfg, f.l, f.k)
	if _, ok := errors.AsType[*os.LinkError](err); !ok {
		t.Fatal("worker did not stop on local snapshot write failure", err)
	}
}

func TestRetryLanePropagatesRouteSnapshotFailure(t *testing.T) {
	f := workerFixtureFor(t)
	if e := state.InitializeLayout(state.OSFS{}, f.l); e != nil {
		t.Fatal(e)
	}
	pl := f.l.Project("project")
	if err := os.Remove(pl.RouteFile()); err != nil {
		t.Fatal(err)
	}
	if e := os.Mkdir(filepath.Join(pl.Root, "route.json"), 0700); e != nil {
		t.Fatal(e)
	}
	guard := kata.NewRouteGuard(f.cfg, f.k, state.OSFS{}, pl, "project")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	calls := 0
	err := retryLane(ctx, guard, func() error {
		calls++
		if calls == 1 {
			return &kata.Error{Code: "transport", Message: "temporary read failure"}
		}
		cancel()
		return errors.New("project route refused")
	})
	var local *os.LinkError
	if !errors.As(err, &local) || calls != 0 {
		t.Fatal("route snapshot failure was retried", err, calls)
	}
}

func TestRetryLaneChecksRouteBeforeWork(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	l := state.Layout{Root: t.TempDir()}
	var checks, calls int
	k := kata.New(config.Kata{Project: "example-project", Actor: "worker", Env: map[string]string{"KATA_HOME": t.TempDir()}}, l.Root, func(_ context.Context, args, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
		if !strings.HasSuffix(strings.Join(args, " "), " health") {
			t.Fatalf("unexpected route call: %v", args)
		}
		checks++
		cancel()
		return nil, nil, 7, errors.New("route unavailable")
	}, nil)
	guard := kata.NewRouteGuard(config.Config{}, k, state.OSFS{}, l, "project-a")
	err := retryLane(ctx, guard, func() error { calls++; return nil })
	if err != nil || checks != 1 || calls != 0 {
		t.Fatal("work ran without a passing route check", checks, calls, err)
	}
}

func TestLaneCapabilitiesRefuseUIDChangeAfterRouteCheck(t *testing.T) {
	f := workerFixtureFor(t)
	guard := kata.NewRouteGuard(f.cfg, f.k, state.OSFS{}, f.l.Project("project"), "project")
	if err := guard.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	reader := kata.GuardedReader{CLI: f.k, Guard: guard}
	f.redirected.Store(true)
	if caps, err := reader.Capabilities(t.Context()); err == nil || guard.Allowed() {
		t.Fatal("capabilities from another project were accepted", caps, err)
	}
	f.redirected.Store(false)
	if err := guard.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if caps, err := reader.Capabilities(t.Context()); err != nil || caps.ProjectUID != "project" {
		t.Fatal("restored lane did not recover its capabilities", caps, err)
	}
}

func TestLaneAccessProbeRefusesUIDChangeAfterRouteCheck(t *testing.T) {
	f := workerFixtureFor(t)
	guard := kata.NewRouteGuard(f.cfg, f.k, state.OSFS{}, f.l.Project("project"), "project")
	if err := guard.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	reader := kata.GuardedReader{CLI: f.k, Guard: guard}
	f.redirected.Store(true)
	kata.ProbeAccess(t.Context(), reader)
	if guard.Allowed() {
		t.Fatal("access probe left redirected lane enabled")
	}
	var row struct {
		Allowed    bool   `json:"allowed"`
		AccessLost bool   `json:"access_lost"`
		Error      string `json:"error"`
		Project    string `json:"project"`
	}
	if err := state.ReadJSON(state.OSFS{}, f.l.Project("project").RouteFile(), 65536, &row); err != nil || row.Allowed || !row.AccessLost {
		t.Fatal("redirected lane did not record access loss", row, err)
	}
}

func TestWorkerRunsRetentionAtStartup(t *testing.T) {
	f := workerFixtureFor(t)
	f.cfg.Limits.MaxActiveRuns = 1
	s := runner.NewStore(state.OSFS{}, f.l)
	ended := time.Now().Add(-15 * 24 * time.Hour)
	if e := s.Create(&runner.Run{Version: 1, UID: "expired", Phase: runner.Settled, Outcome: "succeeded", EndedAt: &ended}); e != nil {
		t.Fatal(e)
	}
	if e := s.Create(&runner.Run{Version: 1, UID: "held", Phase: runner.Held, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project"}}, Job: f.job}); e != nil {
		t.Fatal(e)
	}
	startWorkerFixture(t, f)
	waitWorker(t, func() bool { return definitionsRefreshed(f) })
	if _, e := os.Stat(f.l.RunDir("expired")); !os.IsNotExist(e) {
		t.Fatal("startup left expired journals for another day", e)
	}
	if _, e := os.Stat(f.l.RunFile("held")); e != nil {
		t.Fatal("startup retention removed a held run", e)
	}
}
