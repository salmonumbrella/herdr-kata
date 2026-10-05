package main

import (
	"encoding/json"
	"github.com/salmonumbrella/herdr-kata/internal/lockfile"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTask9Fix2HistoryRecoveryBoundsWorkAndSkipsUnchangedAcknowledgedRuns(t *testing.T) {
	s, _, _ := policyProduct(t)
	j := fix1NotifyJob(t, s, policyIssueUID, map[string]any{"kind": "manual"})
	for range 201 {
		c, rec := fix2SavedRun(t, s, j, "done")
		dto := nativeObservation(c, "succeeded", rec.StartedAt, *rec.EndedAt)
		d := NativeDelivery{Version: 1, TargetKey: nativeTargetKey(c.Target), ProjectUID: c.ProjectUID, Actor: c.Target.Actor, Teammate: c.Target.Teammate, RunUID: c.RunUID}
		if e := statefs.WriteJSON(nativeDeliveryPath(rec.RunDir), d, 262144); e != nil {
			t.Fatal(e)
		}
		if e := statefs.WriteJSON(filepath.Join(rec.RunDir, "native-observed.json"), nativeObserved{Version: 1, RunUID: c.RunUID, DTO: dto}, 262144); e != nil {
			t.Fatal(e)
		}
	}
	if e := recoverMissingObservations(t.Context(), s); e != nil {
		t.Fatal(e)
	}
	comments, e := filepath.Glob(filepath.Join(stateDir(), "comments", "*.json"))
	if e != nil {
		t.Fatal(e)
	}
	if len(comments) > 100 {
		t.Fatalf("one periodic recovery touched %d historical results; budget100", len(comments))
	}
	for range 2 {
		if e := recoverMissingObservations(t.Context(), s); e != nil {
			t.Fatal(e)
		}
	}
	comments, e = filepath.Glob(filepath.Join(stateDir(), "comments", "*.json"))
	if e != nil || len(comments) != 201 {
		t.Fatalf("fair recovery missed history: %d %v", len(comments), e)
	}
	for _, p := range comments {
		l, e := lockfile.Acquire(p + ".lock")
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { l.Release() })
	}
	c, rec := fix2SavedRun(t, s, j, "running")
	rec.EndedAt = nil
	if e := s.PutRun(t.Context(), rec); e != nil {
		t.Fatal(e)
	}
	drain := func() {
		t.Helper()
		for range 3 {
			if e := recoverMissingObservations(t.Context(), s); e != nil {
				t.Fatalf("unchanged history was re-derived while result lock held: %v", e)
			}
		}
	}
	drain()
	var d NativeDelivery
	if e := statefs.ReadJSON(nativeDeliveryPath(rec.RunDir), 262144, &d); e != nil || d.Pending == nil || d.Pending.Status != "running" {
		t.Fatalf("missing new tail not recovered: %+v %v", d, e)
	}
	frozen, _ := json.Marshal(d.Pending)
	end := rec.StartedAt.Add(time.Minute)
	rec.EndedAt = &end
	rec.Outcome = "done"
	if e := s.PutRun(t.Context(), rec); e != nil {
		t.Fatal(e)
	}
	drain()
	if e := statefs.ReadJSON(nativeDeliveryPath(rec.RunDir), 262144, &d); e != nil || d.Unsent == nil || d.Unsent.Status != "succeeded" {
		t.Fatalf("newly ended successor missing: %+v %v", d, e)
	}
	after, _ := json.Marshal(d.Pending)
	if string(frozen) != string(after) {
		t.Fatal("recovery changed frozen pending predecessor")
	}
	// Accepted final evidence followed by a saved same-UID resume must be noticed.
	dto := nativeObservation(c, "succeeded", rec.StartedAt, end)
	d.Pending, d.Unsent = nil, nil
	if e := statefs.WriteJSON(nativeDeliveryPath(rec.RunDir), d, 262144); e != nil {
		t.Fatal(e)
	}
	if e := statefs.WriteJSON(filepath.Join(rec.RunDir, "native-observed.json"), nativeObserved{Version: 1, RunUID: c.RunUID, DTO: dto}, 262144); e != nil {
		t.Fatal(e)
	}
	drain()
	rec.Outcome = "running"
	rec.StartedAt = end.Add(time.Second)
	rec.EndedAt = nil
	if e := s.PutRun(t.Context(), rec); e != nil {
		t.Fatal(e)
	}
	drain()
	if e := statefs.ReadJSON(nativeDeliveryPath(rec.RunDir), 262144, &d); e != nil || d.Pending == nil || d.Pending.Status != "running" {
		t.Fatalf("saved same UID resume not recovered: %+v %v", d, e)
	}
	if e := os.Remove(nativeDeliveryPath(rec.RunDir)); e != nil {
		t.Fatal(e)
	}
	drain()
	if e := statefs.ReadJSON(nativeDeliveryPath(rec.RunDir), 262144, &d); e != nil || d.Pending == nil || d.Pending.Status != "running" {
		t.Fatalf("missing resumed tail not reconstructed: %+v %v", d, e)
	}
}

