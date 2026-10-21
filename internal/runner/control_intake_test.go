package runner

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// The intake contract reads each published request once, then processes releases
// first even when a resume sorts earlier. A queued manual request stays durable.
type requestReadFS struct {
	state.FS
	dir      string
	reads    map[string]int
	withdraw string
}

func (f *requestReadFS) ReadDir(dir string) ([]fs.DirEntry, error) {
	entries, err := f.FS.ReadDir(dir)
	if err == nil && dir == f.dir && f.withdraw != "" {
		if err := f.FS.Remove(f.withdraw); err != nil {
			return nil, err
		}
		f.withdraw = ""
	}
	return entries, err
}

func (f *requestReadFS) Open(path string) (state.File, error) {
	if filepath.Dir(path) == f.dir {
		f.reads[filepath.Base(path)]++
	}
	return f.FS.Open(path)
}

func TestRequestIntakeReadsOnceAndReleasesBeforeResume(t *testing.T) {
	f := newRecovery(t)
	counted := &requestReadFS{FS: f.s.fsys, dir: f.s.layout.RequestsDir(), reads: map[string]int{}}
	f.s.fsys = counted
	for _, req := range []Request{
		{Version: 1, UID: "a-resume", Kind: "resume", RunUID: f.uid},
		{Version: 1, UID: "b-unknown", Kind: "unknown", RunUID: "other"},
		{Version: 1, UID: "c-manual", Kind: "run", RunUID: "manual", Job: "job"},
		{Version: 1, UID: "z-release", Kind: "release", RunUID: f.uid},
	} {
		if err := state.WriteJSON(f.s.fsys, filepath.Join(counted.dir, req.UID+".json"), req); err != nil {
			t.Fatal(err)
		}
	}
	// Already queued in this worker: keep the request without another enqueue.
	f.m.requests.Store("c-manual", struct{}{})
	if err := f.m.HandleRequests(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if r := loaded(t, f.s, f.uid); r.Phase != HandedOff {
		t.Fatal("resume won over release", r.Phase)
	}
	for _, name := range []string{"a-resume.json", "b-unknown.json", "c-manual.json", "z-release.json"} {
		if counted.reads[name] != 1 {
			t.Errorf("%s read %d times", name, counted.reads[name])
		}
	}
	var result RequestResult
	if err := state.ReadJSON(f.s.fsys, RequestResultPath(f.s.layout, "b-unknown"), 64<<10, &result); err != nil || result.Error != "unknown control request kind" || result.Kind != "unknown" {
		t.Fatal("unknown request not refused", result, err)
	}
	if _, err := os.Stat(filepath.Join(counted.dir, "b-unknown.json")); !os.IsNotExist(err) {
		t.Fatal("unknown request remains", err)
	}
	if _, err := os.Stat(filepath.Join(counted.dir, "c-manual.json")); err != nil {
		t.Fatal("pending manual request lost", err)
	}
}

func TestCanceledRequestIntakeKeepsUnknownRequest(t *testing.T) {
	f := newAdmission(t)
	path := filepath.Join(f.s.layout.RequestsDir(), "request.json")
	if err := state.WriteJSON(f.s.fsys, path, Request{Version: 1, UID: "request", Kind: "unknown", RunUID: "manual"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := f.m.HandleRequests(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled scan handled request", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("canceled scan discarded request", err)
	}
}

func TestRequestIntakeResultUsesResolvedProjectUID(t *testing.T) {
	f := newAdmission(t)
	if err := state.WriteJSON(f.s.fsys, f.s.layout.StateFile(), map[string]int{"version": 1}); err != nil {
		t.Fatal(err)
	}
	pl := f.s.layout.Project("project-uid")
	if err := state.WriteJSON(f.s.fsys, pl.StateFile(), kata.Installation{Version: 1, ProjectUID: "project-uid", Name: "project"}); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteJSON(f.s.fsys, pl.DefsSnapshot(), defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{{UID: "job-uid", Name: "Check"}}}); err != nil {
		t.Fatal(err)
	}
	f.m.EnableProjects()
	lane, err := f.m.RegisterProject("project-uid", f.cfg, f.k, f.g, pl)
	if err != nil {
		t.Fatal(err)
	}
	lane.SetScheduler(&projectManualReceiver{result: errors.New("manual job disabled")})
	req := Request{Version: 1, UID: "request", RunUID: "manual", Kind: "run", Job: "Check"}
	if err := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.RequestsDir(), "request.json"), req); err != nil {
		t.Fatal(err)
	}
	if err := f.m.HandleRequests(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	var result RequestResult
	if err := state.ReadJSON(f.s.fsys, RequestResultPath(f.s.layout, "request"), 64<<10, &result); err != nil || result.Kind != "run" || result.Project != "project-uid" || result.Job != "job-uid" || result.Error != "manual job disabled" {
		t.Fatal("result lost resolved identity", result, err)
	}
}

// A request withdrawn after intake must not receive a new acknowledgement.
type withdrawRequestFS struct {
	state.FS
	after, withdraw string
}

func (f *withdrawRequestFS) Remove(path string) error {
	if err := f.FS.Remove(path); err != nil {
		return err
	}
	if path == f.after {
		return f.FS.Remove(f.withdraw)
	}
	return nil
}
func TestRequestWithdrawnBeforeDispatchIsSkipped(t *testing.T) {
	f := newAdmission(t)
	first := filepath.Join(f.s.layout.RequestsDir(), "a-release.json")
	withdrawn := filepath.Join(f.s.layout.RequestsDir(), "z-unknown.json")
	for _, req := range []Request{
		{Version: 1, UID: "a-release", Kind: "release", RunUID: "missing"},
		{Version: 1, UID: "z-unknown", Kind: "unknown", RunUID: "manual"},
	} {
		if err := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.RequestsDir(), req.UID+".json"), req); err != nil {
			t.Fatal(err)
		}
	}
	f.s.fsys = &withdrawRequestFS{FS: f.s.fsys, after: first, withdraw: withdrawn}
	if err := f.m.HandleRequests(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	var result RequestResult
	if err := state.ReadJSON(f.s.fsys, RequestResultPath(f.s.layout, "a-release"), 64<<10, &result); err != nil || result.Error == "" {
		t.Fatal("release not refused", result, err)
	}
	if _, err := os.Stat(RequestResultPath(f.s.layout, "z-unknown")); !os.IsNotExist(err) {
		t.Fatal("withdrawn request acknowledged", err)
	}
}

