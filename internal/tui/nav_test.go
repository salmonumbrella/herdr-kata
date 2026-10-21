package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

func TestDetailItemKeyRoundTrip(t *testing.T) {
	for _, kind := range []string{"run", "step", "used-by", "job"} {
		for _, project := range []string{"", "project:one", "项目"} {
			uid := "id:\xff/例"
			key := detailItemKey(kind, project, uid)
			k, p, u, ok := parseDetailItemKey(key)
			if !ok || k != kind || p != project || u != uid {
				t.Fatal(key, k, p, u, ok)
			}
		}
	}
	if detailItemKey("run", "a", "same") == detailItemKey("run", "b", "same") {
		t.Fatal("collision")
	}
}
func TestDetailItemKeyUnknown(t *testing.T) {
	m, _ := fixture(t)
	for _, key := range []string{"run:uid", "run:?:YQ", "run:YQ==:Yg", "run::", "future::YQ", "job::YQ:extra", "run::YR"} {
		if _, _, _, ok := parseDetailItemKey(key); ok {
			t.Fatal(key)
		}
		if !m.detailItemExists(key) {
			t.Fatal("parse miss proves removal")
		}
		for _, p := range []missingPolicy{keepMissing, nearestSurvivor} {
			s := resolveSelection(detailSelection{Key: key, Ordinal: 4}, []rowHit{{Key: "other"}}, p, m.detailItemExists)
			if s.Key != key {
				t.Fatal(s)
			}
		}
	}
}
func TestNavRestore(t *testing.T) {
	m, _ := fixture(t)
	m.query = "Job"
	m.cursor = 3
	m.scroll = 2
	m.follow = false
	m.expanded["old"] = true
	_ = m.pushDetail(viewJob, "", "j03")
	m.expanded["old"] = false
	_ = m.pushDetail(viewRun, "", "r1")
	m.popDetail()
	if m.currentView() != viewJob {
		t.Fatal(m.nav)
	}
	m.popDetail()
	if m.currentView() != viewList || m.cursor != 3 || m.query != "Job" || m.scroll != 2 || m.follow || !m.expanded["old"] {
		t.Fatal(m.nav, m.cursor, m.scroll, m.query, m.expanded)
	}
}
func TestNavCycle(t *testing.T) {
	m, _ := fixture(t)
	_ = m.pushDetail(viewRun, "", "r1")
	m.scroll = 5
	_ = m.pushDetail(viewJob, "", "j00")
	_ = m.pushDetail(viewRun, "", "r1")
	if len(m.nav) != 2 || m.currentView() != viewRun || m.scroll != 5 {
		t.Fatal(m.nav, m.scroll)
	}
}
func TestNavCap(t *testing.T) {
	m, _ := fixture(t)
	for i := range 10 {
		_ = m.pushDetail(viewJob, "", fmt.Sprintf("j%02d", i))
	}
	if len(m.nav) != 8 || m.nav[0].view != viewList || m.nav[1].uid != "j03" {
		t.Fatal(m.nav)
	}
}
func TestNavMissingAndPoll(t *testing.T) {
	m, _ := fixture(t)
	_ = m.pushDetail(viewJob, "", "j00")
	_ = m.pushDetail(viewRun, "", "r1")
	m.data.Jobs = m.data.Jobs[1:]
	m.popDetail()
	if m.currentView() != viewJob || !strings.Contains(bodyTextForTest(m.renderDetail()), "deleted in Kata") {
		t.Fatal(m.nav, m.viewContent())
	}
	m.popDetail()
	if m.currentView() != viewList {
		t.Fatal(m.nav)
	}
}
func bodyTextForTest(b bodyLines) string {
	s := []string{}
	for _, l := range b {
		s = append(s, l.Text)
	}
	return strings.Join(s, "\n")
}
func TestDetailMakesNoKataCalls(t *testing.T) {
	m, f := fixture(t)
	m.pushDetail(viewJob, "", "j00")
	m.renderDetail()
	m.Update(dataMsg{data: m.data})
	m.pushDetail(viewRun, "", "r1")
	m.pushDetail(viewJob, "", "j00")
	m.popDetail()
	m.pushDetail(viewJob, "", "j00")
	if len(f.calls) != 0 {
		t.Fatal("local navigation called Kata", f.calls)
	}
}

