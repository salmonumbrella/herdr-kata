package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/inbox"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestCacheOnlyChangedFiles(t *testing.T) {
	root := t.TempDir()
	path := cacheTestLane(t, root).DefsSnapshot()
	state.WriteJSON(state.OSFS{}, path, defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{{UID: "one"}}})
	fs := &countFS{}
	c := newCache(root, fs)
	first, err := c.Poll()
	if err != nil || len(first.Jobs) != 1 {
		t.Fatal(first, err)
	}
	n := fs.opens
	for range 1800 {
		if _, err := c.Poll(); err != nil {
			t.Fatal(err)
		}
	}
	if fs.opens != n {
		t.Fatalf("unchanged files opened %d times", fs.opens-n)
	}
	state.WriteJSON(state.OSFS{}, path, defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{{UID: "two"}}})
	later := time.Now().Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	next, err := c.Poll()
	if err != nil || next.Jobs[0].UID != "two" {
		t.Fatal(next, err)
	}
}

func TestCacheDoesNotReopenUnchangedCorruptFile(t *testing.T) {
	root := t.TempDir()
	path := cacheTestLane(t, root).DefsSnapshot()
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	fs := &countFS{}
	c := newCache(root, fs)
	if _, err := c.Poll(); err == nil {
		t.Fatal("missing error")
	}
	n := fs.opens
	for range 10 {
		if _, err := c.Poll(); err == nil {
			t.Fatal("error disappeared")
		}
	}
	if fs.opens != n {
		t.Fatal("unchanged corrupt file reopened")
	}
}

type countFS struct {
	state.OSFS
	opens int
}

func (f *countFS) Open(p string) (state.File, error) { f.opens++; return f.OSFS.Open(p) }

func TestServeFileDoesNotProveLiveness(t *testing.T) {
	root := t.TempDir()
	fs := state.OSFS{}
	state.WriteJSON(fs, filepath.Join(root, "serve.json"), map[string]any{"pid": 999999999, "worker_pid": 999999999})
	c := newCache(root, fs)
	d, err := c.Poll()
	if err != nil || d.Running || d.WorkerRunning {
		t.Fatal(d, err)
	}
	state.WriteJSON(fs, filepath.Join(root, "serve.json"), map[string]any{"pid": os.Getpid(), "worker_pid": os.Getpid()})
	d, err = c.Poll()
	if err != nil || !d.Running || d.WorkerRunning {
		t.Fatal(d, err)
	}
}

