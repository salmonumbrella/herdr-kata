package defs

import (
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"testing"
)

func TestAdmissionDecisionSnapshotSurvivesRefresh(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	c := NewCache(config.Config{}, boardReader{}, state.OSFS{}, l)
	if err := c.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordDecision("job", "parent_closed", true); err != nil {
		t.Fatal(err)
	}
	if err := c.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordDecision("job", "parent_unavailable", false); err != nil {
		t.Fatal(err)
	}
	snap, err := ReadSnapshot(state.OSFS{}, l.DefsSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Jobs[0].LastSkip != "parent_closed" || snap.Jobs[0].Waiting != "parent_unavailable" || len(snap.Workflows) != 1 {
		t.Fatal(snap)
	}
	if err := c.RecordDecision("job", "", false); err != nil {
		t.Fatal(err)
	}
	snap, err = ReadSnapshot(state.OSFS{}, l.DefsSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Jobs[0].LastSkip != "parent_closed" || snap.Jobs[0].Waiting != "" {
		t.Fatal(snap.Jobs)
	}
}
