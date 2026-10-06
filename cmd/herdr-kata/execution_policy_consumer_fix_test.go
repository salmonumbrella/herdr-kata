package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"github.com/salmonumbrella/herdr-kata/internal/workflow"
)

func TestConsumerJobWorkflowResolvedReferenceSavedResume(t *testing.T) {
	for _, tc := range []struct {
		name, original string
		oldRow         bool
	}{
		{"current-empty", "", false}, {"current-different", "01ARZ3NDEKTSV4RRFFQ69G5FAX", false},
		{"old-empty", "", true}, {"old-text", "external ticket ABC", true}, {"old-different", "01ARZ3NDEKTSV4RRFFQ69G5FAX", true},
	} {
		original := tc.original
		t.Run(tc.name, func(t *testing.T) {
			s, dir, _ := policyProduct(t)
			cwd := s.Native.Binding.Checkouts["primary"]
			draft, err := workflow.NativeDraft(workflow.Workflow{NativeName: "Inspect", Steps: []store.Step{{ID: "first", Run: "printf x >> fix-resume-count; printf '%s' \"$HERDR_KATA_REF\" > initial-ref"}, {ID: "second", Run: "test -f ready && printf '%s' \"$HERDR_KATA_INPUT|$HERDR_KATA_REF\" > resumed-values"}}}, "", "")
			if err != nil {
				t.Fatal(err)
			}
			fd, err := s.Native.Save(t.Context(), draft)
			if err != nil {
				t.Fatal(err)
			}
			j := policyJob(t, s, store.Job{Name: "Inspect", CWD: cwd, Workflow: fd.UID, Ref: original, Input: "retained input", Timeout: time.Second, Schedule: store.ScheduleManual})
			def, err := s.Native.Client.Definition(t.Context(), "job", j.ID)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]json.RawMessage
			if err := katacli.Decode(def.Definition, &body); err != nil {
				t.Fatal(err)
			}
			body["issue"] = policyJSON(t, map[string]any{"kind": "existing", "uid": policyIssueUID})
			def.Definition = policyJSON(t, body)
			changed, err := katacli.NewDraft("job", j.ID, j.Name, def.Definition, def.DefinitionEventUID)
			if err != nil {
				t.Fatal(err)
			}
			jd, err := s.Native.Save(t.Context(), changed)
			if err != nil {
				t.Fatal(err)
			}
			j2, err := s.Job(t.Context(), j.ID)
			if err != nil {
				t.Fatal(err)
			}
			j = *j2
			fire := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
			ctx := context.WithValue(t.Context(), nativeFireTime{}, fire)
			first, err := Execute(ctx, s, j, "scheduled")
			if err != nil || first == nil || first.Outcome == runner.OutcomeDone {
				t.Fatalf("first workflow must fail after one real step: %+v %v", first, err)
			}
			rec, err := s.Run(t.Context(), first.RunID)
			if err != nil {
				t.Fatal(err)
			}
			frozen, err := runner.LoadNativeContext(rec.RunDir)
			if err != nil {
				t.Fatal(err)
			}
			if rec.Ref != policyIssueUID || frozen.IssueUID != policyIssueUID || frozen.Occurrence != fire.Format(time.RFC3339Nano) || rec.Input != "retained input" {
				t.Fatalf("incorrect initial linkage/intent: %+v snapshot=%+v", rec, frozen)
			}
			initialRef, _ := os.ReadFile(filepath.Join(cwd, "initial-ref"))
			if frozen.Runtime.Ref != policyIssueUID || string(initialRef) != policyIssueUID {
				t.Fatalf("initial runtime reference differs from actual linkage: runtime=%q process=%q", frozen.Runtime.Ref, initialRef)
			}
			if tc.oldRow {
				// The previous ordinary v1 binary saved BOTH the row and frozen
				// Runtime.Ref as the raw executor option. IssueUID is separate.
				rec.Ref = original
				if err := s.PutRun(t.Context(), *rec); err != nil {
					t.Fatal(err)
				}
				frozen.Runtime.Ref = original
				policyWrite(t, filepath.Join(rec.RunDir, "native-context.json"), frozen)
				old, err := s.Run(t.Context(), rec.ID)
				if err != nil || old.Ref != original {
					t.Fatalf("invalid old-row fixture: %+v %v", old, err)
				}
			} else if original != "" {
				// Retain the prior mixed-shape control: row already projected,
				// while the frozen runtime contains the original option.
				frozen.Runtime.Ref = original
				policyWrite(t, filepath.Join(rec.RunDir, "native-context.json"), frozen)
			}
			contextBytes, err := os.ReadFile(filepath.Join(rec.RunDir, "native-context.json"))
			if err != nil {
				t.Fatal(err)
			}
			// Mutate both shared definitions, then delete them. Resume must not refresh
			// intent or run the changed command, and must retain the original input/ref.
			var changedWorkflow map[string]json.RawMessage
			if err := katacli.Decode(fd.Definition, &changedWorkflow); err != nil {
				t.Fatal(err)
			}
			changedWorkflow["steps"] = policyJSON(t, []map[string]any{{"key": "replacement", "kind": "command", "command": "printf rejected > changed-definition"}})
			fdDraft, err := katacli.NewDraft("workflow", fd.UID, fd.Name, policyJSON(t, changedWorkflow), fd.DefinitionEventUID)
			if err != nil {
				t.Fatal(err)
			}
			editedWorkflow, err := s.Native.Save(t.Context(), fdDraft)
			if err != nil {
				t.Fatal(err)
			}
			body["issue"] = policyJSON(t, map[string]any{"kind": "existing", "uid": "01ARZ3NDEKTSV4RRFFQ69G5FAY"})
			body["action"] = policyJSON(t, map[string]any{"kind": "execute", "prompt": "Changed shared prompt"})
			jdDraft, err := katacli.NewDraft("job", j.ID, j.Name, policyJSON(t, body), jd.DefinitionEventUID)
			if err != nil {
				t.Fatal(err)
			}
			editedJob, err := s.Native.Save(t.Context(), jdDraft)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Native.Delete(t.Context(), "job", editedJob); err != nil {
				t.Fatal(err)
			}
			if err := s.Native.Delete(t.Context(), "workflow", editedWorkflow); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cwd, "ready"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if tc.oldRow {
				// Projection is an internal compatibility step, not permission
				// for an explicit caller override of the stored old row.
				bad := *rec
				bad.Ref = policyIssueUID
				if _, err := runWorkflow(t.Context(), s, j, bad, workflowOpts{}); err == nil || !strings.Contains(err.Error(), "immutable") {
					t.Fatalf("caller replaced old row reference: %v", err)
				}
			}
			if err := resumeWorkflowRun(s, rec.ID); err != nil {
				t.Fatalf("unchanged saved job workflow could not recover after shared edits/deletion: %v", err)
			}
			got, err := s.Run(t.Context(), rec.ID)
			if err != nil || got.Outcome != "done" || got.Ref != policyIssueUID || got.Input != rec.Input {
				t.Fatalf("saved resume changed intent or failed: %+v %v", got, err)
			}
			count, _ := os.ReadFile(filepath.Join(cwd, "fix-resume-count"))
			values, _ := os.ReadFile(filepath.Join(cwd, "resumed-values"))
			if string(count) != "x" || string(values) != "retained input|"+policyIssueUID {
				t.Fatalf("steps/ref/input not preserved: count=%q values=%q", count, values)
			}
			if _, err := os.Stat(filepath.Join(cwd, "changed-definition")); !os.IsNotExist(err) {
				t.Fatal("mutable shared workflow was executed")
			}
			after, err := runner.LoadNativeContext(rec.RunDir)
			if err != nil || after.Job.DefinitionEventUID != jd.DefinitionEventUID || after.Workflow.DefinitionEventUID != fd.DefinitionEventUID || after.Occurrence != frozen.Occurrence {
				t.Fatalf("frozen winners changed: %+v %v", after, err)
			}
			// A real attempted change must fail before process execution and preserve row.
			for _, fault := range []string{"reference", "original-reference", "input"} {
				bad := *got
				switch fault {
				case "reference":
					bad.Ref = "01ARZ3NDEKTSV4RRFFQ69G5FAY"
				case "original-reference":
					bad.Ref = original
				case "input":
					bad.Input = "changed input"
				}
				if _, err := runWorkflow(t.Context(), s, j, bad, workflowOpts{}); err == nil || !strings.Contains(err.Error(), "immutable") {
					t.Fatalf("changed %s accepted: %v", fault, err)
				}
			}
			unchanged, err := os.ReadFile(filepath.Join(rec.RunDir, "native-context.json"))
			if err != nil || string(unchanged) != string(contextBytes) {
				t.Fatal("resume rewrote immutable context")
			}
			policyAssertNoHandshake(t, policyCalls(t, dir))
		})
	}
}

