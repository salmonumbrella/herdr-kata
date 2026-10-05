package katabridge

import (
	"encoding/json"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"hegel.dev/go/hegel"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRuntimeRegistrationNeverLeaksAnotherScope(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		target := strings.ToValidUTF8(hegel.Draw(ht, hegel.Text()), "\ufffd")
		project := strings.ToValidUTF8(hegel.Draw(ht, hegel.Text()), "\ufffd")
		actor := strings.ToValidUTF8(hegel.Draw(ht, hegel.Text()), "\ufffd")
		b := Bridge{Dir: t.TempDir(), Scope: Scope{TargetKey: "target:" + target, ProjectUID: "project:" + project, Actor: "actor:" + actor}}
		r := Registration{Recipient: b.Scope.Actor + "/child", Workspace: "w1", Pane: "p1", Conversation: "session"}
		raw, _ := json.Marshal(registrationsFile{Version: 1, Entries: map[string]localRuntime{b.key(r.Recipient): {Scope: b.Scope, Registration: r}}})
		err := b.Connect(r)
		if len(raw) > 262144 {
			if err == nil {
				ht.Fatal("oversized local registrations accepted")
			}
			return
		}
		if err != nil {
			ht.Fatal(err)
		}
		got, err := b.Registrations()
		if err != nil || len(got) != 1 || got[0] != r {
			ht.Fatal("exact local identity failed round-trip")
		}
		for _, field := range []string{"target", "project", "actor"} {
			other := b
			switch field {
			case "target":
				other.Scope.TargetKey = "other:" + b.Scope.TargetKey
			case "project":
				other.Scope.ProjectUID = "other:" + b.Scope.ProjectUID
			case "actor":
				other.Scope.Actor = "other:" + b.Scope.Actor
			}
			got, err := other.Registrations()
			if err != nil || len(got) != 0 {
				ht.Fatalf("registration leaked %s scope", field)
			}
		}
	})
}
func TestCommentQueueNeverChangesFrozenBody(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		body := strings.ToValidUTF8(hegel.Draw(ht, hegel.Text()), "\ufffd")
		b := CommentOutbox{Dir: t.TempDir(), Scope: Scope{TargetKey: "target", ProjectUID: "project", Actor: "worker"}}
		item := Comment{UID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", IssueUID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", Body: body, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
		raw, _ := json.Marshal(commentFile{Version: 1, Scope: b.Scope, Item: item})
		err := b.Queue(item)
		if len(body) > 65536 || len(raw) > 98304 {
			if err == nil {
				ht.Fatal("oversized comment accepted")
			}
			return
		}
		if err != nil {
			ht.Fatal(err)
		}
		if err := b.Queue(item); err != nil {
			ht.Fatal("exact retry rejected")
		}
		item.Body = "changed:" + body
		if err := b.Queue(item); err == nil {
			ht.Fatal("same comment identity changed body")
		}
	})
}

func TestOrdinaryCancelledSourceRestoresButCompletedSourceDoesNot(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		source := strings.ToValidUTF8(hegel.Draw(ht, hegel.Text()), "\ufffd")
		b := AttentionOutbox{Dir: t.TempDir(), Scope: Scope{TargetKey: "target", ProjectUID: "project", Actor: "worker"}, Teammate: "child"}
		item := Attention{Key: "date-job", Source: "source:" + source, Issue: "abcd", Recipient: "worker/child", Message: "Inspect"}
		raw, _ := json.Marshal(attentionFile{Version: 1, Scope: b.Scope, Teammate: b.Teammate, Item: item, Pending: true})
		err := b.Queue(item)
		if len(raw) > 98304 {
			if err == nil {
				ht.Fatal("oversized attention source accepted")
			}
			return
		}
		if err != nil {
			ht.Fatal(err)
		}
		cancelled, _ := json.Marshal(attentionFile{Version: 1, Scope: b.Scope, Teammate: b.Teammate, Item: item, Cancelled: true})
		cancelErr := b.CancelPending(item.Key)
		if len(cancelled) > 98304 {
			if cancelErr == nil {
				ht.Fatal("oversized cancellation state accepted")
			}
			if pending, err := b.IsPending(item.Key); err != nil || !pending {
				ht.Fatal("rejected oversized cancellation discarded pending intent")
			}
			return
		}
		if cancelErr != nil {
			ht.Fatal(cancelErr)
		}
		// Fresh object models restart: all intent distinction must be on disk.
		restarted := AttentionOutbox{Dir: b.Dir, Scope: b.Scope, Teammate: b.Teammate}
		if err := restarted.Queue(item); err != nil {
			ht.Fatal(err)
		}
		o := &attentionOracle{requests: map[string][]Request{}}
		if err := restarted.FlushOne(t.Context(), item.Key, o); err != nil || o.sent != 1 {
			ht.Fatal("identical cancelled source never became eligible")
		}
		o.requests[item.Recipient] = nil
		if err := restarted.CancelPending(item.Key); err != nil {
			ht.Fatal(err)
		}
		if err := restarted.Queue(item); err != nil {
			ht.Fatal(err)
		}
		if err := restarted.FlushOne(t.Context(), item.Key, o); err != nil || o.sent != 1 {
			ht.Fatal("identical completed source reappeared")
		}
	})
}

