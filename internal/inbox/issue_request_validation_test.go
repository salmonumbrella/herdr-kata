package inbox

import (
	"encoding/json"
	"testing"
	"time"
)

func TestExplicitRequestSurvivesUTCOffsetFreeze(t *testing.T) {
	child := validChild()
	delete(child.Metadata, "cron.occurrence")
	child.Metadata["herdr_kata.request"] = json.RawMessage(`{"version":1,"kind":"execute","request_uid":"01K00000000000000000000004","requested_at":"2026-01-02T03:04:05+00:00"}`)
	request, err := ValidateRequest(child, projectUID)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	child.Metadata["herdr_kata.request"] = frozen
	decoded, err := ValidateRequest(child, projectUID)
	if err != nil || decoded != request {
		t.Fatalf("valid UTC request changed across journal encoding: %v", err)
	}
}

func FuzzExplicitRequestRequiresTypedConsent(f *testing.F) {
	f.Add(`{"version":1,"kind":"execute","request_uid":"01K00000000000000000000004","requested_at":"2026-01-02T03:04:05Z"}`)
	f.Add(`null`)
	f.Add(`{"kind":"notify"}`)
	f.Fuzz(func(t *testing.T, raw string) {
		child := validChild()
		child.Metadata = map[string]json.RawMessage{"herdr_kata.request": json.RawMessage(raw)}
		request, err := ValidateRequest(child, projectUID)
		if err == nil {
			if request.Version != 1 || request.Kind != "execute" || len(request.RequestUID) != 26 || request.RequestedAt.Equal(time.Time{}) {
				t.Fatalf("non-executable consent admitted: %q", raw)
			}
			delete(child.Metadata, "herdr_kata.request")
			if _, err = ValidateRequest(child, projectUID); err == nil {
				t.Fatal("missing consent accepted")
			}
		}
	})
}
