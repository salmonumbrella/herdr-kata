package main

import (
	"context"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"strings"
	"testing"
)

// Task10 owns readonly legacy adoption. Product lookup cannot promote old rows.
func TestNativeWorkflowRemovalDoesNotUsePrivateLegacyDefinitions(t *testing.T) {
	s := workflowStore(t)
	writeWorkflow(t, "nightly", "steps:\n - id: inspect\n   run: true\n")
	if e := s.PutJob(context.Background(), store.Job{ID: "legacy", Name: "Legacy", Workflow: "nightly"}); e != nil {
		t.Fatal(e)
	}
	if e := workflowRemove([]string{"nightly"}); e == nil || !strings.Contains(e.Error(), "ULID") {
		t.Fatalf("legacy removal authority %v", e)
	}
	users, e := jobsUsingWorkflow("nightly")
	if e == nil || !strings.Contains(e.Error(), "refresh native definitions") || len(users) != 0 {
		t.Fatalf("legacy job activated as native workflow user %+v %v", users, e)
	}
}
