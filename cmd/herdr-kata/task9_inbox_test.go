package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/salmonumbrella/herdr-kata/internal/katabridge"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type task9LostAttentionReply struct {
	katabridge.CLIAttention
	submissions int
}

func (f *task9LostAttentionReply) Notify(ctx context.Context, issue, recipient, message string) error {
	f.submissions++
	if err := f.CLIAttention.Notify(ctx, issue, recipient, message); err != nil {
		return err
	}
	return errors.New("injected accepted ordinary notify reply loss")
}

func TestTask9InboxAndTeammateCommandsExist(t *testing.T) {
	for _, name := range []string{"inbox", "teammate"} {
		if commands()[name] == nil {
			t.Fatalf("missing executable %s integration", name)
		}
	}
}

func TestRealTask9ExactInboxReplacementAndHandlerClear(t *testing.T) {
	realPolicyHerdr(t)
	c, _ := realNativeProduct(t)
	var created struct {
		Issue struct {
			UID string `json:"uid"`
		} `json:"issue"`
	}
	if err := c.Create(t.Context(), "Inspect change", "Inbox request", "task9-inbox-fixture", &created); err != nil {
		realNativeFailure(t, err)
	}
	recipient := c.Target.Actor + "/child"
	for _, message := range []string{"First request", "Replacement"} {
		if err := c.Notify(t.Context(), created.Issue.UID, recipient, message, false, new(any)); err != nil {
			realNativeFailure(t, err)
		}
	}
	read := func(forRecipient string) katabridge.Inbox {
		out, err := katabridge.ReadInbox(t.Context(), *c, forRecipient)
		if err != nil {
			realNativeFailure(t, err)
		}
		return out
	}
	if out := read(c.Target.Actor); len(out.Requests) != 0 {
		t.Fatalf("parent aggregated teammates: %+v", out)
	}
	out := read(recipient)
	if len(out.Requests) != 1 || out.Requests[0].Message != "Replacement" {
		t.Fatalf("exact inbox lost replacement: %+v", out)
	}
	if again := read(recipient); len(again.Requests) != 1 {
		t.Fatal("reading cleared native request")
	}
	fn := commands()["inbox"]
	if fn == nil {
		t.Fatal("missing inbox CLI")
	}
	text, err := captureStdout(t, func() error { return fn([]string{"list", "--for", recipient}) })
	if err != nil || !strings.Contains(text, "Replacement") {
		t.Fatalf("CLI lost exact request: %s %v", text, err)
	}
	// The handler owns clearing. The bridge exposes no automatic clear path.
	if err := c.Notify(t.Context(), created.Issue.UID, recipient, "", true, new(any)); err != nil {
		realNativeFailure(t, err)
	}
	if out := read(recipient); len(out.Requests) != 0 {
		t.Fatal("handler clear not reflected")
	}
}

func TestRealTask9SettledHistoryQueuesOrdinaryComment(t *testing.T) {
	realPolicyHerdr(t)
	c, s := realNativeProduct(t)
	var issue struct {
		Issue struct {
			UID string `json:"uid"`
		} `json:"issue"`
	}
	if err := c.Create(t.Context(), "Inspect result", "Historical result", "task9-history", &issue); err != nil {
		realNativeFailure(t, err)
	}
	j := consumerShellJob(t, s)
	j.Ref = issue.Issue.UID
	j.NativeDefinition = policyExistingIssueDefinition(t, j.NativeDefinition, j.Ref)
	run, err := Execute(t.Context(), s, j, "manual")
	if err != nil {
		realNativeFailure(t, err)
	}
	b, _ := localBridge(s)
	out := katabridge.CommentOutbox{Dir: b.Dir, Scope: b.Scope, Teammate: c.Target.Teammate}
	n, _, err := out.Status()
	if err != nil || n != 1 {
		t.Fatalf("settled historical result not in ordinary comment outbox: %d %v", n, err)
	}
	if err := flushHistoricalComments(t.Context(), s); err != nil {
		realNativeFailure(t, err)
	}
	comments, err := (katabridge.CLIComments{Client: *c}).ReadComments(t.Context(), issue.Issue.UID)
	if err != nil {
		realNativeFailure(t, err)
	}
	if len(comments) != 1 || !strings.Contains(comments[0].Body, run.RunID) || comments[0].Author != c.Target.Actor || comments[0].Teammate != c.Target.Teammate {
		t.Fatalf("historical attribution/body lost: %+v", comments)
	}
}

