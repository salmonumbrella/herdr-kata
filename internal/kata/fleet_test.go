package kata

import "testing"

func TestFleetCapabilitiesTransport(t *testing.T) {
	k, args := stub(t, "capabilities-fleet", "0")
	caps, err := k.Capabilities()
	if err != nil || caps.Execution == nil || caps.Execution.Mode != "owned" || caps.Execution.MemberActor != "worker" || len(caps.Controls) != 2 {
		t.Fatalf("caps %+v: %v", caps, err)
	}
	assertArgs(t, args(), "cron", "capabilities")
}
