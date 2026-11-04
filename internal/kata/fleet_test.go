package kata

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func TestFleetRequestsTransport(t *testing.T) {
	k, args := stub(t, "requests-fleet", "0")
	requests, err := k.Requests()
	if err != nil || len(requests) != 2 || requests[0].DefinitionEventUID != "01J00000000000000000000011" || requests[1].RunUID != "01J00000000000000000000020" || requests[0].ExpiresAt.IsZero() {
		t.Fatal(requests, err)
	}
	assertArgs(t, args(), "cron", "request", "list", "--state", "pending")
}

func TestFleetMalformedReadTransport(t *testing.T) {
	for _, raw := range []string{`{`, `{"execution":false}`, `{"controls":{}}`, `{"jobs":false}`} {
		t.Run(raw, func(t *testing.T) {
			base, _ := stub(t, "capabilities-fleet", "0")
			path := filepath.Join(t.TempDir(), "reply.json")
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HK_TEST_REPLY", path)
			k := New(base.(*cli).bin, "fixture", nil, "example-project", "worker")
			var err error
			if raw == `{"jobs":false}` {
				_, err = k.Jobs()
			} else {
				_, err = k.Capabilities()
			}
			if err == nil {
				t.Fatal("malformed discovery treated as success")
			}
		})
	}
}
