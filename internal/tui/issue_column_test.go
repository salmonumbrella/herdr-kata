package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

func cellText(t *testing.T, cols []column, cells []cell, title string) string {
	t.Helper()
	for i, c := range cols {
		if c.Title == title {
			return cells[i].Text
		}
	}
	t.Fatalf("no %s column in %+v", title, cols)
	return ""
}

// Runs show their issue the way Kata's # column does: a ref in the default
// project drops its prefix, and any other project keeps it.
func TestRunsIssueColumn(t *testing.T) {
	for _, tc := range []struct {
		name, uid, ref string
		refs           map[string]string
		want           string
	}{
		{"short ref", "issue-uid", "abcd", nil, "#abcd"},
		{"qualified ref", "issue-uid", "example-project#abcd", nil, "#abcd"},
		{"saved ref map", "issue-uid", "", map[string]string{"issue-uid": "example-project#wxyz"}, "#wxyz"},
		{"other project", "issue-uid", "other#abcd", nil, "other#abcd"},
		{"no issue", "", "", nil, "—"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := jobsFixture(t, "multi-project", 140, 24)
			m.data.Runs = m.data.Runs[:1]
			m.data.Runs[0].IssueUID, m.data.Runs[0].IssueRef, m.data.Runs[0].IssueRefs = tc.uid, tc.ref, tc.refs
			m.rebuildJobIndex()
			m.selectTab(1)
			if got := cellText(t, m.runsColumns(), m.runCells(m.rows[0]), "#"); got != tc.want {
				t.Fatalf("issue cell %q, want %q", got, tc.want)
			}
		})
	}
}

// A job's issue is its newest run's issue, the one `i` opens from Jobs.
func TestJobsIssueColumnUsesNewestRun(t *testing.T) {
	m := jobsFixture(t, "one-project", 140, 24)
	m.data.Runs[1].IssueRef = "example-project#ab12"
	older := *m.data.Runs[1].StartedAt
	older = older.Add(-time.Hour)
	m.data.Runs = append(m.data.Runs, runner.Run{UID: "run-old", Target: m.data.Runs[1].Target, Job: m.data.Runs[1].Job, Phase: runner.Settled, Outcome: "succeeded", StartedAt: &older, IssueRef: "example-project#zz99"})
	m.rebuildJobIndex()
	m.selectTab(0)
	cols := m.jobsColumns()
	for i, r := range m.rows {
		v := m.indexedJob(r)
		got := cellText(t, cols, m.jobCells(v), "#")
		switch v.key.uid {
		case "job-1":
			if got != "#ab12" {
				t.Fatalf("job-1 issue %q, want newest run's #ab12", got)
			}
			m.cursor = i
			run, ok := m.selectedJobRun()
			if !ok || run.IssueRef != "example-project#ab12" {
				t.Fatalf("issue column disagrees with the run i opens: %+v", run)
			}
		case "never":
			if got != "—" {
				t.Fatalf("never-run job issue %q, want —", got)
			}
		}
	}
}

// Updated is the job's most recent activity: its newest run's start or end,
// or a definition change when that is later.
func TestJobsUpdatedIsLatestActivity(t *testing.T) {
	m := jobsFixture(t, "one-project", 140, 24)
	now := m.now()
	ended := now.Add(-10 * time.Minute)
	m.data.Runs[1].EndedAt = &ended                      // job-1 started 2h ago, ended 10m ago
	m.data.Jobs[0].UpdatedAt = now.Add(-5 * time.Minute) // job-0 edited after its 1h-old run
	m.data.Jobs[5].UpdatedAt = time.Time{}               // never run, never edited: creation
	m.rebuildJobIndex()
	m.selectTab(0)
	cols := m.jobsColumns()
	want := map[string]string{"job-0": ago(now, now.Add(-5*time.Minute)), "job-1": ago(now, ended), "job-2": ago(now, now.Add(-3*time.Hour)), "never": ago(now, m.data.Jobs[5].CreatedAt)}
	for _, r := range m.rows {
		v := m.indexedJob(r)
		w, ok := want[v.key.uid]
		if !ok {
			continue
		}
		if got := cellText(t, cols, m.jobCells(v), "updated"); got != w {
			t.Fatalf("%s updated %q, want %q", v.key.uid, got, w)
		}
	}
}

