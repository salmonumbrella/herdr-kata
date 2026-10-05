package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/flow"
	"github.com/salmonumbrella/herdr-kata/internal/herdrcli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

func openFlowSpace(ctx context.Context, s *store.Store, def flow.Flow, rec *store.Run, cwd string) *runner.FlowSpace {
	h := herdrcli.New()
	if h == nil {
		fmt.Fprintln(os.Stderr, "herdr-kata: no herdr, so this flow's steps get no shared "+
			"space; they still run in order")
		return nil
	}

	if id := strings.TrimSpace(rec.Space); id != "" {
		if ws, err := h.WorkspaceGet(ctx, id); err == nil && ws != nil {
			label := ws.Label
			if base := runner.LabelBase(label); base != label {
				// The room was renamed when this run parked. It is running
				// again, so the verdict comes off — a space that still says
				// "parked verify" while verify is running is a screen that
				// contradicts itself, and the label is what an operator trusts
				// first.
				if err := h.WorkspaceRename(ctx, ws.WorkspaceID, base); err != nil {
					fmt.Fprintln(os.Stderr, "herdr-kata: this space still carries its last "+
						"park in its name:", err)
				} else {
					label = base
				}
			}
			space := &runner.FlowSpace{WorkspaceID: ws.WorkspaceID, Label: label}
			return space
		}
		fmt.Fprintln(os.Stderr, "herdr-kata: the recorded workspace is gone; opening a new one")
	}

	label := runner.SpaceLabel(def.ID)
	ws, root, err := h.WorkspaceCreate(ctx, label, cwd, nil)
	if err != nil || ws == nil {
		fmt.Fprintln(os.Stderr, "herdr-kata: could not open a space for this flow, so its steps "+
			"have no workspace:", err)
		return nil
	}
	// The root tab comes with the workspace whether anybody wants it or not, and
	// nobody does: every step opens its own tab and the park path opens one more.
	// Keeping its id is the whole cost of being able to close it later, and the
	// alternative — leaving it — is a dead shell in every flow room forever.
	space := &runner.FlowSpace{WorkspaceID: ws.WorkspaceID, Label: firstNonEmpty(ws.Label, label)}
	if root != nil {
		space.RootTabID = strings.TrimSpace(root.TabID)
	}
	rec.Space = space.WorkspaceID
	return space
}

func closeFlowSpace(ctx context.Context, s *store.Store, space *runner.FlowSpace, def flow.Flow, rec store.Run, wr *runner.FlowRun) {
	if !space.Usable() {
		return
	}
	if wr == nil || wr.Outcome != runner.OutcomeDone {
		parkFlowSpace(ctx, s, space, def, rec, wr)
		return
	}

	h := herdrcli.New()
	if h == nil {
		return
	}
	if err := h.WorkspaceClose(ctx, space.WorkspaceID); err != nil {
		fmt.Fprintln(os.Stderr, "herdr-kata: this flow's space is finished with but still open:", err)
	}
}

// spaceKeeper is the part of herdr the park path uses.
//
// An interface rather than the client itself so a test can watch what a park
// does to a room without a live herdr: the whole point of this path is which
// calls it makes, and a test that made them for real would rename and open tabs
// in whatever window the machine running the suite happens to have.
type spaceKeeper interface {
	PaneList(ctx context.Context, workspaceID string) ([]herdrcli.Pane, error)
	TabCreate(ctx context.Context, workspaceID, label, cwd string, env map[string]string) (*herdrcli.Pane, error)
	TabClose(ctx context.Context, tabID string) error
	WorkspaceRename(ctx context.Context, workspaceID, label string) error
}

// keeper returns the herdr client, or nil when there is none. A variable so a
// test can substitute one.
var keeper = func() spaceKeeper {
	h := herdrcli.New()
	if h == nil {
		return nil
	}
	return h
}