func TestRealTask9AttentionCanonicalIssueWaitsForShortInboxRef(t *testing.T) {
	realPolicyHerdr(t)
	c, s := realNativeProduct(t)
	var created struct {
		Issue struct {
			UID string `json:"uid"`
		} `json:"issue"`
	}
	if err := c.Create(t.Context(), "Inspect request", "Occupied attention", "task9-occupied", &created); err != nil {
		realNativeFailure(t, err)
	}
	recipient := c.Target.Actor + "/child"
	if err := c.Notify(t.Context(), created.Issue.UID, recipient, "Existing request", false, new(any)); err != nil {
		realNativeFailure(t, err)
	}
	b, err := localBridge(s)
	if err != nil {
		t.Fatal(err)
	}
	out := katabridge.AttentionOutbox{Dir: b.Dir, Scope: b.Scope, Teammate: c.Target.Teammate}
	item := katabridge.Attention{Key: "occupied", Source: "source-1", Issue: created.Issue.UID, Recipient: recipient, Message: "New request"}
	if err := out.Queue(item); err != nil {
		t.Fatal(err)
	}
	transport := katabridge.CLIAttention{Client: *c}
	if err := out.FlushOne(t.Context(), item.Key, transport); err != nil {
		realNativeFailure(t, err)
	}
	inbox, err := katabridge.ReadInbox(t.Context(), *c, recipient)
	if err != nil {
		realNativeFailure(t, err)
	}
	if len(inbox.Requests) != 1 || inbox.Requests[0].Ref == created.Issue.UID || inbox.Requests[0].Message != "Existing request" {
		t.Fatalf("canonical UID overwrote occupied short inbox ref: %+v", inbox)
	}
	if pending, err := out.IsPending(item.Key); err != nil || !pending {
		t.Fatalf("occupied attention was not retained: %v %v", pending, err)
	}
	if err := c.Notify(t.Context(), created.Issue.UID, recipient, "", true, new(any)); err != nil {
		realNativeFailure(t, err)
	}
	if err := out.FlushOne(t.Context(), item.Key, transport); err != nil {
		realNativeFailure(t, err)
	}
	inbox, err = katabridge.ReadInbox(t.Context(), *c, recipient)
	if err != nil {
		realNativeFailure(t, err)
	}
	if len(inbox.Requests) != 1 || inbox.Requests[0].Message != item.Message {
		t.Fatalf("waiting canonical attention did not submit after handler clear: %+v", inbox)
	}
	if err := c.Notify(t.Context(), created.Issue.UID, recipient, "", true, new(any)); err != nil {
		realNativeFailure(t, err)
	}
	item.Source = "source-2"
	item.Message = "Accepted request"
	if err := out.Queue(item); err != nil {
		t.Fatal(err)
	}
	lost := &task9LostAttentionReply{CLIAttention: transport}
	if err := out.FlushOne(t.Context(), item.Key, lost); err == nil {
		t.Fatal("accepted reply loss was not injected")
	}
	time.Sleep(katabridge.RetryDelay(1) + 50*time.Millisecond)
	if err := out.FlushOne(t.Context(), item.Key, lost); err != nil {
		t.Fatal(err)
	}
	if pending, err := out.IsPending(item.Key); err != nil || pending || lost.submissions != 1 {
		t.Fatalf("accepted short-ref request was blindly resubmitted: pending=%v calls=%d err=%v", pending, lost.submissions, err)
	}
}

