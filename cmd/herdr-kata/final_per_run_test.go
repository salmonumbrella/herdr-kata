package main

import (
	"encoding/json"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPerRunIssueLostReplyResumesFrozenIdentityAndOffsets(t *testing.T) {
	s, dir, _ := policyProduct(t)
	j := consumerShellJob(t, s)
	var doc map[string]any
	if err := katacli.Decode(j.NativeDefinition, &doc); err != nil {
		t.Fatal(err)
	}
	doc["issue"] = map[string]any{"kind": "per-run", "title": "Inspect new run", "body": "Independent run body", "scheduled_offset_seconds": -60, "deadline_offset_seconds": 600}
	draft, err := katacli.NewDraft("job", j.ID, j.Name, policyJSON(t, doc), j.NativeEventUID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Native.Save(t.Context(), draft); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Job(t.Context(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	j = *saved
	if err := os.WriteFile(filepath.Join(dir, "create-lose-reply"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = Execute(t.Context(), s, j, "manual")
	if err == nil {
		t.Fatal("per-run creation never attempted the lost-reply boundary")
	}
	paths, _ := filepath.Glob(filepath.Join(stateDir(), "runs", "*", "native-context.json"))
	if len(paths) != 1 {
		t.Fatalf("frozen intent missing: %v", paths)
	}
	frozenBytes, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	c, err := runner.LoadNativeContext(filepath.Dir(paths[0]))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := s.Run(t.Context(), c.RunUID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(j.CWD, "consumer-count")); !os.IsNotExist(err) {
		t.Fatal("lost reply launched child")
	}
	if err := recoverMissingObservations(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(nativeDeliveryPath(rec.RunDir)); !os.IsNotExist(err) {
		t.Errorf("pre-launch preparation published incomplete shared run identity: %v", err)
	}
	result, err := runNativeFlow(t.Context(), s, j, *rec, flowOpts{})
	if err != nil || result == nil || result.Outcome != runner.OutcomeDone {
		t.Fatalf("same-run recovery: %+v %v", result, err)
	}
	c, err = runner.LoadNativeContext(rec.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	if c.IssueUID == "" || c.IssueUID == policyIssueUID {
		t.Fatalf("per-run used local ref instead of created identity: %q", c.IssueUID)
	}
	after, err := os.ReadFile(paths[0])
	if err != nil || string(after) != string(frozenBytes) {
		t.Fatal("recovery rewrote frozen intent")
	}
	var rows map[string]struct {
		UID, Title, Body string
		Metadata         map[string]string
	}
	raw, err := os.ReadFile(filepath.Join(dir, "created-issues.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("lost reply duplicated creation: %+v", rows)
	}
	for _, row := range rows {
		if row.UID != c.IssueUID || row.Title != "Inspect new run" || row.Body != "Independent run body" {
			t.Fatalf("creation policy lost: %+v", row)
		}
		scheduled, e := time.Parse(time.RFC3339Nano, row.Metadata["scheduled_on"])
		if e != nil {
			t.Fatal(e)
		}
		deadline, e := time.Parse(time.RFC3339Nano, row.Metadata["deadline_on"])
		if e != nil {
			t.Fatal(e)
		}
		if deadline.Sub(scheduled) != 660*time.Second {
			t.Fatalf("offsets changed: %+v", row.Metadata)
		}
	}
	// Another explicit invocation gets its own issue even for the same definition.
	if err := os.Remove(filepath.Join(dir, "create-lose-reply")); err != nil {
		t.Fatal(err)
	}
	next, err := Execute(t.Context(), s, j, "manual")
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.LoadNativeContext(next.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	if second.IssueUID == c.IssueUID {
		t.Fatal("independent runs shared issue identity")
	}
	for _, call := range policyCalls(t, dir) {
		if slices.Contains(call.Args, "create") && !slices.Contains(call.Args, "automation") {
			if !strings.Contains(strings.Join(call.Args, " "), "--force-new") {
				t.Fatal("independent issue create omitted ordinary lookalike override")
			}
		}
	}
}

func TestExistingIssuePolicyWithoutUIDFailsBeforeLaunch(t *testing.T) {
	s, _, _ := policyProduct(t)
	j := consumerShellJob(t, s)
	j.NativeDefinition = policyExistingIssueDefinition(t, j.NativeDefinition, "")
	if _, err := Execute(t.Context(), s, j, "manual"); err == nil || !strings.Contains(err.Error(), "existing issue policy") {
		t.Fatalf("malformed existing policy was accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(j.CWD, "consumer-count")); !os.IsNotExist(err) {
		t.Fatal("invalid policy launched child")
	}
}

func TestOldPerRunContextMissingCreationIntentPreservesCompletedHistory(t *testing.T) {
	s, _, _ := policyProduct(t)
	j := consumerShellJob(t, s)
	var doc map[string]any
	if err := katacli.Decode(j.NativeDefinition, &doc); err != nil {
		t.Fatal(err)
	}
	doc["issue"] = map[string]any{"kind": "per-run", "title": "Inspect retained run"}
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
	j.Ref, j.Input = "external ticket ABC", "retained input"
	fd, err := s.Native.Client.Definition(t.Context(), "flow", j.Flow)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := katacli.NewUID()
	if err != nil {
		t.Fatal(err)
	}
	target := s.Native.Client.Target
	target.Token = ""
	c := runner.NativeExecutionContext{Version: 1, RunUID: uid, Target: target, ProjectUID: s.Native.Binding.ProjectUID, Runtime: j, Job: &katacli.Definition{UID: j.ID, DefinitionEventUID: j.NativeEventUID, Definition: j.NativeDefinition}, Flow: &fd}
	dir := runDirFor(uid)
	if err := c.Save(dir); err != nil {
		t.Fatal(err)
	}
	ended := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	rec := store.Run{ID: uid, JobID: j.ID, Trigger: "manual", Flow: j.Flow, Ref: j.Ref, Input: j.Input, RunDir: dir, Outcome: "done", Note: "Retained result", StartedAt: ended.Add(-time.Minute), EndedAt: &ended}
	if err := s.PutRun(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	before, err := s.Run(t.Context(), uid)
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(dir, "result.json")
	if err := os.WriteFile(resultPath, []byte("{\"status\":\"ok\",\"summary\":\"Retained result\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	artifacts := map[string][]byte{}
	for _, path := range []string{filepath.Join(dir, "native-context.json"), resultPath} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		artifacts[path] = raw
	}
	_, err = captureStdout(t, func() error { return flowCmd([]string{"resume", uid}) })
	if err == nil || !strings.Contains(err.Error(), "lacks its creation time; inspect this local run") {
		t.Fatalf("missing old-intent diagnostic: %v", err)
	}
	after, err := s.Run(t.Context(), uid)
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatalf("refused resume rewrote historical row:\nbefore %s\nafter %s", beforeJSON, afterJSON)
	}
	for path, want := range artifacts {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(want) {
			t.Fatalf("refused resume changed artifact %s: %v", filepath.Base(path), err)
		}
	}
	if _, err := os.Stat(filepath.Join(j.CWD, "consumer-count")); !os.IsNotExist(err) {
		t.Fatal("refused old context launched child")
	}
}