func TestWorkflowsUpdatedIsLatestActivity(t *testing.T) {
	d := workflowF1GoldenSnapshot()
	m := workflowModel(t, d)
	now := m.now()
	cols := m.workflowColumns()
	v := m.wfIndex[workflowKey{"project-a", "flow"}]
	if got, want := cellText(t, cols, m.workflowCells(v), "updated"), ago(now, now.Add(-10*time.Minute)); got != want {
		t.Fatalf("updated %q, want newest run %q", got, want)
	}
	d.Workflows[0].UpdatedAt = now.Add(-time.Minute)
	m.Update(dataMsg{data: d})
	v = m.wfIndex[workflowKey{"project-a", "flow"}]
	if got, want := cellText(t, cols, m.workflowCells(v), "updated"), ago(now, now.Add(-time.Minute)); got != want {
		t.Fatalf("updated %q, want definition change %q", got, want)
	}
}

// `/` matches the issue shown in the column.
func TestSearchMatchesIssueRef(t *testing.T) {
	m := jobsFixture(t, "one-project", 140, 24)
	m.data.Runs[1].IssueRef = "example-project#ab12"
	m.rebuildJobIndex()
	for _, tab := range []int{0, 1} {
		m.selectTab(tab)
		m.query = "#ab12"
		m.rebuild()
		if len(m.rows) != 1 {
			t.Fatalf("tab %d: %d rows match #ab12", tab, len(m.rows))
		}
		m.query = ""
		m.rebuild()
	}
	m.data.Runs[0].IssueRef = "cd34" // A bare saved ref still matches as #cd34.
	m.rebuildJobIndex()
	m.selectTab(1)
	m.query = "#cd34"
	m.rebuild()
	if len(m.rows) != 1 {
		t.Fatalf("runs: %d rows match #cd34", len(m.rows))
	}
}

// At 80 columns the issue and status columns survive next to the project.
func TestIssueAndStatusSurviveNarrowWidths(t *testing.T) {
	for _, tab := range []int{0, 1} {
		m := jobsFixture(t, "multi-project", 80, 24)
		m.data.Runs[0].IssueRef = "example-project#ab12"
		m.rebuildJobIndex()
		m.selectTab(tab)
		var text string
		if tab == 0 {
			text = bodyTextForTest(m.renderJobs())
		} else {
			text = bodyTextForTest(m.renderRuns())
		}
		text = ansi.Strip(text)
		for _, want := range []string{"#ab12", "parked"} {
			if !strings.Contains(text, want) {
				t.Fatalf("tab %d clipped %s:\n%s", tab, want, text)
			}
		}
	}
}

// A pause or save updates the job's updated cell before the next snapshot,
// as it does for the rest of the row.
func TestJobsUpdatedFollowsPendingSave(t *testing.T) {
	m := jobsFixture(t, "one-project", 140, 24)
	m.rebuildJobIndex()
	m.selectTab(0)
	now := m.now()
	saved := m.jobs.byKey[jobKey{"p", "never"}].job
	saved.Definition = json.RawMessage(strings.Replace(string(saved.Definition), `"enabled":true`, `"enabled":false`, 1))
	saved.DefinitionEventUID = "paused-event"
	saved.UpdatedAt = now.Add(-time.Minute)
	m.Update(jobPausedMsg{saved: saved})
	v := m.jobs.byKey[jobKey{"p", "never"}]
	if got, want := cellText(t, m.jobsColumns(), m.jobCells(v), "updated"), ago(now, saved.UpdatedAt); got != want {
		t.Fatalf("updated %q after pause, want %q", got, want)
	}
}

// `/` on Runs matches the issue ref the column resolves from saved job
// references, not only the run's own fields.
func TestRunsSearchMatchesResolvedIssueRef(t *testing.T) {
	m := jobsFixture(t, "one-project", 140, 24)
	m.data.Runs = m.data.Runs[:1]
	m.data.Runs[0].IssueUID, m.data.Runs[0].IssueRef, m.data.Runs[0].IssueRefs = "issue-uid", "", nil
	m.data.Runs[0].ProjectName = "example-project"
	m.data.Jobs[0].IssueRefs = map[string]string{"issue-uid": "example-project#ef56"}
	m.rebuildJobIndex()
	m.selectTab(1)
	if got := cellText(t, m.runsColumns(), m.runCells(m.rows[0]), "#"); got != "#ef56" {
		t.Fatalf("issue cell %q, want #ef56", got)
	}
	m.query = "#ef56"
	m.rebuild()
	if len(m.rows) != 1 {
		t.Fatalf("%d runs match the #ef56 shown in their issue column", len(m.rows))
	}
}
