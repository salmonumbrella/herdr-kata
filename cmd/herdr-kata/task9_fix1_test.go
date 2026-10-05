package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/katabridge"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

func fix1NotifyJob(t *testing.T, s *store.Store, issue string, trigger map[string]any) store.Job {
	t.Helper()
	raw := policyJSON(t, map[string]any{"version": 1, "kind": "job", "enabled": false, "trigger": trigger, "action": map[string]any{"kind": "notify", "recipient": "worker/child", "message": "Inspect restored attention"}, "overlap": "forbid", "catchup": "latest", "issue": map[string]any{"kind": "existing", "uid": issue}})
	draft, err := katacli.NewDraft("job", "", "Inspect attention", raw, "")
	if err != nil {
		t.Fatal(err)
	}
	def, err := s.Native.Save(t.Context(), draft)
	if err != nil {
		realNativeFailure(t, err)
	}
	if err := s.SetEnabled(t.Context(), def.UID, true); err != nil {
		t.Fatal(err)
	}
	j, err := s.Job(t.Context(), def.UID)
	if err != nil {
		t.Fatal(err)
	}
	return *j
}

func fix1CountNativeCalls(t *testing.T, c *katacli.Client, s *store.Store) string {
	t.Helper()
	dir := t.TempDir()
	oracle := os.Getenv("HERDR_BIN_PATH")
	bin := filepath.Join(dir, filepath.Base(oracle))
	raw, err := os.ReadFile(oracle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, raw, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte("forward"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "forward-binary"), []byte(c.Executable), 0600); err != nil {
		t.Fatal(err)
	}
	c.Executable = bin
	s.Native.Client.Executable = bin
	if err := store.WriteNativeConfig(stateDir(), store.NativeConfig{Client: *c, Binding: s.Native.Binding}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRealTask9Fix1AcceptedNotificationRecoversLocalSettlementFailure(t *testing.T) {
	for _, path := range []string{"immediate", "worker"} {
		t.Run(path, func(t *testing.T) {
			realPolicyHerdr(t)
			c, s := realNativeProduct(t)
			var created struct {
				Issue struct {
					UID string `json:"uid"`
				} `json:"issue"`
			}
			if err := c.Create(t.Context(), "Inspect notification", "Local settlement fault", "task9-fix1-settlement", &created); err != nil {
				realNativeFailure(t, err)
			}
			uid, recipient := created.Issue.UID, "worker/child"
			j := fix1NotifyJob(t, s, uid, map[string]any{"kind": "manual"})
			if path == "worker" {
				if err := c.Notify(t.Context(), uid, recipient, "Existing request", false, new(any)); err != nil {
					realNativeFailure(t, err)
				}
				if run, err := Execute(t.Context(), s, j, "manual"); err != nil || run == nil || run.Outcome != "parked" {
					t.Fatalf("fixture did not wait behind occupied slot: %+v %v", run, err)
				}
				if err := c.Notify(t.Context(), uid, recipient, "", true, new(any)); err != nil {
					realNativeFailure(t, err)
				}
			}
			callsDir := fix1CountNativeCalls(t, c, s)
			// Test-owned ephemeral DDL only: reject the local done write after
			// ordinary acceptance, without blocking the initial parked row.
			db, err := sql.Open("sqlite", filepath.Join(stateDir(), "herdr-kata.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			if _, err := db.Exec(`CREATE TRIGGER fixture_settlement_failure BEFORE UPDATE ON runs WHEN NEW.outcome='done' BEGIN SELECT RAISE(ABORT, 'injected local settlement failure'); END`); err != nil {
				t.Fatal(err)
			}
			if path == "immediate" {
				_, err = Execute(t.Context(), s, j, "manual")
			} else {
				err = flushOrdinaryAttention(t.Context(), s)
			}
			if err == nil || !strings.Contains(err.Error(), "injected local settlement failure") {
				t.Fatalf("local post-acceptance fault did not fire: %v", err)
			}
			runs, err := s.JobRuns(t.Context(), j.ID, 10)
			if err != nil || len(runs) != 1 || runs[0].Outcome != "parked" {
				t.Fatalf("fault did not leave parked history: %+v %v", runs, err)
			}
			runUID := runs[0].ID
			b, err := localBridge(s)
			if err != nil {
				t.Fatal(err)
			}
			out := katabridge.AttentionOutbox{Dir: b.Dir, Scope: b.Scope, Teammate: c.Target.Teammate}
			if pending, err := out.IsPending("notify-job:" + j.ID); err != nil || pending {
				t.Fatalf("fault did not occur after local attention retirement: %v %v", pending, err)
			}
			if path == "worker" {
				// A distinct invocation may replace latest unsent intent while the
				// earlier accepted invocation still needs its local history write.
				if next, err := Execute(t.Context(), s, j, "manual"); err != nil || next == nil || next.RunID == runUID || next.Outcome != "parked" {
					t.Fatalf("independent later invocation fixture failed: %+v %v", next, err)
				}
			}
			if _, err := db.Exec(`DROP TRIGGER fixture_settlement_failure`); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			restarted, err := openStore()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { restarted.Close() })
			for range 2 {
				if err := flushOrdinaryAttention(t.Context(), restarted); err != nil {
					t.Fatal(err)
				}
			}
			rec, err := restarted.Run(t.Context(), runUID)
			if err != nil || rec.Outcome != "done" || rec.ParkReason != "" || !strings.Contains(rec.Note, "handler owns clearing") {
				t.Fatalf("restart stranded accepted notification as parked: %+v %v", rec, err)
			}
			if err := recoverMissingObservations(t.Context(), restarted); err != nil {
				t.Fatal(err)
			}
			var evidence NativeDelivery
			if err := statefs.ReadJSON(nativeDeliveryPath(rec.RunDir), 262144, &evidence); err != nil {
				t.Fatal(err)
			}
			latest := evidence.Unsent
			if latest == nil {
				latest = evidence.Pending
			}
			if latest == nil || latest.Status != "succeeded" {
				t.Fatalf("accepted run evidence remained unknown: %+v", evidence)
			}
			submissions, clears := 0, 0
			for _, call := range policyCalls(t, callsDir) {
				for _, arg := range call.Args {
					if arg == "notify" {
						submissions++
					}
					if arg == "--clear" {
						clears++
					}
				}
			}
			if submissions != 1 || clears != 0 {
				t.Fatalf("settlement recovery resubmitted/cleared native request: sends=%d clears=%d", submissions, clears)
			}
			inbox, err := katabridge.ReadInbox(t.Context(), *c, recipient)
			if err != nil || len(inbox.Requests) != 1 || inbox.Requests[0].Message != "Inspect restored attention" {
				t.Fatalf("settlement recovery changed native attention: %+v %v", inbox, err)
			}
		})
	}
}

func TestRealTask9Fix1CancelledDateRestoresAcrossRestartButCompletedStaysHandled(t *testing.T) {
	realPolicyHerdr(t)
	c, s := realNativeProduct(t)
	var created struct {
		Issue struct {
			UID string `json:"uid"`
		} `json:"issue"`
	}
	if err := c.Create(t.Context(), "Inspect date", "Date restoration", "task9-fix1-date", &created); err != nil {
		realNativeFailure(t, err)
	}
	uid, recipient := created.Issue.UID, "worker/child"
	date := "2000-01-01T00:00:00Z"
	setDate := func(value string) {
		t.Helper()
		if err := c.Call(t.Context(), []string{"deadline", uid, value}, nil, new(any)); err != nil {
			realNativeFailure(t, err)
		}
	}
	setDate(date)
	j := fix1NotifyJob(t, s, uid, map[string]any{"kind": "issue-deadline", "issue_uid": uid, "lead_seconds": 1800})
	if err := c.Notify(t.Context(), uid, recipient, "Existing request", false, new(any)); err != nil {
		realNativeFailure(t, err)
	}
	sweep := func() {
		t.Helper()
		d := &daemon{store: s, slots: make(chan struct{}, 1), inflight: map[string]bool{}}
		d.sweep(t.Context())
		d.wg.Wait()
	}
	sweep()
	b, err := localBridge(s)
	if err != nil {
		t.Fatal(err)
	}
	out := katabridge.AttentionOutbox{Dir: b.Dir, Scope: b.Scope, Teammate: c.Target.Teammate}
	if pending, err := out.IsPending("date-job:" + j.ID); err != nil || !pending {
		t.Fatalf("fixture did not retain occupied date attention: %v %v", pending, err)
	}
	setDate("-")
	sweep()
	if pending, err := out.IsPending("date-job:" + j.ID); err != nil || pending {
		t.Fatalf("absent date did not cancel unsent attention: %v %v", pending, err)
	}
	restart := func() {
		t.Helper()
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		var err error
		s, err = openStore()
		if err != nil {
			t.Fatal(err)
		}
		reopened := s
		t.Cleanup(func() { reopened.Close() })
	}
	restart()
	if err := c.Notify(t.Context(), uid, recipient, "", true, new(any)); err != nil {
		realNativeFailure(t, err)
	}
	setDate(date)
	sweep()
	inbox, err := katabridge.ReadInbox(t.Context(), *c, recipient)
	if err != nil || len(inbox.Requests) != 1 || inbox.Requests[0].Message != "Inspect restored attention" {
		t.Fatalf("identical cancelled date was suppressed after restart: %+v %v", inbox, err)
	}
	// A completed identical source remains handled even across clear/restore.
	if err := c.Notify(t.Context(), uid, recipient, "", true, new(any)); err != nil {
		realNativeFailure(t, err)
	}
	setDate("-")
	sweep()
	restart()
	setDate(date)
	sweep()
	inbox, err = katabridge.ReadInbox(t.Context(), *c, recipient)
	if err != nil || len(inbox.Requests) != 0 {
		t.Fatalf("identical completed date reappeared after handler clear: %+v %v", inbox, err)
	}
	current, err := s.Native.Client.Definition(t.Context(), "job", j.ID)
	if err != nil || current.DefinitionEventUID != j.NativeEventUID {
		t.Fatalf("restoration control changed shared definition: %+v %v", current, err)
	}
}

func TestRealTask9Fix1PollingReachesRegistration101AcrossRestart(t *testing.T) {
	realPolicyHerdr(t)
	c, s := realNativeProduct(t)
	callsDir := fix1CountNativeCalls(t, c, s)
	b, err := localBridge(s)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 101 {
		r := katabridge.Registration{Recipient: fmt.Sprintf("worker/child%03d", i), Workspace: "w1", Pane: "missing-pane", Conversation: "example-session"}
		if err := b.Connect(r); err != nil {
			t.Fatal(err)
		}
	}
	var created struct {
		Issue struct {
			UID string `json:"uid"`
		} `json:"issue"`
	}
	if err := c.Create(t.Context(), "Inspect last inbox", "Bounded polling", "task9-fix1-polling", &created); err != nil {
		realNativeFailure(t, err)
	}
	last := "worker/child100"
	if err := c.Notify(t.Context(), created.Issue.UID, last, "Inspect final registration", false, new(any)); err != nil {
		realNativeFailure(t, err)
	}
	readRecipients := func(offset int) map[string]int {
		t.Helper()
		out := map[string]int{}
		for _, call := range policyCalls(t, callsDir)[offset:] {
			inbox := false
			for _, arg := range call.Args {
				inbox = inbox || arg == "inbox"
			}
			if !inbox {
				continue
			}
			for i, arg := range call.Args {
				if arg == "--for" && i+1 < len(call.Args) && strings.HasPrefix(call.Args[i+1], "worker/") {
					out[call.Args[i+1]]++
				}
			}
		}
		return out
	}
	offset := len(policyCalls(t, callsDir))
	if err := pollNativeInboxes(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	first := readRecipients(offset)
	if len(first) != 100 || first[last] != 0 {
		t.Fatalf("first poll did not remain bounded to its first batch: %+v", first)
	}
	if pending, err := b.PendingCount(); err != nil || pending != 0 {
		t.Fatalf("first empty batch produced pending requests: %d %v", pending, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restarted.Close() })
	offset = len(policyCalls(t, callsDir))
	if err := pollNativeInboxes(t.Context(), restarted); err != nil {
		t.Fatal(err)
	}
	second := readRecipients(offset)
	if len(second) != 100 || second[last] != 1 {
		t.Fatalf("second bounded poll never reached registration101: last attempts=%d unique=%d", second[last], len(second))
	}
	if pending, err := b.PendingCount(); err != nil || pending != 1 {
		t.Fatalf("last recipient missing from local pending counts: %d %v", pending, err)
	}
	parent, err := katabridge.ReadInbox(t.Context(), *c, c.Target.Actor)
	if err != nil || len(parent.Requests) != 1 || !strings.Contains(parent.Requests[0].Message, last) || !strings.Contains(parent.Requests[0].Message, "Needs human") {
		t.Fatalf("last recipient did not raise separate parent needs-human attention: %+v %v", parent, err)
	}
	child, err := katabridge.ReadInbox(t.Context(), *c, last)
	if err != nil || len(child.Requests) != 1 || child.Requests[0].Message != "Inspect final registration" {
		t.Fatalf("polling cleared or replaced the last exact inbox request: %+v %v", child, err)
	}
}