func TestRealTask9CustomDateNotifyNeedsNoCheckoutAndDoesNotExecute(t *testing.T) {
	realPolicyHerdr(t)
	c, s := realNativeProduct(t)
	var issue struct {
		Issue struct {
			UID string `json:"uid"`
		} `json:"issue"`
	}
	if err := c.Create(t.Context(), "Inspect deadline", "Custom notification", "task9-date", &issue); err != nil {
		realNativeFailure(t, err)
	}
	if err := c.Call(t.Context(), []string{"deadline", issue.Issue.UID, "2000-01-01T00:00:00Z"}, nil, new(any)); err != nil {
		realNativeFailure(t, err)
	}
	raw := policyJSON(t, map[string]any{"version": 1, "kind": "job", "enabled": false, "trigger": map[string]any{"kind": "issue-deadline", "issue_uid": issue.Issue.UID, "lead_seconds": 1800}, "action": map[string]any{"kind": "notify", "recipient": "worker/child", "message": "Inspect due deadline"}, "overlap": "forbid", "catchup": "latest", "issue": map[string]any{"kind": "existing", "uid": issue.Issue.UID}})
	draft, err := katacli.NewDraft("job", "", "Notify deadline", raw, "")
	if err != nil {
		t.Fatal(err)
	}
	def, err := s.Native.Save(t.Context(), draft)
	if err != nil {
		realNativeFailure(t, err)
	}
	s.Native.Binding.Checkouts = nil
	if err := s.SetEnabled(t.Context(), def.UID, true); err != nil {
		t.Fatal(err)
	}
	d := &daemon{store: s, slots: make(chan struct{}, 1), inflight: map[string]bool{}}
	d.sweep(t.Context())
	d.wg.Wait()
	inbox, err := katabridge.ReadInbox(t.Context(), *c, "worker/child")
	if err != nil {
		realNativeFailure(t, err)
	}
	if len(inbox.Requests) != 1 || inbox.Requests[0].Message != "Inspect due deadline" {
		t.Fatalf("custom date ordinary notify absent: %+v", inbox)
	}
	if err := c.Notify(t.Context(), issue.Issue.UID, "worker/child", "", true, new(any)); err != nil {
		realNativeFailure(t, err)
	}
	d.sweep(t.Context())
	d.wg.Wait()
	inbox, err = katabridge.ReadInbox(t.Context(), *c, "worker/child")
	if err != nil {
		realNativeFailure(t, err)
	}
	if len(inbox.Requests) != 0 {
		t.Fatal("handled unchanged source reappeared")
	}
	if runs, err := s.Runs(t.Context(), "", 100); err != nil || len(runs) != 0 {
		t.Fatalf("notify-only source executed process: %+v %v", runs, err)
	}
}

func TestTask9DoctorReportsPendingAndFailedOrdinaryDelivery(t *testing.T) {
	s, _, _ := policyProduct(t)
	_, path := task9QueuedRun(t, s)
	deliverNativeObservations(t.Context(), *s.Native.Client, path)
	text, err := captureStdout(t, func() error { return doctorCmd(nil) })
	if err != nil || !strings.Contains(text, "1 pending deliveries") || !strings.Contains(text, "1 failed deliveries") {
		t.Fatalf("doctor hid ordinary logging failure: %s %v", text, err)
	}
}

