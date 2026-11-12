package kata

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInputReadsPinRouteAndPreserveIntegers(t *testing.T) {
	k, args := stub(t, "jobs", "0")
	reply := filepath.Join(t.TempDir(), "reply.json")
	if err := os.WriteFile(reply, []byte(`{"job_uid":"job","definition_event_uid":"event","inputs":[{"name":"number","type":"int","source":"request","value":9223372036854775807}],"errors":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HK_TEST_REPLY", reply)
	k = New(k.(*cli).bin, "fixture", nil, "example-project", "worker")
	result, err := k.JobInputs("job", "request")
	if err != nil || result.JobUID != "job" || result.DefinitionEventUID != "event" || len(result.Inputs) != 1 || string(result.Inputs[0].Value) != "9223372036854775807" {
		t.Fatalf("resolution: %+v %v", result, err)
	}
	assertArgs(t, args(), "cron", "job", "inputs", "job", "--request-uid", "request")
	if err := os.WriteFile(reply, []byte(`{"api_schema_version":"0.49.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	version, err := k.HealthVersion()
	if err != nil || version != "0.49.0" {
		t.Fatalf("health: %q %v", version, err)
	}
	assertArgs(t, args(), "health")
}
