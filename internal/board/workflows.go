package board

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/salmonumbrella/herdr-kata/internal/workflow"
)

// The WORKFLOWS tab: what there is to call, and what happened last time it was
// called.
//
// A workflow reached the board only as whatever its last run happened to be — one
// row in RUNS, named after a job that may not exist. So the board could show a
// parked workflow and do nothing about it: resuming meant leaving the board and
// typing a run id nobody has memorised. This tab is the one place a workflow is a
// workflow, which is what makes `enter` and `u` possible at all.
//
// It reads files rather than rows. That is the same decision the workflow package
// made and it costs a directory listing per tick, which is the price of a workflow
// somebody just wrote appearing without the board being reopened.

// workflowRow is one line of the tab: a workflow that parsed, or the file that did not.
//
// A broken workflow is a row rather than a footnote because it is invisible
// everywhere else — it simply does not appear, which reads as "I never wrote
// that one" — and it is the workflow most in need of being opened.
type workflowRow struct {
	workflow workflow.Workflow
	// err is why the file would not parse, set only on a broken row.
	err error
}

// readWorkflows lists the workflows on disk.
//
// Called from the load command's goroutine, alongside the store reads, because
// it touches the filesystem on every tick.
func (m *Model) readWorkflows() ([]workflow.Workflow, []error) {
	dir := strings.TrimSpace(m.deps.WorkflowDir)
	if dir == "" {
		// Reading "" would be reported by the filesystem as a directory that
		// does not exist, which workflow.List treats as an installation with no
		// workflows — so a board wired without a workflow directory would say "no workflows
		// yet" to somebody who has ten.
		return nil, []error{errors.New("this board was given no workflow directory to read")}
	}
	return workflow.List(dir)
}

// workflowRows is every workflow the tab has to show, broken files first.
//
// They lead for the same reason `workflow list` names them before it lists
// anything: a workflow that will not parse is the one thing on this tab that is
// currently wrong, and a reader who has to page to find it will not.
func (m *Model) workflowRows() []workflowRow {
	rows := make([]workflowRow, 0, len(m.workflowErrs)+len(m.workflows))
	for _, err := range m.workflowErrs {
		rows = append(rows, workflowRow{err: err})
	}
	for _, f := range m.workflows {
		rows = append(rows, workflowRow{workflow: f})
	}
	return rows
}

// visibleWorkflows is the tab after the active search.
func (m *Model) visibleWorkflows() []workflowRow {
	rows := m.workflowRows()
	if m.query == "" {
		return rows
	}
	var out []workflowRow
	for _, r := range rows {
		if matchesWorkflow(r, m.query) {
			out = append(out, r)
		}
	}
	return out
}

// matchesWorkflow reports whether a row matches the query.
//
// A broken row is matched on its error, which is the only text it has — and
// which contains the path, so searching for a filename still finds the file
// that will not load.
func matchesWorkflow(r workflowRow, query string) bool {
	if query == "" {
		return true
	}
	q := strings.ToLower(query)
	haystack := []string{strings.ToLower(r.workflow.ID),
		strings.ToLower(r.workflow.About), strings.ToLower(r.workflow.Input)}
	if r.err != nil {
		haystack = []string{strings.ToLower(r.err.Error())}
	}
	for _, h := range haystack {
		if strings.Contains(h, q) {
			return true
		}
	}
	return false
}

// selectedWorkflow resolves the cursor to a row of this tab, and only of this tab:
// every other list's keys act on a job or a run, and answering them from here
// would act on a row the reader is not looking at.
func (m *Model) selectedWorkflow() (workflowRow, bool) {
	if m.focus != focusWorkflows {
		return workflowRow{}, false
	}
	rows := m.visibleWorkflows()
	if m.cursor < 0 || m.cursor >= len(rows) {
		return workflowRow{}, false
	}
	return rows[m.cursor], true
}

// workflowColumns is the tab's shape: what it is, what it costs, what it needs, and
// how it went.
func (m *Model) workflowColumns() []column {
	return []column{
		{title: "WORKFLOW", width: 18},
		{title: "STEPS", width: 5},
		{title: "INPUT", flex: true, width: 24, max: 46},
		{title: "LAST RUN", width: 9},
		{title: "STATE", width: 8},
	}
}

// blankCell is what a column with nothing true to say shows. An empty cell is
// indistinguishable from a column that failed to render.
const blankCell = "—"

