package tui

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

func jobsFixture(t testing.TB, name string, width, height int) *model {
	t.Helper()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	cfg := config.Config{Profiles: map[string]config.Profile{"worker": {Kind: "codex", Args: []string{"-m", "example-model", "--dangerously-bypass-approvals-and-sandbox"}}}}
	m := newModel(Options{Root: t.TempDir(), Now: func() time.Time { return now }, Config: cfg, Project: "example-project"})
	m.width, m.height = width, height
	m.data.Running, m.data.WorkerRunning = true, true
	for i, phase := range []runner.Phase{runner.Held, runner.Settled, runner.Settled, runner.Admitted, runner.Running} {
		j := indexJob("p", fmt.Sprintf("job-%d", i), []string{"Example daily check", "Example report", "Example paused check", "Example queued work", "Example unsupported work"}[i])
		m.data.Jobs = append(m.data.Jobs, j)
		started := now.Add(-time.Duration(i+1) * time.Hour)
		r := runner.Run{UID: fmt.Sprintf("run-%d", i), Target: runner.Target{Identity: kata.Identity{ProjectUID: "p"}}, Job: jToKata(j), Phase: phase, StartedAt: &started, Reason: "review required"}
		if i < 3 {
			r.IssueRef = fmt.Sprintf("example-project#a%d2b", i) // Leave later runs without an issue.
		}
		if i == 1 {
			r.Outcome = "succeeded"
		}
		if i == 2 {
			r.Outcome = "failed"
		}
		if i == 3 {
			r.StartedAt = nil
		}
		m.data.Runs = append(m.data.Runs, r)
	}
	m.data.Jobs[2].Definition = []byte(strings.Replace(string(m.data.Jobs[2].Definition), `"enabled":true`, `"enabled":false`, 1))
	m.data.Jobs[4].Supported = false
	m.data.Jobs[4].Reasons = []string{"a local support reason that wraps onto its continuation line"}
	m.data.Jobs = append(m.data.Jobs, indexJob("p", "never", "Example never run"))
	m.favorites[scopedRowKey("p", "job-0")] = true
	switch name {
	case "multi-project":
		m.opts.Projects = map[string]*Lane{"example-project": {Config: cfg}, "other-project": {}}
		m.data.Projects = map[string]Data{"p": {Project: "example-project"}, "q": {Project: "other-project"}}
		m.data.Jobs[1].ProjectUID = "q"
		m.data.Jobs[1].Project = "other-project"
	case "empty":
		m.data.Jobs = nil
		m.data.Runs = nil
	case "no-match":
		m.query = "absent"
	case "inspector-roles-paused":
		m.data.Jobs = m.data.Jobs[2:3]
	case "inspector-roles-unsupported":
		m.data.Jobs = m.data.Jobs[4:5]
	case "inspector-roles-native":
		m.data.Jobs = m.data.Jobs[:1]
		m.data.Jobs[0].Definition = json.RawMessage(`{"enabled":false,"trigger":{"kind":"interval","interval_seconds":3600},"action":{"kind":"notify","message":"Review the example"},"issue":{"kind":"per-run","parent_uid":"parent"}}`)
		m.data.Jobs[0].Owner = defs.Native
		m.data.Jobs[0].IssueRefs = map[string]string{"parent": "example-project#ab12"}
	case "inspector-roles-unpinned":
		m.data.Jobs = m.data.Jobs[:1]
		m.data.Jobs[0].ProjectUID = ""
	}
	if strings.HasPrefix(name, "marquee-") {
		m.data.Jobs[1].Name = "Example 界" + strings.Repeat(" scrolling job", 5)
		switch name {
		case "marquee-7":
			m.frame = 7
		case "marquee-end":
			m.frame = len([]rune(m.data.Jobs[1].Name)) + 12
		case "marquee-wrap":
			m.frame = len([]rune(m.data.Jobs[1].Name)) + 13
		}
	}
	m.rebuildJobIndex()
	m.rebuild()
	switch name {
	case "confirm-prune", "confirm-delete":
		m.confirm = []kata.Job{m.jobs.byKey[jobKey{"p", "job-0"}].job, m.jobs.byKey[jobKey{"p", "job-1"}].job}
		m.confirmMode = "prune"
		if name == "confirm-delete" {
			m.confirm = m.confirm[:1]
			m.confirmMode = "delete"
		}
	case "conflict-pause-newer", "conflict-prune-partial", "conflict-delete-readback-unavailable":
		pending := m.jobs.byKey[jobKey{"p", "job-0"}].job
		newer := pending
		newer.Definition = json.RawMessage(strings.Replace(string(pending.Definition), `"enabled":true`, `"enabled":false`, 1))
		m.conflict = &ConflictError{Pending: pending, Newer: newer, Operation: "pause"}
		if name != "conflict-pause-newer" {
			m.conflict.Operation = "prune"
			m.conflict.Done = []kata.Job{m.jobs.byKey[jobKey{"p", "job-1"}].job}
			m.conflict.Remaining = []kata.Job{pending}
		}
		if name == "conflict-delete-readback-unavailable" {
			m.conflict.Operation = "delete"
			m.conflict.Newer = kata.Job{}
		}
	}
	return m
}
func jToKata(j defs.JobEntry) kata.Job {
	return kata.Job{UID: j.UID, Name: j.Name, DefinitionEventUID: j.DefinitionEventUID}
}
func TestJobsGolden(t *testing.T) {
	for _, name := range []string{"one-project", "marquee-0", "marquee-7", "marquee-end", "marquee-wrap", "multi-project", "empty", "no-match", "wide-inspector", "inspector-roles-paused", "inspector-roles-unsupported", "inspector-roles-native", "inspector-roles-unpinned", "confirm-prune", "confirm-delete", "conflict-pause-newer", "conflict-prune-partial", "conflict-delete-readback-unavailable"} {
		sizes := [][2]int{{120, 40}, {80, 24}}
		if name == "wide-inspector" {
			sizes = [][2]int{{200, 50}}
		} else if strings.Contains(name, "inspector") {
			sizes = append(sizes, [2]int{200, 50})
		}
		t.Run(name, func(t *testing.T) {
			assertFrames(t, "jobs", name, sizes, func(w, h int) string { return jobsFixture(t, name, w, h).viewContent() })
		})
	}
}
func TestJobsInspectorRoles(t *testing.T) {
	for _, name := range []string{"wide-inspector", "inspector-roles-paused", "inspector-roles-unsupported", "inspector-roles-native", "inspector-roles-unpinned"} {
		t.Run(name, func(t *testing.T) {
			_, err := renderFrames(200, 50, func(_ string) string {
				w, h := 200, 50
				m := jobsFixture(t, name, w, h)
				v := m.indexedJob(m.rows[0])
				body := m.renderJobInspector(v, 64)
				text := ""
				for _, l := range body {
					text += l.Text + "\n"
				}
				expected := []string{titleStyle.Render(pad(v.entry.Name, 64))}
				switch name {
				case "wide-inspector":
					expected = append(expected, styledInspectorRunes(detailMetaStyle, " (default)"), styledInspectorRunes(detailMetaStyle, "run dir"), styledInspectorRunes(detailMetaStyle, "bypassed"))
				case "inspector-roles-paused":
					expected = append(expected, styledInspectorRunes(attentionStyle, "paused"))
				case "inspector-roles-unsupported":
					expected = append(expected, styledInspectorRunes(attentionStyle, " · unsupported"))
				case "inspector-roles-native":
					expected = append(expected, styledInspectorRunes(detailMetaStyle, " · native"))
				case "inspector-roles-unpinned":
					expected = append(expected, styledInspectorRunes(attentionStyle, "project unpinned"))
				}
				for _, want := range expected {
					if !strings.Contains(text, want) {
						t.Fatalf("role %q missing in %q", want, text)
					}
				}
				for _, l := range body {
					if strings.Contains(l.Text, "last run") || strings.Contains(l.Text, "review required") {
						if l.Hit != nil || len(l.Animations) > 0 {
							t.Fatal("static summary animates")
						}
					}
				}
				return m.viewContent()
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestJobsRunLabels(t *testing.T) {
	for _, tc := range runLabelFixture() {
		m := jobsFixture(t, "one-project", 120, 40)
		m.data.Runs[0] = tc.Run
		m.data.Runs[0].Job.UID = "job-0"
		m.data.Runs[0].Target.Identity.ProjectUID = "p"
		m.rebuildJobIndex()
		v := m.jobs.byKey[jobKey{"p", "job-0"}]
		cells := m.jobCells(v)
		status := cells[slices.IndexFunc(m.jobsColumns(), func(c column) bool { return c.Title == "status" })]
		label := tc.Label
		role := map[string]lipgloss.Style{"DetailMeta": detailMetaStyle, "Pass": passStyle, "Fail": failStyle, "Attention": attentionStyle, "Neutral": neutralStyle, "Subtle": subtleStyle, "Open": openStyle, "Closed": closedStyle, "Error": errorStyle}[tc.LabelRole]
		if status.Text != label || !reflect.DeepEqual(status.Style, role) {
			t.Fatalf("%s: %+v", label, status)
		}
	}
}

func styledInspectorRunes(role lipgloss.Style, text string) string {
	var b strings.Builder
	for _, r := range text {
		b.WriteString(role.Render(string(r)))
	}
	return b.String()
}