func TestOrdinarySettlementSurvivesLaterIntentWithoutCrossingScope(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		message := "Inspect:" + strings.ToValidUTF8(hegel.Draw(ht, hegel.Text()), "\ufffd")
		b := AttentionOutbox{Dir: t.TempDir(), Scope: Scope{TargetKey: "target", ProjectUID: "project", Actor: "worker"}, Teammate: "child"}
		first := Attention{Key: "notify-job", Source: "run-1", Issue: "abcd", Recipient: "worker/child", Message: message, RecordRun: true}
		err := b.Queue(first)
		if len(message) > 1024 {
			if err == nil {
				ht.Fatal("oversized ordinary message accepted")
			}
			return
		}
		if err != nil {
			ht.Fatal(err)
		}
		o := &attentionOracle{requests: map[string][]Request{}}
		if err := b.FlushOne(t.Context(), first.Key, o); err != nil {
			ht.Fatal(err)
		}
		second := first
		second.Source = "run-2"
		if err := b.Queue(second); err != nil {
			ht.Fatal(err)
		}
		if err := b.CancelPending(second.Key); err != nil {
			ht.Fatal(err)
		}
		restarted := AttentionOutbox{Dir: b.Dir, Scope: b.Scope, Teammate: b.Teammate}
		unfinished, err := restarted.Unsettled()
		if err != nil || len(unfinished) != 1 || unfinished[0].Item != first || unfinished[0].CompletedAt.IsZero() {
			ht.Fatal("new/cancelled intent discarded an earlier unfinished settlement")
		}
		if err := restarted.Queue(second); err != nil {
			ht.Fatal(err)
		}
		o.requests[second.Recipient] = nil
		if err := restarted.FlushOne(t.Context(), second.Key, o); err != nil || o.sent != 2 {
			ht.Fatal("second independent intent failed")
		}
		if err := restarted.FinishSettlement(unfinished[0]); err != nil {
			ht.Fatal(err)
		}
		unfinished, err = restarted.Unsettled()
		if err != nil || len(unfinished) != 1 || unfinished[0].Item != second {
			ht.Fatal("finishing an older run discarded newer unfinished history")
		}
		changed := unfinished[0]
		changed.Item.Message += "changed"
		if err := restarted.FinishSettlement(changed); err == nil {
			ht.Fatal("changed settlement identity was accepted")
		}
		other := restarted
		other.Scope.Actor = "another-worker"
		if leaked, err := other.Unsettled(); err != nil || len(leaked) != 0 {
			ht.Fatal("unfinished local settlement crossed actor scope")
		}
		if err := restarted.FinishSettlement(unfinished[0]); err != nil {
			ht.Fatal(err)
		}
		if remaining, err := restarted.Unsettled(); err != nil || len(remaining) != 0 {
			ht.Fatal("finished local settlement remained queued")
		}
	})
}

func TestRuntimePollingCoversScopedRegistryFromEveryCursor(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		after := strings.ToValidUTF8(hegel.Draw(ht, hegel.Text()), "\ufffd")
		b := Bridge{Dir: t.TempDir(), Scope: Scope{TargetKey: "target", ProjectUID: "project", Actor: "worker"}}
		f := registrationsFile{Version: 1, Entries: map[string]localRuntime{}, PollAfter: map[string]string{b.key(""): after}}
		for i := range 101 {
			r := Registration{Recipient: fmt.Sprintf("worker/child%03d", i), Workspace: "w1", Pane: "p1", Conversation: "session"}
			f.Entries[b.key(r.Recipient)] = localRuntime{Scope: b.Scope, Registration: r}
		}
		other := b
		other.Scope.ProjectUID = "another-project"
		foreign := Registration{Recipient: "worker/foreign", Workspace: "w1", Pane: "p1", Conversation: "session"}
		f.Entries[other.key(foreign.Recipient)] = localRuntime{Scope: other.Scope, Registration: foreign}
		f.PollAfter[other.key("")] = foreign.Recipient
		raw, _ := json.Marshal(f)
		err := statefs.WriteJSON(filepath.Join(b.Dir, "runtime-registrations.json"), f, 262144)
		if len(raw) > 262144 {
			if err == nil {
				ht.Fatal("oversized cursor/registry accepted")
			}
			return
		}
		if err != nil {
			ht.Fatal(err)
		}
		seen := map[string]bool{}
		for range 2 {
			restarted := Bridge{Dir: b.Dir, Scope: b.Scope}
			batch, err := restarted.PollBatch()
			if err != nil || len(batch) != 100 {
				ht.Fatal("poll batch did not remain bounded")
			}
			inBatch := map[string]bool{}
			for _, r := range batch {
				if r.Recipient == foreign.Recipient || inBatch[r.Recipient] {
					ht.Fatal("poll leaked another project or duplicated a registration")
				}
				inBatch[r.Recipient] = true
				seen[r.Recipient] = true
			}
		}
		if len(seen) != 101 {
			ht.Fatal("two bounded polls starved a valid registration")
		}
		remaining, err := b.read()
		if err != nil || remaining.PollAfter[other.key("")] != foreign.Recipient {
			ht.Fatal("poll overwrote another project cursor")
		}
	})
}
