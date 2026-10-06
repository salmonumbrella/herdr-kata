package main

import (
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"strings"
	"testing"
	"time"
)

func TestExpiredPerRunPreparationRequiresInspectionWithoutRetryHint(t *testing.T) {
	s, dir, herdrDir := policyProduct(t)
	j := policyJob(t, s, store.Job{Name: "Inspect", Kind: "codex", Prompt: "Inspect workspace", CWD: s.Native.Binding.Checkouts["primary"], Schedule: store.ScheduleManual, Timeout: time.Second})
	uid, err := katacli.NewUID()
	if err != nil {
		t.Fatal(err)
	}
	target := s.Native.Client.Target
	target.Token = ""
	c := runner.NativeExecutionContext{Version: 1, RunUID: uid, Target: target, ProjectUID: s.Native.Binding.ProjectUID, Runtime: j, Job: &katacli.Definition{UID: j.ID, DefinitionEventUID: j.NativeEventUID, Definition: j.NativeDefinition}, IssuePreparedAt: time.Now().Add(-8 * 24 * time.Hour)}
	if err := c.Save(runDirFor(uid)); err != nil {
		t.Fatal(err)
	}
	_, err = runNativeContext(t.Context(), s, c, store.Run{ID: uid, JobID: j.ID, Input: j.Input, RunDir: runDirFor(uid)}, workflowOpts{})
	if err == nil || !strings.Contains(err.Error(), "replay window expired") || !strings.Contains(err.Error(), "inspect") {
		t.Fatalf("expiry diagnostic: %v", err)
	}
	if strings.Contains(err.Error(), "retry with herdr-kata run resume") {
		t.Fatalf("expired intent recommended impossible retry: %v", err)
	}
	for _, call := range policyCalls(t, dir) {
		if len(call.Args) > 0 && call.Args[0] == "create" {
			t.Fatal("expired ambiguous intent repeated issue creation")
		}
	}
	if state := policyHerdr(t, herdrDir); state.Started || len(state.PromptResults) != 0 {
		t.Fatal("expired intent launched a child")
	}
}
