package kata

import (
	"context"
	"encoding/json"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"strings"
	"testing"
)

func TestEditUsesExpectedEventAndStdin(t *testing.T) {
	var argv []string
	var body []byte
	k := New(config.Kata{Bin: "kata", Actor: "actor", Project: "test"}, t.TempDir(), func(_ context.Context, a, e []string, d string, in []byte) ([]byte, []byte, int, error) {
		argv = a
		body = in
		return []byte(`{"job":{"uid":"j"}}`), nil, 0, nil
	}, nil)
	j := Job{UID: "j", Name: "name", DefinitionEventUID: "e", Definition: json.RawMessage(`{"enabled":false}`)}
	if _, err := k.SaveJob(t.Context(), j, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(argv, " "), "cron job update j --expected-event-uid e --file -") || !json.Valid(body) {
		t.Fatal(argv, string(body))
	}
	if err := k.DeleteJob(t.Context(), j); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(argv, " "), "cron job delete j --expected-event-uid e") {
		t.Fatal(argv)
	}
}

func TestCreateRetainsUIDAndParent(t *testing.T) {
	var argv []string
	k := New(config.Kata{Bin: "kata", Actor: "actor", Project: "test"}, t.TempDir(), func(_ context.Context, a, e []string, d string, in []byte) ([]byte, []byte, int, error) {
		argv = a
		return []byte(`{"job":{"uid":"draft"}}`), nil, 0, nil
	}, nil)
	_, err := k.SaveJob(t.Context(), Job{UID: "draft", Name: "child", Definition: json.RawMessage(`{"enabled":false}`)}, "test#parent")
	joined := strings.Join(argv, " ")
	if err != nil || !strings.Contains(joined, "create --uid draft --file - --parent test#parent") || strings.Contains(joined, "expected-event") {
		t.Fatal(argv, err)
	}
}

func TestWorkflowSaveGuard(t *testing.T) {
	var argv []string
	k := New(config.Kata{Bin: "kata"}, t.TempDir(), func(_ context.Context, a, e []string, d string, in []byte) ([]byte, []byte, int, error) {
		argv = a
		return []byte(`{"workflow":{"uid":"flow"}}`), nil, 0, nil
	}, nil)
	_, err := k.SaveWorkflow(t.Context(), Workflow{UID: "flow", DefinitionEventUID: "event", Name: "Flow", Definition: json.RawMessage(`{"version":1,"steps":[]}`)})
	if err != nil || !strings.Contains(strings.Join(argv, " "), "cron workflow update flow --expected-event-uid event --file -") {
		t.Fatal(argv, err)
	}
}
