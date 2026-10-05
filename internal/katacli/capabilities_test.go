package katacli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEmptyProjectCapabilityDiscoveryDoesNotRequireAuthorityState(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		accepted   bool
	}{
		{"empty native project", `{"project_uid":"` + uid + `","event_features":["future_v2","cron_v1"]}`, true},
		{"missing header", `{"project_uid":"` + uid + `","event_features":[]}`, false},
		{"substring", `{"project_uid":"` + uid + `","event_features":["cron_v10"]}`, false},
		{"missing project", `{"event_features":["cron_v1"]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nativeResponses(t, map[string]json.RawMessage{"capabilities show": json.RawMessage(`{"body":` + tc.body + `}`), "authority show": json.RawMessage(`{"exit":4,"body":{"error":"cron state absent"}}`)})
			_, e := testClient(t).Capabilities(t.Context())
			if (e == nil) != tc.accepted {
				t.Fatalf("capabilities accepted=%v error=%v", tc.accepted, e)
			}
		})
	}
}

func TestUnavailableNativeDiscoveryHasActionableDiagnostics(t *testing.T) {
	nativeResponses(t, map[string]json.RawMessage{"capabilities show": json.RawMessage(`{"exit":4,"body":{"error":"unsupported command"}}`)})
	_, e := testClient(t).Capabilities(t.Context())
	if e == nil || !strings.Contains(e.Error(), "target") || !strings.Contains(e.Error(), "upgrade") {
		t.Fatalf("unactionable discovery %v", e)
	}
}
