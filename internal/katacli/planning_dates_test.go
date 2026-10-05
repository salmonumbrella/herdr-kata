package katacli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPlanningDatesRetainsNativeInstantsIncludingYearOne(t *testing.T) {
	mode(t, "execution-policy")
	dir := filepath.Dir(fixture)
	path := filepath.Join(dir, "planning-dates.json")
	t.Cleanup(func() { os.Remove(path); os.Remove(filepath.Join(dir, "policy-calls.jsonl")) })
	for _, instant := range []string{"0001-01-01T00:00:00Z", "2000-01-01T00:00:00.123456789Z"} {
		raw := []byte(`{"project_id":73,"issue_uid":"01ARZ3NDEKTSV4RRFFQ69G5FAW","revision":3,"scheduled_on":{"field":"scheduled_on","value":"` + instant + `","timezone":"UTC","instant":"` + instant + `"},"deadline_on":null,"future":9007199254740993}`)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		got, err := testClient(t).PlanningDates(t.Context(), "01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW")
		want, _ := time.Parse(time.RFC3339Nano, instant)
		if err != nil || got.ScheduledOn == nil || !got.ScheduledOn.Instant.Equal(want) || got.ScheduledOn.Timezone != "UTC" || got.ScheduledOn.Value != instant || got.DeadlineOn != nil {
			t.Errorf("native source was reinterpreted or discarded: %+v %v", got, err)
		}
	}
}

func TestPlanningDatesRejectsForeignAndMalformedProjection(t *testing.T) {
	mode(t, "execution-policy")
	dir := filepath.Dir(fixture)
	path := filepath.Join(dir, "planning-dates.json")
	t.Cleanup(func() { os.Remove(path); os.Remove(filepath.Join(dir, "policy-calls.jsonl")) })
	for _, fault := range []string{"project", "issue", "revision", "required", "field", "instant", "timezone"} {
		t.Run(fault, func(t *testing.T) {
			date := map[string]any{"field": "scheduled_on", "value": "2000-01-01", "timezone": "UTC", "instant": "2000-01-01T00:00:00Z"}
			out := map[string]any{"project_id": 73, "issue_uid": "01ARZ3NDEKTSV4RRFFQ69G5FAW", "revision": 1, "scheduled_on": date, "deadline_on": nil}
			switch fault {
			case "project":
				out["project_id"] = 74
			case "issue":
				out["issue_uid"] = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
			case "revision":
				out["revision"] = 0
			case "required":
				delete(out, "deadline_on")
			case "field":
				date["field"] = "deadline_on"
			case "instant":
				date["instant"] = "2000-01-01T00:00:00+00:00"
			case "timezone":
				delete(date, "timezone")
			}
			raw, _ := json.Marshal(out)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := testClient(t).PlanningDates(t.Context(), "01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW"); err == nil {
				t.Fatal("invalid ordinary native projection accepted")
			}
		})
	}
}
