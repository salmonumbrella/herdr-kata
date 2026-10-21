package daemon

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"github.com/salmonumbrella/herdr-kata/internal/version"
)

func init() {
	if os.Getenv("HK_PROCESS_HELPER") != "1" || len(os.Args) < 2 {
		return
	}
	l := state.Layout{Root: os.Getenv("HERDR_PLUGIN_STATE_DIR")}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	switch os.Args[1] {
	case "serve":
		// Accelerate restart polling while keeping production health budgets.
		// The dedicated unreachable-server test supplies its own short budget.
		o := serveOptions{poll: 20 * time.Millisecond, pingEvery: 30 * time.Second, goneAfter: 2 * time.Minute, backoff: 10 * time.Millisecond, maxBackoff: 40 * time.Millisecond, resetAfter: time.Second}
		if err := serve(ctx, l, os.Args[0], o); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	case "worker":
		lock, ok, err := state.TryLock(l.WorkerLock())
		if err != nil || !ok {
			os.Exit(2)
		}
		defer lock.Unlock()
		f, err := os.OpenFile(filepath.Join(l.Root, "starts"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(3)
		}
		fmt.Fprintln(f, os.Getpid())
		f.Close()
		if os.Getenv("HK_CRASH_ONCE") == "1" {
			p := filepath.Join(l.Root, "crashed")
			if _, e := os.Stat(p); os.IsNotExist(e) {
				os.WriteFile(p, []byte("yes"), 0600)
				os.Exit(7)
			}
		}
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				os.Exit(0)
			case <-ticker.C:
				if exists(l.StopFile()) || exists(l.UpgradeFile()) {
					if os.Getenv("HK_DRAIN") == "1" && !exists(filepath.Join(l.Root, "drained")) {
						if !exists(filepath.Join(l.Root, "drain-waiting")) {
							if err := os.WriteFile(filepath.Join(l.Root, "drain-waiting"), nil, 0600); err != nil {
								os.Exit(4)
							}
						}
						continue
					}
					os.Exit(0)
				}
			}
		}
	}
}
func processFixture(t *testing.T) (config.Config, state.Layout, string) {
	t.Helper()
	s := herdrtest.New(t)
	t.Setenv("HERDR_SOCKET_PATH", s.Path)
	t.Setenv("HK_PROCESS_HELPER", "1")
	l := state.Layout{Root: t.TempDir()}
	t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := config.Config{Session: herdr.SessionFromSocket(s.Path)}
	t.Cleanup(func() {
		if t.Failed() {
			b, err := os.ReadFile(l.LogFile())
			t.Logf("supervisor log (%v):\n%s", err, b)
		}
		_ = Stop(l)
		waitFor(t, func() bool { held, _ := state.Held(l.ServeLock()); return !held })
	})
	return c, l, exe
}
func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for !f() {
		select {
		case <-ctx.Done():
			t.Fatal("condition not reached:", ctx.Err())
		case <-tick.C:
		}
	}
}
func waitWorkerInfo(t *testing.T, l state.Layout, pid int, exe string) ServeInfo {
	t.Helper()
	var i ServeInfo
	waitFor(t, func() bool {
		return state.ReadJSON(state.OSFS{}, l.ServeInfo(), 4096, &i) == nil && i.WorkerPID == pid && i.Exe == exe
	})
	return i
}
func info(t *testing.T, l state.Layout) ServeInfo {
	t.Helper()
	var i ServeInfo
	if e := state.ReadJSON(state.OSFS{}, l.ServeInfo(), 4096, &i); e != nil {
		t.Fatal(e)
	}
	return i
}
func started(l state.Layout) []string {
	b, _ := os.ReadFile(filepath.Join(l.Root, "starts"))
	return strings.Fields(string(b))
}
func TestUpWrongSessionExitsZeroNoProcess(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/other/herdr.sock")
	l := state.Layout{Root: filepath.Join(t.TempDir(), "absent")}
	r, e := Up(context.Background(), config.Config{Session: "wanted"}, l, "/missing")
	if e != nil || r != WrongSession {
		t.Fatalf("%v %v", r, e)
	}
	if exists(l.Root) {
		t.Fatal("wrong session wrote state")
	}
}
func TestUpStartsServeOnceConcurrent(t *testing.T) {
	c, l, exe := processFixture(t)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, e := Up(context.Background(), c, l, exe); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	waitFor(t, func() bool { return len(started(l)) == 1 })
	time.Sleep(100 * time.Millisecond)
	if n := len(started(l)); n != 1 {
		t.Fatalf("workers: %d", n)
	}
}
func TestServeRestartsCrashedWorker(t *testing.T) {
	t.Setenv("HK_CRASH_ONCE", "1")
	c, l, exe := processFixture(t)
	if _, e := Up(context.Background(), c, l, exe); e != nil {
		t.Fatal(e)
	}
	waitFor(t, func() bool { return len(started(l)) >= 2 })
}
func TestKilledServeOrphanWorkerBlocksSecond(t *testing.T) {
	c, l, exe := processFixture(t)
	if _, e := Up(context.Background(), c, l, exe); e != nil {
		t.Fatal(e)
	}
	waitFor(t, func() bool { return len(started(l)) == 1 })
	first := info(t, l)
	syscall.Kill(first.PID, syscall.SIGKILL)
	waitFor(t, func() bool { h, _ := state.Held(l.ServeLock()); return !h })
	if _, e := Up(context.Background(), c, l, exe); e != nil {
		t.Fatal(e)
	}
	time.Sleep(150 * time.Millisecond)
	if len(started(l)) != 1 {
		t.Fatal("second worker launched while orphan holds lock")
	}
	pid, _ := strconv.Atoi(started(l)[0])
	syscall.Kill(pid, syscall.SIGTERM)
	waitFor(t, func() bool { return len(started(l)) == 2 })
}
func TestServeExitsWhenHerdrGone2m(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(t.TempDir(), "gone", "herdr.sock"))
	t.Setenv("HK_PROCESS_HELPER", "1")
	l := state.Layout{Root: t.TempDir()}
	t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
	exe, _ := os.Executable()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	e := serve(ctx, l, exe, serveOptions{poll: 10 * time.Millisecond, pingEvery: 20 * time.Millisecond, goneAfter: 120 * time.Millisecond, backoff: 10 * time.Millisecond, maxBackoff: 20 * time.Millisecond, resetAfter: time.Second})
	if e != nil || time.Since(start) < 120*time.Millisecond || ctx.Err() != nil {
		t.Fatalf("duration %v error %v context %v", time.Since(start), e, ctx.Err())
	}
}
func TestStopThenUpRestarts(t *testing.T) {
	c, l, exe := processFixture(t)
	if _, e := Up(context.Background(), c, l, exe); e != nil {
		t.Fatal(e)
	}
	waitFor(t, func() bool { return len(started(l)) == 1 })
	if e := Stop(l); e != nil {
		t.Fatal(e)
	}
	waitFor(t, func() bool { h, _ := state.Held(l.ServeLock()); return !h })
	if _, e := Up(context.Background(), c, l, exe); e != nil {
		t.Fatal(e)
	}
	waitFor(t, func() bool { return len(started(l)) == 2 })
}
func TestUpgradeDrainsThenReexecs(t *testing.T) {
	t.Setenv("HK_DRAIN", "1")
	c, l, exe := processFixture(t)
	// Always release the helper before the fixture's Stop cleanup.
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(l.Root, "drained"), nil, 0600) })
	if _, e := Up(context.Background(), c, l, exe); e != nil {
		t.Fatal(e)
	}
	waitFor(t, func() bool { return len(started(l)) == 1 })
	pid, err := strconv.Atoi(started(l)[0])
	if err != nil {
		t.Fatal(err)
	}
	id, err := version.BuildID(exe)
	if err != nil {
		t.Fatal(err)
	}
	before := waitWorkerInfo(t, l, pid, filepath.Join(l.BinDir(), "herdr-kata-"+id))
	candidate := filepath.Join(t.TempDir(), "candidate")
	b, _ := os.ReadFile(exe)
	b = append(b, 0)
	if e := os.WriteFile(candidate, b, 0700); e != nil {
		t.Fatal(e)
	}
	r, e := Up(context.Background(), c, l, candidate)
	if e != nil || r != UpgradeRequested {
		t.Fatalf("%v %v", r, e)
	}
	// The helper acknowledges observing the upgrade before we inspect the gate.
	waitFor(t, func() bool { return exists(filepath.Join(l.Root, "drain-waiting")) })
	if len(started(l)) != 1 {
		t.Fatal("worker replaced before drain")
	}
	if err := os.WriteFile(filepath.Join(l.Root, "drained"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(started(l)) == 2 })
	pid, err = strconv.Atoi(started(l)[1])
	if err != nil {
		t.Fatal(err)
	}
	id, err = version.BuildID(candidate)
	if err != nil {
		t.Fatal(err)
	}
	after := waitWorkerInfo(t, l, pid, filepath.Join(l.BinDir(), "herdr-kata-"+id))
	if after.PID != before.PID || after.Exe == before.Exe {
		t.Fatalf("not reexec: %+v %+v", before, after)
	}
}
func TestStagedBinaryNotOverwrittenInUse(t *testing.T) {
	c, l, exe := processFixture(t)
	if _, e := Up(context.Background(), c, l, exe); e != nil {
		t.Fatal(e)
	}
	waitFor(t, func() bool { return len(started(l)) == 1 })
	i := info(t, l)
	before, _ := os.Stat(i.Exe)
	if r, e := Up(context.Background(), c, l, exe); e != nil || r != AlreadyRunning {
		t.Fatalf("%v %v", r, e)
	}
	after, _ := os.Stat(i.Exe)
	if !os.SameFile(before, after) {
		t.Fatal("staged binary overwritten")
	}
	id, _ := version.BuildID(i.Exe)
	if !strings.HasSuffix(i.Exe, id) {
		t.Fatal("not build-addressed")
	}
}
func TestStatusDoesNotStart(t *testing.T) {
	l := state.Layout{Root: filepath.Join(t.TempDir(), "absent")}
	s, e := ReadStatus(l)
	if e != nil || s.Running {
		t.Fatalf("%+v %v", s, e)
	}
	if exists(l.Root) {
		t.Fatal("status created state")
	}
}
func TestLogRotates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	w, e := newLogWriter(p, 8, 5)
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"12345678", "abcdefgh", "ABCDEFGH", "87654321", "ijklmnop", "QRSTUVWX"} {
		if _, e := w.Write([]byte(s)); e != nil {
			t.Fatal(e)
		}
	}
	w.Close()
	b, _ := os.ReadFile(p)
	if !bytes.Equal(b, []byte("QRSTUVWX")) {
		t.Fatalf("log %q", b)
	}
	files, _ := filepath.Glob(p + "*")
	if len(files) != 5 {
		t.Fatalf("files %v", files)
	}
}