func TestTask9Fix2CorruptHistoryDiagnosticDoesNotRepeatAndRepairIsRecovered(t *testing.T) {
	s, _, _ := policyProduct(t)
	j := fix1NotifyJob(t, s, policyIssueUID, map[string]any{"kind": "manual"})
	_, rec := fix2SavedRun(t, s, j, "done")
	contextPath := filepath.Join(rec.RunDir, "native-context.json")
	original, e := os.ReadFile(contextPath)
	if e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(contextPath, []byte("{"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := recoverMissingObservations(t.Context(), s); e == nil {
		t.Fatal("corrupt context failure disappeared")
	}
	if e := recoverMissingObservations(t.Context(), s); e != nil {
		t.Fatalf("unchanged corrupt history repeated diagnostic: %v", e)
	}
	if e := os.WriteFile(contextPath, original, 0600); e != nil {
		t.Fatal(e)
	}
	if e := recoverMissingObservations(t.Context(), s); e != nil {
		t.Fatal(e)
	}
	var d NativeDelivery
	if e := statefs.ReadJSON(nativeDeliveryPath(rec.RunDir), 262144, &d); e != nil || d.Pending == nil || d.Pending.Status != "succeeded" {
		t.Fatalf("repaired history not recovered: %+v %v", d, e)
	}
}

func TestTask9Fix2HistoryIdentityFailureRemainsAFailureOnEveryDrain(t *testing.T) {
	s, _, _ := policyProduct(t)
	j := fix1NotifyJob(t, s, policyIssueUID, map[string]any{"kind": "manual"})
	c, rec := fix2SavedRun(t, s, j, "done")
	c.Target.Actor = "another-worker"
	if e := statefs.WriteJSON(filepath.Join(rec.RunDir, "native-context.json"), c, 8<<20); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if e := recoverMissingObservations(t.Context(), s); e == nil {
			t.Fatal("saved identity failure disappeared behind recovery progress")
		}
	}
	if _, e := os.Stat(nativeDeliveryPath(rec.RunDir)); !os.IsNotExist(e) {
		t.Fatalf("foreign identity queued evidence: %v", e)
	}
}

func TestTask9Fix2ContextOnlyOutboxDoesNotRequireLocalSQLHistory(t *testing.T) {
	s, _, _ := policyProduct(t)
	_, path := task9QueuedRun(t, s)
	before, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if e := recoverMissingObservations(t.Context(), s); e != nil {
		t.Fatalf("context-only outbox was incorrectly treated as missing execution history: %v", e)
	}
	after, e := os.ReadFile(path)
	if e != nil || string(before) != string(after) {
		t.Fatal("context-only recovery changed frozen queued evidence")
	}
}
