package main

import (
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFrozenNativeSecretReferencesReachProcessAndFailBeforeLaunch(t *testing.T) {
	s, _, _ := policyProduct(t)
	j := consumerShellJob(t, s)
	fd, err := s.Native.Client.Definition(t.Context(), "workflow", j.Workflow)
	if err != nil {
		t.Fatal(err)
	}
	var workflowDoc map[string]any
	if err := katacli.Decode(fd.Definition, &workflowDoc); err != nil {
		t.Fatal(err)
	}
	workflowDoc["steps"] = []any{map[string]any{"key": "inspect", "kind": "command", "command": "printf '%s' \"$SERVICE_TOKEN\" > received-secret"}}
	fd.Definition = policyJSON(t, workflowDoc)
	s.Native.Binding.Secrets = map[string]string{"service": "local-only-value"}
	for _, tc := range []struct {
		name, env, ref string
		reject         bool
	}{
		{"resolved", "SERVICE_TOKEN", "service", false},
		{"missing", "SERVICE_TOKEN", "absent", true},
		{"routing", "KATA_SERVER", "service", true},
		{"proxy", "https_proxy", "service", true},
		{"launcher", "HERDR_BIN_PATH", "service", true},
		{"shell path", "PATH", "service", true},
		{"invalid name", "BAD=KEY", "service", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var doc map[string]any
			if err := katacli.Decode(j.NativeDefinition, &doc); err != nil {
				t.Fatal(err)
			}
			doc["secret_refs"] = map[string]string{tc.env: tc.ref}
			// Current editable state deliberately differs from frozen launch intent.
			frozen := j
			frozen.NativeDefinition = policyJSON(t, doc)
			frozen.Timeout = 5 * time.Second
			uid, err := katacli.NewUID()
			if err != nil {
				t.Fatal(err)
			}
			target := s.Native.Client.Target
			target.Token = ""
			c := runner.NativeExecutionContext{Version: 1, RunUID: uid, Target: target, ProjectUID: policyProjectUID, Runtime: frozen, IssueUID: policyIssueUID, Job: &katacli.Definition{UID: j.ID, DefinitionEventUID: j.NativeEventUID, Definition: frozen.NativeDefinition}, Workflow: &fd}
			if err := c.Save(runDirFor(uid)); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(j.CWD, "received-secret")
			os.Remove(marker)
			rec := store.Run{ID: uid, JobID: j.ID, Workflow: j.Workflow, Ref: policyIssueUID, RunDir: runDirFor(uid)}
			_, err = runNativeContext(t.Context(), s, c, rec, workflowOpts{})
			raw, _ := os.ReadFile(marker)
			if tc.reject {
				if err == nil || len(raw) > 0 {
					t.Fatalf("invalid secret binding launched: err=%v effects=%q", err, raw)
				}
				if strings.Contains(err.Error(), "local-only-value") {
					t.Fatal("secret leaked in error")
				}
			} else if err != nil || string(raw) != "local-only-value" {
				t.Fatalf("secret not resolved from frozen references: %q %v", raw, err)
			}
			saved, readErr := os.ReadFile(filepath.Join(rec.RunDir, "native-context.json"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.Contains(string(saved), "local-only-value") {
				t.Fatal("secret value entered frozen context")
			}
		})
	}
}
