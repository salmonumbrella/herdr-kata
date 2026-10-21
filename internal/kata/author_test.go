package kata

import (
	"encoding/json"
	"testing"
)

// Job and Workflow are embedded in persisted run and definition snapshots.
func TestJobAuthorRemainsTransient(t *testing.T) {
	const snapshot = `{"uid":"job","name":"Shared job","definition_event_uid":"event","created_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z","definition":{"version":1}}`
	j := Job{Author: "worker", UID: "job", Name: "Shared job", DefinitionEventUID: "event", Definition: json.RawMessage(`{"version":1}`)}
	for _, value := range []any{j, Workflow(j)} {
		b, err := json.Marshal(value)
		if err != nil || string(b) != snapshot {
			t.Fatalf("persisted envelope changed: %s, %v", b, err)
		}
	}
}