func TestRequestIntakeToleratesVanishedFile(t *testing.T) {
	f := newAdmission(t)
	dir := f.s.layout.RequestsDir()
	for _, uid := range []string{"a-unknown", "z-vanished"} {
		if err := state.WriteJSON(f.s.fsys, filepath.Join(dir, uid+".json"), Request{Version: 1, UID: uid, Kind: "unknown", RunUID: "manual"}); err != nil {
			t.Fatal(err)
		}
	}
	f.s.fsys = &requestReadFS{FS: f.s.fsys, dir: dir, reads: map[string]int{}, withdraw: filepath.Join(dir, "z-vanished.json")}
	if err := f.m.HandleRequests(t.Context(), nil); err != nil {
		t.Fatal("vanished request blocked valid refusal", err)
	}
	var result RequestResult
	if err := state.ReadJSON(f.s.fsys, RequestResultPath(f.s.layout, "a-unknown"), 64<<10, &result); err != nil || result.Error != "unknown control request kind" {
		t.Fatal(result, err)
	}
}

// The first release can remain pending while a duplicate is withdrawn. Its
// suppression of resume must survive the missing duplicate.
type withdrawReleaseFS struct {
	state.FS
	path  string
	stats int
}

func (f *withdrawReleaseFS) Lstat(path string) (fs.FileInfo, error) {
	if path == f.path {
		f.stats++
		if f.stats == 2 {
			if err := f.FS.Remove(path); err != nil {
				return nil, err
			}
		}
	}
	return f.FS.Lstat(path)
}