func (m *Model) renderWorkflows(start, end int) string {
	var b strings.Builder
	rows := m.visibleWorkflows()
	if len(rows) == 0 {
		if m.query != "" {
			b.WriteString(dimStyle.Render("  no workflows match "+m.query) + "\n")
		} else {
			b.WriteString(dimStyle.Render("  no workflows yet — `herdr-kata workflow new <id>` writes one") + "\n")
		}
		return b.String()
	}
	cols := m.workflowColumns()
	widths := layout(cols, m.contentWidth())
	b.WriteString("  " + dimStyle.Render(row(titles(cols), widths)) + "\n")

	// How wide a row of this table actually is. Every other row is built from
	// the columns and lands on it exactly; the broken row below is free text and
	// has to be held to it, or the table's widest line is a parse error and the
	// inspector beside it gets pushed off the right of the pane.
	tableW := len(cols) - 1
	for _, w := range widths {
		tableW += w
	}

	line := 1
	for i := start; i < end; i++ {
		fr := rows[i]
		m.mark(line, hitWorkflow, i)
		line++
		cursor := "  "
		selected := m.focus == focusWorkflows && i == m.cursor
		if selected {
			cursor = selectedStyle.Render(cursorMark + " ")
		}
		if fr.err != nil {
			// A broken file has no columns to fill: it did not parse, so there
			// are no steps to count and no input to declare. What it has is the
			// path and the reason, which is what fixing it takes — cut to the
			// table's width here, and shown whole on the inspector beside it.
			b.WriteString(cursor + outcomeStyles["failed"].Render(
				truncate(workflowErrorLine(fr.err), tableW)) + "\n")
			continue
		}

		input := fr.workflow.Input
		if input == "" {
			input = blankCell
		}
		// "never" rather than a formatted zero time, which renders as 1970 and
		// reads as a bug in the store rather than as a workflow nobody has called.
		when, state := "never", blankCell
		if r, ok := m.lastWorkflow[fr.workflow.ID]; ok {
			when, state = ago(r.StartedAt), r.Outcome
		}
		cells := []string{fr.workflow.ID, itoa(len(fr.workflow.Steps)), input, when, state}

		// Fit first, then style: escape sequences must not be measured as
		// width, so colour is applied to the already-sized cell.
		sized := make([]string, len(cells))
		for k, c := range cells {
			sized[k] = fitMarquee(c, widths[k], m.frame)
		}
		if selected {
			// One bright style across the whole row, so it reads as a single
			// selected line rather than a row of differently coloured cells.
			for k := range sized {
				sized[k] = rowSelected.Render(sized[k])
			}
		} else {
			sized[1] = dimStyle.Render(sized[1])
			sized[2] = dimStyle.Render(sized[2])
			sized[3] = dimStyle.Render(sized[3])
			sized[4] = styleOutcome(sized[4])
		}
		b.WriteString(cursor + strings.Join(sized, " ") + "\n")
	}
	return b.String()
}

// workflowErrorLine puts a parse error onto exactly one line.
//
// A YAML error arrives as a heading with the offending line under it, and a
// table row that is secretly two rows breaks the arithmetic the pane is sized
// by: the view renders one line more than it budgeted for, the terminal
// scrolls, and the whole board smears upwards.
func workflowErrorLine(err error) string {
	path, message := workflowErrorParts(err)
	if path != "" {
		message = filepath.Base(path) + ": " + message
	}
	return "! " + strings.Join(strings.Fields(message), " ")
}

// launchSelectedWorkflow is what enter does on this tab.
func (m *Model) launchSelectedWorkflow() tea.Cmd {
	fr, ok := m.selectedWorkflow()
	if !ok {
		return nil
	}
	if fr.err != nil {
		// There is nothing to run: the file did not parse, so it has no steps.
		// Said out loud, because a key that silently does nothing reads as a
		// broken board rather than as a broken workflow.
		return func() tea.Msg { return actionMsg{err: fr.err} }
	}
	if fr.workflow.TakesInput() {
		// Asked for before the run exists, not after. Every prompt saying
		// {{input}} would otherwise get a hole where its subject should be, and
		// an agent handed that will invent something to fill it.
		m.openWorkflowInput(fr.workflow)
		return nil
	}
	return m.startWorkflow(fr.workflow.ID, "")
}

// startWorkflow launches one, off the event loop.
//
// A workflow is several agent turns in series and runs for minutes or hours.
// Called inline it would hold Update for all of it: the board would stop
// redrawing, stop ticking and stop answering keys, looking hung rather than
// busy. So it goes out as a command, the same way a job run does.
func (m *Model) startWorkflow(id, input string) tea.Cmd {
	if m.deps.RunWorkflow == nil {
		return func() tea.Msg {
			return actionMsg{status: "this board has no way to start a workflow"}
		}
	}
	key := workflowRunKey(id)
	if m.running[key] {
		return func() tea.Msg { return actionMsg{status: "workflow " + id + " is already running"} }
	}
	m.running[key] = true
	run := m.deps.RunWorkflow
	return func() tea.Msg { return workflowDoneMsg{workflowID: id, err: run(id, input)} }
}

