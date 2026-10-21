package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

func (m *model) View() tea.View {
	text := m.viewContent()
	if !m.kataActive {
		text = m.boardText(text)
	}
	v := tea.NewView(text)
	v.AltScreen = true
	if !m.mouseOff {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}
func (m *model) viewContent() string { return m.renderFrame().Text }
func (m *model) healthProblem() string {
	switch {
	case !m.data.Running:
		return "runtime stopped"
	case !m.data.WorkerRunning:
		return "worker stopped"
	case m.snapshotErr != nil:
		return "snapshot error"
	case m.data.Route["access_lost"] == true || m.data.Route["allowed"] == false:
		return "route refused"
	case m.signingProblem() != "":
		return "signing blocked"
	}
	return ""
}
func (m *model) signingProblem() string {
	names := map[string]bool{}
	for _, r := range m.data.Runs {
		for _, op := range r.Ops {
			if op.SigningRefused {
				name := m.projectName(r.Target.Identity.ProjectUID)
				if name == "" {
					name, _ = m.data.Route["project"].(string)
				}
				if name == "" {
					name = "<name>"
				}
				names[name] = true
			}
		}
	}
	if m.data.Route["signing_blocked"] == true {
		project, _ := m.data.Route["project"].(string)
		if project == "" {
			project, _ = m.data.Health["project_uid"].(string)
		}
		if project == "" {
			project = "<name>"
		}
		names[project] = true
	}
	if len(names) == 0 {
		return ""
	}
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	commands := []string{}
	for _, name := range sorted {
		commands = append(commands, "kata federation root-key repin --project "+name)
	}
	return "signing_blocked: root owner: " + strings.Join(commands, "; ")
}
func (m *model) footer() bodyLines {
	conditions := []string{}
	if m.snapshotErr != nil {
		conditions = append(conditions, m.snapshotErr.Error())
	}
	if m.data.Route["access_lost"] == true || m.data.Route["allowed"] == false {
		conditions = append(conditions, fmt.Sprint(m.data.Route["error"]))
	}
	if s := m.signingProblem(); s != "" {
		conditions = append(conditions, s)
	}
	wrapped := []string{}
	for _, s := range conditions {
		wrapped = append(wrapped, wrapText(s, max(1, m.width-7))...)
	}
	return footer(m.notice, wrapped, searchState{Query: m.query, Editing: m.searching})
}
func (m *model) pageRows() int {
	help := jobsHelp()
	if m.tab == 1 {
		help = runsHelp()
	} else if m.tab == 2 {
		help = workflowHelp()
	}
	borders := 0
	if _, split := m.splitGeometry(); split {
		borders = 2
	}
	return max(3, m.height-5-len(help.Lines(m.width))-len(m.footer())-m.jobBoxHeight()-borders)
}
func (m *model) pageSize() int {
	if m.height > 4 {
		return m.height - 3
	}
	return 1
}
func countRuns(runs []runner.Run) tabCount {
	c := tabCount{}
	for _, r := range runs {
		switch r.Phase {
		case runner.Running, runner.Admitted:
			c.Running++
		case runner.Held, runner.Blocked:
			c.Held++
		}
	}
	return c
}
func (m *model) tabCounts() []tabCount {
	out := []tabCount{countRuns(m.data.Runs)}
	if m.showProjects() {
		out = nil
		lanes := map[string][]runner.Run{}
		for _, r := range m.data.Runs {
			name := m.projectName(r.Target.Identity.ProjectUID)
			lanes[name] = append(lanes[name], r)
		}
		names := make([]string, 0, len(lanes))
		for name := range lanes {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			c := countRuns(lanes[name])
			c.Project = name
			out = append(out, c)
		}
	}
	return out
}
func (m *model) renderFrame() renderedFrame {
	if m.frameValid {
		return m.rendered
	}
	if m.kataActive {
		m.rendered = m.renderKata()
		m.hits = m.rendered.Hits
		m.frameValid = true
		return m.rendered
	}
	if m.width <= 0 {
		m.width = 80
	}
	if m.height <= 0 {
		m.height = 24
	}
	if m.editor != nil {
		text := m.editor.View()
		body := block(text)
		for i := range body {
			body[i].Text = ansi.Truncate(body[i].Text, m.width, "")
		}
		if len(body) > m.height {
			body = body[:m.height]
		}
		lines := []string{}
		for _, l := range body {
			lines = append(lines, l.Text)
		}
		m.rendered = renderedFrame{Text: strings.Join(lines, "\n"), Hits: hits{Width: m.width}}
		m.hits = m.rendered.Hits
		m.frameValid = true
		return m.rendered
	}
	var top, body, bottom bodyLines
	follow := m.follow
	scroll := &m.scroll
	if m.currentView() == viewRun && !m.artifactLoaded {
		// A re-entry scan temporarily shortens the body. Clamp only the displayed
		// loading frame; keep the saved offset until the artifacts are ready.
		pendingScroll := m.scroll
		scroll = &pendingScroll
	}
	var tabs []tabHit
	tabY := -1
	help := footerHelp{Full: []helpItem{hintGroup("j/k", "move", 3, keys.Down, keys.Up, keys.ScrollUp, keys.ScrollDown), hintGroup("l", "detail", 3, keys.Open), hintGroup("h", "back", 2, keys.Back), keys.Search.hint(2), hintGroup("[ ]", "page", 1, keys.PrevPage, keys.NextPage, keys.PrevPageAlt, keys.NextPageAlt), hintGroup("K", "Kata", 2, keys.Kata), keys.Mouse.hint(5), hintGroup("q", "quit", 5, keys.Quit, keys.Interrupt)}, Short: []helpItem{hintGroup("j/k", "move", 2, keys.Down, keys.Up, keys.ScrollUp, keys.ScrollDown), hintGroup("l", "detail", 2, keys.Open), hintGroup("K", "Kata", 1, keys.Kata), keys.Mouse.hint(5), hintGroup("q", "quit", 5, keys.Quit, keys.Interrupt)}}
	modal := m.editor != nil || m.openingEditor || len(m.projectChoices) > 0
	if modal {
		scroll = &m.modalScroll
		follow = false
		switch {
		case m.editor != nil:
			body = block(m.editor.View())
		case m.openingEditor:
			body = block("Opening job editor · Esc cancels")
		case len(m.projectChoices) > 0:
			labels, active := m.folderTabs()
			tabText, tabHits := renderTabs(labels, active, m.width, m.width, m.tabInfo())
			top = block(tabText)
			tabs = tabHits
			tabY = 1
			body = m.renderProjectPicker()
		}
	} else if m.currentView() != viewList {
		if m.currentView() == viewWorkflow {
			body = m.workflowDetail()
			help = workflowDetailHelp()
		} else {
			body = m.renderDetail()
		}
		if m.currentView() == viewRun {
			help = m.runDetailHelp()
		} else if m.currentView() == viewJob {
			help = m.jobDetailHelp()
		}
	} else {
		labels, active := m.folderTabs()
		tabText, tabHits := renderTabs(labels, active, m.width, m.width, m.tabInfo())
		top = block(tabText)
		tabs = tabHits
		tabY = 1
		if m.tab == 0 {
			body = m.renderJobs()
			help = jobsHelp()
		} else if m.tab == 1 {
			body = m.renderRuns()
			help = runsHelp()
		} else if m.tab == 2 {
			body = m.renderWorkflows()
			help = workflowHelp()
		}
		extras := []string{}
		if m.tab == 0 {
			extras = m.jobsExtras()
		}
		if m.tab == 1 {
			extras = append(extras, m.runsMatchCount())
		}
		if m.tab == 2 && m.query != "" {
			extras = append(extras, fmt.Sprintf("%d of %d match", len(m.rows), len(m.wfIndex)))
		}
		_, _, page, pages := pager(len(m.rows), m.cursor, m.pageRows())
		bottom = append(bottom, bodyLine{Text: pageLabel(page, pages, extras...)})
	}
	bottom = append(bottom, m.renderJobBox()...)
	bottom = append(bottom, m.footer()...)
	if len(m.projectChoices) == 0 && m.conflict == nil && len(m.confirm) == 0 {
		for _, line := range help.Lines(m.width) {
			bottom = append(bottom, bodyLine{Text: line})
		}
	}
	if len(m.projectChoices) > 0 {
		focus := m.projectCursor + 2
		if m.projectPurpose == "to open Kata" {
			focus = m.projectCursor + 1
		}
		body, _ = window(body, max(1, m.height-len(top)-len(bottom)), &m.modalScroll, true, focusLine(focus))
		scroll = nil
	}
	listScroll := m.scroll
	if m.jobBoxHeight() > 0 {
		scroll = &listScroll
	}
	if _, split := m.splitGeometry(); split && !modal {
		body = m.renderSplitBody(body, len(top), len(bottom), scroll, follow)
		scroll = nil
		follow = false
	}
	frame := (pane{Top: top, Body: body, Bottom: bottom, Width: m.width, Scroll: scroll, Follow: follow}).Render(m.height)
	frame.Hits.SetTabs(tabY, tabs, m.height)
	m.rendered = frame
	m.hits = frame.Hits
	m.frameValid = true
	return frame
}

func (m *model) tabInfo() tabInfoData {
	return tabInfoData{Counts: m.tabCounts(), Problem: m.healthProblem()}
}

func renderTabs(labels []string, active, tableWidth, width int, info ...tabInfoData) (string, []tabHit) {
	if len(labels) == 0 {
		return "", nil
	}
	active = max(0, min(active, len(labels)-1))
	start, end := active, active+1
	size := func(a, b int) int {
		n := 1
		for _, label := range labels[a:b] {
			n += ansi.StringWidth(plain(label)) + 3
		}
		return n
	}
	tabLimit := width
	status := tabInfoData{}
	if len(info) > 0 {
		status = info[0]
	}
	if text := status.minimum(); text != "" {
		minimum := size(active, active+1)
		reserved := min(ansi.StringWidth(text), max(0, width-minimum-1))
		tabLimit = max(minimum, width-reserved-1)
	}
	for {
		changed := false
		if start > 0 && size(start-1, end) <= tabLimit {
			start--
			changed = true
		}
		if end < len(labels) && size(start, end+1) <= tabLimit {
			end++
			changed = true
		}
		if !changed {
			break
		}
	}
	var top, mid, bot strings.Builder
	hits := []tabHit{}
	x := 1
	for i := start; i < end; i++ {
		label := plain(labels[i])
		w := ansi.StringWidth(label) + 2
		rule := strings.Repeat("─", w)
		if i == start {
			top.WriteString("╭")
			mid.WriteString(subtleStyle.Render("│"))
			edge := "┴"
			if i == active {
				edge = "┘"
			}
			bot.WriteString(edge)
		}
		top.WriteString(rule)
		style := tabInactive
		if i == active {
			style = tabActive
		}
		mid.WriteString(style.Render(" " + label + " "))
		hits = append(hits, tabHit{Index: i, X0: x, X1: x + w})
		x += w + 1
		if i == active {
			bot.WriteString(strings.Repeat(" ", w))
		} else {
			bot.WriteString(rule)
		}
		if i == end-1 {
			top.WriteString("╮")
			mid.WriteString(subtleStyle.Render("│"))
			edge := "┴"
			if i == active {
				edge = "└"
			}
			bot.WriteString(edge)
		} else {
			top.WriteString("┬")
			mid.WriteString(subtleStyle.Render("│"))
			edge := "┴"
			if i == active {
				edge = "└"
			} else if i+1 == active {
				edge = "┘"
			}
			bot.WriteString(edge)
		}
	}
	if text := tabStatus(status, max(0, width-ansi.StringWidth(mid.String())-1)); text != "" {
		mid.WriteString(strings.Repeat(" ", max(1, width-ansi.StringWidth(mid.String())-ansi.StringWidth(text))))
		mid.WriteString(text)
	}
	bottom := bot.String() + strings.Repeat("─", max(0, tableWidth-ansi.StringWidth(bot.String())))
	return ansi.Truncate(subtleStyle.Render(top.String()), width, "") + "\n" + ansi.Truncate(mid.String(), width, "") + "\n" + ansi.Truncate(subtleStyle.Render(bottom), width, ""), hits
}

type tabCount struct {
	Project       string
	Running, Held int
}

type tabInfoData struct {
	Hint    string
	Counts  []tabCount
	Problem string
}

// tabStatus prefers health, then totals, then the per-project breakdown.
func tabStatus(info tabInfoData, width int) string {
	if width <= 0 {
		return ""
	}
	withHint := func(text string) string {
		if info.Hint == "" {
			return text
		}
		if text == "" {
			return subtleStyle.Render(truncate(info.Hint, width))
		}
		combined := subtleStyle.Render(info.Hint+" · ") + text
		if ansi.StringWidth(combined) <= width {
			return combined
		}
		return text
	}
	problem := ""
	if info.Problem != "" {
		problem = attentionStyle.Render(plain(info.Problem))
	}
	join := func(parts []string) string {
		if problem != "" {
			parts = append(parts, problem)
		}
		return strings.Join(parts, subtleStyle.Render(" · "))
	}

	for stage := range 3 {
		parts := []string{}
		for _, c := range info.Counts {
			if c.Running == 0 && c.Held == 0 {
				continue
			}
			label := plain(c.Project)
			if stage > 0 {
				label = truncate(label, 12)
			}
			if label != "" {
				label = subtleStyle.Render(label + ": ")
			}
			parts = append(parts, label+countSummary(c, stage == 2))
		}
		if s := join(parts); ansi.StringWidth(s) <= width {
			return withHint(s)
		}
	}
	total := tabCount{}
	for _, c := range info.Counts {
		total.Running += c.Running
		total.Held += c.Held
	}
	for _, c := range []tabCount{total, {Held: total.Held}, {Running: total.Running}} {
		s := countSummary(c, true)
		parts := []string{}
		if s != "" {
			parts = append(parts, s)
		}
		if s = join(parts); ansi.StringWidth(s) <= width {
			return withHint(s)
		}
	}
	return ansi.Truncate(problem, width, "")
}

func countSummary(c tabCount, compact bool) string {
	parts := []string{}
	if c.Running > 0 {
		text := fmt.Sprintf("%d running", c.Running)
		if compact {
			text = fmt.Sprintf("%dr", c.Running)
		}
		parts = append(parts, subtleStyle.Render(text))
	}
	if c.Held > 0 {
		text := fmt.Sprintf("%d parked", c.Held)
		if compact {
			text = fmt.Sprintf("%dp", c.Held)
		}
		parts = append(parts, attentionStyle.Render(text))
	}
	gap := " · "
	if compact {
		gap = "/"
	}
	return strings.Join(parts, subtleStyle.Render(gap))
}

func (info tabInfoData) minimum() string {
	if info.Problem != "" {
		return plain(info.Problem)
	}
	total := tabCount{}
	for _, c := range info.Counts {
		total.Running += c.Running
		total.Held += c.Held
	}
	return countSummary(total, true)
}

func pager(total, cursor, rows int) (start, end, page, pages int) {
	rows = max(1, rows)
	total = max(0, total)
	pages = max(1, (total+rows-1)/rows)
	cursor = max(0, min(cursor, max(0, total-1)))
	start = cursor / rows * rows
	end = min(total, start+rows)
	page = cursor/rows + 1
	return
}
func pageLabel(page, pages int, extras ...string) string {
	var s strings.Builder
	s.WriteString(fmt.Sprintf("  page %d/%d", max(1, page), max(1, pages)))
	for _, extra := range extras {
		if extra != "" {
			s.WriteString(" · " + plain(extra))
		}
	}
	return subtleStyle.Render(strings.TrimRight(s.String(), " "))
}