func TestLegacyRunJobPin(t *testing.T) {
	m, _ := fixture(t)
	m.data.Health = map[string]any{"version": float64(1), "project_uid": "p"}
	m.data.Runs[0].Target.Identity.ProjectUID = "p"
	_ = m.pushDetail(viewRun, "p", "r1")
	_ = m.open()
	if m.currentView() != viewJob || m.nav[len(m.nav)-1].projectUID != "p" {
		t.Fatal(m.nav)
	}
	m.popDetail()
	m.data.Health["project_uid"] = "other"
	_ = m.open()
	if m.currentView() != viewRun || !strings.Contains(m.notice.Text, "no longer exists") {
		t.Fatal("snapshot identity changed", m.nav)
	}

}
func TestDetailPollKeepsKey(t *testing.T) {
	for _, kind := range []viewKind{viewJob, viewWorkflow} {
		m, _ := fixture(t)
		wf := kata.Workflow{UID: "w1", Definition: json.RawMessage(`{"version":1,"steps":[]}`)}
		m.data.Workflows = []kata.Workflow{wf}
		m.data.Runs = nil
		for i := range 21 {
			r := runner.Run{UID: fmt.Sprintf("r%02d", 20-i), Job: kata.Job{UID: "j00"}, Workflow: &wf, Phase: runner.Held}
			m.data.Runs = append(m.data.Runs, r)
		}
		uid := "j00"
		if kind == viewWorkflow {
			uid = "w1"
		}
		m.Update(dataMsg{data: m.data})
		_ = m.pushDetail(kind, "", uid)
		m.detailKey = detailItemKey("run", "", "r01")
		m.detailOrdinal = 19
		_ = m.detailItems()
		r := m.data.Runs[0]
		r.UID = "r99"
		m.Update(dataMsg{data: func() Data { d := m.data; d.Runs = append([]runner.Run{r}, d.Runs...); return d }()})
		items := m.detailItems()
		if m.detailKey != detailItemKey("run", "", "r01") || m.detailOrdinal != 20 {
			t.Fatal(kind, m.detailKey, m.detailOrdinal)
		}
		m.move(-1)
		if m.detailKey != detailItemKey("run", "", "r02") {
			t.Fatal(kind, m.detailKey)
		}
		m.detailKey = detailItemKey("run", "", "r01")
		m.detailOrdinal = 20
		m.data.Runs = m.data.Runs[:20]
		m.Update(dataMsg{data: m.data})
		m.reconcileDetailSelection(m.detailItems())
		if (m.detailKey == detailItemKey("run", "", "r01")) != (kind == viewJob) {
			t.Fatal(kind, m.detailKey, items)
		}
	}
}
func TestNavModalReturn(t *testing.T) {
	m, _ := fixture(t)
	_ = m.pushDetail(viewJob, "", "j00")
	m.scroll = 0
	m.follow = false
	m.Update(editorClosedMsg{Message: "done"})
	if m.scroll != 0 || m.currentView() != viewJob {
		t.Fatal(m.scroll, m.nav)
	}
}
func TestStepExistenceScopedToCurrentWorkflow(t *testing.T) {
	m, _ := fixture(t)
	m.data.Workflows[0].Definition = json.RawMessage(`{"version":1,"steps":[{"key":"here"}]}`)
	m.data.Workflows = append(m.data.Workflows, kata.Workflow{UID: "other", Definition: json.RawMessage(`{"version":1,"steps":[{"key":"there"}]}`)})
	_ = m.pushDetail(viewWorkflow, "", "w1")
	if !m.detailItemExists(detailItemKey("step", "", "here")) || m.detailItemExists(detailItemKey("step", "", "there")) {
		t.Fatal("cross-workflow step existence")
	}
}