func TestTask9DefaultDateNotifyDelegatesToNativeSweeper(t *testing.T) {
	s, dir, _ := policyProduct(t)
	j := consumerDateJob(t, s, "issue-scheduled", "notify", 0)
	var raw map[string]any
	if err := katacli.Decode(j.NativeDefinition, &raw); err != nil {
		t.Fatal(err)
	}
	raw["action"] = map[string]any{"kind": "notify", "recipient": "current-owner-or-author"}
	draft, err := katacli.NewDraft("job", j.ID, j.Name, policyJSON(t, raw), j.NativeEventUID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Native.Save(t.Context(), draft); err != nil {
		t.Fatal(err)
	}
	dateProjection(t, dir, "2000-01-01", "UTC", "2000-01-01T00:00:00Z", 1)
	d := &daemon{store: s, slots: make(chan struct{}, 1), inflight: map[string]bool{}}
	d.sweep(t.Context())
	d.wg.Wait()
	for _, call := range policyCalls(t, dir) {
		for _, arg := range call.Args {
			if arg == "notify" || arg == "inbox" {
				t.Fatalf("second default-date producer: %v", call.Args)
			}
		}
	}
}

func TestRealTask9ManualDeliveryRevalidatesRuntimeAndRetainsInbox(t *testing.T) {
	herdrDir := realPolicyHerdr(t)
	c, s := realNativeProduct(t)
	state := policyHerdrState{Started: true, Session: "example-session", Kind: "codex", Name: "example-agent", Panes: map[string]int{"w1:p9": 123}}
	policyWrite(t, filepath.Join(herdrDir, "herdr.json"), state)
	connect := []string{"connect", "--for", "worker/child", "--workspace", "w1", "--pane", "w1:p9", "--conversation", "example-session"}
	if err := teammateCmd(connect); err != nil {
		t.Fatal(err)
	}
	b, _ := localBridge(s)
	regs, err := b.Registrations()
	if err != nil || len(regs) != 1 {
		t.Fatalf("local exact registration lost: %+v %v", regs, err)
	}
	var issue struct {
		Issue struct {
			UID string `json:"uid"`
		} `json:"issue"`
	}
	if err := c.Create(t.Context(), "Inspect inbox", "Manual delivery", "task9-runtime", &issue); err != nil {
		realNativeFailure(t, err)
	}
	if err := c.Notify(t.Context(), issue.Issue.UID, "worker/child", "Review request", false, new(any)); err != nil {
		realNativeFailure(t, err)
	}
	text, err := captureStdout(t, func() error { return inboxCmd([]string{"deliver", "--for", "worker/child"}) })
	if err != nil || !strings.Contains(text, "manual wake") || !strings.Contains(text, "Review request") {
		t.Fatalf("manual handoff absent: %s %v", text, err)
	}
	raw, _ := os.ReadFile(filepath.Join(herdrDir, "herdr.json"))
	json.Unmarshal(raw, &state)
	for _, call := range state.Calls {
		if strings.Contains(strings.Join(call, " "), "agent prompt") {
			t.Fatal("generic bridge automatically typed")
		}
	}
	// A reconnect or pane reuse never silently moves the exact inbox address.
	state.Session = "foreign-session"
	policyWrite(t, filepath.Join(herdrDir, "herdr.json"), state)
	if err := inboxCmd([]string{"deliver", "--for", "worker/child"}); err == nil {
		t.Fatal("explicit delivery accepted foreign conversation")
	}
	inbox, err := katabridge.ReadInbox(t.Context(), *c, "worker/child")
	if err != nil || len(inbox.Requests) != 1 {
		t.Fatal("manual delivery cleared native request")
	}
	if err := pollNativeInboxes(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	parent, err := katabridge.ReadInbox(t.Context(), *c, c.Target.Actor)
	if err != nil || len(parent.Requests) != 1 || !strings.Contains(parent.Requests[0].Message, "Needs human") {
		t.Fatalf("unavailable teammate needs-human signal absent: %+v %v", parent, err)
	}
	if err := teammateCmd([]string{"disconnect", "--for", "worker/child"}); err != nil {
		t.Fatal(err)
	}
	if regs, _ := b.Registrations(); len(regs) != 0 {
		t.Fatal("disconnect retained local runtime")
	}
}

func TestRealTask9OrdinaryNotifyOnceUsesNoProcessCheckout(t *testing.T) {
	realPolicyHerdr(t)
	c, s := realNativeProduct(t)
	var issue struct {
		Issue struct {
			UID string `json:"uid"`
		} `json:"issue"`
	}
	if err := c.Create(t.Context(), "Inspect attention", "Ordinary notification", "task9-once", &issue); err != nil {
		realNativeFailure(t, err)
	}
	raw := policyJSON(t, map[string]any{"version": 1, "kind": "job", "enabled": false, "trigger": map[string]any{"kind": "once", "at": "2000-01-01T00:00:00Z"}, "action": map[string]any{"kind": "notify", "recipient": "worker/child", "message": "Inspect once"}, "overlap": "forbid", "catchup": "latest", "issue": map[string]any{"kind": "existing", "uid": issue.Issue.UID}})
	draft, err := katacli.NewDraft("job", "", "Notify once", raw, "")
	if err != nil {
		t.Fatal(err)
	}
	def, err := s.Native.Save(t.Context(), draft)
	if err != nil {
		realNativeFailure(t, err)
	}
	s.Native.Binding.Checkouts = nil
	if err := s.SetEnabled(t.Context(), def.UID, true); err != nil {
		t.Fatal(err)
	}
	d := &daemon{store: s, slots: make(chan struct{}, 1), inflight: map[string]bool{}}
	d.sweep(t.Context())
	d.wg.Wait()
	inbox, err := katabridge.ReadInbox(t.Context(), *c, "worker/child")
	if err != nil {
		realNativeFailure(t, err)
	}
	if len(inbox.Requests) != 1 || inbox.Requests[0].Message != "Inspect once" {
		t.Fatalf("ordinary notify did not reach exact inbox: %+v", inbox)
	}
	if err := c.Notify(t.Context(), issue.Issue.UID, "worker/child", "", true, new(any)); err != nil {
		realNativeFailure(t, err)
	}
	d.sweep(t.Context())
	d.wg.Wait()
	inbox, err = katabridge.ReadInbox(t.Context(), *c, "worker/child")
	if err != nil {
		realNativeFailure(t, err)
	}
	if len(inbox.Requests) != 0 {
		t.Fatal("one-shot notification reappeared after handler clear")
	}
}

func TestRealTask9ParkedRunNeedsHumanAndResumeCancelsUnsentAttention(t *testing.T) {
	herdrDir := realPolicyHerdr(t)
	c, s := realNativeProduct(t)
	var issue struct {
		Issue struct {
			UID string `json:"uid"`
		} `json:"issue"`
	}
	if err := c.Create(t.Context(), "Inspect blocked run", "Runtime needs attention", "task9-parked", &issue); err != nil {
		realNativeFailure(t, err)
	}
	// Owned Herdr oracle reports blocked; there is no live user runtime.
	policyWrite(t, filepath.Join(herdrDir, "herdr.json"), policyHerdrState{Panes: map[string]int{"w1:p9": 123}, Session: "example-session", PromptStatus: "none"})
	j := policyJob(t, s, store.Job{Name: "Inspect", Prompt: "Inspect workspace", Kind: "codex", CWD: s.Native.Binding.Checkouts["primary"], Ref: issue.Issue.UID, Schedule: store.ScheduleManual, Timeout: time.Second})
	run, err := Execute(t.Context(), s, j, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if run == nil || run.Outcome != runner.OutcomeParked {
		t.Fatalf("fixture did not park: %+v", run)
	}
	b, _ := localBridge(s)
	out := katabridge.AttentionOutbox{Dir: b.Dir, Scope: b.Scope, Teammate: c.Target.Teammate}
	n, _, err := out.Status()
	if err != nil || n != 1 {
		t.Fatalf("parked run did not retain needs-human attention: %d %v", n, err)
	}
	// Resume supersedes unsent attention; a logged comment remains historical.
	rec, err := s.Run(t.Context(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	rec.Outcome = "running"
	rec.EndedAt = nil
	if err := s.PutRun(t.Context(), *rec); err != nil {
		t.Fatal(err)
	}
	if err := flushOrdinaryAttention(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if n, _, err := out.Status(); err != nil || n != 0 {
		t.Fatalf("superseded parked attention retained: %d %v", n, err)
	}
	inbox, err := katabridge.ReadInbox(t.Context(), *c, c.Target.Actor+"/"+c.Target.Teammate)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox.Requests) != 0 {
		t.Fatal("resumed run resurrected old parked attention")
	}
}
