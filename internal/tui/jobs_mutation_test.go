package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type jobActionSpy struct {
	fakeActions
	deleted, paused []kata.Job
	failAt          int
	failure         error
}

func (s *jobActionSpy) Delete(j kata.Job) error {
	s.deleted = append(s.deleted, j)
	if len(s.deleted) == s.failAt {
		return s.failure
	}
	return nil
}
func (s *jobActionSpy) Pause(j kata.Job) (kata.Job, error) {
	s.paused = append(s.paused, j)
	saved := savedPauseJob(j)
	saved.ProjectUID = ""
	saved.Project = "" // Kata responses carry no local lane fields.
	return saved, s.failure
}
func actionJobs(t *testing.T) (*model, *jobActionSpy) {
	t.Helper()
	s := &jobActionSpy{}
	m := newModel(Options{Root: t.TempDir(), ProjectUID: "p", Project: "example-project", Service: s})
	m.width, m.height = 120, 40
	m.data.Health = map[string]any{"version": float64(1), "project_uid": "p"}
	for _, uid := range []string{"a", "b", "c"} {
		j := indexJob("p", uid, "example-"+uid)
		j.Definition = json.RawMessage(`{"enabled":true,"trigger":{"kind":"once","at":"2026-10-01T00:00:00Z"},"action":{"kind":"execute","prompt":"work"}}`)
		j.Owner = defs.Adapter
		m.data.Jobs = append(m.data.Jobs, j)
		m.data.Runs = append(m.data.Runs, runner.Run{UID: "run-" + uid, Target: runner.Target{Identity: kata.Identity{ProjectUID: "p"}}, Job: kata.Job{UID: uid, DefinitionEventUID: "event"}, Phase: runner.Settled, Outcome: "succeeded", Occurrence: "2026-10-01T00:00:00Z"})
	}
	m.rebuildJobIndex()
	m.rebuild()
	return m, s
}
func TestPruneMatchesQuery(t *testing.T) {
	m, s := actionJobs(t)
	m.query = "example-b"
	m.rebuild()
	send(m, "P")
	if len(m.confirm) != 1 || m.confirm[0].UID != "b" {
		t.Fatal(m.confirm)
	}
	data := m.data
	data.Jobs = []defs.JobEntry{indexJob("p", "z", "replacement")}
	m.Update(dataMsg{data: data})
	send(m, "y")
	if len(s.deleted) != 1 || s.deleted[0].UID != "b" || s.deleted[0].DefinitionEventUID != "event" {
		t.Fatal(s.deleted)
	}
	if len(m.data.Runs) != 3 || m.notice.Text != "1 finished one-shot(s) pruned; run history kept" {
		t.Fatal(m.notice.Text)
	}
}
func TestPrunePartialConflict(t *testing.T) {
	m, s := actionJobs(t)
	s.failAt = 2
	s.failure = &ConflictError{Pending: m.jobs.byKey[jobKey{"p", "b"}].job, Cause: errors.New("conflict")}
	send(m, "P")
	send(m, "y")
	if len(s.deleted) != 2 || m.conflict == nil || len(m.conflict.Done) != 1 || len(m.conflict.Remaining) != 2 {
		t.Fatal(s.deleted, m.conflict)
	}
	m.Update(dataMsg{data: Data{}})
	for _, k := range []string{"y", "R", "p", "D", "P", "f"} {
		send(m, k)
	}
	if len(s.deleted) != 2 || len(m.conflict.Done) != 1 || m.conflict.Remaining[0].UID != "b" {
		t.Fatal(s.deleted, m.conflict)
	}
	send(m, "esc")
	if m.notice.Text != "operation discarded; nothing retried" {
		t.Fatal(m.notice.Text)
	}
}
func TestJobActionStatusCopy(t *testing.T) {
	for _, tc := range []struct {
		name, definition, want string
		supported              bool
		owner                  defs.Owner
	}{
		{"paused", `{"enabled":false,"action":{"kind":"execute"}}`, "example-a is paused; p resumes it", true, defs.Adapter},
		{"native", `{"enabled":true,"action":{"kind":"notify"},"issue":{"kind":"per-run"}}`, "Kata owns native delivery and has no force-fire", true, defs.Native},
		{"unsupported", `{"enabled":true,"action":{"kind":"execute"}}`, "unmapped profile", false, defs.Adapter},
		{"run", `{"enabled":true,"action":{"kind":"execute"}}`, "run requested · example-a · project example-project", true, defs.Adapter},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, s := actionJobs(t)
			m.data.Jobs = m.data.Jobs[:1]
			m.data.Jobs[0].Definition = []byte(tc.definition)
			m.data.Jobs[0].Supported = tc.supported
			m.data.Jobs[0].Owner = tc.owner
			m.data.Jobs[0].Reasons = []string{"unmapped profile", "second reason"}
			m.rebuildJobIndex()
			m.rebuild()
			send(m, "R")
			if m.notice.Text != tc.want {
				t.Fatalf("%q", m.notice.Text)
			}
			want := 0
			if tc.name == "run" {
				want = 1
			}
			if len(s.calls) != want {
				t.Fatal(s.calls)
			}
		})
	}
	for _, tc := range []struct{ key, cancel, want string }{{"P", "x", "prune cancelled"}, {"D", "esc", "delete cancelled"}} {
		m, _ := actionJobs(t)
		m.finished = true
		m.rebuild()
		send(m, tc.key)
		send(m, tc.cancel)
		if m.notice.Text != tc.want {
			t.Fatal(m.notice.Text)
		}
	}
	m, _ := actionJobs(t)
	m.query = "missing"
	m.rebuild()
	send(m, "P")
	if m.notice.Text != "nothing to prune" {
		t.Fatal(m.notice.Text)
	}
}
func TestJobDeleteReleasesBusy(t *testing.T) {
	for _, failure := range []error{nil, errors.New("offline"), &ConflictError{Cause: errors.New("guard")}} {
		m, s := actionJobs(t)
		m.finished = true
		m.rebuild()
		m.pushDetail(viewJob, "p", "a")
		s.failAt = 1
		s.failure = failure
		send(m, "D")
		send(m, "y")
		if m.busy {
			t.Fatal("delete retained busy")
		}
		if failure == nil {
			if m.currentView() != viewList {
				t.Fatal(m.nav)
			}
		} else {
			if m.currentView() != viewJob {
				t.Fatal(m.nav)
			}
		}
		if m.conflict != nil {
			send(m, "p")
			if len(s.paused) != 0 {
				t.Fatal("conflict authorized a write")
			}
			send(m, "esc")
		}
		send(m, "p")
		if m.busy || len(s.paused) != 1 {
			t.Fatal("next action refused", m.notice.Text, s.paused)
		}
	}
}
func TestJobActionUnpinnedRefusal(t *testing.T) {
	m, s := actionJobs(t)
	m.data.Health = nil
	for i := range m.data.Jobs {
		m.data.Jobs[i].ProjectUID = ""
	}
	m.rebuildJobIndex()
	m.finished = true
	m.rebuild()
	send(m, "p")
	if len(s.paused) != 0 || !strings.Contains(m.notice.Text, "unpinned") {
		t.Fatal(m.notice.Text, s.paused)
	}
}
func TestPruneStopsAtUnavailableLane(t *testing.T) {
	m, s := actionJobs(t)
	m.opts.Projects = map[string]*Lane{"example-project": {Service: s}}
	m.data.Projects = map[string]Data{"p": {Project: "example-project"}}
	m.data.Jobs[2].ProjectUID = "q"
	m.data.Jobs[2].Project = "removed-project"
	m.data.Runs[2].Target.Identity.ProjectUID = "q"
	m.rebuildJobIndex()
	m.rebuild()
	send(m, "P")
	send(m, "y")
	if len(s.deleted) != 2 || s.deleted[0].UID != "a" || s.deleted[1].UID != "b" || !strings.Contains(m.notice.Text, "pending 1: example-c") {
		t.Fatal(s.deleted, m.notice.Text)
	}
}
func TestJobActionTargets(t *testing.T) {
	m, s := actionJobs(t)
	m.finished = true
	m.rebuild()
	_, cmd := m.Update(press("p"))
	if cmd == nil {
		t.Fatal("pause did not start")
	}
	replacement := &jobActionSpy{}
	m.opts.Service = replacement
	data := m.data
	data.Jobs = []defs.JobEntry{indexJob("p", "replacement", "replacement")}
	m.Update(dataMsg{data: data})
	applyTestCommand(m, cmd)
	if len(s.paused) != 1 || s.paused[0].UID != "a" || s.paused[0].ProjectUID != "p" || s.paused[0].DefinitionEventUID != "event" || len(replacement.paused) != 0 {
		t.Fatal(s.paused, replacement.paused)
	}
}
func TestPruneOrdinaryFailureKeepsTail(t *testing.T) {
	m, s := actionJobs(t)
	s.failAt = 2
	s.failure = errors.New("offline")
	send(m, "P")
	send(m, "y")
	if len(s.deleted) != 2 || m.busy || !strings.Contains(m.notice.Text, "deleted 1: example-a; pending 2: example-b, example-c: offline") {
		t.Fatal(s.deleted, m.notice.Text)
	}
}
func TestJobActionRefusals(t *testing.T) {
	m, s := actionJobs(t)
	m.data.Runs = nil
	m.rebuildJobIndex()
	m.rebuild()
	send(m, "a")
	if m.notice.Text != "example-a has never run" || len(s.calls) != 0 {
		t.Fatal(m.notice.Text, s.calls)
	}
	send(m, "b")
	if m.notice.Text != "b edits recurring jobs only" {
		t.Fatal(m.notice.Text)
	}
	binding := &bindingActionService{service: s}
	m.opts.Service = binding
	m.data.Jobs[0].Definition = []byte(`{"enabled":false,"action":{"kind":"notify"},"issue":{"kind":"per-run","parent_uid":"parent"}}`)
	m.data.Jobs[0].Owner = defs.Ignored
	m.rebuildJobIndex()
	m.rebuild()
	send(m, "b")
	if binding.ref != "parent" {
		t.Fatal("paused recurring binding unavailable", m.notice.Text)
	}

	m.editor = nil
	m.data.Jobs[0].IssueRefs = map[string]string{"parent": "example-project#abc45"}
	send(m, "b")
	if binding.ref != "example-project#abc45" {
		t.Fatal("binding editor ignored saved ref", binding.ref)
	}
}

