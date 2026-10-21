package daemon

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// An unbuffered health tick acknowledges that the lane completed its previous
// select branch, including any refresh after the route snapshot was saved.
type reconnectLane struct {
	fixture *workerFixture
	ticks   chan time.Time
	saved   chan struct{}
	ctx     context.Context
	done    chan error
}

func startReconnectLane(t *testing.T, f *workerFixture) *reconnectLane {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	lane := &reconnectLane{fixture: f, ticks: make(chan time.Time), saved: make(chan struct{}, 16), ctx: ctx, done: make(chan error, 1)}
	guard := kata.NewRouteGuard(f.cfg, f.k, routeSnapshotFS{FS: state.OSFS{}, root: f.l.Project("project").Root, saved: lane.saved}, f.l.Project("project"))
	manager := runner.NewManager(f.cfg, f.k, nil, runner.NewStore(state.OSFS{}, f.l), nil, guard, nil)
	manager.EnableProjects()
	manager, err := manager.RegisterProject("project", f.cfg, f.k, guard, f.l.Project("project"))
	if err != nil {
		t.Fatal(err)
	}
	go func() { lane.done <- runLane(ctx, f.cfg, f.l.Project("project"), f.k, guard, manager, lane.ticks, nil) }()
	t.Cleanup(func() {
		cancel()
		if err := <-lane.done; err != nil {
			t.Error(err)
		}
	})
	// Recovery and the scheduler's start reuse the recovery wrapper's check.
	lane.wait(t, lane.saved)
	return lane
}

func (l *reconnectLane) wait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-l.ctx.Done():
		t.Fatal("lane acknowledgement:", l.ctx.Err())
	}
}

func (l *reconnectLane) tick(t *testing.T) {
	t.Helper()
	select {
	case l.ticks <- time.Now():
	case <-l.ctx.Done():
		t.Fatal("lane health tick:", l.ctx.Err())
	}
	l.wait(t, l.saved)
}

func (l *reconnectLane) refuseReset(t *testing.T) {
	t.Helper()
	// A reset that opens a stream is that stream's start refresh. This one
	// follows the start refresh, which writes the lane's first snapshot.
	waitWorker(t, func() bool {
		_, err := defs.ReadSnapshot(state.OSFS{}, l.fixture.l.Project("project").DefsSnapshot())
		return err == nil
	})
	l.fixture.redirected.Store(true)
	l.fixture.events <- `{"reset_required":true,"reset_after_id":1}`
	l.wait(t, l.saved) // reset's denied route check
	l.tick(t)          // the reset's refresh branch has now completed while denied
}

func disabledRevision(f *workerFixture) kata.Job {
	job := f.job
	job.DefinitionEventUID, job.Name = "new-event", "Disabled after reconnect"
	job.Definition = []byte(strings.Replace(string(job.Definition), `"enabled":true`, `"enabled":false`, 1))
	return job
}

func TestLaneRefreshesPendingDefinitionsAfterReconnect(t *testing.T) {
	for _, failFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "reconnect", true: "retry failed refresh"}[failFirst], func(t *testing.T) {
			f := workerFixtureFor(t)
			if err := os.Remove(f.l.Project("project").JobCheckpoint(f.job.UID)); err != nil {
				t.Fatal(err)
			}
			lane := startReconnectLane(t, f)
			lane.refuseReset(t)
			job := disabledRevision(f)
			f.remoteJob.Store(&job)
			f.failJobList.Store(failFirst)
			f.redirected.Store(false)
			lane.tick(t)
			if failFirst {
				lane.tick(t) // complete the failed refresh before restoring reads
				f.failJobList.Store(false)
			}
			lane.tick(t)
			lane.tick(t) // complete the successful refresh before reading its file
			snapshot, err := defs.ReadSnapshot(state.OSFS{}, f.l.Project("project").DefsSnapshot())
			if err != nil || len(snapshot.Jobs) != 1 || snapshot.Jobs[0].DefinitionEventUID != "new-event" || snapshot.Jobs[0].Name != "Disabled after reconnect" {
				t.Fatal("reconnected lane kept a stale definition snapshot", snapshot, err)
			}
		})
	}
}