func TestPruneKeepsLiveReportBinary(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	os.MkdirAll(l.BinDir(), 0700)
	current := filepath.Join(l.BinDir(), "herdr-kata-current")
	live := filepath.Join(l.BinDir(), "herdr-kata-live")
	old := filepath.Join(l.BinDir(), "herdr-kata-old")
	for _, p := range []string{current, live, old} {
		os.WriteFile(p, []byte("binary"), 0700)
	}
	if e := state.WriteJSON(state.OSFS{}, l.RunFile("run"), map[string]any{"phase": "handed_off", "herdr": map[string]string{"report_bin": live}}); e != nil {
		t.Fatal(e)
	}
	if e := prune(l, current); e != nil {
		t.Fatal(e)
	}
	if !exists(live) || !exists(current) || exists(old) {
		t.Fatal("unsafe binary retention")
	}
}
func TestUpRejectsSymlinkStagedBinary(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	exe, _ := os.Executable()
	id, _ := version.BuildID(exe)
	os.MkdirAll(l.BinDir(), 0700)
	os.Symlink(exe, filepath.Join(l.BinDir(), "herdr-kata-"+id))
	if _, e := stage(l, exe); e == nil {
		t.Fatal("staging accepted symlink")
	}
}

func TestPruneKeepsOrphanAfterReplacementServeStarts(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	os.MkdirAll(l.BinDir(), 0700)
	old := filepath.Join(l.BinDir(), "herdr-kata-orphan")
	current := filepath.Join(l.BinDir(), "herdr-kata-replacement")
	for _, p := range []string{old, current} {
		os.WriteFile(p, []byte("binary"), 0700)
	}
	sl, _, _ := state.TryLock(l.ServeLock())
	defer sl.Unlock()
	wl, _, _ := state.TryLock(l.WorkerLock())
	defer wl.Unlock()
	if e := state.WriteJSON(state.OSFS{}, l.ServeInfo(), ServeInfo{PID: os.Getpid(), Exe: current}); e != nil {
		t.Fatal(e)
	}
	if e := prune(l, current); e != nil {
		t.Fatal(e)
	}
	if !exists(old) {
		t.Fatal("orphan's live executable removed while replacement serve waits")
	}
}

