package katabridge

import (
	"os"
	"testing"
	"time"
)

func TestUnchangedCompletedAttentionDoesNotRewriteState(t *testing.T) {
	b := AttentionOutbox{Dir: t.TempDir(), Scope: Scope{TargetKey: "target", ProjectUID: "project", Actor: "worker"}}
	item := Attention{Key: "date-job", Source: "source", Issue: "abcd", Recipient: "worker", Message: "Due"}
	if err := b.Queue(item); err != nil {
		t.Fatal(err)
	}
	o := &attentionOracle{requests: map[string][]Request{}}
	if err := b.FlushOne(t.Context(), item.Key, o); err != nil {
		t.Fatal(err)
	}
	old := time.Unix(1, 0)
	if err := os.Chtimes(b.path(item.Key), old, old); err != nil {
		t.Fatal(err)
	}
	if err := b.Queue(item); err != nil {
		t.Fatal(err)
	}
	if err := b.FlushOne(t.Context(), item.Key, o); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(b.path(item.Key))
	if err != nil {
		t.Fatal(err)
	}
	if !st.ModTime().Equal(old) {
		t.Fatalf("unchanged completed state rewritten: %v", st.ModTime())
	}
}
