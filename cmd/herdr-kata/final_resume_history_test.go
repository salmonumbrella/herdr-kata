package main

import (
	"encoding/json"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompletedPerRunRefusalPreservesHistory(t *testing.T) {
	s, dir, _ := policyProduct(t)
	j := consumerShellJob(t, s)
	var doc map[string]any
	if err := katacli.Decode(j.NativeDefinition, &doc); err != nil {
		t.Fatal(err)
	}
	doc["issue"] = map[string]any{"kind": "per-run", "title": "Inspect isolated run"}
	draft, err := katacli.NewDraft("job", j.ID, j.Name, policyJSON(t, doc), j.NativeEventUID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Native.Save(t.Context(), draft); err != nil {
		t.Fatal(err)
	}
	mapped, err := s.Job(t.Context(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := Execute(t.Context(), s, *mapped, "manual")
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.Run(t.Context(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Outcome != "done" || before.Ref == "" {
		t.Fatalf("control did not complete: %+v", before)
	}
	policyWrite(t, filepath.Join(dir, "issues.json"), map[string]any{before.Ref: map[string]any{"uid": before.Ref, "project_id": 73, "status": "closed", "author": "worker"}})
	_, err = captureStdout(t, func() error { return workflowCmd([]string{"resume", run.RunID}) })
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("expected readiness refusal, got %v", err)
	}
	after, err := s.Run(t.Context(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatalf("refused modern resume rewrote completed history: before outcome=%q note=%q end=%v; after outcome=%q note=%q end=%v", before.Outcome, before.Note, before.EndedAt, after.Outcome, after.Note, after.EndedAt)
	}
}