func TestLanePendingRefreshFencesOldSchedulerRead(t *testing.T) {
	for _, failRefresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "successful refresh", true: "failed refresh"}[failRefresh], func(t *testing.T) {
			f := workerFixtureFor(t)
			if err := os.WriteFile(f.l.StopFile(), nil, 0600); err != nil {
				t.Fatal(err)
			}
			oldRead, release, nextRead := make(chan struct{}), make(chan struct{}), make(chan struct{})
			f.afterJobShow = func(ctx context.Context, call int32) {
				switch call {
				case 2:
					close(oldRead)
					select {
					case <-release:
					case <-ctx.Done():
					}
				case 3:
					close(nextRead)
				}
			}
			lane := startReconnectLane(t, f)
			lane.wait(t, oldRead) // scheduler froze a successful old response before reset
			if err := os.Remove(f.l.StopFile()); err != nil {
				t.Fatal(err)
			}
			lane.refuseReset(t)
			job := disabledRevision(f)
			f.remoteJob.Store(&job)
			f.failJobList.Store(failRefresh)
			f.redirected.Store(false)
			lane.tick(t)
			lane.tick(t) // finish the reconnect refresh before releasing the old response
			close(release)
			if failRefresh {
				f.events <- `{"event_id":2,"type":"issue.updated"}`
				lane.wait(t, nextRead) // old admission finished while refresh remained pending
				assertNoScheduledRun(t, f)
				f.failJobList.Store(false)
				lane.tick(t)
				lane.tick(t)
			}

			// A successful refresh must eventually supersede the old pending occurrence.
			tick := time.NewTicker(time.Millisecond)
			defer tick.Stop()
			for {
				var cp sched.Checkpoint
				if err := state.ReadJSON(state.OSFS{}, f.l.Project("project").JobCheckpoint(f.job.UID), 64<<10, &cp); err == nil && cp.DefinitionEventUID == "new-event" && len(cp.Pending) == 0 {
					break
				}
				select {
				case <-lane.ctx.Done():
					t.Fatal("scheduler did not supersede the old pending occurrence")
				case <-tick.C:
				}
			}
			assertNoScheduledRun(t, f)
		})
	}
}

func assertNoScheduledRun(t *testing.T, f *workerFixture) {
	t.Helper()
	ids, err := os.ReadDir(f.l.RunDir(""))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatal("scheduler prepared a run from an old read while refresh was pending", ids)
	}
}

func TestLaneKeepsDefinitionEventArrivingDuringRefresh(t *testing.T) {
	f := workerFixtureFor(t)
	if err := os.Remove(f.l.Project("project").JobCheckpoint(f.job.UID)); err != nil {
		t.Fatal(err)
	}
	initial, refreshing, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	f.afterJobList = func(ctx context.Context, call int32) {
		switch call {
		case 1: // the scheduler loads this listing too
			close(initial)
		case 2:
			close(refreshing)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}
	lane := startReconnectLane(t, f)
	lane.wait(t, initial)
	job := disabledRevision(f)
	f.remoteJob.Store(&job)
	f.events <- `{"event_id":1,"type":"cron.job.updated"}`
	lane.wait(t, refreshing) // the first refresh has frozen new-event
	newer := job
	newer.DefinitionEventUID = "newest-event"
	f.remoteJob.Store(&newer)
	f.events <- `{"event_id":2,"type":"cron.job.updated"}`
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		var cursor kata.Cursor
		if err := state.ReadJSON(state.OSFS{}, f.l.Project("project").EventsCursor(), 1024, &cursor); err == nil && cursor.AfterID == 2 {
			break // the second hook marked its refresh before the first completed
		}
		select {
		case <-tick.C:
		case <-lane.ctx.Done():
			t.Fatal("definition event was not acknowledged")
		}
	}
	close(release)
	lane.tick(t)
	lane.tick(t)
	snapshot, err := defs.ReadSnapshot(state.OSFS{}, f.l.Project("project").DefsSnapshot())
	if err != nil || len(snapshot.Jobs) != 1 || snapshot.Jobs[0].DefinitionEventUID != "newest-event" {
		t.Fatal("completed refresh erased a newer definition event", snapshot, err)
	}
}

func TestLaneDefinitionUpdateWhileAdmissionChecksRun(t *testing.T) {
	f := workerFixtureFor(t)
	checking, release := make(chan struct{}), make(chan struct{})
	f.afterRunShow = func(ctx context.Context) {
		close(checking)
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	lane := startReconnectLane(t, f)
	lane.wait(t, checking)   // Admit passed the old fence and is waiting on ShowRun
	lane.wait(t, lane.saved) // Admit's completed route check
	job := disabledRevision(f)
	f.remoteJob.Store(&job)
	f.events <- `{"event_id":1,"type":"cron.job.updated"}`
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		snapshot, err := defs.ReadSnapshot(state.OSFS{}, f.l.Project("project").DefsSnapshot())
		if err == nil && len(snapshot.Jobs) == 1 && snapshot.Jobs[0].DefinitionEventUID == "new-event" {
			break // refresh is known and queued while Admit still holds the old due
		}
		select {
		case <-tick.C:
		case <-lane.ctx.Done():
			t.Fatal("definition update did not refresh during admission")
		}
	}
	close(release)
	for {
		var cp sched.Checkpoint
		if err := state.ReadJSON(state.OSFS{}, f.l.Project("project").JobCheckpoint(f.job.UID), 64<<10, &cp); err == nil && cp.DefinitionEventUID == "new-event" && len(cp.Pending) == 0 {
			break
		}
		select {
		case <-tick.C:
		case <-lane.ctx.Done():
			t.Fatal("scheduler did not apply the pending definition update")
		}
	}
	assertNoScheduledRun(t, f)
}