func TestUpUpgradeOrphanRequestsDrain(t *testing.T) {
	t.Setenv("HK_DRAIN", "1")
	c, l, exe := processFixture(t)
	if _, e := Up(context.Background(), c, l, exe); e != nil {
		t.Fatal(e)
	}
	waitFor(t, func() bool { return len(started(l)) == 1 })
	first := info(t, l)
	syscall.Kill(first.PID, syscall.SIGKILL)
	waitFor(t, func() bool { h, _ := state.Held(l.ServeLock()); return !h })
	candidate := filepath.Join(t.TempDir(), "candidate")
	b, _ := os.ReadFile(exe)
	os.WriteFile(candidate, append(b, 0), 0700)
	if _, e := Up(context.Background(), c, l, candidate); e != nil {
		t.Fatal(e)
	}
	defer func() { pid, _ := strconv.Atoi(started(l)[0]); syscall.Kill(pid, syscall.SIGTERM) }()
	if !exists(l.UpgradeFile()) {
		t.Fatal("orphan was not asked to drain for a different build")
	}
	if len(started(l)) != 1 {
		t.Fatal("replacement launched before orphan drained")
	}
	os.WriteFile(filepath.Join(l.Root, "drained"), nil, 0600)
	waitFor(t, func() bool { return len(started(l)) == 2 })
	if exists(l.UpgradeFile()) {
		t.Fatal("upgrade marker not consumed")
	}
}

