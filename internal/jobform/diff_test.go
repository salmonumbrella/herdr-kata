package jobform

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
)

func TestJobDiff(t *testing.T) {
	a := defs.JobDef{Version: 1, Kind: "job", Enabled: true, Trigger: defs.Trigger{Kind: "interval", IntervalSeconds: 3600}, Action: defs.Action{Kind: "execute", Prompt: "work"}, Issue: &defs.IssuePolicy{Kind: "per-run", Title: "example"}, Options: json.RawMessage(`{"tags":["daily"],"description":"first","opaque_z":9007199254740993,"opaque_a":"secret-value"}`), SecretRefs: map[string]string{"TOKEN": "example"}}
	b := a
	b.Enabled = false
	b.Trigger.IntervalSeconds = 7200
	b.SecretRefs = map[string]string{"TOKEN": "other"}
	b.Options = json.RawMessage(`{"tags":["weekly"],"description":"second","opaque_z":1,"opaque_a":"changed-value"}`)
	changes := Diff(a, b, "example-job", "renamed")
	if len(changes) != 8 {
		t.Fatalf("changes %+v", changes)
	}
	want := []Change{{"name", "example-job", "renamed"}, {"description", "first", "second"}, {"tags", "daily", "weekly"}, {"schedule", "every 1h0m0s", "every 2h0m0s"}, {"secret refs", "TOKEN=example", "TOKEN=other"}, {"enabled", "yes", "no"}, {"options.opaque_a", "present (preserved)", "present (preserved)"}, {"options.opaque_z", "present (preserved)", "present (preserved)"}}
	for i, c := range changes {
		if c != want[i] {
			t.Fatalf("change %d: %+v want %+v", i, c, want[i])
		}
		if strings.Contains(c.Yours, "secret-value") || strings.Contains(c.Yours, "9007199254740993") {
			t.Fatal("opaque value dumped", c)
		}
	}
	if got := Diff(a, a, "example-job", "example-job"); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestDiffKeepsUnresolvedReferenceChangesVisible(t *testing.T) {
	a := defs.JobDef{Action: defs.Action{Kind: "execute", Prompt: "work"}, Issue: &defs.IssuePolicy{Kind: "existing", UID: "01K00000000000000000000001"}}
	b := a
	b.Issue = &defs.IssuePolicy{Kind: "existing", UID: "01K00000000000000000000002"}
	got := Diff(a, b, "Example", "Example")
	if len(got) != 1 || got[0].Field != "existing issue" || got[0].Yours != "previous reference" || got[0].Kata != "different reference" {
		t.Fatalf("reference change: %+v", got)
	}
}