func TestOrphanWorkerLivenessFromLock(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	if err := state.WriteJSON(state.OSFS{}, l.ServeInfo(), map[string]any{"worker_pid": 0}); err != nil {
		t.Fatal(err)
	}
	lock, ok, err := state.TryLock(l.WorkerLock())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer lock.Unlock()
	c := newCache(l.Root, nil)
	d, err := c.Poll()
	if err != nil || !d.WorkerRunning {
		t.Fatal("orphan shown as stopped", d, err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	d, err = c.Poll()
	if err != nil || d.WorkerRunning {
		t.Fatal("released lock shown as running", d, err)
	}
}

func TestCacheReadsMultipleProjectLanes(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	f := state.OSFS{}
	if e := state.InitializeLayout(f, l); e != nil {
		t.Fatal(e)
	}
	for _, uid := range []string{"project-a", "project-b"} {
		pl := l.Project(uid)
		if e := state.WriteJSON(f, pl.StateFile(), kata.Installation{Version: 1, ProjectUID: uid, Name: uid}); e != nil {
			t.Fatal(e)
		}
		if e := state.WriteJSON(f, pl.DefsSnapshot(), defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{{UID: "job-" + uid, Name: "job"}}}); e != nil {
			t.Fatal(e)
		}
	}
	counted := &countFS{}
	c := newCache(l.Root, counted)
	d, e := c.Poll()
	if e != nil || len(d.Jobs) != 2 || len(d.Projects) != 2 {
		t.Fatal(d, e)
	}
	n := counted.opens
	for range 5 {
		if _, e := c.Poll(); e != nil {
			t.Fatal(e)
		}
	}
	if counted.opens != n {
		t.Fatal("unchanged lane snapshots reopened")
	}
	if d.Jobs[0].Project == "" || d.Jobs[1].Project == "" || d.Jobs[0].Project == d.Jobs[1].Project {
		t.Fatal("lost project identity", d.Jobs)
	}
}

func TestCachePreservesInboxHealthWithoutRequests(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	f := state.OSFS{}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if e := state.InitializeLayout(f, l); e != nil {
		t.Fatal(e)
	}
	for i, uid := range []string{"project-a", "project-b"} {
		pl := l.Project(uid)
		if e := state.WriteJSON(f, pl.StateFile(), kata.Installation{Version: 1, Name: uid, ProjectUID: uid}); e != nil {
			t.Fatal(e)
		}
		snapshot := inbox.Snapshot{Version: 1, LastSuccessfulScan: at.Add(time.Duration(i) * time.Hour)}
		if i == 0 {
			snapshot.Error = "scan unavailable"
		}
		if e := inbox.WriteSnapshot(f, pl.Root, snapshot); e != nil {
			t.Fatal(e)
		}
	}
	d, e := newCache(l.Root, f).Poll()
	if e != nil || !d.Inbox.LastSuccessfulScan.Equal(at) || !strings.Contains(d.Inbox.Error, "project-a: scan unavailable") {
		t.Fatal("empty inboxes lost their health", d.Inbox, e)
	}
}

func TestProjectRenameKeepsPreviousSnapshotStable(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	f := state.OSFS{}
	pl := l.Project("project-a")
	if e := state.InitializeLayout(f, l); e != nil {
		t.Fatal(e)
	}
	pin := kata.Installation{Version: 1, Name: "old-name", ProjectUID: "project-a"}
	if e := state.WriteJSON(f, pl.StateFile(), pin); e != nil {
		t.Fatal(e)
	}
	if e := state.WriteJSON(f, pl.DefsSnapshot(), defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{{UID: "job"}}}); e != nil {
		t.Fatal(e)
	}
	c := newCache(l.Root, f)
	before, e := c.Poll()
	if e != nil {
		t.Fatal(e)
	}
	pin.Name = "new-name"
	if e := state.WriteJSON(f, pl.StateFile(), pin); e != nil {
		t.Fatal(e)
	}
	later := time.Now().Add(time.Second)
	if e := os.Chtimes(pl.StateFile(), later, later); e != nil {
		t.Fatal(e)
	}
	after, e := c.Poll()
	if e != nil {
		t.Fatal(e)
	}
	if before.Projects["project-a"].Jobs[0].Project != "old-name" || after.Projects["project-a"].Jobs[0].Project != "new-name" {
		t.Fatal("poll mutated an earlier snapshot", before.Projects["project-a"].Jobs, after.Projects["project-a"].Jobs)
	}
}

func TestCacheSkipsCheckpointsWithoutProjectPin(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	f := state.OSFS{}
	if e := state.InitializeLayout(f, l); e != nil {
		t.Fatal(e)
	}
	if e := state.WriteJSON(f, l.Project("project-a").JobCheckpoint("job"), map[string]any{"version": 1, "job_uid": "job"}); e != nil {
		t.Fatal(e)
	}
	data, e := newCache(l.Root, f).Poll()
	if e != nil || len(data.Projects) != 0 || len(data.Jobs) != 0 {
		t.Fatal("unpinned checkpoint became a project", data, e)
	}
}

func TestMissingLaneUsesSavedProjectName(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	if err := state.InitializeLayout(state.OSFS{}, l); err != nil {
		t.Fatal(err)
	}
	r := &runner.Run{Version: 1, UID: "run", ProjectName: "example-project", Target: runner.Target{Identity: kata.Identity{ProjectUID: "01K00000000000000000000001"}}, Phase: runner.Held}
	if err := runner.NewStore(state.OSFS{}, l).Create(r); err != nil {
		t.Fatal(err)
	}
	d, err := newCache(l.Root, state.OSFS{}).Poll()
	if err != nil || len(d.Runs) != 1 || d.Runs[0].Reason != "waiting for project example-project" {
		t.Fatal(d.Runs, err)
	}
}

func cacheTestLane(t *testing.T, root string) state.Layout {
	t.Helper()
	l := state.Layout{Root: root}
	if err := state.InitializeLayout(state.OSFS{}, l); err != nil {
		t.Fatal(err)
	}
	lane := l.Project("project")
	if err := state.WriteJSON(state.OSFS{}, lane.StateFile(), kata.Installation{Version: 1, ProjectUID: "project", Name: "example-project"}); err != nil {
		t.Fatal(err)
	}
	return lane
}