func TestWithdrawnDuplicateReleaseStillSuppressesResume(t *testing.T) {
	f := heldResumeFixture(t)
	resume := Request{Version: 1, UID: "a-resume", RunUID: f.uid, Kind: "resume"}
	resumeCtx, stopResume := context.WithTimeout(t.Context(), time.Second)
	err := f.m.Resume(resumeCtx, resume)
	stopResume()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	deliver(t, f.q, f.uid)
	for _, req := range []Request{resume,
		{Version: 1, UID: "b-release", RunUID: f.uid, Kind: "release"},
		{Version: 1, UID: "z-duplicate", RunUID: f.uid, Kind: "release"},
	} {
		publishResume(t, f, req)
	}
	f.s.fsys = &withdrawReleaseFS{FS: f.s.fsys, path: filepath.Join(f.s.layout.RequestsDir(), "z-duplicate.json")}
	ctx, cancel := context.WithCancel(t.Context())
	defer f.m.Wait()
	defer cancel()
	func() {
		f.q.issueMu.Lock()
		defer f.q.issueMu.Unlock()
		if err := f.m.HandleRequests(ctx, nil); err != nil {
			t.Fatal(err)
		}
	}()
	r := loaded(t, f.s, f.uid)
	if r.Phase != Held || r.Resume == nil || !r.Resume.Pending || r.Steps[0].Submission != None {
		t.Fatalf("withdrawn duplicate allowed resume past pending release: phase=%s resume=%+v step=%+v", r.Phase, r.Resume, r.Steps[0])
	}
	if _, err := os.Stat(filepath.Join(f.s.layout.RequestsDir(), "b-release.json")); err != nil {
		t.Fatal("pending release disappeared", err)
	}
}

// A filesystem denial is injected at Open so this also reproduces under root.
type unreadableRequestFS struct {
	state.FS
	path string
}

func (f unreadableRequestFS) Open(path string) (state.File, error) {
	if path == f.path {
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrPermission}
	}
	return f.FS.Open(path)
}
func TestUnreadableRequestDoesNotStarveRelease(t *testing.T) {
	f := newRecovery(t)
	path := filepath.Join(f.s.layout.RequestsDir(), "a-unreadable.json")
	for _, req := range []Request{{Version: 1, UID: "a-unreadable", Kind: "resume", RunUID: f.uid}, {Version: 1, UID: "z-release", Kind: "release", RunUID: f.uid}} {
		if err := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.RequestsDir(), req.UID+".json"), req); err != nil {
			t.Fatal(err)
		}
	}
	f.s.fsys = unreadableRequestFS{FS: f.s.fsys, path: path}
	if err := f.m.HandleRequests(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if loaded(t, f.s, f.uid).Phase != HandedOff {
		t.Fatal("unreadable file starved release")
	}
	var result RequestResult
	if err := state.ReadJSON(state.OSFS{}, RequestResultPath(f.s.layout, "a-unreadable"), 64<<10, &result); err != nil || !strings.Contains(result.Error, "permission denied") || strings.Contains(result.Error, f.s.layout.Root) {
		t.Fatal(result, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unreadable file remains", err)
	}
	files, err := os.ReadDir(filepath.Join(f.s.layout.Root, "quarantine", "requests"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	data, err := os.ReadFile(filepath.Join(f.s.layout.Root, "quarantine", "requests", files[0].Name()))
	if err != nil || !strings.Contains(string(data), "a-unreadable") {
		t.Fatal(string(data), err)
	}
}
func TestRejectedRequestKeepsReasonWithoutPath(t *testing.T) {
	f := newRecovery(t)
	req := Request{Version: 2, UID: "bad", Kind: "resume", RunUID: f.uid}
	if err := state.WriteJSON(f.s.fsys, filepath.Join(f.s.layout.RequestsDir(), "bad.json"), req); err != nil {
		t.Fatal(err)
	}
	if err := f.m.HandleRequests(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	var result RequestResult
	if err := state.ReadJSON(f.s.fsys, RequestResultPath(f.s.layout, "bad"), 64<<10, &result); err != nil || result.Error != "invalid control request" {
		t.Fatal(result, err)
	}
}
