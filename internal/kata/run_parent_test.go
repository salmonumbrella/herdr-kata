package kata

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestCreateRunParentAndSortedLabels(t *testing.T) {
	var req CreateReq
	requireNoError(t, json.Unmarshal([]byte(`{"Title":"Run","Body":"Details","IdempotencyKey":"key","Parent":"01K00000000000000000000001","Labels":["z","herdr-kata:run"]}`), &req))
	c := fakeCLI(t, `{"issue":{"uid":"child"}}`, func(_ context.Context, argv, _ []string, _ string, body []byte) {
		want := []string{"create", "--body-stdin", "--idempotency-key", "key", "--parent", "01K00000000000000000000001", "--label", "herdr-kata:run", "--label", "z", "--", "Run"}
		if !reflect.DeepEqual(argv[len(argv)-len(want):], want) || string(body) != "Details" {
			t.Fatal(argv, string(body))
		}
	})
	_, err := c.CreateIssue(t.Context(), req)
	requireNoError(t, err)
}
func TestCloseThrottleRetryIsPrecommit(t *testing.T) {
	for _, code := range []string{"sibling_throttle", "duplicate_message"} {
		err := &Error{Code: code, Exit: 1}
		if Class(err) != Retry || Ambiguous(err) {
			t.Fatalf("%s: class=%v ambiguous=%v", code, Class(err), Ambiguous(err))
		}
	}
}
