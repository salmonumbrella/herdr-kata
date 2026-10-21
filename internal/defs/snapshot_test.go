package defs

import (
	"context"
	"encoding/json"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"testing"
	"time"
)

type boardReader struct{}

func (boardReader) ListJobs(_ context.Context) ([]kata.Job, error) {
	return []kata.Job{{UID: "job", Name: "name", UpdatedAt: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC), Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"hello"},"overlap":"forbid","catchup":"skip"}`)}}, nil
}
func (boardReader) ListWorkflows(_ context.Context) ([]kata.Workflow, error) {
	return []kata.Workflow{{UID: "flow", Name: "Flow", Definition: json.RawMessage(`{"version":1,"steps":[]}`)}}, nil
}
func TestBoardSnapshotFullDefinitions(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	c := NewCache(config.Config{}, boardReader{}, state.OSFS{}, l)
	if err := c.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	var snap Snapshot
	if err := state.ReadJSON(state.OSFS{}, l.DefsSnapshot(), 4<<20, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Version != 2 || len(snap.Jobs) != 1 || len(snap.Workflows) != 1 || len(snap.Jobs[0].Definition) == 0 {
		t.Fatalf("%+v", snap)
	}
	// The TUI's updated column reads definition changes from the snapshot.
	if want := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC); !snap.Jobs[0].UpdatedAt.Equal(want) {
		t.Fatalf("job updated_at %v, want %v", snap.Jobs[0].UpdatedAt, want)
	}
}

func TestReadDefsRejectsUnknownEnvelopeFields(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	if err := state.WriteJSON(state.OSFS{}, l.DefsSnapshot(), map[string]any{"version": 2, "jobz": []any{}, "workflows": []any{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshot(state.OSFS{}, l.DefsSnapshot()); err == nil {
		t.Fatal("unknown snapshot field silently discarded")
	}
}