func TestLinkedDetailProjectActions(t *testing.T) {
	m, _ := fixture(t)
	a, b := &fakeActions{}, &fakeActions{}
	m.opts.Projects = map[string]*Lane{"one": {Project: "one", ProjectUID: "p1", Service: a}, "two": {Project: "two", ProjectUID: "p2", Service: b}}
	m.opts.ForProject = func(uid, name string) Lane {
		if uid == "p1" {
			return *m.opts.Projects["one"]
		}
		return *m.opts.Projects["two"]
	}
	m.data.Jobs[0].ProjectUID = "p1"
	m.data.Jobs[0].Project = "one"
	r := m.data.Runs[0]
	r.Target.Identity.ProjectUID = "p1"
	other := r
	other.Target.Identity.ProjectUID = "p2"
	m.data.Runs = []runner.Run{other, r}
	m.rebuildJobIndex()
	m.rebuild()
	_ = m.pushDetail(viewJob, "p1", "j00")
	m.detailKey = detailItemKey("run", "p1", "r1")
	send(m, "a")
	if strings.Join(a.calls, ",") != "focus:r1" || len(b.calls) != 0 {
		t.Fatal(a.calls, b.calls)
	}
	m.detailKey = "run:r1"
	send(m, "a")
	if m.notice.Text != "invalid detail selection" || len(a.calls) != 1 || len(b.calls) != 0 {
		t.Fatal(m.notice.Text, a.calls, b.calls)
	}
	m.detailKey = detailItemKey("run", "p1", "r1")
	m.data.Runs = m.data.Runs[:1]
	m.rebuildJobIndex()
	m.rebuild()
	send(m, "a")
	if m.notice.Text != "run was removed" || len(a.calls) != 1 {
		t.Fatal(m.notice.Text, a.calls)
	}
}
func TestLocalJobNavigationCannotCrossProjects(t *testing.T) {
	m, f := fixture(t)
	m.data.Jobs[0].ProjectUID = "p1"
	other := m.data.Jobs[0]
	other.ProjectUID = "p2"
	m.data.Jobs = append(m.data.Jobs, other)
	m.rebuildJobIndex()
	m.rebuild()
	m.pushDetail(viewJob, "p1", "j00")
	m.pushDetail(viewJob, "p2", "j00")
	m.popDetail()
	if top := m.nav[len(m.nav)-1]; top.projectUID != "p1" || len(f.calls) != 0 {
		t.Fatal(m.nav, f.calls)
	}
}

func TestBackToRemovedListSelectionUsesFirstRow(t *testing.T) {
	m, _ := fixture(t)
	m.cursor = 5
	_ = m.pushDetail(viewJob, "", "j05")
	m.data.Jobs = append(m.data.Jobs[:5], m.data.Jobs[6:]...)
	m.Update(dataMsg{data: m.data})
	m.popDetail()
	r, ok := m.selected()
	if !ok || r.uid != "j00" {
		t.Fatal("deleted selection did not fall back to first row", r)
	}
}

func TestU1LegacyWorkflowUsedByOpensJob(t *testing.T) {
	m, f := fixture(t)
	m.data.Health = map[string]any{"version": float64(1), "project_uid": "p"}
	m.data.Jobs = m.data.Jobs[:1]
	m.data.Jobs[0].Definition = json.RawMessage(`{"action":{"kind":"execute","workflow_uid":"w1"}}`)
	m.Update(dataMsg{data: m.data})
	m.selectTab(2)
	send(m, "enter")
	items := m.detailItems()
	if len(items) != 1 {
		t.Fatal("missing local job link", items)
	}
	send(m, "l")
	if m.currentView() != viewJob || !strings.Contains(uncachedView(m), "Job 00") || len(f.calls) != 0 {
		t.Fatal("legacy used-by job link refused", m.nav, f.calls, m.notice.Text)
	}
}

func TestU1NestedRunGuards(t *testing.T) {
	for _, tc := range []struct {
		name, key, reason, want   string
		unproven, noPane, noIssue bool
	}{
		{name: "rejected", key: "u", reason: "admission_rejected", want: "run herdr-kata stop, then herdr-kata status --json to identify and abandon this run --local."},
		{name: "unproven", key: "u", unproven: true, want: "run herdr-kata stop, then herdr-kata status --json to identify and abandon this run --local."},
		{name: "no-pane", key: "a", noPane: true, want: "no agent pane for this run"},
		{name: "no-issue", key: "i", noIssue: true, want: "run has no Kata issue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, f := fixture(t)
			pinLegacyActions(m)
			r := &m.data.Runs[0]
			r.Phase, r.Reason = runner.Held, tc.reason
			if tc.unproven {
				admitted := false
				r.Admitted = &admitted
			}
			if tc.noPane {
				r.Herdr = runner.HerdrRef{}
			}
			if tc.noIssue {
				r.IssueUID, r.IssueRef = "", ""
			}
			_ = m.pushDetail(viewJob, "p", "j00")
			m.detailKey = detailItemKey("run", "p", "r1")
			send(m, tc.key)
			if len(f.calls) != 0 || m.notice.Text != tc.want {
				t.Fatal(m.notice.Text, f.calls)
			}
		})
	}
	m, f := fixture(t)
	pinLegacyActions(m)
	m.data.Runs[0].Phase = runner.Held
	_ = m.pushDetail(viewJob, "p", "j00")
	m.detailKey = detailItemKey("run", "p", "r1")
	send(m, "u")
	if strings.Join(f.calls, ",") != "resume:r1" {
		t.Fatal(m.notice.Text, f.calls)
	}
}