// unparkSelectedWorkflow resumes the selected workflow's latest run.
func (m *Model) unparkSelectedWorkflow() tea.Cmd {
	fr, ok := m.selectedWorkflow()
	if !ok || fr.err != nil {
		return nil
	}
	rec, ok := m.lastWorkflow[fr.workflow.ID]
	if !ok {
		return func() tea.Msg {
			return actionMsg{status: "workflow " + fr.workflow.ID + " has never run, so there is nothing to resume"}
		}
	}
	if rec.Outcome != outcomeParked {
		// Only a parked run is resumable. Reaching this key on a finished one
		// would re-run steps that already cost money, under a keystroke that
		// promised to pick one up where it stopped.
		return func() tea.Msg {
			return actionMsg{status: "workflow " + fr.workflow.ID + "'s last run is " +
				rec.Outcome + ", not parked"}
		}
	}
	if m.deps.ResumeWorkflow == nil {
		return func() tea.Msg {
			return actionMsg{status: "this board has no way to resume a workflow"}
		}
	}
	key := workflowRunKey(fr.workflow.ID)
	if m.running[key] {
		return func() tea.Msg {
			return actionMsg{status: "workflow " + fr.workflow.ID + " is already running"}
		}
	}
	m.running[key] = true
	resume, id, runID := m.deps.ResumeWorkflow, fr.workflow.ID, rec.ID
	return func() tea.Msg { return workflowDoneMsg{workflowID: id, err: resume(runID)} }
}

// outcomeParked is the one run outcome this tab acts on.
const outcomeParked = "parked"

// workflowRunKey namespaces a workflow inside the running set, which is otherwise keyed
// by job id. A job that starts a workflow carries the workflow's id, and without the
// prefix a running job would refuse to let its own workflow be called by hand — a
// refusal with nothing on screen to explain it.
func workflowRunKey(id string) string { return "workflow:" + id }

// workflowDoneMsg reports a launch or a resume back to the event loop.
type workflowDoneMsg struct {
	workflowID string
	err        error
}

// workflowPrompt is the open "what is this workflow called with" box.
type workflowPrompt struct {
	workflowID string
	// about is the workflow's own sentence about what belongs here. It is shown
	// above the box because the workflow file is the only thing that knows, and
	// "input" on its own tells the reader nothing about what to type.
	about string
	input textinput.Model
	// err is the last refusal, kept on screen rather than swallowed.
	err string
}

// openWorkflowInput asks for the input before the run starts.
func (m *Model) openWorkflowInput(f workflow.Workflow) {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 0
	in.Placeholder = f.Input
	in.Focus()
	m.workflowInput = &workflowPrompt{workflowID: f.ID, about: f.Input, input: in}
}

func (m *Model) handleWorkflowInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.workflowInput = nil
		return m, nil
	case "enter":
		return m, m.submitWorkflowInput()
	}
	var cmd tea.Cmd
	m.workflowInput.input, cmd = m.workflowInput.input.Update(msg)
	return m, cmd
}

// submitWorkflowInput starts the workflow with what was typed, or refuses to.
func (m *Model) submitWorkflowInput() tea.Cmd {
	p := m.workflowInput
	input := strings.TrimSpace(p.input.Value())
	if input == "" {
		// Refused rather than started blank. Closing the box on an empty value
		// would look identical to launching, and the workflow would run with a hole
		// where its subject should be — which is worse than not running.
		p.err = "workflow " + p.workflowID + " needs an input: " + p.about
		return nil
	}
	m.workflowInput = nil
	return m.startWorkflow(p.workflowID, input)
}

// workflowInputWidth is how wide the box may be drawn: inside the pane, and no
// wider than a line of prose is worth reading back.
func (m *Model) workflowInputWidth() int {
	return min(m.contentWidth()-4, 60)
}

// renderWorkflowInput draws the box under the table it was opened from.
func (m *Model) renderWorkflowInput() string {
	p := m.workflowInput
	p.input.Width = m.workflowInputWidth()
	var b strings.Builder
	b.WriteString("\n" + headerStyle.Render("run workflow "+p.workflowID) + "\n")
	b.WriteString("  " + dimStyle.Render(truncate(p.about, m.contentWidth()-2)) + "\n")
	b.WriteString("  " + p.input.View() + "\n")
	if p.err != "" {
		b.WriteString("  " + outcomeStyles["failed"].Render(
			truncate(p.err, m.contentWidth()-2)) + "\n")
	}
	b.WriteString("  " + dimStyle.Render("enter run · esc cancel") + "\n")
	return b.String()
}
