package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdrcli"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

func TestExecutionPolicyPersistentProvenanceWithoutSessionCapture(t *testing.T) {
	for _, keep := range []bool{false, true} {
		name := "clear"
		if keep {
			name = "keep"
		}
		t.Run(name, func(t *testing.T) {
			s, _, herdrDir := policyProduct(t)
			policyWrite(t, filepath.Join(herdrDir, "herdr.json"), policyHerdrState{Panes: map[string]int{"w1:p9": 123}})
			j := policyJob(t, s, store.Job{Name: "Inspect", Kind: "codex", Prompt: "Inspect workspace", CWD: s.Native.Binding.Checkouts["primary"], Persistent: true, KeepContext: keep, Timeout: time.Second})
			for i := 0; i < 2; i++ {
				run, err := Execute(t.Context(), s, j, "manual")
				if err != nil || run == nil || run.Outcome != runner.OutcomeDone {
					t.Fatalf("unchanged local provenance cannot reuse live process: %+v %v", run, err)
				}
			}
			if _, err := s.JobSession(t.Context(), j.ID); err == nil {
				t.Fatal("fixture unexpectedly captured harness session")
			}
			before := len(policyHerdr(t, herdrDir).PromptResults)
			for _, field := range []string{"actor", "server", "project", "teammate", "checkout"} {
				target := s.Native.Client.Target
				originalCWD := j.CWD
				originalMapping := s.Native.Binding.Checkouts["primary"]
				switch field {
				case "actor":
					s.Native.Client.Target.Actor = "other-worker"
				case "server":
					s.Native.Client.Target.Server = "http://127.0.0.1:8888"
				case "project":
					s.Native.Client.Target.Project = "other-project"
				case "teammate":
					s.Native.Client.Target.Teammate = "other-task"
				case "checkout":
					j.CWD = t.TempDir()
					s.Native.Binding.Checkouts["primary"] = j.CWD
				}
				run, err := Execute(t.Context(), s, j, "manual")
				s.Native.Client.Target = target
				j.CWD = originalCWD
				s.Native.Binding.Checkouts["primary"] = originalMapping
				if err == nil {
					t.Errorf("changed %s reached persistent process: %+v", field, run)
				}
				if got := len(policyHerdr(t, herdrDir).PromptResults); got != before {
					t.Errorf("changed %s sent a reused prompt: %d want %d", field, got, before)
					before = got
				}
			}
			state := policyHerdr(t, herdrDir)
			starts, clears := 0, 0
			for _, call := range state.Calls {
				if len(call) > 1 && call[0] == "agent" {
					if call[1] == "start" {
						starts++
					}
					if call[1] == "prompt" && len(call) > 3 && call[3] == "/clear" {
						clears++
					}
				}
			}
			if starts != 1 {
				t.Errorf("legitimate reuse restarted process: starts=%d", starts)
			}
			if !keep && clears == 0 {
				t.Error("KeepContext=false lost ordinary clearing")
			}
			last, err := s.LastRun(t.Context(), j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(last.RunDir, "native-context.json")); err != nil {
				t.Fatal(err)
			}
			if _, err := Execute(t.Context(), s, j, "manual"); err == nil {
				t.Error("missing local provenance allowed reuse")
			}
			if got := len(policyHerdr(t, herdrDir).PromptResults); got != before {
				t.Error("missing provenance sent a prompt")
			}
		})
	}
}