func TestDetailRemovedDoesNotRetarget(t *testing.T) {
	m, _ := fixture(t)
	send(m, "l")
	m.data.Jobs = m.data.Jobs[1:]
	m.rebuildJobIndex()
	m.rebuild()
	m.frameValid = false
	if !strings.Contains(uncachedView(m), "deleted in Kata") {
		t.Fatal("detail silently retargeted", uncachedView(m))
	}
}

func TestDetailOpenAndReturnUseLocalRuns(t *testing.T) {
	m, f := fixture(t)
	send(m, "l")
	send(m, "l")
	send(m, "enter")
	send(m, "esc")
	send(m, "l")
	if len(f.calls) != 0 {
		t.Fatal(f.calls)
	}
}

func TestLastRowRemovedWhileDetailOpenClampsCursor(t *testing.T) {
	m, _ := fixture(t)
	m.cursor = len(m.rows) - 1
	send(m, "l")
	if m.currentView() != viewJob {
		t.Fatal("detail not open")
	}
	m.data.Jobs = m.data.Jobs[:len(m.data.Jobs)-1]
	m.rebuildJobIndex()
	m.rebuild()
	m.frameValid = false
	send(m, "esc")
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic after closing detail: %v", r)
		}
	}()
	_ = uncachedView(m)
	send(m, "j")
}

func TestScopedRowsKeepIdentityWhenOpened(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, tc := range []struct {
			tab           int
			view          viewKind
			uid, identity string
		}{
			{0, viewJob, "job", "project-a\x00job"},
			{1, viewRun, "run-b", "project-a\x00run-b"},
			{2, viewWorkflow, "flow", "project-a\x00flow"},
		} {
			t.Run(fmt.Sprintf("%s/explicit=%t", tc.view, explicit), func(t *testing.T) {
				d := workflowSnapshot()
				d.Runs = d.Runs[:1]
				if explicit {
					d.Projects = map[string]Data{"project-a": {Project: "example-project"}}
					d.Jobs[0].ProjectUID, d.Workflows[0].ProjectUID = "project-a", "project-a"
				}
				m := workflowModel(t, d)
				m.selectTab(tc.tab)
				if len(m.rows) != 1 || m.rows[0].identity() != tc.identity {
					t.Fatalf("list identity: %+v", m.rows)
				}
				send(m, "l")
				f := m.nav[len(m.nav)-1]
				if f.view != tc.view || f.projectUID != "project-a" || f.uid != tc.uid {
					t.Fatal("opened wrong target", f)
				}
				r, ok := m.resolveFrame(f)
				if !ok || r.identity() != tc.identity {
					t.Fatalf("detail identity differs from list: %+v, found=%t", r, ok)
				}
			})
		}
	}
}

func TestUnscopedRunIdentityDoesNotUseDefinitionPin(t *testing.T) {
	d := workflowSnapshot()
	d.Runs = d.Runs[:1]
	d.Runs[0].Target.Identity.ProjectUID = ""
	m := workflowModel(t, d)
	m.selectTab(1)
	if m.rows[0].identity() != "run-b" {
		t.Fatal("recorded run identity normalized to the definition pin", m.rows[0])
	}
	send(m, "l")
	if f := m.nav[len(m.nav)-1]; f.projectUID != "" || f.uid != "run-b" {
		t.Fatal("unscoped run opened under a definition pin", f)
	}
}

func TestTabsRestoreListSelectionAfterBackgroundChange(t *testing.T) {
	m, _ := fixture(t)
	second := m.data.Runs[0]
	second.UID = "r2"
	second.Job.Name = "Second run"
	m.data.Runs = append(m.data.Runs, second)
	m.Update(dataMsg{data: m.data})
	for range 3 {
		m.Update(press("j"))
	}
	m.Update(press("3"))
	m.Update(press("j"))
	run, ok := m.selected()
	if !ok {
		t.Fatal("run selection missing")
	}
	m.Update(press("4"))
	arrival := m.data.Jobs[0]
	arrival.UID, arrival.Name = "a-new", "New arrival"
	m.data.Jobs = append(m.data.Jobs, arrival)
	m.Update(dataMsg{data: m.data})
	m.Update(press("2"))
	job, ok := m.selected()
	if !ok || job.uid != "j03" {
		t.Fatalf("job selection lost on tab return: %#v", job)
	}
	m.Update(press("3"))
	got, ok := m.selected()
	if !ok || got.uid != run.uid {
		t.Fatalf("run selection lost on tab return: got %#v want %#v", got, run)
	}
	m.Update(press("l"))
	if text := ansi.Strip(m.viewContent()); !strings.Contains(text, second.Job.Name) {
		t.Fatal("restored action opened another run:\n" + text)
	}
}