func TestConsumerPerRunIssueReplacesLocalTextReference(t *testing.T) {
	s, _, _ := policyProduct(t)
	cwd := s.Native.Binding.Checkouts["primary"]
	draft, err := workflow.NativeDraft(workflow.Workflow{NativeName: "Inspect", Steps: []store.Step{{ID: "inspect", Run: "printf '%s' \"$HERDR_KATA_REF\" > local-reference"}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	fd, err := s.Native.Save(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	ref := "external ticket ABC"
	j := policyJob(t, s, store.Job{Name: "Inspect", CWD: cwd, Workflow: fd.UID, Ref: ref, Schedule: store.ScheduleManual, Timeout: time.Second})
	first, err := Execute(t.Context(), s, j, "manual")
	if err != nil || first == nil || first.Outcome != runner.OutcomeDone {
		t.Fatalf("local reference execution: %+v %v", first, err)
	}
	rec, err := s.Run(t.Context(), first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := runner.LoadNativeContext(rec.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(cwd, "local-reference"))
	if frozen.IssueUID == "" || rec.Ref != frozen.IssueUID || frozen.Runtime.Ref != "" || string(raw) != frozen.IssueUID {
		t.Fatalf("per-run issue did not replace local reference: row=%q runtime=%q issue=%q process=%q", rec.Ref, frozen.Runtime.Ref, frozen.IssueUID, raw)
	}
	dto := nativeObservation(frozen, "succeeded", first.StartedAt, first.EndedAt)
	if dto.IssueUID != frozen.IssueUID {
		t.Fatalf("created issue identity missing from portable evidence: %+v", dto)
	}
	for _, field := range []string{"reference", "input"} {
		bad := *rec
		if field == "reference" {
			bad.Ref = "changed external ticket"
		} else {
			bad.Input = "changed input"
		}
		if _, err := runWorkflow(t.Context(), s, j, bad, workflowOpts{}); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("local reference %s override accepted: %v", field, err)
		}
	}
}
