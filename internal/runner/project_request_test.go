package runner

import (
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveManualProject(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	f := state.OSFS{}
	if e := state.InitializeLayout(f, l); e != nil {
		t.Fatal(e)
	}
	c := config.Config{Projects: []config.Project{{Name: "example-project", Actor: "worker"}, {Name: "team-project", Actor: "worker"}}}
	for _, p := range []struct{ uid, name, job string }{{"project-a", "example-project", "job-a"}, {"project-b", "team-project", "job-b"}} {
		pl := l.Project(p.uid)
		if e := state.WriteJSON(f, pl.StateFile(), kata.Installation{Version: 1, Name: p.name, ProjectUID: p.uid}); e != nil {
			t.Fatal(e)
		}
		if e := state.WriteJSON(f, pl.DefsSnapshot(), defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{{UID: p.job, Name: "same-name"}}}); e != nil {
			t.Fatal(e)
		}
	}
	if _, _, e := ResolveJob(c, f, l, "same-name", ""); e == nil {
		t.Fatal("ambiguous name accepted")
	}
	uid, job, e := ResolveJob(c, f, l, "same-name", "team-project")
	if e != nil || uid != "project-b" || job != "job-b" {
		t.Fatal(uid, job, e)
	}
	uid, job, e = ResolveJob(c, f, l, "job-a", "")
	if e != nil || uid != "project-a" || job != "job-a" {
		t.Fatal(uid, job, e)
	}
	if _, _, e = ResolveJob(c, f, l, "job-a", "team-project"); e == nil {
		t.Fatal("cross-project selection accepted")
	}
}

func TestRetargetedProjectSnapshotDoesNotKeepOldJobsSelectable(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	f := state.OSFS{}
	if e := state.InitializeLayout(f, l); e != nil {
		t.Fatal(e)
	}
	c := config.Config{Projects: []config.Project{{Name: "example-project", Actor: "worker"}}}
	for _, uid := range []string{"old-project", "new-project"} {
		pl := l.Project(uid)
		if e := state.WriteJSON(f, pl.StateFile(), kata.Installation{Version: 1, Name: "example-project", ProjectUID: uid}); e != nil {
			t.Fatal(e)
		}
		if e := state.WriteJSON(f, pl.DefsSnapshot(), defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{{UID: "job-" + uid, Name: "same-name"}}}); e != nil {
			t.Fatal(e)
		}
	}
	if e := state.WriteJSON(f, filepath.Join(l.Project("old-project").Root, "route.json"), map[string]any{"project": "example-project", "allowed": false, "error": "waiting for project example-project"}); e != nil {
		t.Fatal(e)
	}
	// A positively checked replacement mapping proves the old UID was superseded.
	if e := state.WriteJSON(f, filepath.Join(l.Project("new-project").Root, "route.json"), map[string]any{"project": "example-project", "allowed": true}); e != nil {
		t.Fatal(e)
	}
	uid, job, e := ResolveJob(c, f, l, "same-name", "example-project")
	if e != nil || uid != "new-project" || job != "job-new-project" {
		t.Fatal("old snapshot remained selectable", uid, job, e)
	}
	// Inspecting an old row can save a refused route. It cannot rebind the entry.
	if e := state.WriteJSON(f, filepath.Join(l.Project("old-project").Root, "route.json"), map[string]any{"project": "example-project", "allowed": false, "error": "project UID differs from lane"}); e != nil {
		t.Fatal(e)
	}
	uid, job, e = ResolveJob(c, f, l, "same-name", "example-project")
	if e != nil || uid != "new-project" || job != "job-new-project" {
		t.Fatal("old-row refusal replaced current binding", uid, job, e)
	}
}

func TestOldAmbiguousRequestIsRefused(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	f := state.OSFS{}
	if e := state.InitializeLayout(f, l); e != nil {
		t.Fatal(e)
	}
	c := config.Config{Projects: []config.Project{{Name: "project-a", Actor: "worker"}, {Name: "project-b", Actor: "worker"}}}
	for _, uid := range []string{"project-a", "project-b"} {
		if e := state.WriteJSON(f, l.Project(uid).StateFile(), kata.Installation{Version: 1, Name: uid, ProjectUID: uid}); e != nil {
			t.Fatal(e)
		}
		if e := state.WriteJSON(f, l.Project(uid).DefsSnapshot(), defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{{UID: "job-" + uid, Name: "same-name"}}}); e != nil {
			t.Fatal(e)
		}
	}
	req := Request{Version: 1, UID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", RunUID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", Kind: "run", Job: "same-name"}
	if e := state.WriteJSON(f, filepath.Join(l.RequestsDir(), req.UID+".json"), req); e != nil {
		t.Fatal(e)
	}
	m := NewManager(c, nil, nil, NewStore(f, l), nil, nil, nil)
	m.EnableProjects()
	if e := m.HandleRequests(t.Context(), nil); e != nil {
		t.Fatal(e)
	}
	var result RequestResult
	if e := state.ReadJSON(f, RequestResultPath(l, req.UID), 65536, &result); e != nil || !strings.Contains(result.Error, "ambiguous") {
		t.Fatal("ambiguous legacy request was not refused", result, e)
	}
}