func parkFlowSpace(ctx context.Context, s *store.Store, space *runner.FlowSpace,
	def flow.Flow, rec store.Run, wr *runner.FlowRun) {

	stoppedAt, reason := "", runner.ParkReason("")
	if wr != nil {
		stoppedAt, reason = wr.StoppedAt, wr.ParkReason
	}

	h := keeper()
	if h == nil {
		return
	}
	if label := strings.TrimSpace(space.Label); label != "" {
		if err := h.WorkspaceRename(ctx, space.WorkspaceID,
			runner.LabelParked(label, stoppedAt, reason)); err != nil {
			fmt.Fprintln(os.Stderr, "herdr-kata: this space keeps its running name:", err)
		}
	}
	// A tab sitting on the run directory, named for the verdict. An agent step
	// that failed already left its own tab and that is the better thing to read;
	// this is for the case there is none — a `run:` step has no agent and no
	// pane, so without it the room is a blank shell. It costs one tab in the
	// case where both exist, which is cheaper than the case where neither does.
	//
	// One per room, not one per park: a run that is resumed and parks again is
	// the ordinary case for a flow that needs a human, and a tab per attempt
	// turns the room an operator opened for the verdict into a pile of
	// identical shells. The one already sitting in the run directory is that
	// tab — no step's tab is there, because a step runs in the job's working
	// directory.
	if dir := strings.TrimSpace(rec.RunDir); dir != "" && !hasPaneIn(ctx, h, space.WorkspaceID, dir) {
		env := map[string]string{"HERDR_KATA_RUN_ID": rec.ID, "HERDR_KATA_RUN_DIR": dir}

		if _, err := h.TabCreate(ctx, space.WorkspaceID, parkedTabLabel(stoppedAt, reason), dir, env); err != nil {
			fmt.Fprintln(os.Stderr, "herdr-kata: no tab for this run's evidence:", err)
		}
	}
	reapRootTab(ctx, h, space)
	fmt.Fprintf(os.Stderr, "herdr-kata: this run parked and its space is still open — "+
		"close the space to acknowledge it.\n  herdr-kata flow status %s\n", rec.ID)

}

// reapRootTab closes the empty tab herdr made along with the workspace, now that
// the room holds one somebody will actually read.
//
// It runs at park because that is the moment the room stops being a workspace a
// run is using and becomes a screen an operator opens: the label carries the
// verdict, one tab sits on the artifacts, and the shell that has been empty
// since the space was created is the only thing left that means nothing. A run
// that finishes never gets here — closeFlowSpace takes the whole workspace.
//
// The check that some other tab exists is explicit rather than delegated to
// herdr's own refusal to close the last tab in a workspace. Both end with the
// room intact, but leaning on the refusal turns the ordinary case — a `run:`
// step that parked before anything opened a second tab — into an error line
// about a failed close, which trains a reader to ignore the one line that would
// matter if the close failed for a real reason.
//
// A herdr that cannot list panes is treated as "do not touch it": the cost of
// that guess is the spare tab we already have, and the cost of the other guess
// is an empty room.
func reapRootTab(ctx context.Context, h spaceKeeper, space *runner.FlowSpace) {
	root := strings.TrimSpace(space.RootTabID)
	if root == "" {
		// Either herdr never told us which tab it made, or this is a resumed run
		// whose root tab the first attempt already reaped.
		return
	}
	panes, err := h.PaneList(ctx, space.WorkspaceID)
	if err != nil {
		return
	}
	occupied := false
	for _, p := range panes {
		if strings.TrimSpace(p.TabID) != root {
			occupied = true
			break
		}
	}
	if !occupied {
		return
	}
	if err := h.TabClose(ctx, root); err != nil {
		fmt.Fprintln(os.Stderr, "herdr-kata: this space keeps the empty tab it was opened with:", err)
		return
	}
	// One reap per space: saying it is gone stops a second park from asking herdr
	// to close a tab that no longer exists.
	space.RootTabID = ""
}

// hasPaneIn reports whether the space already holds a shell sitting in this
// directory.
//
// A herdr that cannot answer is treated as "no": the cost of the wrong guess is
// one spare tab, and the cost of the other wrong guess is a parked run with
// nothing in its room.
func hasPaneIn(ctx context.Context, h spaceKeeper, workspaceID, dir string) bool {
	panes, err := h.PaneList(ctx, workspaceID)
	if err != nil {
		return false
	}
	for _, p := range panes {
		if p.CWD == dir {
			return true
		}
	}
	return false
}

// parkedTabLabel names the tab an operator lands in.
func parkedTabLabel(stoppedAt string, reason runner.ParkReason) string {
	label := "parked"
	if stoppedAt != "" {
		label += ": " + stoppedAt
	}
	if reason != "" {
		label += " (" + string(reason) + ")"
	}
	return label
}