func TestJobBindingRefusesMissingParent(t *testing.T) {
	m, _ := actionJobs(t)
	m.data.Jobs[0].Definition = []byte(`{"enabled":false,"action":{"kind":"notify"},"issue":{"kind":"per-run"}}`)
	m.rebuildJobIndex()
	m.rebuild()
	_, cmd := m.Update(press("b"))
	if cmd != nil || m.editor != nil || m.notice.Text != "recurring job has no parent issue" {
		t.Fatalf("cmd=%v editor=%T status=%q", cmd != nil, m.editor, m.notice.Text)
	}
}

func TestFavoritePublishAfterWrite(t *testing.T) {
	m, _ := fixture(t)
	m.favoriteWrite = func(map[string]bool) error { return errors.New("disk unavailable") }
	if err := m.setFavorite("p", "j00", true); err == nil {
		t.Fatal("write succeeded")
	}
	if m.favorites[scopedRowKey("p", "j00")] {
		t.Fatal("published failed write")
	}
}
func TestFavoriteExactRetry(t *testing.T) {
	for _, view := range []viewKind{viewList, viewJob, viewKind("editor")} {
		t.Run(string(view), func(t *testing.T) {
			m, actions := fixture(t)
			key := scopedRowKey("p", "j00")
			m.favoriteWrite = func(map[string]bool) error { return errors.New("disk unavailable") }
			if err := m.setFavorite("p", "j00", true); err == nil {
				t.Fatal("write succeeded")
			}
			m.favoriteWrite = nil
			if view == viewJob {
				_ = m.pushDetail(viewJob, "", "j01")
				actions.calls = nil
			}
			if view == viewKind("editor") {
				m.editor = projectEditor{text: "editor"}
			}
			m.Update(tickMsg(time.Now()))
			var got map[string]bool
			if err := state.ReadJSON(state.OSFS{}, filepath.Join(m.opts.Root, "board-preferences.json"), 64<<10, &got); err != nil {
				t.Fatal(err)
			}
			if !got[key] || !m.favorites[key] || len(m.pendingFavorites) != 0 || len(actions.calls) != 0 {
				t.Fatalf("disk %v pending %v actions %v", got, m.pendingFavorites, actions.calls)
			}
		})
	}
}
func TestFavoritePendingKeys(t *testing.T) {
	m, _ := fixture(t)
	m.favoriteWrite = func(map[string]bool) error { return errors.New("disk unavailable") }
	_ = m.setFavorite("p", "first", true)
	_ = m.setFavorite("q", "second", true)
	_ = m.setFavorite("p", "first", false)
	m.favoriteWrite = nil
	m.retryFavorites()
	if m.favorites[scopedRowKey("p", "first")] || !m.favorites[scopedRowKey("q", "second")] || len(m.pendingFavorites) != 0 {
		t.Fatal(m.favorites, m.pendingFavorites)
	}
}
func TestFavoriteLegacyFalse(t *testing.T) {
	m, _ := fixture(t)
	m.favorites["j00"] = true
	m.favorites[scopedRowKey("p", "j00")] = false
	if m.favoriteKey("p", "j00") {
		t.Fatal("explicit false lost to legacy fallback")
	}
}
func TestFavoriteRemovedRow(t *testing.T) {
	m, _ := fixture(t)
	m.cursor = 10
	before := m.rows[m.cursor].identity()
	if err := m.setFavorite("", "j30", true); err != nil {
		t.Fatal(err)
	}
	if m.rows[m.cursor].identity() != before {
		t.Fatal("favorite reorder retargeted cursor")
	}
	m.favoriteWrite = func(map[string]bool) error { return errors.New("disk unavailable") }
	_ = m.setFavorite("p", "removed", true)
	m.data.Jobs = nil
	m.Update(dataMsg{data: m.data})
	m.favoriteWrite = nil
	m.retryFavorites()
	if !m.favorites[scopedRowKey("p", "removed")] {
		t.Fatal("removed target was retargeted")
	}
}
func TestFavoriteStatusCopy(t *testing.T) {
	for _, project := range []string{"", "example-project"} {
		for _, tc := range []struct {
			initial bool
			want    string
		}{{false, "example-job pinned"}, {true, "example-job unpinned"}} {
			m, _ := fixture(t)
			m.opts.Project = project
			m.opts.Projects = map[string]*Lane{project: &m.opts.Lane}
			m.data.Jobs[0].Name = "example-job"
			m.data.Jobs[0].Project = project
			m.rebuildJobIndex()
			m.rebuild()
			m.favorites["j00"] = tc.initial
			send(m, "f")
			want := tc.want
			if project != "" {
				want += " · project example-project"
			}
			if m.notice.Text != want {
				t.Fatalf("%q want %q", m.notice.Text, want)
			}
		}
	}
}