type projectManualReceiver struct {
	calls      int
	result     error
	enqueueErr error
}

func (r *projectManualReceiver) RunNow(job, uid string, input ...*string) error {
	r.calls++
	return r.enqueueErr
}
func (r *projectManualReceiver) ManualResult(string) (bool, error) { return r.result != nil, r.result }

func TestFrozenRequestCanFinishAfterJobLeavesSnapshot(t *testing.T) {
	f := newAdmission(t)
	f.m.EnableProjects()
	lane, e := f.m.RegisterProject("project", f.cfg, f.k, f.g, f.s.layout.Project("project"))
	if e != nil {
		t.Fatal(e)
	}
	receiver := &projectManualReceiver{}
	lane.SetScheduler(receiver)
	req := Request{Version: 1, UID: "request", Kind: "run", RunUID: "manual-run", Project: "project", Job: "deleted-job"}
	if e := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.RequestsDir(), "request.json"), req); e != nil {
		t.Fatal(e)
	}
	if e := f.m.HandleRequests(t.Context(), nil); e != nil || receiver.calls != 1 {
		t.Fatal("frozen job required snapshot membership", e, receiver.calls)
	}
	receiver.result = errors.New("manual job deleted")
	if e := f.m.HandleRequests(t.Context(), nil); e != nil {
		t.Fatal(e)
	}
	var result RequestResult
	if e := state.ReadJSON(f.s.fsys, RequestResultPath(f.s.layout, req.UID), 65536, &result); e != nil || result.Error != "manual job deleted" {
		t.Fatal(result, e)
	}
}

func TestPendingProjectRequestDoesNotStarveAnotherProject(t *testing.T) {
	f := newAdmission(t)
	f.m.EnableProjects()
	first, second := &projectManualReceiver{enqueueErr: errors.New("backpressure")}, &projectManualReceiver{}
	for i, row := range []struct {
		uid      string
		receiver *projectManualReceiver
	}{{"project-a", first}, {"project-b", second}} {
		lane, e := f.m.RegisterProject(row.uid, f.cfg, nil, f.g, f.s.layout.Project(row.uid))
		if e != nil {
			t.Fatal(e)
		}
		lane.SetScheduler(row.receiver)
		req := Request{Version: 1, UID: fmt.Sprintf("request-%d", i), Kind: "run", RunUID: fmt.Sprintf("manual-%d", i), Project: row.uid, Job: "job"}
		if e := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.RequestsDir(), req.UID+".json"), req); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.m.HandleRequests(t.Context(), nil); e != nil || first.calls != 1 || second.calls != 1 {
		t.Fatal("one project's request starved another", e, first.calls, second.calls)
	}
}

func TestUnavailableProjectStillMakesJobNameAmbiguous(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	f := state.OSFS{}
	if e := state.InitializeLayout(f, l); e != nil {
		t.Fatal(e)
	}
	cfg := config.Config{Projects: []config.Project{{Name: "project-a", Actor: "worker"}, {Name: "project-b", Actor: "worker"}}}
	for _, uid := range []string{"project-a", "project-b"} {
		pl := l.Project(uid)
		if e := state.WriteJSON(f, pl.StateFile(), kata.Installation{Version: 1, Name: uid, ProjectUID: uid}); e != nil {
			t.Fatal(e)
		}
		if e := state.WriteJSON(f, pl.DefsSnapshot(), defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{{UID: "job-" + uid, Name: "same-name"}}}); e != nil {
			t.Fatal(e)
		}
		status := map[string]any{"project": uid, "allowed": true}
		if uid == "project-a" {
			status["allowed"] = false
			status["error"] = "waiting for project project-a"
		}
		if e := state.WriteJSON(f, filepath.Join(pl.Root, "route.json"), status); e != nil {
			t.Fatal(e)
		}
	}
	if _, _, e := ResolveJob(cfg, f, l, "same-name", ""); !errors.Is(e, ErrAmbiguousProject) {
		t.Fatal("unavailable project disappeared from ambiguity detection", e)
	}
}
