package katabridge

import (
	"context"
	"os"
	"strings"
	"testing"
)

type attentionOracle struct {
	requests map[string][]Request
	sent     int
	lose     bool
}

func (o *attentionOracle) Inbox(_ context.Context, recipient, _ string) ([]Request, error) {
	return o.requests[recipient], nil
}
func (o *attentionOracle) Notify(_ context.Context, issue, recipient, message string) error {
	o.sent++
	o.requests[recipient] = append(o.requests[recipient], Request{Ref: issue, Message: message, From: "worker", Teammate: "child"})
	return nil
}
func TestOrdinaryAttentionWaitsForOccupiedSlotAndDoesNotReappearAfterClear(t *testing.T) {
	b := AttentionOutbox{Dir: t.TempDir(), Scope: Scope{TargetKey: "target", ProjectUID: "project", Actor: "worker"}, Teammate: "child"}
	item := Attention{Key: "date-job", Source: "source-1", Issue: "abcd", Recipient: "worker/child", Message: "Due"}
	if err := b.Queue(item); err != nil {
		t.Fatal(err)
	}
	o := &attentionOracle{requests: map[string][]Request{"worker/child": {{Ref: "abcd", Message: "Existing"}}}}
	if err := b.FlushOne(t.Context(), item.Key, o); err != nil {
		t.Fatal(err)
	}
	if o.sent != 0 {
		t.Fatal("occupied ordinary attention replaced")
	}
	o.requests[item.Recipient] = nil
	if err := b.FlushOne(t.Context(), item.Key, o); err != nil {
		t.Fatal(err)
	}
	if o.sent != 1 {
		t.Fatal("waiting ordinary notify not sent")
	}
	o.requests[item.Recipient] = nil // handler clear
	if err := b.Queue(item); err != nil {
		t.Fatal(err)
	}
	if err := b.FlushOne(t.Context(), item.Key, o); err != nil {
		t.Fatal(err)
	}
	if o.sent != 1 {
		t.Fatal("handled unchanged source reappeared")
	}
	item.Source = "source-2"
	item.Message = "Moved due"
	if err := b.Queue(item); err != nil {
		t.Fatal(err)
	}
	if err := b.FlushOne(t.Context(), item.Key, o); err != nil {
		t.Fatal(err)
	}
	if o.sent != 2 {
		t.Fatal("new source was dropped")
	}
	item.Source = "source-3"
	item.Message = "Cancelled"
	if err := b.Queue(item); err != nil {
		t.Fatal(err)
	}
	if err := b.CancelPending(item.Key); err != nil {
		t.Fatal(err)
	}
	o.requests[item.Recipient] = nil
	if err := b.FlushOne(t.Context(), item.Key, o); err != nil {
		t.Fatal(err)
	}
	if o.sent != 2 {
		t.Fatal("cancelled source notified")
	}
}
func TestOrdinaryAttentionRejectsNativeMessageOverflow(t *testing.T) {
	b := AttentionOutbox{Dir: t.TempDir(), Scope: Scope{TargetKey: "target", ProjectUID: "project", Actor: "worker"}}
	item := Attention{Key: "job", Source: "source", Issue: "abcd", Recipient: "worker", Message: strings.Repeat("x", 1025)}
	if err := b.Queue(item); err == nil {
		t.Fatal("ordinary native notify message limit ignored")
	}
	item.Message = strings.Repeat("x", 1024)
	if err := b.Queue(item); err != nil {
		t.Fatal(err)
	}
}

func TestAttentionSavedKeyMismatchNeverSendsAnotherItem(t *testing.T) {
	b := AttentionOutbox{Dir: t.TempDir(), Scope: Scope{TargetKey: "target", ProjectUID: "project", Actor: "worker"}, Teammate: "child"}
	item := Attention{Key: "original", Source: "source", Issue: "abcd", Recipient: "worker/child", Message: "Review"}
	if err := b.Queue(item); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(b.path(item.Key))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.path("different"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	o := &attentionOracle{requests: map[string][]Request{}}
	if err := b.FlushOne(t.Context(), "different", o); err == nil || o.sent != 0 {
		t.Fatal("saved key mismatch sent another attention item")
	}
}