func TestEditorFavoriteCompletion(t *testing.T) {
	m, _ := fixture(t)
	m.data.Jobs[0].ProjectUID = "p"
	m.data.Runs[0].Target.Identity.ProjectUID = "p"
	m.Update(dataMsg{data: m.data})
	_ = m.pushDetail(viewJob, "p", "j00")
	m.height = 8
	m.detailKey = detailItemKey("run", "p", "r1")
	m.scroll = 1
	m.follow = false
	m.frameValid = false
	_ = m.viewContent()
	before := append([]navFrame(nil), m.nav...)
	selected, scroll := m.detailKey, m.scroll
	m.favoriteWrite = func(map[string]bool) error { return errors.New("disk unavailable") }
	favorite := true
	saved := m.job(m.rows[0])
	saved.Name = "changed"
	m.Update(editorClosedMsg{Message: "changed saved", Target: editorTarget{"p", "j00"}, Saved: &saved, Favorite: &favorite})
	if m.notice.Text != "changed saved · favorite not saved: disk unavailable · retrying" {
		t.Fatal(m.notice.Text)
	}
	if !reflect.DeepEqual(m.nav, before) || m.detailKey != selected || m.scroll != scroll {
		t.Fatal("save changed detail context")
	}
	m.data.Jobs = m.data.Jobs[1:]
	m.Update(dataMsg{data: m.data})
	m.favoriteWrite = nil
	m.Update(tickMsg(time.Now()))
	if !m.favorites[scopedRowKey("p", "j00")] {
		t.Fatal("favorite completion retargeted")
	}
}
func TestEditorNewSelectionWaitsForSnapshot(t *testing.T) {
	m, _ := fixture(t)
	m.editorCreating = true
	saved := kata.Job{ProjectUID: "p", UID: "new-job", Name: "new example"}
	favorite := true
	m.Update(editorClosedMsg{Message: "new example saved", Target: editorTarget{"p", "new-job"}, Saved: &saved, Favorite: &favorite})
	if m.notice.Text != "new example saved · appears after the worker's next definition refresh" || !m.favorites[scopedRowKey("p", "new-job")] {
		t.Fatal(m.notice.Text, m.favorites)
	}
	m.data.Jobs = append(m.data.Jobs, defs.JobEntry{ProjectUID: "p", UID: "new-job", Name: "new example", Definition: []byte(`{"trigger":{"kind":"manual"}}`)})
	m.Update(dataMsg{data: m.data})
	if m.rows[m.cursor].uid != "new-job" {
		t.Fatal("new job not selected", m.rows[m.cursor])
	}
}

