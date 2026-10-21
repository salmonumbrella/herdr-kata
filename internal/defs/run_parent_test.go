package defs

import (
	"encoding/json"
	"strings"
	"testing"
)

// Contract: only execute/per-run jobs accept a canonical run parent.
func TestRunParentSupport(t *testing.T) {
	for _, tc := range []struct {
		name, value, policy, action string
		supported                   bool
	}{
		{"valid", `"01K00000000000000000000001"`, "per-run", "execute", true},
		{"number", `42`, "per-run", "execute", false},
		{"null", `null`, "per-run", "execute", false},
		{"short ref", `"abcd"`, "per-run", "execute", false},
		{"invalid uid", `"ZZZZZZZZZZZZZZZZZZZZZZZZZZ"`, "per-run", "execute", false},
		{"existing", `"01K00000000000000000000001"`, "existing", "execute", false},
		{"notify", `"01K00000000000000000000001"`, "existing", "notify", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := fixture(t, "job")
			j.Options = json.RawMessage(`{"run_parent":` + tc.value + `}`)
			if tc.policy == "existing" {
				j.Issue = &IssuePolicy{Kind: "existing", UID: "issue"}
			}
			if tc.action == "notify" {
				j.Action = Action{Kind: "notify", Recipient: "current-owner-or-author"}
			}
			_, s := Classify(j, nil, cfg())
			if s.Supported != tc.supported || len(s.Warnings) != 0 || (!tc.supported && !strings.Contains(strings.Join(s.Reasons, " "), "run_parent")) {
				t.Fatalf("%+v", s)
			}
		})
	}
	j := fixture(t, "native")
	owner, s := Classify(j, nil, cfg())
	if owner != Native || !s.Supported {
		t.Fatal(owner, s)
	}
	j.Options = json.RawMessage(`{"run_parent":"01K00000000000000000000001"}`)
	_, s = Classify(j, nil, cfg())
	if s.Supported {
		t.Fatal("native run_parent accepted")
	}
}

// Ignored option spellings must not select a parent that Classify never checked.
func TestRunParentExactKeys(t *testing.T) {
	for _, tc := range []struct {
		name, options, shape, want string
		warnings                   int
	}{
		{"variant only", `{"Run_Parent":"01K00000000000000000000001"}`, "execute", "", 1},
		{"canonical then variant", `{"run_parent":"01K00000000000000000000001","RUN_PARENT":"01K00000000000000000000002"}`, "execute", "01K00000000000000000000001", 1},
		{"variant then canonical", `{"RUN_PARENT":"01K00000000000000000000002","run_parent":"01K00000000000000000000001"}`, "execute", "01K00000000000000000000001", 1},
		{"duplicate canonical", `{"run_parent":"01K00000000000000000000002","run_parent":"01K00000000000000000000001"}`, "execute", "01K00000000000000000000001", 0},
		{"existing variant", `{"Run_Parent":"01K00000000000000000000001"}`, "existing", "", 1},
		{"notify variant", `{"Run_Parent":"01K00000000000000000000001"}`, "notify", "", 1},
		{"native variant", `{"Run_Parent":"01K00000000000000000000001"}`, "native", "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := fixture(t, "job")
			if tc.shape == "native" {
				j = fixture(t, "native")
			}
			if tc.shape == "existing" || tc.shape == "notify" {
				j.Issue = &IssuePolicy{Kind: "existing", UID: "issue"}
			}
			if tc.shape == "notify" {
				j.Action = Action{Kind: "notify", Recipient: "current-owner-or-author"}
				j.CheckoutKey = ""
			}
			j.Options = json.RawMessage(tc.options)
			_, s := Classify(j, nil, cfg())
			if !s.Supported || len(s.Warnings) != tc.warnings {
				t.Fatalf("support: %+v", s)
			}
			if got := RunParent(j); got != tc.want {
				t.Fatalf("parent = %q, want %q", got, tc.want)
			}
		})
	}
}
