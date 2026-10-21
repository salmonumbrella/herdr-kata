package runner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

func TestNotifyResolvesOwnerOrAuthor(t *testing.T) {
	for _, owner := range []string{"owner", ""} {
		t.Run(owner, func(t *testing.T) { testNotify(t, owner, "current-owner-or-author", "requested", false, false) })
	}
	t.Run("explicit", func(t *testing.T) { testNotify(t, "owner", "recipient/teammate", "requested", false, false) })
}
func TestNotifyDefaultMessage(t *testing.T) {
	testNotify(t, "owner", "current-owner-or-author", "", false, false)
}
func TestNotifyReadBackLostReply(t *testing.T) {
	testNotify(t, "owner", "current-owner-or-author", "requested", true, false)
}
func TestNotifyToScheduledIssueDelivered(t *testing.T) {
	testNotify(t, "owner", "current-owner-or-author", "requested", false, true)
}
func testNotify(t *testing.T, owner, recipient, message string, lost, scheduled bool) {
	t.Helper()
	f := newAdmission(t)
	f.d.Def.Action = defs.Action{Kind: "notify", Recipient: recipient, Message: message}
	f.d.Def.Issue = &defs.IssuePolicy{Kind: "existing", UID: "scheduled-issue"}
	f.d.WFDef = nil
	f.d.Workflow = nil
	f.d.Job.Name = "Reminder"
	expectedTo := recipient
	if recipient == "current-owner-or-author" {
		expectedTo = owner
		if expectedTo == "" {
			expectedTo = "author"
		}
	}
	expectedMessage := message
	if expectedMessage == "" {
		expectedMessage = "Kata cron: Reminder is due"
	}
	var notifications, observations atomic.Int32
	var delivered atomic.Bool
	f.k.show = func(context.Context, string) (kata.IssueView, error) {
		issue := kata.IssueView{UID: "scheduled-issue", QualifiedID: "project#abcd", Status: "open", Owner: owner, Author: "author", Metadata: map[string]json.RawMessage{}}
		if scheduled {
			issue.Metadata["scheduled_on"] = json.RawMessage(`"2099-01-01"`)
		}
		if delivered.Load() {
			p, _ := json.Marshal(map[string]string{"message": expectedMessage, "from": "worker", "teammate": "herdr-kata"})
			issue.Metadata["notify."+base64.RawURLEncoding.EncodeToString([]byte(expectedTo))] = p
		}
		return issue, nil
	}
	f.k.notify = func(_ context.Context, ref, to, msg string) error {
		notifications.Add(1)
		if ref != "project#abcd" || to != expectedTo || msg != expectedMessage {
			t.Error(ref, to, msg)
		}
		delivered.Store(true)
		if lost {
			return lostReply
		}
		return nil
	}
	f.k.observe = func(_ context.Context, _ string, o kata.Observation) (kata.Run, bool, error) {
		n := observations.Add(1)
		if n == 1 && o.Status != "running" || n == 2 && (o.Status != "succeeded" || !strings.Contains(o.Summary.Message, expectedMessage)) {
			t.Errorf("observation %+v", o)
		}
		return kata.Run{}, false, nil
	}
	f.k.comment = func(context.Context, string, string, string) error { return nil }
	f.start(t)
	decision, e := f.m.Admit(t.Context(), f.d)
	if e != nil || decision == 0 {
		t.Fatal(decision, e)
	}
	f.m.Wait()
	if e = f.q.wait(t.Context(), f.uid, "notify-observe"); e != nil {
		t.Fatal(e)
	}
	r := loaded(t, f.s, f.uid)
	if r.IssueRef != "project#abcd" || r.Phase != Settled || r.Outcome != "succeeded" || r.IssueOwned || notifications.Load() != 1 || observations.Load() != 2 || f.m.GateHeld(f.d.Job.UID) {
		t.Fatal(r, notifications.Load(), observations.Load())
	}
	for _, op := range r.Ops {
		if op.Kind != "observe" && op.Kind != "resolve-recipient" && op.Kind != "notify" {
			t.Fatal("unexpected mutation", op)
		}
	}
}
