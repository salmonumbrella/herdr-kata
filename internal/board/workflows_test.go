package board

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/salmonumbrella/herdr-kata/internal/herdrcli"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

// The WORKFLOWS tab, driven the way a person drives it: write workflow files, send
// keys, assert what the board did with them.
//
// Nothing here reaches the real thing. The state directory is a temporary one
// and the two workflow dependencies are fakes that only record — a test that used
// the real ones would launch agents on whichever machine ran it.

// The three workflows every test here works with: one that needs an input, one that
// does not, and one that does not parse.
const (
	workflowWithInput = `about: triage a ticket
input: the ticket to look at
steps:
  - id: assess
    agent: |
      Look at {{input}} and say whether it matters.
  - id: record
    run: echo "$HERDR_KATA_PREVIOUS"
`
	workflowWithoutInput = `about: sweep the workspace
steps:
  - id: sweep
    run: echo swept
`
	// `agnet` is the failure the workflow package refuses files for: a dropped key
	// is a step that never runs, in a workflow that reports success.
	workflowBroken = `about: this one does not parse
steps:
  - id: nope
    agnet: a typo where agent should be
`
)

// startedWorkflow is one call the board made to the command layer.
type startedWorkflow struct {
	id    string
	input string
}

// workflowBoard is a board over a private state directory with workflows on disk, plus
// the record of what it asked the command layer to do.
type workflowBoard struct {
	*Model
	started []startedWorkflow
	resumed []string
	dir     string
}

