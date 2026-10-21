package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

// Like Kata's closed issues, settled runs keep the default text color; only
// the status cell carries the outcome color.
func TestRunRowsKeepDefaultText(t *testing.T) {
	preserveTheme(t)
	for _, mode := range []colorMode{colorLight, colorDark, colorNone} {
		applyColorMode(mode, false)
		for _, tc := range []struct {
			name           string
			phase          runner.Phase
			outcome, label string
			dim            bool
			outcomeRole    lipgloss.Style
		}{
			{"succeeded", runner.Settled, "succeeded", "succeeded", false, closedStyle},
			{"failed", runner.Settled, "failed", "failed  ", false, failStyle},
			{"parked", runner.Held, "succeeded", "parked  ", false, attentionStyle},
			{"running", runner.Running, "", "running ", false, openStyle},
			{"needs-human", runner.HandedOff, "", "needs-human", false, attentionStyle},
		} {
			t.Run(fmt.Sprint(mode)+"/"+tc.name, func(t *testing.T) {
				m := newModel(Options{Root: t.TempDir(), Now: func() time.Time { return runNow }})
				m.data.Runs = []runner.Run{
					{UID: "example-run", Job: kata.Job{Name: "Example job"}, Phase: tc.phase, Outcome: tc.outcome},
					{UID: "other-run", Job: kata.Job{Name: "Other job"}, Phase: runner.Running},
				}
				m.width = 120
				m.selectTab(1)
				m.cursor = 1 // Observe the semantic colors without the selection override.
				text := m.renderRuns()[1].Text
				role := detailMetaStyle
				if tc.dim {
					role = subtleStyle
				}
				// The issue and status cells precede the job, as in Kata.
				if !strings.Contains(text, strings.TrimSuffix(role.Render("Example job"), "\x1b[m")) {
					t.Errorf("wrong job color: %q", text)
				}
				if !strings.Contains(text, strings.TrimSuffix(tc.outcomeRole.Render(strings.TrimSpace(tc.label)), "\x1b[m")) {
					t.Errorf("outcome lost its color: %q", text)
				}
			})
		}
	}
}

// No run outcome dims its row on any run surface, matching Kata's rows.
func TestRunRowsNeverDimByOutcome(t *testing.T) {
	preserveTheme(t)
	applyColorMode(colorDark, false)
	// Mark the dim role so inherited row backgrounds cannot hide its use.
	subtleStyle = subtleStyle.Foreground(lipgloss.Color("123"))
	dimStyle = dimStyle.Foreground(lipgloss.Color("123"))
	for _, tc := range []struct {
		name    string
		phase   runner.Phase
		outcome string
		dim     bool
	}{
		{"done", runner.Settled, "succeeded", false},
		{"running", runner.Running, "", false},
		{"resumed running", runner.Running, "succeeded", false},
		{"queued prepared", runner.Prepared, "", false},
		{"queued admitted", runner.Admitted, "", false},
		{"queued stale outcome", runner.Prepared, "succeeded", false},
		{"failed", runner.Settled, "failed", false},
		{"parked", runner.Held, "succeeded", false},
		{"handoff", runner.HandedOff, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := jobDetailFixture(t, `{"enabled":true,"action":{"kind":"execute","prompt":"Example"},"options":{"agent":"worker"}}`)
			m.now = func() time.Time { return runNow }
			r := runner.Run{UID: "example-run", Target: runner.Target{Identity: kata.Identity{ProjectUID: "p"}}, Job: kata.Job{UID: "job", Name: "Example job"}, Workflow: &kata.Workflow{UID: "flow"}, Phase: tc.phase, Outcome: tc.outcome, Occurrence: runNow.Add(time.Hour).Format(time.RFC3339), Steps: []runner.StepState{{Note: "Example progress"}}}
			other := r
			other.UID = "other-run"
			if tc.phase == runner.Running {
				started := runNow.Add(-time.Minute)
				r.StartedAt = &started
			}
			other.Occurrence = runNow.Add(-time.Hour).Format(time.RFC3339)
			m.data.Runs = []runner.Run{r, other}
			m.rebuildJobIndex()
			m.rebuild()
			m.detailKey = detailItemKey("run", "p", "other-run")
			check := func(screen, text string) {
				t.Helper()
				if !strings.Contains(text, "Example progress") {
					t.Fatalf("%s missing run content: %q", screen, text)
				}
				// Placeholders stay subtle; the run's own text must not.
				if dim := dimAt(text, "Example progress", "\x1b[38;5;123m"); dim != tc.dim {
					t.Errorf("%s dim = %v for %s: %q", screen, dim, tc.name, text)
				}
			}
			rowText := func(body bodyLines) string {
				t.Helper()
				for _, l := range body {
					if l.Hit != nil && (l.Hit.Key == detailItemKey("run", "p", "example-run") || l.Hit.Key == detailItemKey("run", "project-a", "example-run")) && l.Hit.Kind == "run" {
						return l.Text
					}
				}
				t.Fatal("run row missing")
				return ""
			}
			check("job detail", rowText(m.buildJobDetail().Body()))
			for _, v := range m.jobs.byKey {
				text := bodyTextForTest(m.renderJobInspector(v, 120))
				pos := strings.Index(text, "Last run")
				if pos < 0 {
					t.Fatal("last run missing")
				}
				check("last run inspector", text[pos:])
			}
			m.selectTab(1)
			m.cursor = 1
			check("runs", m.renderRuns()[1].Text)
			d := workflowSnapshot()
			r.Target.Identity.ProjectUID = "project-a"
			other.Target.Identity.ProjectUID = "project-a"
			d.Runs = []runner.Run{r, other}
			wm := openWorkflow(t, d)
			wm.detailKey = detailItemKey("run", "project-a", "other-run")
			check("workflow detail", rowText(wm.buildDetail().Body()))
			text := bodyTextForTest(wm.workflowInspector(wm.wfIndex[workflowKey{"project-a", "flow"}], 120))
			pos := strings.Index(text, "Last run")
			if pos < 0 {
				t.Fatal("workflow last run missing")
			}
			check("workflow last run inspector", text[pos:])
		})
	}
}