func TestExecutionPolicyUnknownLiveConversationCannotBecomeProvenance(t *testing.T) {
	s, _, herdrDir := policyProduct(t)
	policyWrite(t, filepath.Join(herdrDir, "herdr.json"), policyHerdrState{Panes: map[string]int{"w1:p9": 123}})
	j := policyJob(t, s, store.Job{Name: "Inspect", Kind: "codex", Prompt: "Inspect workspace", CWD: s.Native.Binding.Checkouts["primary"], Persistent: true, Timeout: time.Second})
	// An upstream live process exists without a native run/context record.
	// The fixture is created by actual Runner behavior, not fabricated naming.
	control := runner.FromStore(j)
	control.WorkspaceID = "w1"
	r := runner.Runner{Herdr: herdrcli.New(), Store: s, StateDir: t.TempDir()}
	uid, err := katacli.NewUID()
	if err != nil {
		t.Fatal(err)
	}
	run, err := r.Execute(t.Context(), control, uid)
	if err != nil || run.Outcome != runner.OutcomeDone {
		t.Fatalf("fixture control: %+v %v", run, err)
	}
	before := len(policyHerdr(t, herdrDir).PromptResults)
	for i := 0; i < 2; i++ {
		if _, err := Execute(t.Context(), s, j, "manual"); err == nil {
			t.Fatal("unrecorded live process adopted")
		}
		if got := len(policyHerdr(t, herdrDir).PromptResults); got != before {
			t.Fatal("failed adoption created reusable provenance")
		}
	}
}

func TestExecutionPolicyReturnedConversationBinding(t *testing.T) {
	for _, keep := range []bool{false, true} {
		t.Run(map[bool]string{false: "clear", true: "keep"}[keep], func(t *testing.T) {
			s, _, dir := policyProduct(t)
			// No session capture: actual stable tab/name provenance must suffice.
			policyWrite(t, filepath.Join(dir, "herdr.json"), policyHerdrState{Panes: map[string]int{"w1:p9": 123}})
			j := policyJob(t, s, store.Job{Name: "Inspect", Kind: "codex", Prompt: "Inspect workspace", CWD: s.Native.Binding.Checkouts["primary"], Persistent: true, KeepContext: keep, Timeout: time.Second})
			if run, err := Execute(t.Context(), s, j, "manual"); err != nil || run.Outcome != runner.OutcomeDone {
				t.Fatalf("first run: %+v %v", run, err)
			}
			original := policyHerdr(t, dir)
			replacement := original
			replacement.TabID, replacement.PaneID = "w1:t42", "w1:p42"
			replacement.Env = map[string]string{"HERDR_KATA_RUN_DIR": t.TempDir(), "KATA_SERVER": "http://127.0.0.1:8888"}
			policyWrite(t, filepath.Join(dir, "herdr.json"), replacement)
			before := len(original.Calls)
			for attempt := 0; attempt < 2; attempt++ {
				_, err := Execute(t.Context(), s, j, "manual")
				after := policyHerdr(t, dir)
				prompts := 0
				for _, call := range after.Calls[before:] {
					if len(call) > 1 && call[0] == "agent" && call[1] == "prompt" {
						prompts++
					}
				}
				if err == nil || prompts != 0 {
					t.Fatalf("replacement received work or laundered provenance: err=%v prompts=%d", err, prompts)
				}
				before = len(after.Calls)
			}
			// Refusing the replacement must not erase the actual old conversation.
			policyWrite(t, filepath.Join(dir, "herdr.json"), original)
			if run, err := Execute(t.Context(), s, j, "manual"); err != nil || run.Outcome != runner.OutcomeDone {
				t.Fatalf("legitimate conversation after refusal: %+v %v", run, err)
			}
		})
	}
}

func TestExecutionPolicyVerifiedConversationRestoration(t *testing.T) {
	for _, mode := range []string{"adopted", "resumed"} {
		t.Run(mode, func(t *testing.T) {
			s, _, dir := policyProduct(t)
			j := policyJob(t, s, store.Job{Name: "Inspect", Kind: "codex", Prompt: "Inspect workspace", CWD: s.Native.Binding.Checkouts["primary"], Persistent: true, KeepContext: true, Timeout: time.Second})
			if run, err := Execute(t.Context(), s, j, "manual"); err != nil || run.Outcome != runner.OutcomeDone {
				t.Fatalf("first run: %+v %v", run, err)
			}
			state := policyHerdr(t, dir)
			if mode == "adopted" {
				state.Name = "restored-agent"
				state.TabID, state.PaneID = "w1:t42", "w1:p42"
			} else {
				state.Started = false
				state.NextTab = 41
			}
			policyWrite(t, filepath.Join(dir, "herdr.json"), state)
			run, err := Execute(t.Context(), s, j, "manual")
			if err != nil || run.Outcome != runner.OutcomeDone || run.Context != mode {
				t.Fatalf("verified restoration: %+v %v", run, err)
			}
		})
	}
}

