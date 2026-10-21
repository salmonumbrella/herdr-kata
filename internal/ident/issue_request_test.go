package ident

import "testing"

func TestIssueRequestUIDUsesChildIdentity(t *testing.T) {
	got, err := IssueRequestUID("01K00000000000000000000001")
	if err != nil || got != "01K00000003CJTVSTHD1P8EVCV" {
		t.Fatalf("identity %q: %v", got, err)
	}
	other, err := IssueRequestUID("01K00000000000000000000002")
	if err != nil || other == got {
		t.Fatal("distinct children share a run", other, err)
	}
	for _, invalid := range []string{"", "01k00000000000000000000001", "81K00000000000000000000001", "../../run"} {
		if _, err := IssueRequestUID(invalid); err == nil {
			t.Fatalf("invalid issue UID accepted: %q", invalid)
		}
	}
}
