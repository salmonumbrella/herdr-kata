package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/katabridge"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
)

func TestDateAttentionRetainsAmbiguousIssueAndCoalescesReads(t *testing.T) {
	s, dir, _ := policyProduct(t)
	j := consumerDateJob(t, s, "issue-scheduled", "notify", 0)
	dateProjection(t, dir, "2000-01-01", "UTC", "2000-01-01T00:00:00Z", 1)
	projected, source, err := nativeDateJob(t.Context(), s, j)
	if err != nil {
		t.Fatal(err)
	}
	var def nativeDateDefinition
	if err := katacli.Decode(j.NativeDefinition, &def); err != nil {
		t.Fatal(err)
	}
	bridge, err := localBridge(s)
	if err != nil {
		t.Fatal(err)
	}
	out := katabridge.AttentionOutbox{Dir: bridge.Dir, Scope: bridge.Scope, Teammate: s.Native.Client.Target.Teammate}
	key := "date-job:" + j.ID
	for _, tc := range []struct {
		name, uid, status string
		project           int64
	}{
		{"wrong identity", policyProjectUID, "open", 73},
		{"wrong project", policyIssueUID, "open", 74},
		{"unknown status", policyIssueUID, "paused", 73},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := out.Queue(katabridge.Attention{Key: key, Source: tc.name, Issue: policyIssueUID, Recipient: "worker/child", Message: "Due"}); err != nil {
				t.Fatal(err)
			}
			policyWrite(t, filepath.Join(dir, "issues.json"), map[string]any{policyIssueUID: map[string]any{"uid": tc.uid, "project_id": tc.project, "status": tc.status, "author": "worker"}})
			if err := deliverNativeDateNotification(t.Context(), s, projected, source, def); err == nil {
				t.Fatal("ambiguous read delivered")
			}
			pending, failed, err := out.Status()
			if err != nil || pending != 1 || failed != 1 {
				t.Fatalf("ambiguity retired intent or lost diagnostic: pending=%d failed=%d err=%v", pending, failed, err)
			}
		})
	}
	policyWrite(t, filepath.Join(dir, "issues.json"), map[string]any{policyIssueUID: map[string]any{"uid": policyIssueUID, "project_id": 73, "status": "closed", "author": "worker"}})
	if err := deliverNativeDateNotification(t.Context(), s, projected, source, def); err == nil {
		t.Fatal("closed source delivered")
	}
	pending, _, err := out.Status()
	if err != nil || pending != 0 {
		t.Fatalf("closed issue retained pending: %d %v", pending, err)
	}
	policyWrite(t, filepath.Join(dir, "issues.json"), map[string]any{policyIssueUID: map[string]any{"uid": policyIssueUID, "project_id": 73, "status": "open", "author": "worker"}})
	before := len(policyCalls(t, dir))
	_ = deliverNativeDateNotification(t.Context(), s, projected, source, def)
	count := 0
	for _, call := range policyCalls(t, dir)[before:] {
		for i, arg := range call.Args {
			if arg == "show" && i+2 < len(call.Args) && call.Args[i+1] == "--" && call.Args[i+2] == policyIssueUID && !slices.Contains(call.Args, "--planning-dates") {
				count++
				break
			}
		}
	}
	if count != 1 {
		t.Fatalf("identical source/target ordinary reads=%d, want 1", count)
	}
	paths, err := filepath.Glob(filepath.Join(bridge.Dir, "attention", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("attention state: %v %v", paths, err)
	}
	if err := os.WriteFile(paths[0], []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	policyWrite(t, filepath.Join(dir, "issues.json"), map[string]any{policyIssueUID: map[string]any{"uid": policyIssueUID, "project_id": 73, "status": "closed", "author": "worker"}})
	if err := deliverNativeDateNotification(t.Context(), s, projected, source, def); err == nil || !strings.Contains(err.Error(), "invalid character") {
		t.Fatalf("cancellation persistence error lost: %v", err)
	}
}
