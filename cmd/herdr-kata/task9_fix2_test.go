package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/katabridge"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

func fix2SavedRun(t *testing.T, s *store.Store, j store.Job, outcome string) (runner.NativeExecutionContext, store.Run) {
	t.Helper()
	uid, err := katacli.NewUID()
	if err != nil {
		t.Fatal(err)
	}
	target := s.Native.Client.Target
	target.Token = ""
	c := runner.NativeExecutionContext{Version: 1, RunUID: uid, Target: target, ProjectUID: s.Native.Binding.ProjectUID, ExecutorLabel: s.Native.Binding.ExecutorLabel, Runtime: j, IssueUID: policyIssueUID, Job: &katacli.Definition{UID: j.ID, DefinitionEventUID: j.NativeEventUID, Name: j.Name, Definition: j.NativeDefinition}}
	if err := c.Save(runDirFor(uid)); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := store.Run{ID: uid, JobID: j.ID, Ref: c.IssueUID, RunDir: runDirFor(uid), Outcome: outcome, StartedAt: at, EndedAt: &at, Note: "Inspect result"}
	if outcome == "parked" {
		r.ParkReason = "blocked"
	}
	if err := s.PutRun(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	return c, r
}

func TestTask9Fix2SettlementAfterPersistentFailingPrefixRecoversAcrossRestart(t *testing.T) {
	s, callsDir, _ := policyProduct(t)
	j := fix1NotifyJob(t, s, policyIssueUID, map[string]any{"kind": "manual"})
	c, rec := fix2SavedRun(t, s, j, "parked")
	b, err := localBridge(s)
	if err != nil {
		t.Fatal(err)
	}
	key := "notify-job:" + j.ID
	item := katabridge.Attention{Key: key, Source: c.RunUID, Issue: c.IssueUID, Recipient: "worker/child", Message: "Inspect restored attention", RecordRun: true}
	at := rec.StartedAt.Add(time.Second)
	var missing []katabridge.AttentionSettlement
	for range 100 {
		uid, err := katacli.NewUID()
		if err != nil {
			t.Fatal(err)
		}
		bad := item
		bad.Source = uid // Accepted local intent; its context is unavailable.
		missing = append(missing, katabridge.AttentionSettlement{Item: bad, CompletedAt: at})
	}
	all := append(append([]katabridge.AttentionSettlement(nil), missing...), katabridge.AttentionSettlement{Item: item, CompletedAt: at})
	data := struct {
		Version   int                              `json:"version"`
		Scope     katabridge.Scope                 `json:"scope"`
		Teammate  string                           `json:"teammate"`
		Item      katabridge.Attention             `json:"item"`
		Pending   bool                             `json:"pending"`
		Unsettled []katabridge.AttentionSettlement `json:"unsettled"`
	}{1, b.Scope, s.Native.Client.Target.Teammate, item, false, all}
	sum := sha256.Sum256([]byte(key))
	if err := statefs.WriteJSON(filepath.Join(b.Dir, "attention", hex.EncodeToString(sum[:])+".json"), data, 98304); err != nil {
		t.Fatal(err)
	}
	if err := flushOrdinaryAttention(t.Context(), s); err == nil {
		t.Fatal("missing-context failures were suppressed")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restarted.Close() })
	if err := flushOrdinaryAttention(t.Context(), restarted); err == nil {
		t.Fatal("persistent missing-context failures were suppressed after restart")
	}
	got, err := restarted.Run(t.Context(), c.RunUID)
	if err != nil || got.Outcome != "done" {
		t.Fatalf("accepted settlement101 remained parked behind failed prefix: %+v %v", got, err)
	}
	out := katabridge.AttentionOutbox{Dir: b.Dir, Scope: b.Scope, Teammate: s.Native.Client.Target.Teammate}
	remaining, err := out.Unsettled()
	before, _ := json.Marshal(missing)
	after, _ := json.Marshal(remaining)
	if err != nil || string(before) != string(after) {
		t.Fatal("fair drain discarded or changed the frozen failing settlements")
	}
	for _, call := range policyCalls(t, callsDir) {
		for _, arg := range call.Args {
			if arg == "notify" || arg == "--clear" {
				t.Fatal("accepted settlement recovery submitted or cleared native attention")
			}
		}
	}
}

func TestTask9Fix2ParkedAttentionCancelsOnlyConfirmedStaleTargets(t *testing.T) {
	s, dir, _ := policyProduct(t)
	j := fix1NotifyJob(t, s, policyIssueUID, map[string]any{"kind": "manual"})
	for _, mode := range []string{"closed", "deleted", "transport", "identity", "project", "auth", "ambiguous-missing"} {
		t.Run(mode, func(t *testing.T) {
			c, rec := fix2SavedRun(t, s, j, "parked")
			if err := queueRunAttention(s, c, rec); err != nil {
				t.Fatal(err)
			}
			issue := map[string]any{"uid": c.IssueUID, "project_id": 73, "status": "open", "author": "worker"}
			switch mode {
			case "closed":
				issue["status"] = "closed"
			case "deleted":
				issue["deleted_at"] = "2026-01-01T00:00:01Z"
			case "identity":
				issue["uid"] = policyProjectUID
			case "project":
				issue["project_id"] = 74
			case "transport", "auth", "ambiguous-missing":
				code, kind := "unavailable", "daemon_unavailable"
				if mode == "auth" {
					code, kind = "unauthorized", "internal"
				}
				if mode == "ambiguous-missing" {
					code, kind = "issue_not_found", "not_found"
				}
				policyWrite(t, filepath.Join(dir, "issue-error.json"), map[string]any{"error": map[string]any{"code": code, "kind": kind, "exit_code": 4, "message": "Fixture read failed"}})
			}
			policyWrite(t, filepath.Join(dir, "issues.json"), map[string]any{c.IssueUID: issue})
			err := flushOrdinaryAttention(t.Context(), s)
			b, e := localBridge(s)
			if e != nil {
				t.Fatal(e)
			}
			out := katabridge.AttentionOutbox{Dir: b.Dir, Scope: b.Scope, Teammate: c.Target.Teammate}
			pending, e := out.IsPending("run-job:" + j.ID)
			if e != nil {
				t.Fatal(e)
			}
			stale := mode == "closed" || mode == "deleted"
			if stale {
				if err != nil || pending {
					t.Fatalf("confirmed %s attention remains pending: %v %v", mode, pending, err)
				}
				n, _, e := out.Status()
				if e != nil || n != 0 {
					t.Fatalf("stale pending count %d: %v", n, e)
				}
				if e = flushOrdinaryAttention(t.Context(), s); e != nil {
					t.Fatalf("stale repeated diagnostic: %v", e)
				}
			} else {
				if err == nil || !pending {
					t.Fatalf("%s failure disappeared: pending=%v err=%v", mode, pending, err)
				}
				if again := flushOrdinaryAttention(t.Context(), s); again == nil {
					t.Fatal("failed target read stopped retrying")
				}
				if pending, e := out.IsPending("run-job:" + j.ID); e != nil || !pending {
					t.Fatalf("retry discarded attention: %v %v", pending, e)
				}
				if err = os.Remove(filepath.Join(dir, "issue-error.json")); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
			for _, call := range policyCalls(t, dir) {
				for _, arg := range call.Args {
					if arg == "notify" || arg == "--clear" {
						t.Fatal("cancel or failed read submitted/cleared attention")
					}
				}
			}
		})
	}
}
