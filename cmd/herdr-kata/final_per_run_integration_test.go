package main

import (
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealPerRunIssuePolicyCreatesAttributedIndependentIssues(t *testing.T) {
	realPolicyHerdr(t)
	client, s := realNativeProduct(t)
	j := consumerShellJob(t, s)
	var doc map[string]any
	if err := katacli.Decode(j.NativeDefinition, &doc); err != nil {
		t.Fatal(err)
	}
	doc["issue"] = map[string]any{"kind": "per-run", "title": "Independent inspection", "body": "Inspect this run only", "scheduled_offset_seconds": -60, "deadline_offset_seconds": 600}
	draft, err := katacli.NewDraft("job", j.ID, j.Name, policyJSON(t, doc), j.NativeEventUID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Native.Save(t.Context(), draft); err != nil {
		t.Fatal(err)
	}
	mapped, err := s.Job(t.Context(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	j = *mapped
	seen := map[string]bool{}
	for range 2 {
		run, err := Execute(t.Context(), s, j, "manual")
		if err != nil {
			realNativeFailure(t, err)
		}
		frozen, err := runner.LoadNativeContext(run.RunDir)
		if err != nil {
			t.Fatal(err)
		}
		if frozen.IssueUID == "" || frozen.IssueUID == policyIssueUID || seen[frozen.IssueUID] {
			t.Fatalf("non-independent per-run identity %q", frozen.IssueUID)
		}
		seen[frozen.IssueUID] = true
		var out struct {
			Issue struct {
				UID, Title, Body, Author string
				Metadata                 map[string]any
			} `json:"issue"`
		}
		if err := client.Show(t.Context(), frozen.IssueUID, &out); err != nil {
			t.Fatal(err)
		}
		if out.Issue.Title != "Independent inspection" || out.Issue.Body != "Inspect this run only" || out.Issue.Author != client.Target.Actor {
			t.Fatalf("ordinary issue policy/attribution lost: %+v", out.Issue)
		}
		for key, offset := range map[string]time.Duration{"scheduled_on": -time.Minute, "deadline_on": 10 * time.Minute} {
			if out.Issue.Metadata[key] != frozen.IssuePreparedAt.Add(offset).UTC().Format(time.RFC3339Nano) {
				t.Fatalf("%s offset lost: %+v", key, out.Issue.Metadata)
			}
		}
	}
	// A future scheduled issue is created and retained but cannot launch early.
	doc["issue"] = map[string]any{"kind": "per-run", "title": "Deferred inspection", "scheduled_offset_seconds": 3600}
	draft, err = katacli.NewDraft("job", j.ID, j.Name, policyJSON(t, doc), j.NativeEventUID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Native.Save(t.Context(), draft); err != nil {
		t.Fatal(err)
	}
	mapped, err = s.Job(t.Context(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(j.CWD, "consumer-count")
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(t.Context(), s, *mapped, "manual"); err == nil || !strings.Contains(err.Error(), "ready list") {
		t.Fatalf("future scheduled issue launched: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("future per-run schedule made process effects")
	}
	// Independent producer validation rejects negative timeout before persistence.
	doc["timeout_seconds"] = -300
	invalid, err := katacli.NewDraft("job", "", "Invalid timeout", policyJSON(t, doc), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Save(t.Context(), invalid); err == nil {
		t.Fatal("native producer accepted negative timeout")
	}
	doc["timeout_seconds"] = 0
	doc["issue"] = map[string]any{"kind": "existing", "uid": ""}
	emptyIssue, err := katacli.NewDraft("job", "", "Invalid issue", policyJSON(t, doc), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Save(t.Context(), emptyIssue); err == nil {
		t.Fatal("native producer accepted empty existing issue UID")
	}
	defs, err := client.Definitions(t.Context(), "job", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, def := range defs {
		if def.UID == invalid.UID || def.UID == emptyIssue.UID {
			t.Fatal("native validation persisted invalid timeout or issue policy")
		}
	}
}
