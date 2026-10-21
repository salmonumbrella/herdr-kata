package main

import (
	"bytes"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"strings"
	"testing"
)

func TestStatusRunParentDecisions(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	if err := state.WriteJSON(state.OSFS{}, setupStatusLane(t, root).DefsSnapshot(), defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{{UID: "job", Name: "Command runs", LastSkip: "parent_closed", Waiting: "parent_unavailable"}}}); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := runCommandStatus(&out, &stderr, false); code != 0 {
		t.Fatal(code, stderr.String())
	}
	if !strings.Contains(out.String(), "last_skip=parent_closed") || !strings.Contains(out.String(), "waiting=parent_unavailable") {
		t.Fatal(out.String())
	}
}