func TestFavoriteExplicitSupersedesSaved(t *testing.T) {
	m, _ := fixture(t)
	m.pendingFavorites = map[string]pendingFavorite{"j00": {target: editorTarget{UID: "j00"}, name: "Job 00", want: true, saved: true}}
	m.favoriteWrite = func(map[string]bool) error { return errors.New("disk unavailable") }
	send(m, "f")
	if m.notice.Text != "Job 00 · favorite not saved: disk unavailable · retrying" {
		t.Fatal("explicit favorite inherited saved status", m.notice.Text)
	}
}

func TestEditorLateSaveKeepsCurrentFrame(t *testing.T) {
	m, _ := fixture(t)
	_ = m.pushDetail(viewJob, "", "j01")
	_ = m.pushDetail(viewJob, "", "j02")
	before := append([]navFrame(nil), m.nav...)
	saved := m.job(row{job: 0})
	m.Update(editorClosedMsg{Message: "Job 00 saved", Target: editorTarget{"p", "j00"}, Saved: &saved})
	if !reflect.DeepEqual(m.nav, before) || m.nav[len(m.nav)-1].uid != "j02" {
		t.Fatal("late save popped or replaced current frame")
	}
}

func TestPruneNamesThenConfirms(t *testing.T) {
	m, f := fixture(t)
	pinLegacyActions(m)
	m.data.Jobs[0].Definition = json.RawMessage(`{"enabled":true,"trigger":{"kind":"once","at":"2020-01-01T00:00:00Z"}}`)
	m.data.Runs[0].Phase = runner.Settled
	m.data.Runs[0].Outcome = "succeeded"
	m.data.Runs[0].Occurrence = "2020-01-01T00:00:00Z"
	m.rebuildJobIndex()
	m.rebuild()
	m.frameValid = false
	send(m, "P")
	if !strings.Contains(uncachedView(m), "Job 00") || len(f.calls) != 0 {
		t.Fatal(uncachedView(m), f.calls)
	}
	send(m, "y")
	if strings.Join(f.calls, ",") != "delete:j00" {
		t.Fatal(f.calls)
	}
}

