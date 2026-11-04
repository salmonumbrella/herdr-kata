package kata

import (
	"encoding/json"
	"testing"
)

func TestFleetCapabilitiesTransport(t *testing.T) {
	k, args := stub(t, "capabilities-fleet", "0")
	caps, err := k.Capabilities()
	if err != nil || caps.Execution == nil || caps.Execution.Mode != "owned" || caps.Execution.MemberActor != "worker" || len(caps.Controls) != 2 {
		t.Fatalf("caps %+v: %v", caps, err)
	}
	assertArgs(t, args(), "cron", "capabilities")
}

func TestFleetPauseEnvelope(t *testing.T) {
	for _, raw := range []string{`{}`, `{"project_pause":null}`, `{"project_pause":{}}`, `{"project_pause":false}`} {
		var list JobList
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			t.Fatal(err)
		}
		if list.Paused() != (raw == `{"project_pause":{}}` || raw == `{"project_pause":false}`) {
			t.Fatal(raw, list.Paused())
		}
	}
}
