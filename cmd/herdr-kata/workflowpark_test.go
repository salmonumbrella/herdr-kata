package main

import (
	"context"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/herdrcli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"github.com/salmonumbrella/herdr-kata/internal/workflow"
)

// What a parked run leaves behind for the person who has to deal with it.
//
// A parked workflow keeps its space, because a human has to look at it — and for a
// workflow of `run:` steps that room held a blank shell and nothing else, so the
// verdict existed only in the database. These tests are about the room saying
// what happened. No herdr is touched: the keeper is faked, because a test that
// renamed spaces for real would rename whatever window the machine running the
// suite happens to have open.

type fakeKeeper struct {
	panes   []herdrcli.Pane
	renamed []string
	closed  []string
	tabs    []struct {
		label, cwd string
		env        map[string]string
	}
}

func (f *fakeKeeper) TabClose(_ context.Context, tabID string) error {
	f.closed = append(f.closed, tabID)
	return nil
}

func (f *fakeKeeper) WorkspaceRename(_ context.Context, workspaceID, label string) error {
	f.renamed = append(f.renamed, workspaceID+"="+label)
	return nil
}

func (f *fakeKeeper) TabCreate(_ context.Context, _, label, cwd string, env map[string]string) (*herdrcli.Pane, error) {
	f.tabs = append(f.tabs, struct {
		label, cwd string
		env        map[string]string
	}{label, cwd, env})
	return &herdrcli.Pane{PaneID: "p9", TabID: "t9"}, nil
}

// withKeeper substitutes the herdr calls the park path makes.
func withKeeper(t *testing.T) *fakeKeeper {
	t.Helper()
	f := &fakeKeeper{}
	old := keeper
	keeper = func() spaceKeeper { return f }
	t.Cleanup(func() { keeper = old })
	return f
}

func parkedRun(t *testing.T, s *store.Store) (*runner.WorkflowSpace, workflow.Workflow, store.Run) {
	t.Helper()
	space := &runner.WorkflowSpace{WorkspaceID: "w7", Label: "WORKFLOWS:heals:X7Tf19"}
	def := workflow.Workflow{ID: "heals", Steps: []store.Step{
		{ID: "implement", Run: "true"},
		{ID: "verify", Run: "false"},
	}}
	rec := store.Run{ID: "20260801T000000Z-heals", JobID: "heals", Workflow: "heals",
		RunDir: t.TempDir(), Space: space.WorkspaceID}
	return space, def, rec
}

// The thread is the record, and a parked run used to leave it stopped
// mid-sentence: every step's findings, and nothing saying how it ended.
func TestAParkedRunNamesTheRoomAndLandsATabOnTheEvidence(t *testing.T) {
	s := workflowStore(t)
	k := withKeeper(t)
	ctx := context.Background()
	space, def, rec := parkedRun(t, s)

	wr := &runner.WorkflowRun{RunID: rec.ID, Outcome: runner.OutcomeParked,
		ParkReason: runner.ParkStepFailed, StoppedAt: "verify", Total: 2}
	parkWorkflowSpace(ctx, s, space, def, rec, wr)

	if len(k.renamed) != 1 || !strings.Contains(k.renamed[0], "verify") ||
		!strings.Contains(k.renamed[0], "step_failed") {
		t.Errorf("the space was renamed %v; it should carry the verdict", k.renamed)
	}
	if !strings.HasPrefix(k.renamed[0], "w7=WORKFLOWS:heals:X7Tf19") {
		t.Errorf("the rename %q lost the room's own name", k.renamed[0])
	}
	if len(k.tabs) != 1 {
		t.Fatalf("opened %d tabs, want one for the evidence", len(k.tabs))
	}
	tab := k.tabs[0]
	if !strings.Contains(tab.label, "verify") {
		t.Errorf("the tab is labelled %q and does not name the step that stopped", tab.label)
	}
	// A shell in the run directory: result.json, result.attempt-N.json and
	// output.txt are all sitting there, which is what somebody opening the room
	// came to read.
	if tab.cwd != rec.RunDir {
		t.Errorf("the tab opened in %q, want the run directory %q", tab.cwd, rec.RunDir)
	}
	if tab.env["HERDR_KATA_RUN_ID"] != rec.ID || tab.env["HERDR_KATA_RUN_DIR"] != rec.RunDir {
		t.Errorf("the tab's environment is %v and cannot reach the run artifacts", tab.env)
	}
}

// Degradation, never refusal: with no herdr there is no room to label, and the
// record still has to be written.
func TestParkingTwiceReusesTheTabAlreadyInTheRunDirectory(t *testing.T) {
	s := workflowStore(t)
	k := withKeeper(t)
	ctx := context.Background()
	space, def, rec := parkedRun(t, s)
	wr := &runner.WorkflowRun{RunID: rec.ID, Outcome: runner.OutcomeParked,
		ParkReason: runner.ParkLoopExhausted, StoppedAt: "verify", Total: 2}

	parkWorkflowSpace(ctx, s, space, def, rec, wr)
	if len(k.tabs) != 1 {
		t.Fatalf("the first park opened %d tabs, want 1", len(k.tabs))
	}
	// The tab it opened is now in the room, which is what the second park sees.
	k.panes = append(k.panes, herdrcli.Pane{PaneID: "p9", TabID: "t9", CWD: rec.RunDir})

	parkWorkflowSpace(ctx, s, space, def, rec, wr)
	if len(k.tabs) != 1 {
		t.Errorf("parking twice opened %d tabs; the room accumulates a shell per attempt", len(k.tabs))
	}
	// The label still gets re-applied, because the verdict may be a new one.
	if len(k.renamed) != 2 {
		t.Errorf("the room was renamed %d times, want one per park", len(k.renamed))
	}
}

