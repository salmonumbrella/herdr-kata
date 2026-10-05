package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/lockfile"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"hegel.dev/go/hegel"
)

func fix3UnchangedRecoverySkipsResultLock(t *testing.T, recover func() error, dir string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(stateDir(), "comments", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("expected one local result buffer: %d %v", len(paths), err)
	}
	lock, err := lockfile.Acquire(paths[0] + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	before, err := os.ReadFile(filepath.Join(dir, "native-recovered.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := recover(); err != nil {
		t.Fatalf("unchanged recovered history touched its held result lock: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "native-recovered.json"))
	if err != nil || string(before) != string(after) {
		t.Fatal("unchanged recovery rewrote progress")
	}
}

func TestTask9Fix3ExecutorLabelChangeRepairsWithFrozenAttribution(t *testing.T) {
	s, _, _ := policyProduct(t)
	s.Native.Binding.ExecutorLabel = "original executor"
	j := fix1NotifyJob(t, s, policyIssueUID, map[string]any{"kind": "manual"})
	c, rec := fix2SavedRun(t, s, j, "done")
	contextPath := filepath.Join(rec.RunDir, "native-context.json")
	frozenContext, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatal(err)
	}
	recover := func() error { return recoverMissingObservations(t.Context(), s) }
	if err := recover(); err != nil {
		t.Fatal(err)
	}
	var before NativeDelivery
	if err := statefs.ReadJSON(nativeDeliveryPath(rec.RunDir), 262144, &before); err != nil || before.Pending == nil {
		t.Fatalf("initial recovery: %+v %v", before, err)
	}
	fix3UnchangedRecoverySkipsResultLock(t, recover, rec.RunDir)
	s.Native.Binding.ExecutorLabel = "renamed executor"
	if err := c.Check(s.Native); err != nil {
		t.Fatalf("metadata-only label edit changed frozen routing acceptance: %v", err)
	}
	if err := os.Remove(nativeDeliveryPath(rec.RunDir)); err != nil {
		t.Fatal(err)
	}
	if err := recover(); err != nil {
		t.Fatalf("metadata-only label change blocked frozen run repair: %v", err)
	}
	var after NativeDelivery
	if err := statefs.ReadJSON(nativeDeliveryPath(rec.RunDir), 262144, &after); err != nil || after.Pending == nil {
		t.Fatalf("missing final buffer not repaired: %+v %v", after, err)
	}
	if after.RunUID != rec.ID || after.Pending.ExecutorLabel != c.ExecutorLabel || !sameObservationEvidence(*before.Pending, *after.Pending) {
		t.Fatalf("label edit reinterpreted frozen run evidence: before=%+v after=%+v", before.Pending, after.Pending)
	}
	got, err := os.ReadFile(contextPath)
	if err != nil || string(got) != string(frozenContext) {
		t.Fatal("metadata repair changed immutable execution context")
	}
	fix3UnchangedRecoverySkipsResultLock(t, recover, rec.RunDir)
}

func TestTask9Fix3LabelChangePreservesRealRoutingRefusals(t *testing.T) {
	s, _, _ := policyProduct(t)
	s.Native.Binding.ExecutorLabel = "original executor"
	j := fix1NotifyJob(t, s, policyIssueUID, map[string]any{"kind": "manual"})
	c, rec := fix2SavedRun(t, s, j, "done")
	if err := recoverMissingObservations(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	target, binding := s.Native.Client.Target, s.Native.Binding
	frozen, err := os.ReadFile(nativeDeliveryPath(rec.RunDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"target", "project", "actor", "teammate"} {
		t.Run(field, func(t *testing.T) {
			s.Native.Client.Target, s.Native.Binding = target, binding
			s.Native.Binding.ExecutorLabel = "renamed executor"
			switch field {
			case "target":
				s.Native.Client.Target.Server = "http://127.0.0.1:7778"
			case "project":
				s.Native.Binding.ProjectUID = policyIssueUID
			case "actor":
				s.Native.Client.Target.Actor = "another-worker"
			case "teammate":
				s.Native.Client.Target.Teammate = "another-child"
			}
			if err := c.Check(s.Native); err == nil {
				t.Fatal("real frozen routing change accepted")
			}
			for range 2 {
				if err := recoverMissingObservations(t.Context(), s); err == nil {
					t.Fatal("metadata edit bypassed a routing refusal")
				}
			}
			got, err := os.ReadFile(nativeDeliveryPath(rec.RunDir))
			if err != nil || string(got) != string(frozen) {
				t.Fatal("routing refusal changed frozen pending evidence")
			}
		})
	}
	s.Native.Client.Target, s.Native.Binding = target, binding
}

func TestTask9Fix3LabelMetadataNeverRewritesFrozenEvidence(t *testing.T) {
	s, _, _ := policyProduct(t)
	s.Native.Binding.ExecutorLabel = "original executor"
	j := fix1NotifyJob(t, s, policyIssueUID, map[string]any{"kind": "manual"})
	c, rec := fix2SavedRun(t, s, j, "done")
	if err := recoverMissingObservations(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	var original NativeDelivery
	if err := statefs.ReadJSON(nativeDeliveryPath(rec.RunDir), 262144, &original); err != nil || original.Pending == nil {
		t.Fatal("initial frozen evidence absent")
	}
	expected, _ := json.Marshal(original.Pending)
	b, err := localBridge(s)
	if err != nil {
		t.Fatal(err)
	}
	hegel.Test(t, func(ht *hegel.T) {
		label := "renamed:" + strings.ToValidUTF8(hegel.Draw(ht, hegel.Text()), "\ufffd")
		s.Native.Binding.ExecutorLabel = label
		if err := c.Check(s.Native); err != nil {
			ht.Fatal("metadata altered credential-free routing check")
		}
		if err := os.Remove(nativeDeliveryPath(rec.RunDir)); err != nil {
			ht.Fatal(err)
		}
		materialized, _ := json.Marshal(nativeRecoveryMarker{Version: 1, Scope: b.Scope, Teammate: c.Target.Teammate, ExecutorLabel: label, RunUID: c.RunUID, Input: strings.Repeat("0", 64)})
		err := recoverMissingObservations(t.Context(), s)
		if len(materialized) > 98304 {
			if err == nil {
				ht.Fatal("oversized local metadata progress accepted")
			}
		} else if err != nil {
			ht.Fatalf("metadata cache invalidation blocked repair: %v", err)
		}
		var got NativeDelivery
		if err := statefs.ReadJSON(nativeDeliveryPath(rec.RunDir), 262144, &got); err != nil || got.Pending == nil {
			ht.Fatal("label edit withheld repaired evidence")
		}
		actual, _ := json.Marshal(got.Pending)
		if got.RunUID != rec.ID || string(actual) != string(expected) || got.Pending.ExecutorLabel != c.ExecutorLabel {
			ht.Fatal("metadata edit changed frozen run UID/label/body")
		}
	})
}
