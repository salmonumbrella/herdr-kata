package katacli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunHistoryUsesPublicPageAndExactSummary(t *testing.T) {
	mode(t, "execution-policy")
	project := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	cursor := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	path := filepath.Join(filepath.Dir(fixture), "history.json")
	raw := []byte(`{"runs":[{"uid":"01ARZ3NDEKTSV4RRFFQ69G5FAX","project_id":73,"flow_uid":"01ARZ3NDEKTSV4RRFFQ69G5FAY","flow_definition_event_uid":"01ARZ3NDEKTSV4RRFFQ69G5FAZ","actor":"worker","status":"unknown","summary":{"version":1,"message":"Pending report","input_tokens":9007199254740993,"output_tokens":9223372036854775807},"revision":1,"created_at":"2026-10-05T00:00:00Z","updated_at":"2026-10-05T00:00:00Z"}],"next_before_uid":"` + cursor + `"}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(path); os.Remove(filepath.Join(filepath.Dir(fixture), "policy-calls.jsonl")) })
	page, err := testClient(t).RunHistory(t.Context(), project, cursor)
	if err != nil || len(page.Runs) != 1 || page.NextBeforeUID != cursor {
		t.Fatalf("public page lost: %+v %v", page, err)
	}
	row := page.Runs[0]
	if row.Summary.InputTokens != 9007199254740993 || row.Summary.OutputTokens != 9223372036854775807 || row.JobUID != "" || row.FlowUID == "" {
		t.Fatalf("flat reduced row rounded or lost nullable references: %+v", row)
	}
	// The independent executable records actual routed argv. Cursor remains the
	// ordinary existing before-uid flag, never a new snapshot/authority request.
	calls, err := os.ReadFile(filepath.Join(filepath.Dir(fixture), "policy-calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		var call struct{ Args []string }
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatal(err)
		}
		for i, arg := range call.Args {
			if arg == "run" && i+1 < len(call.Args) && call.Args[i+1] == "list" {
				seen = true
				last := call.Args[len(call.Args)-2:]
				if last[0] != "--before-uid" || last[1] != cursor {
					t.Fatalf("cursor argv lost: %v", call.Args)
				}
			}
		}
	}
	if !seen {
		t.Fatal("ordinary list command did not run")
	}
}
