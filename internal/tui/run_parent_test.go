package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

func TestInspectorRunParentAndAdmissionReason(t *testing.T) {
	m, _ := fixture(t)
	m.data.Jobs = []defs.JobEntry{{UID: "job", Name: "Command runs", LastSkip: "parent_closed", Waiting: "parent_unavailable", Definition: json.RawMessage(`{"options":{"run_parent":"01K00000000000000000000001"}}`)}}
	m.data.Runs = []runner.Run{{IssueRefs: map[string]string{"01K00000000000000000000001": "example-project#ab12"}}}
	m.rebuildJobIndex()
	m.rebuild()
	text := strings.Join(m.inspector(), "\n")
	for _, want := range []string{"parent    example-project#ab12", "skip      parent_closed", "waiting   parent_unavailable"} {
		if !strings.Contains(text, want) {
			t.Fatal(text)
		}
	}
}
