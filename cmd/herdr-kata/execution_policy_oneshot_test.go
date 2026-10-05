package main

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/flow"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

func TestExecutionPolicyNativeOnceDeactivatesOnlyAfterOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, status, command string
		want                  runner.Outcome
		prelaunch             bool
	}{
		{name: "prompt-done", want: runner.OutcomeDone},
		{name: "prompt-failed", status: "error", want: runner.OutcomeFailed},
		{name: "prompt-parked", status: "none", want: runner.OutcomeParked},
		{name: "flow-done", command: "true", want: runner.OutcomeDone},
		{name: "flow-parked", command: "false", want: runner.OutcomeParked},
		{name: "prelaunch", prelaunch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.command != "" {
				if _, err := exec.LookPath("sh"); err != nil {
					t.Skip("requires sh")
				}
			}
			s, _, herdrDir := policyProduct(t)
			policyWrite(t, filepath.Join(herdrDir, "herdr.json"), policyHerdrState{Panes: map[string]int{"w1:p9": 123}, PromptStatus: tc.status})
			at := time.Now().Add(time.Hour)
			j := store.Job{Name: "Inspect", Kind: "codex", Prompt: "Inspect workspace", CWD: s.Native.Binding.Checkouts["primary"], Schedule: store.ScheduleOnce, RunAt: &at, Timeout: time.Second}
			if tc.command != "" {
				draft, err := flow.NativeDraft(flow.Flow{NativeName: "Inspect", Steps: []store.Step{{ID: "inspect", Run: tc.command}}}, "", "")
				if err != nil {
					t.Fatal(err)
				}
				fd, err := s.Native.Save(t.Context(), draft)
				if err != nil {
					t.Fatal(err)
				}
				j.Flow = fd.UID
				j.Prompt = ""
			}
			j = policyJob(t, s, j)
			if err := s.SetEnabled(t.Context(), j.ID, true); err != nil {
				t.Fatal(err)
			}
			before, err := s.Native.Client.Definition(t.Context(), "job", j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.prelaunch {
				j.NativeOffline = true
			}
			run, err := Execute(t.Context(), s, j, "manual")
			if tc.prelaunch {
				if err == nil || run != nil {
					t.Fatalf("expected nil prelaunch refusal: %+v %v", run, err)
				}
			} else if err != nil || run == nil || run.Outcome != tc.want {
				t.Fatalf("unexpected actual outcome: %+v %v", run, err)
			}
			active, err := s.Native.IsActivated(j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if active != tc.prelaunch {
				t.Errorf("activation after outcome=%v want %v", active, tc.prelaunch)
			}
			after, err := s.Native.Client.Definition(t.Context(), "job", j.ID)
			if err != nil || before.DefinitionEventUID != after.DefinitionEventUID || string(before.Definition) != string(after.Definition) {
				t.Fatalf("local deactivation edited shared winner: %+v %v", after, err)
			}
			if !tc.prelaunch && tc.want == runner.OutcomeDone {
				again, err := Execute(t.Context(), s, j, "manual")
				if err != nil || again == nil || again.Outcome != runner.OutcomeDone || again.RunID == run.RunID {
					t.Fatalf("inactive once job excluded explicit independent invocation: %+v %v", again, err)
				}
			}
		})
	}
}
