package main

import (
	"context"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"strings"
	"testing"
)

// Task10 owns readonly legacy adoption. Product lookup cannot promote old rows.
func TestNativeFlowRemovalDoesNotUsePrivateLegacyDefinitions(t *testing.T) {
	s := flowStore(t)
	writeFlow(t, "nightly", "steps:\n - id: inspect\n   run: true\n")
	if e := s.PutJob(context.Background(), store.Job{ID: "legacy", Name: "Legacy", Flow: "nightly"}); e != nil {
		t.Fatal(e)
	}
	if e := flowRemove([]string{"nightly"}); e == nil || !strings.Contains(e.Error(), "ULID") {
		t.Fatalf("legacy removal authority %v", e)
	}
	users, e := jobsUsingFlow("nightly")
	if e == nil || !strings.Contains(e.Error(), "refresh native definitions") || len(users) != 0 {
		t.Fatalf("legacy job activated as native flow user %+v %v", users, e)
	}
}
