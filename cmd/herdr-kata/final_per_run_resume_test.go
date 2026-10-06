package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

func TestPerRunPromptResumesThroughPublicCommandOnlyWhilePreparationPending(t *testing.T) {
	s, dir, herdrDir := policyProduct(t)
	j := policyJob(t, s, store.Job{Name: "Inspect", Kind: "codex", Prompt: "Inspect workspace", CWD: s.Native.Binding.Checkouts["primary"], Schedule: store.ScheduleManual, Timeout: time.Second})
	if err := os.WriteFile(filepath.Join(dir, "create-lose-reply"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(t.Context(), s, j, "manual"); err == nil {
		t.Fatal("creation reply was not lost")
	}
	rows, err := s.Runs(t.Context(), "parked", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("missing preparation row: %+v %v", rows, err)
	}
	rec := rows[0]
	if state := policyHerdr(t, herdrDir); state.Started || len(state.PromptResults) != 0 {
		t.Fatal("preparation launched a prompt")
	}
	c, err := runner.LoadNativeContext(rec.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return runCmd([]string{"resume", rec.ID}) })
	if err != nil || !strings.Contains(out, rec.ID) {
		t.Fatalf("public prompt resume: %q %v", out, err)
	}
	fresh, err := s.Run(t.Context(), rec.ID)
	if err != nil || fresh.Outcome != "done" || fresh.Ref == "" {
		t.Fatalf("prompt did not complete with its issue: %+v %v", fresh, err)
	}
	before := policyHerdr(t, herdrDir)
	if len(before.PromptResults) != 1 {
		t.Fatalf("wanted one prompt: %+v", before)
	}
	if _, err := captureStdout(t, func() error { return runCmd([]string{"resume", rec.ID}) }); err == nil {
		t.Fatal("completed run accepted preparation resume")
	}
	// A caller which fetched the old row before the first resume must also be
	// rejected by the fresh check under this run's execution lock.
	if _, err := runNativeContext(t.Context(), s, c, rec, workflowOpts{OnlyIssuePreparation: true}); err == nil {
		t.Fatal("stale preparation row launched again")
	}
	after := policyHerdr(t, herdrDir)
	if len(after.PromptResults) != len(before.PromptResults) {
		t.Fatal("rejected resume launched another prompt")
	}
}
