package kata

import (
	"context"
	"reflect"
	"testing"
)

// The lane lease lives on one issue found by its label: the oldest such
// issue, open or closed, in a single list call.
func TestOldestLabeledIssue(t *testing.T) {
	c := fakeCLI(t, `{"issues":[{"uid":"01K00000000000000000000LSE","status":"closed","revision":4,"metadata":{"herdr_kata.lease":{"version":1}}}]}`, func(_ context.Context, argv, _ []string, _ string, _ []byte) {
		want := []string{"list", "--status", "all", "--label", "herdr-kata:lease", "--sort", "oldest", "--limit", "1"}
		if got := argv[len(argv)-len(want):]; !reflect.DeepEqual(got, want) {
			t.Fatalf("argv %q", argv)
		}
	})
	issue, found, err := c.OldestLabeled(t.Context(), "herdr-kata:lease")
	if err != nil || !found || issue.UID != "01K00000000000000000000LSE" || issue.Revision != 4 || string(issue.Metadata["herdr_kata.lease"]) != `{"version":1}` {
		t.Fatalf("%+v %t %v", issue, found, err)
	}
	empty := fakeCLI(t, `{"issues":[]}`, nil)
	if _, found, err := empty.OldestLabeled(t.Context(), "herdr-kata:lease"); err != nil || found {
		t.Fatal(found, err)
	}
}
