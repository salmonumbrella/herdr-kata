package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"github.com/salmonumbrella/herdr-kata/internal/workflow"
)

func TestExecutionPolicyNativeWorkflowRestoresWorkspaceLifecycle(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("requires sh")
	}
	for _, route := range []string{"direct", "job"} {
		t.Run(route, func(t *testing.T) {
			s, _, herdrDir := policyProduct(t)
			draft, err := workflow.NativeDraft(workflow.Workflow{NativeName: "Inspect", Steps: []store.Step{{ID: "first", Run: "printf x >> workspace-count"}, {ID: "second", Run: "test -f ready"}}}, "", "")
			if err != nil {
				t.Fatal(err)
			}
			fd, err := s.Native.Save(t.Context(), draft)
			if err != nil {
				t.Fatal(err)
			}
			cwd := s.Native.Binding.Checkouts["primary"]
			var run *runner.Run
			if route == "job" {
				j := policyJob(t, s, store.Job{Name: "Inspect", Workflow: fd.UID, CWD: cwd})
				run, err = Execute(t.Context(), s, j, "manual")
			} else {
				uid, e := katacli.NewUID()
				if e != nil {
					t.Fatal(e)
				}
				run, err = runWorkflow(t.Context(), s, store.Job{ID: fd.UID, Workflow: fd.UID, CWD: cwd}, store.Run{ID: uid, JobID: fd.UID, Workflow: fd.UID, Trigger: "manual", RunDir: runDirFor(uid)}, workflowOpts{})
			}
			if err != nil || run == nil || run.Outcome == runner.OutcomeDone {
				t.Fatalf("expected shell workflow failure with evidence: %+v %v", run, err)
			}
			rec, err := s.Run(t.Context(), run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if rec.Space == "" {
				t.Fatal("native workflow lost workspace reference")
			}
			state := policyHerdr(t, herdrDir)
			if label := state.Workspaces[rec.Space]; !strings.Contains(label, "parked") {
				t.Fatalf("failed workspace not retained/labeled: %q", label)
			}
			evidence := false
			for _, pane := range state.Tabs {
				if pane["workspace_id"] == rec.Space && pane["cwd"] == run.RunDir {
					evidence = true
				}
			}
			if !evidence {
				t.Fatal("parked workspace lacks run artifact tab")
			}
			if err := os.WriteFile(filepath.Join(cwd, "ready"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := resumeWorkflowRun(s, run.RunID); err != nil {
				t.Fatal(err)
			}
			settled, err := s.Run(t.Context(), run.RunID)
			if err != nil || settled.Outcome != "done" || settled.Space != rec.Space {
				t.Fatalf("saved workspace not retained through resume: %+v %v", settled, err)
			}
			state = policyHerdr(t, herdrDir)
			if _, ok := state.Workspaces[rec.Space]; ok {
				t.Fatal("completed native workflow workspace not closed")
			}
			creates := 0
			renamed, closed := false, false
			for _, call := range state.Calls {
				if len(call) < 2 || call[0] != "workspace" {
					continue
				}
				switch call[1] {
				case "create":
					creates++
				case "rename":
					if len(call) > 2 && call[2] == rec.Space {
						renamed = true
					}
				case "close":
					if len(call) > 2 && call[2] == rec.Space {
						closed = true
					}
				}
			}
			if creates != 1 || !renamed || !closed {
				t.Fatalf("workspace was recreated or lifecycle missing: creates=%d renamed=%v closed=%v", creates, renamed, closed)
			}
			raw, err := os.ReadFile(filepath.Join(cwd, "workspace-count"))
			if err != nil || string(raw) != "x" {
				t.Fatalf("resume replayed completed step: %q %v", raw, err)
			}
		})
	}
}