func TestExecutionPolicyWorkingConversationReplacement(t *testing.T) {
	for _, keep := range []bool{false, true} {
		t.Run(map[bool]string{false: "clear", true: "keep"}[keep], func(t *testing.T) {
			s, _, dir := policyProduct(t)
			j := policyJob(t, s, store.Job{Name: "Inspect", Kind: "codex", Prompt: "Inspect workspace", CWD: s.Native.Binding.Checkouts["primary"], Persistent: true, KeepContext: keep, Timeout: time.Second})
			if run, err := Execute(t.Context(), s, j, "manual"); err != nil || run.Outcome != runner.OutcomeDone {
				t.Fatalf("first run: %+v %v", run, err)
			}
			original := policyHerdr(t, dir)
			working := original
			working.AgentStatus = "working"
			working.WaitTabID, working.WaitPaneID = "w1:t42", "w1:p42"
			policyWrite(t, filepath.Join(dir, "herdr.json"), working)
			_, err := Execute(t.Context(), s, j, "manual")
			after := policyHerdr(t, dir)
			for _, call := range after.Calls[len(original.Calls):] {
				if len(call) > 1 && call[0] == "agent" && call[1] == "prompt" {
					t.Errorf("replacement after wait received prompt: %v", call[:3])
				}
			}
			if err == nil {
				t.Error("replacement after wait was accepted")
			}
			policyWrite(t, filepath.Join(dir, "herdr.json"), original)
			if run, err := Execute(t.Context(), s, j, "manual"); err != nil || run.Outcome != runner.OutcomeDone {
				t.Fatalf("old conversation after wait refusal: %+v %v", run, err)
			}
		})
	}
}

func TestExecutionPolicyRestoreAfterBlockedAttempt(t *testing.T) {
	for _, mode := range []string{"adopted", "resumed"} {
		t.Run(mode, func(t *testing.T) {
			s, _, dir := policyProduct(t)
			j := policyJob(t, s, store.Job{Name: "Inspect", Kind: "codex", Prompt: "Inspect workspace", CWD: s.Native.Binding.Checkouts["primary"], Persistent: true, KeepContext: true, Timeout: time.Second})
			first, err := Execute(t.Context(), s, j, "manual")
			if err != nil || first.Outcome != runner.OutcomeDone {
				t.Fatalf("capture run: %+v %v", first, err)
			}
			captured, err := s.JobSession(t.Context(), j.ID)
			if err != nil || captured.RunID != first.RunID {
				t.Fatalf("no captured provenance: %+v %v", captured, err)
			}
			state := policyHerdr(t, dir)
			before := len(state.PromptResults)
			state.AgentStatus = "blocked"
			policyWrite(t, filepath.Join(dir, "herdr.json"), state)
			blocked, err := Execute(t.Context(), s, j, "manual")
			if err != nil || blocked.Outcome != runner.OutcomeParked || blocked.ParkReason != runner.ParkBlocked {
				t.Fatalf("blocked attempt: %+v %v", blocked, err)
			}
			retained, err := s.JobSession(t.Context(), j.ID)
			if err != nil || retained.RunID != captured.RunID || retained.RunID == blocked.RunID {
				t.Fatalf("blocked attempt changed capture: %+v %v", retained, err)
			}
			selected, err := s.LastConversationRun(t.Context(), j.ID)
			if err != nil || selected.ID != blocked.RunID {
				t.Fatalf("blocked row not selected: %+v %v", selected, err)
			}
			state = policyHerdr(t, dir)
			if len(state.PromptResults) != before {
				t.Fatal("blocked attempt prompted conversation")
			}
			state.AgentStatus = "idle"
			if mode == "adopted" {
				state.Name = "restored-agent"
				state.TabID, state.PaneID = "w1:t42", "w1:p42"
			} else {
				state.Started = false
				state.NextTab = 41
			}
			policyWrite(t, filepath.Join(dir, "herdr.json"), state)
			restored, err := Execute(t.Context(), s, j, "manual")
			if err != nil || restored.Outcome != runner.OutcomeDone || restored.Context != mode || restored.TabID != "w1:t42" {
				t.Fatalf("verified %s after blocked attempt: %+v %v", mode, restored, err)
			}
			after := policyHerdr(t, dir)
			if len(after.PromptResults) != before+1 {
				t.Fatal("restored conversation did not receive exactly one fresh prompt")
			}
			latest, err := s.JobSession(t.Context(), j.ID)
			if err != nil || latest.RunID != restored.RunID || latest.Value != captured.Value {
				t.Fatalf("restored capture not refreshed: %+v %v", latest, err)
			}
		})
	}
}