func newWorkflowBoard(t *testing.T, files map[string]string) *workflowBoard {
	t.Helper()
	// A state directory of this test's own. The real one holds a live store and
	// the workflows this machine actually runs.
	state := t.TempDir()
	t.Setenv("HERDR_KATA_HOME", state)

	dir := filepath.Join(state, "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s, err := store.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	fb := &workflowBoard{dir: dir}
	fb.Model = New(s, herdrcli.New(), Deps{
		Run: func(store.Job, string) error { return nil },
		RunWorkflow: func(id, input string) error {
			fb.started = append(fb.started, startedWorkflow{id: id, input: input})
			return nil
		},
		ResumeWorkflow: func(runID string) error {
			fb.resumed = append(fb.resumed, runID)
			return nil
		},
		WorkflowDir:   dir,
		DaemonRunning: func() bool { return true },
		EnsureDaemon:  func() error { return nil },
	})
	fb.width, fb.height = 160, 40
	fb.daemonUp = true
	fb.apply(t, fb.load()())
	fb.selectTab(focusWorkflows)
	return fb
}

// putRun records a run of a workflow, the way the command layer would.
func (fb *workflowBoard) putRun(t *testing.T, id, workflowID, outcome string, at time.Time) {
	t.Helper()
	err := fb.store.PutRun(context.Background(), store.Run{
		ID: id, JobID: workflowID, Workflow: workflowID, Trigger: "manual",
		Outcome: outcome, StartedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	fb.apply(t, fb.load()())
	fb.selectTab(focusWorkflows)
}

// selectWorkflow puts the cursor on a workflow by id, so a test never depends on where
// in the list it happened to land.
func (fb *workflowBoard) selectWorkflow(t *testing.T, id string) {
	t.Helper()
	for i, r := range fb.visibleWorkflows() {
		if r.err == nil && r.workflow.ID == id {
			fb.cursor = i
			return
		}
	}
	t.Fatalf("no workflow %q on the tab", id)
}

// A workflow that will not parse is invisible everywhere else, which reads as "I
// never wrote that one". It is the workflow most in need of being seen.
func TestTheWorkflowsTabListsWorkflowsIncludingTheBrokenOnes(t *testing.T) {
	fb := newWorkflowBoard(t, map[string]string{
		"triage.yml": workflowWithInput,
		"sweep.yml":  workflowWithoutInput,
		"broken.yml": workflowBroken,
	})

	if len(fb.workflows) != 2 {
		t.Fatalf("loaded %d workflows, want the 2 that parse", len(fb.workflows))
	}
	if len(fb.workflowErrs) != 1 {
		t.Fatalf("kept %d parse errors, want 1", len(fb.workflowErrs))
	}
	if got := len(fb.visibleWorkflows()); got != 3 {
		t.Fatalf("the tab shows %d rows, want all 3 workflow files", got)
	}

	out := fb.renderWorkflows(0, len(fb.visibleWorkflows()))
	for _, want := range []string{"triage", "sweep", "broken.yml"} {
		if !strings.Contains(out, want) {
			t.Errorf("the workflows table does not mention %q:\n%s", want, out)
		}
	}
	// The declared input belongs on screen: it is what the reader has to supply
	// and the only place it is written down is the file.
	if !strings.Contains(out, "the ticket to look at") {
		t.Error("a workflow's declared input is not shown, so nobody can tell what to type")
	}
}

// A workflow nobody has called must not read as one that ran at the epoch.
func TestAWorkflowThatHasNeverRunSaysSo(t *testing.T) {
	fb := newWorkflowBoard(t, map[string]string{"sweep.yml": workflowWithoutInput})
	out := fb.renderWorkflows(0, len(fb.visibleWorkflows()))
	if !strings.Contains(out, "never") {
		t.Errorf("an uncalled workflow should read as never:\n%s", out)
	}
	if strings.Contains(out, "1970") {
		t.Errorf("an uncalled workflow rendered a zero timestamp:\n%s", out)
	}
}

// The latest run of the workflow, found by workflow id rather than by job id: a workflow
// called directly has no job to be keyed under.
func TestTheTabShowsTheLatestRunOfEachWorkflow(t *testing.T) {
	fb := newWorkflowBoard(t, map[string]string{"sweep.yml": workflowWithoutInput})
	fb.putRun(t, "old", "sweep", "done", time.Now().Add(-2*time.Hour))
	fb.putRun(t, "new", "sweep", "parked", time.Now().Add(-time.Minute))

	if got := fb.lastWorkflow["sweep"].ID; got != "new" {
		t.Fatalf("the tab is showing run %q, want the most recent one", got)
	}
	if !strings.Contains(fb.renderWorkflows(0, len(fb.visibleWorkflows())), "parked") {
		t.Error("the workflow's state is not on its row")
	}
}

// A workflow that declares an input must not start with a blank one: every prompt
// saying {{input}} would get a hole where its subject should be.
func TestAWorkflowThatNeedsAnInputCannotBeLaunchedBlank(t *testing.T) {
	fb := newWorkflowBoard(t, map[string]string{"triage.yml": workflowWithInput})
	fb.selectWorkflow(t, "triage")

	fb.pressSpecial(t, tea.KeyEnter)
	if fb.workflowInput == nil {
		t.Fatal("enter on a workflow that takes an input should ask for it")
	}
	if len(fb.started) != 0 {
		t.Fatalf("the workflow was started before the input was given: %v", fb.started)
	}

	// Committing an empty box must refuse rather than close: closing silently
	// looks exactly like launching.
	fb.pressSpecial(t, tea.KeyEnter)
	if fb.workflowInput == nil {
		t.Fatal("an empty input closed the box instead of refusing")
	}
	if fb.workflowInput.err == "" {
		t.Error("the refusal says nothing, so nothing on screen explains it")
	}
	if len(fb.started) != 0 {
		t.Fatalf("a blank input started the workflow anyway: %v", fb.started)
	}

	fb.typeText(t, "  ticket 41  ")
	fb.apply(t, tea.KeyMsg{Type: tea.KeyEnter})
	if fb.workflowInput != nil {
		t.Error("a filled input should close the box")
	}
	if len(fb.started) != 1 {
		t.Fatalf("started %d workflows, want 1", len(fb.started))
	}
	if fb.started[0] != (startedWorkflow{id: "triage", input: "ticket 41"}) {
		t.Errorf("started %+v, want triage with the typed input, trimmed", fb.started[0])
	}
}

// A workflow that declares no input needs no box in the way.
func TestAWorkflowWithNoInputRunsStraightAway(t *testing.T) {
	fb := newWorkflowBoard(t, map[string]string{"sweep.yml": workflowWithoutInput})
	fb.selectWorkflow(t, "sweep")
	fb.apply(t, tea.KeyMsg{Type: tea.KeyEnter})

	if fb.workflowInput != nil {
		t.Fatal("a workflow that takes no input should not be asked for one")
	}
	if len(fb.started) != 1 || fb.started[0].id != "sweep" {
		t.Fatalf("started %+v, want one call for sweep", fb.started)
	}
}

// The box owns the keyboard while it is open, or a `q` typed into an input
// would quit the board mid-sentence.
func TestTheInputBoxOwnsTheKeyboard(t *testing.T) {
	fb := newWorkflowBoard(t, map[string]string{"triage.yml": workflowWithInput})
	fb.selectWorkflow(t, "triage")
	fb.pressSpecial(t, tea.KeyEnter)
	before := fb.cursor

	fb.typeText(t, "quit j k 3")
	if fb.workflowInput == nil {
		t.Fatal("typing closed the box")
	}
	if got := fb.workflowInput.input.Value(); got != "quit j k 3" {
		t.Errorf("the box holds %q, want the text that was typed", got)
	}
	if fb.cursor != before {
		t.Error("a key pressed in the box moved the list behind it")
	}
}

// There is nothing to run: the file did not parse, so it has no steps.
func TestABrokenWorkflowCannotBeLaunched(t *testing.T) {
	fb := newWorkflowBoard(t, map[string]string{"broken.yml": workflowBroken})
	fb.cursor = 0
	if r, ok := fb.selectedWorkflow(); !ok || r.err == nil {
		t.Fatal("the broken workflow should be the row under the cursor")
	}
	fb.apply(t, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fb.started) != 0 {
		t.Fatalf("a workflow that does not parse was launched: %v", fb.started)
	}
	if fb.err == nil {
		t.Error("enter on a broken workflow said nothing, which reads as a broken board")
	}
}

// Only a parked run can be resumed. Reaching this key on a finished one would
// re-run steps that already cost money.
func TestUnparkDoesNothingToARunThatIsNotParked(t *testing.T) {
	fb := newWorkflowBoard(t, map[string]string{"sweep.yml": workflowWithoutInput})
	fb.putRun(t, "r-done", "sweep", "done", time.Now().Add(-time.Hour))
	fb.selectWorkflow(t, "sweep")

	fb.press(t, "u")
	if len(fb.resumed) != 0 {
		t.Fatalf("u resumed a run that was not parked: %v", fb.resumed)
	}
	if !strings.Contains(fb.status, "not parked") {
		t.Errorf("status is %q, want it to say why nothing happened", fb.status)
	}
}

// The gap this tab exists to close: a parked workflow was visible on the board and
// could only be resumed by leaving it and typing a run id nobody memorises.
func TestUnparkResumesTheLatestParkedRun(t *testing.T) {
	fb := newWorkflowBoard(t, map[string]string{"sweep.yml": workflowWithoutInput})
	fb.putRun(t, "r-parked", "sweep", "parked", time.Now().Add(-time.Minute))
	fb.selectWorkflow(t, "sweep")

	fb.press(t, "u")
	if len(fb.resumed) != 1 || fb.resumed[0] != "r-parked" {
		t.Fatalf("resumed %v, want the parked run r-parked", fb.resumed)
	}
	// Resume goes at the run, never at the workflow: starting the workflow again by name
	// would redo the steps the parked run already paid for.
	if len(fb.started) != 0 {
		t.Errorf("u started a fresh run as well: %v", fb.started)
	}
}

// A workflow the reader cannot see must not be the one a key acts on.
func TestWorkflowKeysRespectTheFilter(t *testing.T) {
	fb := newWorkflowBoard(t, map[string]string{
		"triage.yml": workflowWithInput,
		"sweep.yml":  workflowWithoutInput,
	})
	fb.query = "sweep"
	fb.cursor = 0
	r, ok := fb.selectedWorkflow()
	if !ok || r.workflow.ID != "sweep" {
		t.Fatalf("selected %q (%v), want sweep", r.workflow.ID, ok)
	}
}

// One row is one line, including a broken one — a YAML error arrives with the
// offending line under a heading, and a row that is secretly two breaks the
// arithmetic the pane is sized by.
func TestEveryWorkflowRowIsExactlyOneLine(t *testing.T) {
	fb := newWorkflowBoard(t, map[string]string{
		"triage.yml": workflowWithInput,
		"sweep.yml":  workflowWithoutInput,
		"broken.yml": workflowBroken,
	})
	rows := len(fb.visibleWorkflows())
	out := strings.TrimRight(fb.renderWorkflows(0, rows), "\n")
	// One line per row, plus the column titles.
	if got := strings.Count(out, "\n") + 1; got != rows+1 {
		t.Errorf("rendered %d lines for %d rows:\n%s", got, rows, out)
	}
}

// The open input box is chrome, and every row it takes has to come out of the
// list rather than off the bottom of the pane: a view one row taller than the
// pane makes the terminal scroll and smears the whole board upwards.
func TestTheWorkflowsTabFitsThePaneWithTheInputBoxOpen(t *testing.T) {
	fb := newWorkflowBoard(t, map[string]string{
		"triage.yml": workflowWithInput,
		"sweep.yml":  workflowWithoutInput,
		"broken.yml": workflowBroken,
	})
	for _, width := range []int{60, 100, 160} {
		for height := 6; height <= 30; height++ {
			for _, prompting := range []bool{false, true} {
				fb.width, fb.height = width, height
				fb.workflowInput = nil
				if prompting {
					fb.selectWorkflow(t, "triage")
					fb.pressSpecial(t, tea.KeyEnter)
				}
				if rows := strings.Count(fb.View(), "\n") + 1; rows > height {
					t.Fatalf("%dx%d, box open %v: the view renders %d rows",
						width, height, prompting, rows)
				}
			}
		}
	}
}

// Width is measured in display columns: a styled line carries escape sequences
// that are not printed, so counting runes would flag lines that fit.
func TestNoWorkflowsTableLineExceedsThePane(t *testing.T) {
	fb := newWorkflowBoard(t, map[string]string{
		"triage.yml": workflowWithInput,
		"sweep.yml":  workflowWithoutInput,
		"broken.yml": workflowBroken,
	})
	for _, width := range []int{60, 100, 160} {
		fb.width = width
		table := fb.renderWorkflows(0, len(fb.visibleWorkflows()))
		for _, line := range strings.Split(table, "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Errorf("at %d columns a row is %d wide: %q", width, w, line)
			}
		}
	}
}

// A board wired with no workflow directory would otherwise read as an installation
// with no workflows, which is a wrong answer rather than an empty one.
func TestABoardWithNoWorkflowDirectorySaysSoRatherThanShowingNone(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	m := New(s, herdrcli.New(), Deps{})
	workflows, errs := m.readWorkflows()
	if len(workflows) != 0 {
		t.Fatalf("read %d workflows from nowhere", len(workflows))
	}
	if len(errs) != 1 {
		t.Fatalf("reported %d problems, want 1 naming the missing directory", len(errs))
	}
}
