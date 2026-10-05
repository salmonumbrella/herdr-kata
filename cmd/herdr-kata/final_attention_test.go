package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/katabridge"
)

func TestAttentionPendingPrefixCannotStarveHealthyRunAfterRestart(t *testing.T) {
	s, dir, _ := policyProduct(t)
	j := fix1NotifyJob(t, s, policyIssueUID, map[string]any{"kind": "manual"})
	c, rec := fix2SavedRun(t, s, j, "parked")
	if err := queueRunAttention(s, c, rec); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "attention-enabled"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	b, err := localBridge(s)
	if err != nil {
		t.Fatal(err)
	}
	out := katabridge.AttentionOutbox{Dir: b.Dir, Scope: b.Scope, Teammate: c.Target.Teammate}
	key := "run-job:" + j.ID
	last := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	for _, kind := range []string{"run-job:", "date-job:"} {
		count := 0
		for i := 0; count < 100; i++ {
			prefix := kind + fmt.Sprint(i)
			if fmt.Sprintf("%x", sha256.Sum256([]byte(prefix))) >= last {
				continue
			}
			if err := out.Queue(katabridge.Attention{Key: prefix, Source: "missing-context", Issue: c.IssueUID, Recipient: "worker/launch", Message: "Inspect"}); err != nil {
				t.Fatal(err)
			}
			count++
		}
	}
	if err := flushOrdinaryAttention(t.Context(), s); err == nil {
		t.Fatal("missing-context failures disappeared")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err := flushOrdinaryAttention(t.Context(), restarted); err == nil {
		t.Fatal("missing-context failures disappeared on restart")
	}
	if pending, err := out.IsPending(key); err != nil || pending {
		t.Fatalf("healthy run starved behind deferred prefix: pending=%v err=%v", pending, err)
	}
	remaining, err := out.PendingItems()
	if err != nil || len(remaining) != 200 {
		t.Fatalf("failed/date items dropped: %d %v", len(remaining), err)
	}
}

func TestContextOnlyRecoveryRetainsParseDiagnostic(t *testing.T) {
	s, _, _ := policyProduct(t)
	uid, path := task9QueuedRun(t, s)
	if err := os.WriteFile(filepath.Join(runDirFor(uid), "native-context.json"), []byte(`{broken`), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	err = recoverMissingObservations(t.Context(), s)
	if err == nil || !strings.Contains(err.Error(), "invalid character") {
		t.Fatalf("lost context parse diagnosis: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("pending outbox changed: %v", err)
	}
}
