package katacli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const uid = "01ARZ3NDEKTSV4RRFFQ69G5FAD"

func TestDefinitionRetryRetainsIdentityAndExactNumbers(t *testing.T) {
	c := testClient(t)
	d, e := NewDraft("job", strings.ToLower(uid), "Daily", json.RawMessage(`{"version":1,"options":{"counter":9007199254740993}}`), "")
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		var out echo
		if e := c.Call(t.Context(), d.Args(), d.Body(c.Target.Actor), &out); e != nil {
			t.Fatal(e)
		}
		want := []string{"automation", "job", "create", "--uid", uid, "--file", "-"}
		if !reflect.DeepEqual(out.Argv[9:], want) || !strings.Contains(out.Stdin, "9007199254740993") {
			t.Fatalf("retry changed %+v", out)
		}
	}
}
func nativeResponses(t *testing.T, entries map[string]json.RawMessage) {
	t.Helper()
	mode(t, "responses")
	if _, ok := entries["capabilities show"]; !ok {
		entries["capabilities show"] = json.RawMessage(`{"body":{"project_uid":"` + uid + `","event_features":["automations_v1"]}}`)
	}
	p := filepath.Join(filepath.Dir(fixture), "responses.json")
	raw, _ := json.Marshal(entries)
	if e := os.WriteFile(p, raw, 0600); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.Remove(p) })
}
func TestCreateRecoveryRequiresExactLiveReadback(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current string
		wantErr bool
	}{{"exact", `{"uid":"` + uid + `","name":"Daily","definition_event_uid":"` + uid + `","definition":{"version":1,"options":{"counter":9007199254740993}}}`, false}, {"rounded", `{"uid":"` + uid + `","name":"Daily","definition_event_uid":"` + uid + `","definition":{"version":1,"options":{"counter":9007199254740992}}}`, true}, {"tombstone", `{"uid":"` + uid + `","name":"Daily","definition_event_uid":"` + uid + `","deleted_at":"2026-10-04T00:00:00Z","definition":{"version":1,"options":{"counter":9007199254740993}}}`, true}} {
		t.Run(tc.name, func(t *testing.T) {
			nativeResponses(t, map[string]json.RawMessage{"job create": json.RawMessage(`{"exit":5,"body":{"error":"conflict"}}`), "job show": json.RawMessage(`{"body":{"job":` + tc.current + `}}`)})
			c := testClient(t)
			d, _ := NewDraft("job", uid, "Daily", json.RawMessage(`{"version":1,"options":{"counter":9007199254740993}}`), "")
			_, e := c.Save(t.Context(), d)
			if (e != nil) != tc.wantErr {
				t.Fatalf("recovery error=%v expected=%v", e, tc.wantErr)
			}
		})
	}
}
func TestOpaqueJSONUsesExactNumbers(t *testing.T) {
	c := testClient(t)
	nativeResponses(t, map[string]json.RawMessage{"job show": json.RawMessage(`{"body":{"job":{"uid":"` + uid + `","name":"Daily","definition_event_uid":"` + uid + `","definition":{"version":1,"options":{"counter":9007199254740993}}}}}`)})
	d, e := c.Definition(t.Context(), "job", uid)
	if e != nil {
		t.Fatal(e)
	}
	var out map[string]any
	if e := Decode(d.Definition, &out); e != nil {
		t.Fatal(e)
	}
	got := out["options"].(map[string]any)["counter"]
	if got != json.Number("9007199254740993") {
		t.Fatalf("precision lost %T %v", got, got)
	}
}
func TestInvalidOperationAndDefinitionInputs(t *testing.T) {
	for _, value := range []string{"", "abc4", "81ARZ3NDEKTSV4RRFFQ69G5FAD", "01ARZ3NDEKTSV4RRFFQ69G5FAI"} {
		if _, e := NormalizeUID(value); e == nil {
			t.Fatalf("invalid UID accepted %q", value)
		}
	}
	for _, resource := range []string{"issue", "--daemon"} {
		if _, e := NewDraft(resource, uid, "Daily", json.RawMessage(`{}`), ""); e == nil {
			t.Fatal("unknown definition accepted")
		}
	}
}
