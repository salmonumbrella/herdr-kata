package main

import (
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnsupportedPortablePoliciesStopActivationAndLaunch(t *testing.T) {
	s, _, _ := policyProduct(t)
	baseline := consumerShellJob(t, s)
	for _, tc := range []struct {
		field string
		value any
	}{{"overlap", "allow"}, {"grace_seconds", 30}} {
		t.Run(tc.field, func(t *testing.T) {
			var raw map[string]any
			if err := katacli.Decode(baseline.NativeDefinition, &raw); err != nil {
				t.Fatal(err)
			}
			raw[tc.field] = tc.value
			draft, err := katacli.NewDraft("job", "", tc.field, policyJSON(t, raw), "")
			if err != nil {
				t.Fatal(err)
			}
			peer, err := s.Native.Save(t.Context(), draft)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Native.JobFrom(peer, false); err == nil {
				t.Error("unsupported peer policy projected")
			}
			if err := s.SetEnabled(t.Context(), peer.UID, true); err == nil {
				t.Error("unsupported peer policy activated")
			}
			marker := filepath.Join(baseline.CWD, "consumer-count")
			os.Remove(marker)
			j := baseline
			j.ID = peer.UID
			j.NativeEventUID = peer.DefinitionEventUID
			j.NativeDefinition = peer.Definition
			if _, err := Execute(t.Context(), s, j, "manual"); err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Errorf("unsupported policy launched: %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Error("unsupported policy made process effects")
			}
		})
	}
	fd, err := s.Native.Client.Definition(t.Context(), "workflow", baseline.Workflow)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := katacli.Decode(fd.Definition, &doc); err != nil {
		t.Fatal(err)
	}
	doc["steps"].([]any)[0].(map[string]any)["retries"] = 2
	draft, err := katacli.NewDraft("workflow", "", "Retry peer", policyJSON(t, doc), "")
	if err != nil {
		t.Fatal(err)
	}
	peerWorkflow, err := s.Native.Save(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	j := baseline
	j.ID = ""
	j.NativeEventUID = ""
	j.NativeDefinition = nil
	j.Workflow = peerWorkflow.UID
	j = policyJob(t, s, j)
	if err := s.SetEnabled(t.Context(), j.ID, true); err == nil {
		t.Error("unsupported workflow retries activated")
	}
	marker := filepath.Join(j.CWD, "consumer-count")
	os.Remove(marker)
	if _, err := Execute(t.Context(), s, j, "manual"); err == nil || !strings.Contains(err.Error(), "retries") {
		t.Errorf("unsupported workflow retries launched: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("unsupported workflow made process effects")
	}
	// Frozen saved execution must apply the same supported subset on resume.
	uid, _ := katacli.NewUID()
	target := s.Native.Client.Target
	target.Token = ""
	c := runner.NativeExecutionContext{Version: 1, RunUID: uid, Target: target, ProjectUID: policyProjectUID, Runtime: j, Workflow: &peerWorkflow, IssueUID: policyIssueUID}
	if err := c.Save(runDirFor(uid)); err != nil {
		t.Fatal(err)
	}
	_, err = runNativeContext(t.Context(), s, c, store.Run{ID: uid, JobID: j.ID, Workflow: j.Workflow, RunDir: runDirFor(uid)}, workflowOpts{})
	if err == nil || !strings.Contains(err.Error(), "retries") {
		t.Errorf("saved unsupported workflow resumed: %v", err)
	}
}