func TestExecutionPolicyRestorationRequiresCapturingRunProvenance(t *testing.T) {
	s, _, dir := policyProduct(t)
	j := policyJob(t, s, store.Job{Name: "Inspect", Kind: "codex", Prompt: "Inspect workspace", CWD: s.Native.Binding.Checkouts["primary"], Persistent: true, KeepContext: true, Timeout: time.Second})
	first, err := Execute(t.Context(), s, j, "manual")
	if err != nil || first.Outcome != runner.OutcomeDone {
		t.Fatalf("capture: %+v %v", first, err)
	}
	captured, err := s.JobSession(t.Context(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	record, err := s.Run(t.Context(), captured.RunID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(record.RunDir, "native-context.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	state := policyHerdr(t, dir)
	state.AgentStatus = "blocked"
	policyWrite(t, filepath.Join(dir, "herdr.json"), state)
	if run, err := Execute(t.Context(), s, j, "manual"); err != nil || run.ParkReason != runner.ParkBlocked {
		t.Fatalf("blocked: %+v %v", run, err)
	}
	state = policyHerdr(t, dir)
	before := len(state.PromptResults)
	state.AgentStatus = "idle"
	state.Name = "restored-agent"
	state.TabID, state.PaneID = "w1:t42", "w1:p42"
	policyWrite(t, filepath.Join(dir, "herdr.json"), state)
	for _, fault := range []string{"missing-run", "missing-context", "run-identity", "job-identity", "actor", "teammate", "origin", "project", "checkout", "captured-value"} {
		t.Run(fault, func(t *testing.T) {
			proof, err := runner.LoadNativeContext(record.RunDir)
			if err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "missing-run":
				bad := *captured
				bad.RunID = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
				if err := s.PutJobSession(t.Context(), bad); err != nil {
					t.Fatal(err)
				}
			case "missing-context":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "run-identity":
				proof.RunUID = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
			case "job-identity":
				proof.Job.UID = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
			case "actor":
				proof.Target.Actor = "other-worker"
			case "teammate":
				proof.Target.Teammate = "other-task"
			case "origin":
				proof.Target.Server = "http://127.0.0.1:8888"
			case "project":
				proof.ProjectUID = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
			case "checkout":
				proof.Runtime.CWD = t.TempDir()
			case "captured-value":
				bad := *record
				bad.ContextSession = "other-session"
				if err := s.PutRun(t.Context(), bad); err != nil {
					t.Fatal(err)
				}
			}
			if fault != "missing-context" {
				policyWrite(t, path, proof)
			}
			_, err = Execute(t.Context(), s, j, "manual")
			if err == nil {
				t.Errorf("%s capture provenance accepted", fault)
			}
			if len(policyHerdr(t, dir).PromptResults) != before {
				t.Errorf("%s capture proof sent work", fault)
			}
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := s.PutJobSession(t.Context(), *captured); err != nil {
				t.Fatal(err)
			}
			if err := s.PutRun(t.Context(), *record); err != nil {
				t.Fatal(err)
			}
		})
	}
	// Invalid proof attempts must not erase the legitimate capturing run/session.
	run, err := Execute(t.Context(), s, j, "manual")
	if err != nil || run.Outcome != runner.OutcomeDone || run.Context != "adopted" {
		t.Fatalf("restoration after proof repair: %+v %v", run, err)
	}
}
