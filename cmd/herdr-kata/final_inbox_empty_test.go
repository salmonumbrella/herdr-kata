package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/katabridge"
)

func TestEmptyInboxBatchSkipsRemoteReadButRegistrationsCheckProject(t *testing.T) {
	s, dir, _ := policyProduct(t)
	policyWrite(t, filepath.Join(dir, "project.json"), map[string]any{"project": map[string]any{"id": 73, "uid": "01ARZ3NDEKTSV4RRFFQ69G5FAX"}})
	before := len(policyCalls(t, dir))
	if err := pollNativeInboxes(t.Context(), s); err != nil {
		t.Errorf("empty local batch consulted unavailable/mismatched remote state: %v", err)
	}
	if calls := policyCalls(t, dir)[before:]; len(calls) != 0 {
		t.Errorf("empty local batch made remote calls: %+v", calls)
	}
	if _, err := os.Stat(filepath.Join(s.Native.StateDir, "runtime-registrations.json")); !os.IsNotExist(err) {
		t.Errorf("empty polling created local registry state: %v", err)
	}
	bridge, err := localBridge(s)
	if err != nil {
		t.Fatal(err)
	}
	registration := katabridge.Registration{Recipient: "worker/child", Workspace: "w1", Pane: "p1", Conversation: "example-session"}
	if err := bridge.Connect(registration); err != nil {
		t.Fatal(err)
	}
	before = len(policyCalls(t, dir))
	if err := pollNativeInboxes(t.Context(), s); err == nil || !strings.Contains(err.Error(), "project identity differs") {
		t.Fatalf("actual batch skipped fresh project validation: %v", err)
	}
	calls := policyCalls(t, dir)[before:]
	if len(calls) != 1 || !strings.Contains(strings.Join(calls[0].Args, " "), "projects show") {
		t.Fatalf("mismatched project reached inbox reads: %+v", calls)
	}
	if err := os.Remove(filepath.Join(dir, "project.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "attention-enabled"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	before = len(policyCalls(t, dir))
	if err := pollNativeInboxes(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	calls = policyCalls(t, dir)[before:]
	if len(calls) < 2 || !strings.Contains(strings.Join(calls[0].Args, " "), "projects show") || !strings.Contains(strings.Join(calls[1].Args, " "), "inbox --for worker/child") {
		t.Fatalf("valid batch did not validate before exact-recipient read: %+v", calls)
	}
}