func TestStopWaitsForSupervisorExit(t *testing.T) {
	c, l, exe := processFixture(t)
	if _, e := Up(context.Background(), c, l, exe); e != nil {
		t.Fatal(e)
	}
	waitFor(t, func() bool { return len(started(l)) == 1 })
	if e := Stop(l); e != nil {
		t.Fatal(e)
	}
	held, e := state.Held(l.ServeLock())
	if e != nil || held {
		t.Fatal("stop returned while supervisor still running")
	}
}
func TestStatusIgnoresInterruptedCheckpointWrite(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	pl := statusTestLane(t, l)
	os.MkdirAll(filepath.Join(pl.Root, "jobs"), 0700)
	os.WriteFile(filepath.Join(pl.Root, "jobs", ".json-interrupted"), nil, 0600)
	if _, e := ReadStatus(l); e != nil {
		t.Fatalf("temporary checkpoint broke status: %v", e)
	}
}

func TestStatusNextFireFromCheckpoint(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	pl := statusTestLane(t, l)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if e := state.WriteJSON(state.OSFS{}, pl.DefsSnapshot(), []defs.JobEntry{{UID: "job", Owner: defs.Adapter, Supported: true, Trigger: &defs.Trigger{Kind: "interval", IntervalSeconds: 60}, CreatedAt: at}}); e != nil {
		t.Fatal(e)
	}
	state.WriteJSON(state.OSFS{}, filepath.Join(pl.Root, "jobs", "job.json"), map[string]any{"job_uid": "job", "handled_through": at.Add(2 * time.Minute)})
	s, e := ReadStatus(l)
	if e != nil {
		t.Fatal(e)
	}
	if s.NextFires["job"] != at.Add(3*time.Minute) {
		t.Fatalf("next fires: %v", s.NextFires)
	}
}

func TestStatusSkipsRunWithoutJournal(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	os.MkdirAll(l.RunDir("quarantined"), 0700)
	s, e := ReadStatus(l)
	if e != nil || len(s.Runs) != 0 {
		t.Fatalf("empty run directory broke status: %+v %v", s, e)
	}
}
func TestUpRemovesInterruptedStaging(t *testing.T) {
	c, l, _ := processFixture(t)
	os.MkdirAll(l.BinDir(), 0700)
	temp := filepath.Join(l.BinDir(), ".stage-interrupted")
	os.WriteFile(temp, []byte("partial binary"), 0700)
	if _, e := Up(t.Context(), c, l, "/missing"); e == nil {
		t.Fatal("missing build accepted")
	}
	if exists(temp) {
		t.Fatal("interrupted staging copy leaked")
	}
}

func TestUpCanceledReadinessStillReportsStarted(t *testing.T) {
	c, l, exe := processFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := Up(ctx, c, l, exe)
	if err != nil || result != Started {
		t.Fatalf("detached start reported failure: %v %v", result, err)
	}
	waitFor(t, func() bool { return len(started(l)) == 1 })
}

func TestStatusVersion2Definitions(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	pl := statusTestLane(t, l)
	snap := defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{{UID: "job", Name: "name", Supported: true}}, Workflows: nil}
	if err := state.WriteJSON(state.OSFS{}, pl.DefsSnapshot(), snap); err != nil {
		t.Fatal(err)
	}
	s, err := ReadStatus(l)
	if err != nil || len(s.Definitions) != 1 || s.Definitions[0].Name != "name" {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestUpRefusesStoppingOrphanWorker(t *testing.T) {
	c, l, exe := processFixture(t)
	lock, ok, err := state.TryLock(l.WorkerLock())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer lock.Unlock()
	if err := state.WriteJSON(state.OSFS{}, l.StopFile(), map[string]any{"version": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := Up(t.Context(), c, l, exe); err == nil || !strings.Contains(err.Error(), "runtime is stopping") {
		t.Fatal("up did not refuse stopping orphan", err)
	}
	if !exists(l.StopFile()) {
		t.Fatal("up removed pending stop")
	}
}
