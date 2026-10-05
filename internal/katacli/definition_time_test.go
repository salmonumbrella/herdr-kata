package katacli

import (
	"encoding/json"
	"testing"
	"time"
)

// Public server creation time must survive decoding and re-encoding into the
// derived cache. Losing it deadlocks a never-run interval/cron schedule.
func FuzzDefinitionCreationRoundTrip(f *testing.F) {
	f.Add(int64(1791158400), int32(123456789))
	f.Add(int64(1791158400+62135596800), int32(123456789))
	f.Add(int64(0), int32(0))
	f.Fuzz(func(t *testing.T, seconds int64, nanos int32) {
		// Cover every nanosecond and year supported by RFC3339 JSON timestamps.
		const span = int64(253402300800 + 62135596800)
		seconds %= span
		if seconds < 0 {
			seconds += span
		}
		seconds -= 62135596800
		fraction := int64(nanos) % 1000000000
		if fraction < 0 {
			fraction += 1000000000
		}
		stamp := time.Unix(seconds, fraction).UTC()
		raw, err := json.Marshal(map[string]any{"uid": "01ARZ3NDEKTSV4RRFFQ69G5FAV", "created_at": stamp})
		if err != nil {
			t.Fatal(err)
		}
		var def Definition
		if err := Decode(raw, &def); err != nil {
			t.Fatal(err)
		}
		cached, err := json.Marshal(def)
		if err != nil {
			t.Fatal(err)
		}
		var restored struct {
			CreatedAt time.Time `json:"created_at"`
		}
		if err := json.Unmarshal(cached, &restored); err != nil {
			t.Fatal(err)
		}
		if !restored.CreatedAt.Equal(stamp) {
			t.Fatalf("server creation lost: got %s want %s", restored.CreatedAt, stamp)
		}
	})
}

func TestDefinitionMalformedCreationTimeRejected(t *testing.T) {
	for _, raw := range []string{`{"created_at":"not-a-date"}`, `{"created_at":42}`, `{"created_at":"2026-13-01T00:00:00Z"}`} {
		var def Definition
		if err := Decode([]byte(raw), &def); err == nil {
			t.Fatalf("malformed server timestamp accepted: %s", raw)
		}
	}
}
