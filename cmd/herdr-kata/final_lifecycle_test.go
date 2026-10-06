package main

import (
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"strings"
	"testing"
)

func TestRawNativeLifecycleRefreshesOfflineCache(t *testing.T) {
	s, _, _ := policyProduct(t)
	j := consumerShellJob(t, s)
	winner := j.NativeEventUID
	for _, action := range []string{"delete", "restore"} {
		output, err := captureStdout(t, func() error { return nativeDefinitionCmd(t.Context(), "job", []string{action, j.ID, winner}) })
		if err != nil {
			t.Fatal(err)
		}
		var def katacli.Definition
		if err := katacli.Decode([]byte(output), &def); err != nil {
			t.Fatal(err)
		}
		winner = def.DefinitionEventUID
		cached, err := s.Native.Cached(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, v := range cached.Jobs {
			if v.UID == j.ID {
				found = true
			}
		}
		if found != (action == "restore") {
			t.Errorf("%s left wrong cache live=%v", action, found)
		}
	}
}
func TestWorkflowRemovalWarnsAboutRawDependencies(t *testing.T) {
	s, _, _ := policyProduct(t)
	j := consumerShellJob(t, s)
	var raw map[string]any
	if err := katacli.Decode(j.NativeDefinition, &raw); err != nil {
		t.Fatal(err)
	}
	raw["options"] = map[string]any{"herdr": map[string]any{"Persistent": "opaque"}}
	draft, err := katacli.NewDraft("job", "", "Opaque referring peer", policyJSON(t, raw), "")
	if err != nil {
		t.Fatal(err)
	}
	peer, err := s.Native.Save(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	var runErr error
	text := captureDateDiagnostics(t, func() { _, runErr = captureStdout(t, func() error { return workflowRemove([]string{j.Workflow}) }) })
	if runErr != nil {
		t.Fatal(runErr)
	}
	for _, uid := range []string{j.ID, peer.UID} {
		if !strings.Contains(text, uid) {
			t.Errorf("missing referencing job %s in %q", uid, text)
		}
	}
	def, err := s.Native.Client.Definition(t.Context(), "workflow", j.Workflow)
	if err != nil || def.DeletedAt == nil {
		t.Fatalf("workflow not tombstoned: %+v %v", def, err)
	}
}