// A step's tab is not the evidence tab: a step runs in the job's working
// directory, so a workflow whose cwd differs from the run directory must still get
// somewhere to read the artifacts.
func TestAStepsOwnTabDoesNotStandInForTheEvidenceTab(t *testing.T) {
	s := workflowStore(t)
	k := withKeeper(t)
	ctx := context.Background()
	space, def, rec := parkedRun(t, s)
	k.panes = append(k.panes, herdrcli.Pane{PaneID: "p1", TabID: "t1", CWD: t.TempDir()})

	parkWorkflowSpace(ctx, s, space, def, rec, &runner.WorkflowRun{RunID: rec.ID,
		Outcome: runner.OutcomeParked, ParkReason: runner.ParkStepFailed, StoppedAt: "verify"})
	if len(k.tabs) != 1 {
		t.Fatalf("opened %d tabs; a pane somewhere else was mistaken for the evidence", len(k.tabs))
	}
}

func (f *fakeKeeper) PaneList(_ context.Context, _ string) ([]herdrcli.Pane, error) {
	return f.panes, nil
}

// herdr makes a tab along with the workspace and nothing ever runs in it, so
// every parked workflow room used to show a dead tab "1" next to the one worth
// reading. Park is where the room becomes a screen for an operator, so that is
// where the empty shell goes.
func TestAParkClosesTheEmptyTabTheSpaceWasOpenedWith(t *testing.T) {
	s := workflowStore(t)
	k := withKeeper(t)
	ctx := context.Background()
	space, def, rec := parkedRun(t, s)
	space.RootTabID = "t-root"
	// The evidence tab is already in the room, so the park has no reason to open
	// another one and the root tab is demonstrably not the only thing left.
	k.panes = append(k.panes,
		herdrcli.Pane{PaneID: "p-root", TabID: "t-root"},
		herdrcli.Pane{PaneID: "p9", TabID: "t9", CWD: rec.RunDir})

	parkWorkflowSpace(ctx, s, space, def, rec, &runner.WorkflowRun{RunID: rec.ID,
		Outcome: runner.OutcomeParked, ParkReason: runner.ParkStepFailed, StoppedAt: "verify"})

	if len(k.closed) != 1 || k.closed[0] != "t-root" {
		t.Errorf("closed %v, want the workspace's own empty tab", k.closed)
	}
	// Reaped once. A second park must not ask herdr to close a tab that is gone.
	if space.RootTabID != "" {
		t.Errorf("the space still claims a root tab %q after reaping it", space.RootTabID)
	}
	parkWorkflowSpace(ctx, s, space, def, rec, &runner.WorkflowRun{RunID: rec.ID,
		Outcome: runner.OutcomeParked, ParkReason: runner.ParkStepFailed, StoppedAt: "verify"})
	if len(k.closed) != 1 {
		t.Errorf("parking twice closed %d tabs, want the one reap", len(k.closed))
	}
}

// The room must never be left with nothing in it. herdr would refuse the close
// anyway, but relying on the refusal would turn the ordinary case — a `run:`
// step that parked before anything else opened a tab — into an error line.
func TestAParkKeepsTheRootTabWhenItIsTheOnlyOne(t *testing.T) {
	s := workflowStore(t)
	k := withKeeper(t)
	ctx := context.Background()
	space, def, rec := parkedRun(t, s)
	space.RootTabID = "t-root"
	k.panes = append(k.panes, herdrcli.Pane{PaneID: "p-root", TabID: "t-root"})

	parkWorkflowSpace(ctx, s, space, def, rec, &runner.WorkflowRun{RunID: rec.ID,
		Outcome: runner.OutcomeParked, ParkReason: runner.ParkStepFailed, StoppedAt: "verify"})

	if len(k.closed) != 0 {
		t.Errorf("closed %v and would have emptied the room", k.closed)
	}
}

// A resumed run did not create its space, so it has no root tab id — the attempt
// that opened the room already reaped it. Nothing to close, and nothing to
// guess at.
func TestAResumedParkClosesNoTabs(t *testing.T) {
	s := workflowStore(t)
	k := withKeeper(t)
	ctx := context.Background()
	space, def, rec := parkedRun(t, s)
	k.panes = append(k.panes,
		herdrcli.Pane{PaneID: "p1", TabID: "t1"},
		herdrcli.Pane{PaneID: "p9", TabID: "t9", CWD: rec.RunDir})

	parkWorkflowSpace(ctx, s, space, def, rec, &runner.WorkflowRun{RunID: rec.ID,
		Outcome: runner.OutcomeParked, ParkReason: runner.ParkStepFailed, StoppedAt: "verify"})

	if len(k.closed) != 0 {
		t.Errorf("a resumed park closed %v; it knows of no root tab to reap", k.closed)
	}
}
