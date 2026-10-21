package tui

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/jobform"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

func (m *model) runsColumns() []column {
	return executionColumns("detail", "detail", "started", false)
}

// RunsPage builds the visible page's cells once, for both sizing and drawing.
func (m *model) runsPage() [][]cell {
	start, end, _, _ := pager(len(m.rows), m.cursor, m.pageRows())
	rows := make([][]cell, 0, end-start)
	for _, r := range m.rows[start:end] {
		rows = append(rows, m.runCells(r))
	}
	return rows
}

func (m *model) runsWidths() []int { return m.runsWidthsFor(m.runsPage()) }

func (m *model) runsWidthsFor(rows [][]cell) []int {
	return listWidths(m.runsColumns(), rows, m.listWidth())
}

func (m *model) runCells(r row) []cell {
	rr := m.data.Runs[r.run]
	v := m.runViews[r.identity()]
	if v.Run.UID != rr.UID {
		v = runView(rr)
		v.Issue = m.runIssueRef(rr)
	}
	label, role := runLabel(rr)
	detail, detailRole := runDetailCell(rr, m.now())
	started := "—"
	if !v.When.IsZero() {
		started = ago(m.now(), v.When)
	}
	project := m.projectName(rr.Target.Identity.ProjectUID)
	text := detailMetaStyle
	issue := m.listIssueRef(project, v.Issue)
	cells := []cell{{Text: issue, Style: text}, {Text: label, Style: role}, {Text: m.listName(project, v.Job, issue), Style: text}, {Text: runModel(rr), Style: text}, {Text: detail, Style: detailRole}, {Text: started, Style: text}}
	return dimPlaceholders(cells)
}

func (m *model) renderRuns() bodyLines {
	page := m.runsPage()
	widths := m.runsWidthsFor(page)
	body := bodyLines{headerRow(m.runsColumns(), widths)}
	start, end, _, _ := pager(len(m.rows), m.cursor, m.pageRows())
	for i := start; i < end; i++ {
		r := m.rows[i]
		line := parentRow(page[i-start], widths, i == m.cursor, m.frame, i, runExpandable(m.data.Runs[r.run]), m.expanded[r.uid])
		line.Hit = &rowHit{Kind: "run", Key: r.identity(), Index: i, X0: 0, X1: m.listWidth()}
		body = append(body, line)
		if m.expanded[r.uid] {
			body = append(body, renderStepLines(m.data.Runs[r.run], m.listWidth()-1, m.now(), m.frame, true)...)
		}
	}
	if len(m.rows) == 0 {
		body = append(body, bodyLine{Text: subtleStyle.Render("  none")})
	}
	return body
}

func (m *model) runsMatchCount() string {
	if m.query != "" {
		return fmt.Sprintf("%d of %d match", len(m.rows), len(m.data.Runs))
	}
	return ""
}

func runNote(r runner.Run, now time.Time) (string, lipgloss.Style) {
	if text, role := reportState(r); text != "" {
		if text == "reporting…" {
			role = detailMetaStyle
		}
		return text, role
	}
	return runDetailCell(r, now)
}

func runDetailCell(r runner.Run, now time.Time) (string, lipgloss.Style) {
	rep, role := reportState(r)
	if rep != "" && rep != "reporting…" {
		return rep, role
	}
	if (r.Phase == runner.Held || r.Phase == runner.Blocked || r.Phase == runner.HandedOff) && r.Reason != "" {
		wait := "waiting: " + clean(r.Reason)
		if s := activeStep(r); s != nil && s.Limit != nil && s.Limit.ResetsAt != nil {
			wait += " · resets in " + in(s.Limit.ResetsAt.Sub(now))
		}
		return wait, detailMetaStyle
	}
	parts := []string{}
	if runNeedsAgent(r) && (r.WFDef != nil || r.Phase == runner.Running && r.ActiveStep != "") {
		progress := stepProgress(r.Steps)
		if edge, _ := defs.WorkflowLoop(r.WFDef); edge != nil {
			progress += fmt.Sprintf(" · loop %d/%d", r.Loop.Used, edge.MaxLoops)
		}
		parts = append(parts, progress)
	}
	if len(parts) > 0 {
		return strings.Join(parts, " · "), detailMetaStyle
	}
	if rep != "" {
		return rep, detailMetaStyle
	}
	for _, s := range slices.Backward(r.Steps) {

		if strings.TrimSpace(s.Note) != "" {
			return clean(s.Note), detailMetaStyle
		}
		if s.Result != nil && strings.TrimSpace(s.Result.Summary) != "" {
			return clean(s.Result.Summary), detailMetaStyle
		}
	}
	return "", detailMetaStyle
}

func renderStepLines(r runner.Run, width int, now time.Time, frame int, animate bool) bodyLines {
	var lines bodyLines
	render := func(s runner.StepState, archived bool) {
		d := stepDisplay(r, s)
		indent := 6
		if archived {
			d = attemptDisplay(s)
			indent = 12
		}
		outcome := d.Outcome
		if s.Attempt > 1 {
			outcome += fmt.Sprintf(" #%d", s.Attempt)
		}
		kind := s.Kind
		switch kind {
		case "prompt":
			kind = "agent"
		case "command":
			kind = "cmd"
		}
		line := bodyLine{Text: strings.Repeat(" ", indent) + d.Role.Render(d.Mark)}
		if animate {
			values := []string{s.Key, kind, outcome, stepDuration(r, s, archived, now), s.Note}
			widths := []int{16, 5, 16, 8}
			x := ansi.StringWidth(line.Text)
			for i, value := range values {
				x++ // One space separates adjacent step cells.
				cellWidth := max(0, width-x)
				if i < len(widths) {
					cellWidth = widths[i]
				}
				line.Text += subtleStyle.Render(" " + marquee(value, cellWidth, frame))
				if ansi.StringWidth(plain(value)) > cellWidth {
					if span, ok := clipSpan(animationSpan{x, x + cellWidth}, width); ok {
						line.Animations = append(line.Animations, span)
					}
				}
				x += cellWidth
			}
		} else {
			tail := " " + fitCell(s.Key, 16) + " " + fitCell(kind, 5) + " " + pad(outcome, 16) + " " + pad(stepDuration(r, s, archived, now), 8) + " " + clean(s.Note)
			line.Text += subtleStyle.Render(tail)
		}
		line.Text = ansi.Truncate(line.Text, max(0, width), "")
		lines = append(lines, line)
	}
	for _, s := range r.Steps {
		render(s, false)
		var past []runner.StepState
		for _, a := range r.PastAttempts {
			if a.Key == s.Key {
				past = append(past, a)
			}
		}
		sort.SliceStable(past, func(i, j int) bool { return past[i].Attempt < past[j].Attempt })
		for _, a := range past {
			render(a, true)
		}
	}
	return lines
}

func runNeedsAgent(r runner.Run) bool {
	if r.JobDef.Action.Kind == "notify" {
		return false
	}
	if r.WFDef != nil && len(r.WFDef.Steps) > 0 {
		for _, step := range r.WFDef.Steps {
			if step.Kind != "command" {
				return true
			}
		}
		return false
	}
	return true
}
func runModel(r runner.Run) string {
	if !runNeedsAgent(r) {
		return "—"
	}
	parts := []string{profileModel(jobform.ProfileFacts(r.Profile))}
	for i, s := range r.Steps {
		if name, p := runner.StepProfile(&r, i); s.Kind == "prompt" && name != r.ProfileName {
			parts = append(parts, s.Key+" "+profileModel(jobform.ProfileFacts(p)))
		}
	}
	return strings.Join(parts, " · ")
}
