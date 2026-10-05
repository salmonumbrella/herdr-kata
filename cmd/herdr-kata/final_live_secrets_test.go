package main

import (
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Catches accepting a prompt into an existing process whose environment cannot
// receive current mapped secrets. A fresh captured-session process still works.
func TestLiveMappedSecretsRefuseReuseWithoutStoppingConversation(t *testing.T) {
	for _, keep := range []bool{false, true} {
		name := "clear"
		if keep {
			name = "keep"
		}
		t.Run(name, func(t *testing.T) {
			s, _, dir := policyProduct(t)
			s.Native.Binding.Secrets = map[string]string{"service": "first-test-value"}
			j := policyJob(t, s, store.Job{Name: "Inspect", Kind: "codex", Prompt: "Inspect workspace", Ref: policyIssueUID, CWD: s.Native.Binding.Checkouts["primary"], Schedule: store.ScheduleManual, Timeout: time.Second, Persistent: true, KeepContext: keep})
			var doc map[string]any
			if err := katacli.Decode(j.NativeDefinition, &doc); err != nil {
				t.Fatal(err)
			}
			doc["secret_refs"] = map[string]string{"SERVICE_TOKEN": "service"}
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
			firstRun, err := Execute(t.Context(), s, *mapped, "manual")
			if err != nil || firstRun.Outcome != runner.OutcomeDone {
				t.Fatalf("fresh control: %+v %v", firstRun, err)
			}
			before := policyHerdr(t, dir)
			if before.Env["SERVICE_TOKEN"] != "first-test-value" {
				t.Fatal("fresh launch did not receive secret")
			}
			s.Native.Binding.Secrets["service"] = "second-test-value"
			_, err = Execute(t.Context(), s, *mapped, "manual")
			if err == nil || !strings.Contains(err.Error(), "mapped secrets require a fresh agent process") {
				t.Fatalf("live reuse must refuse unsupported environment update: %v", err)
			}
			if strings.Contains(err.Error(), "test-value") {
				t.Fatal("diagnostic exposed secret")
			}
			after := policyHerdr(t, dir)
			if len(after.PromptResults) != len(before.PromptResults) {
				t.Fatal("refused live reuse sent a prompt")
			}
			for _, call := range after.Calls[len(before.Calls):] {
				if len(call) > 1 && ((call[0] == "tab" && call[1] == "close") || (call[0] == "agent" && (call[1] == "stop" || call[1] == "prompt"))) {
					t.Fatalf("refusal disturbed live conversation: %v", call)
				}
			}
			if !keep {
				return
			}
			// Simulate an independently ended process, without terminating it in the
			// product. The recorded harness session starts in a new secret-bearing tab.
			after.Started = false
			after.Session = ""
			after.NextTab = 41
			policyWrite(t, filepath.Join(dir, "herdr.json"), after)
			resumed, err := Execute(t.Context(), s, *mapped, "manual")
			if err != nil || resumed.Outcome != runner.OutcomeDone || resumed.Context != "resumed" {
				t.Fatalf("fresh captured-session resume: %+v %v", resumed, err)
			}
			fresh := policyHerdr(t, dir)
			if fresh.Env["SERVICE_TOKEN"] != "second-test-value" || len(fresh.PromptResults) != len(before.PromptResults)+1 {
				t.Fatal("fresh resume did not receive current secret and one prompt")
			}
		})
	}
}