func TestUnsupportedRunNowRefused(t *testing.T) {
	m, f := fixture(t)
	m.data.Jobs[0].Supported = false
	m.data.Jobs[0].Reasons = []string{"unmapped checkout"}
	m.rebuildJobIndex()
	m.rebuild()
	m.frameValid = false
	send(m, "R")
	if len(f.calls) != 0 || !strings.Contains(m.notice.Text, "unmapped checkout") {
		t.Fatal(m.notice.Text, f.calls)
	}
}

func TestNativeRunNowRefused(t *testing.T) {
	m, f := fixture(t)
	m.data.Jobs[0].Owner = defs.Native
	m.data.Jobs[0].Definition = []byte(`{"enabled":true,"action":{"kind":"notify"},"issue":{"kind":"per-run"}}`)
	m.rebuildJobIndex()
	send(m, "R")
	if len(f.calls) > 0 || !strings.Contains(m.notice.Text, "Kata owns native delivery") {
		t.Fatal(m.notice.Text, f.calls)
	}
}

func TestPruneConfirmationCanPageAllNamedJobs(t *testing.T) {
	m, _ := fixture(t)
	m.confirm = nil
	for i := range 40 {
		m.confirm = append(m.confirm, kata.Job{UID: fmt.Sprint(i), Name: fmt.Sprintf("Prune %02d", i)})
	}
	send(m, "]")
	if len(m.confirm) != 40 || !strings.Contains(uncachedView(m), "Prune 23") {
		t.Fatal("cannot inspect remaining prune names", uncachedView(m))
	}
}

func TestPauseConflictRetainsGuardAcrossPolls(t *testing.T) {
	m, f := fixture(t)
	pinLegacyActions(m)
	f.mutationErr = &ConflictError{Pending: kata.Job{UID: "j00", Name: "original", DefinitionEventUID: "old"}, Newer: kata.Job{UID: "j00", Name: "newer", DefinitionEventUID: "new"}, Cause: errors.New("stale guard")}
	send(m, "p")
	m.Update(dataMsg{data: m.data})
	send(m, "p")
	if len(f.calls) != 1 || m.conflict == nil || m.conflict.Pending.DefinitionEventUID != "old" || !strings.Contains(uncachedView(m), "newer") {
		t.Fatal("conflict was replaced", uncachedView(m), f.calls)
	}
	send(m, "esc")
	if m.conflict != nil {
		t.Fatal("cannot explicitly discard conflict")
	}
}

func TestConflictReadbackUnavailableIsExplicit(t *testing.T) {
	m, _ := fixture(t)
	m.conflict = &ConflictError{Pending: kata.Job{UID: "job"}, Cause: errors.New("readback failed: offline")}
	if !strings.Contains(uncachedView(m), "Readback unavailable") {
		t.Fatal(uncachedView(m))
	}
}
