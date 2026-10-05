package katabridge

import (
	"context"
	"errors"
	"testing"
	"time"
)

type commentOracle struct {
	bodies         []string
	keys           []string
	comments       []HistoricalComment
	failRead, lose bool
}

func (o *commentOracle) SendComment(_ context.Context, issue, body, key string) error {
	o.bodies = append(o.bodies, body)
	o.keys = append(o.keys, key)
	o.comments = append(o.comments, HistoricalComment{Body: body, Author: "worker", Teammate: "child"})
	if o.lose {
		o.lose = false
		return errors.New("reply lost")
	}
	return nil
}
func (o *commentOracle) ReadComments(context.Context, string) ([]HistoricalComment, error) {
	if o.failRead {
		return nil, errors.New("outage")
	}
	return o.comments, nil
}
func TestCommentLostReplyBeyondSevenDaysReconcilesMarker(t *testing.T) {
	b := CommentOutbox{Dir: t.TempDir(), Scope: Scope{TargetKey: "target", ProjectUID: "project", Actor: "worker"}, Teammate: "child"}
	item := Comment{UID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", IssueUID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", Body: "Historical result", CreatedAt: time.Now().Add(-8 * 24 * time.Hour)}
	if err := b.Queue(item); err != nil {
		t.Fatal(err)
	}
	oracle := &commentOracle{lose: true}
	if err := b.Flush(t.Context(), oracle, time.Now()); err == nil {
		t.Fatal("reply loss was not visible")
	}
	// Restart, idempotency window expired: reconcile exactly one marker first.
	b = CommentOutbox{Dir: b.Dir, Scope: b.Scope, Teammate: "child"}
	if err := b.Flush(t.Context(), oracle, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(oracle.bodies) != 1 {
		t.Fatal("blind duplicate beyond native idempotency window")
	}
}
func TestCommentOutageAmbiguousMarkerAndTargetChangeNeverBlindlyPost(t *testing.T) {
	for _, kind := range []string{"outage", "ambiguous", "target"} {
		t.Run(kind, func(t *testing.T) {
			b := CommentOutbox{Dir: t.TempDir(), Scope: Scope{TargetKey: "target", ProjectUID: "project", Actor: "worker"}, Teammate: "child"}
			item := Comment{UID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", IssueUID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", Body: "Historical result", CreatedAt: time.Now().Add(-8 * 24 * time.Hour)}
			if err := b.Queue(item); err != nil {
				t.Fatal(err)
			}
			o := &commentOracle{}
			switch kind {
			case "outage":
				o.failRead = true
			case "ambiguous":
				o.comments = []HistoricalComment{{Body: item.Body + "\n" + CommentMarker(item.UID), Author: "worker", Teammate: "child"}, {Body: item.Body + "\n" + CommentMarker(item.UID), Author: "worker", Teammate: "child"}}
			case "target":
				b.Scope.TargetKey = "other"
			}
			_ = b.Flush(t.Context(), o, time.Now())
			if len(o.bodies) != 0 {
				t.Fatal("blind duplicate/misrouted historical comment")
			}
		})
	}
}
func TestCommentRetryInsideSevenDaysRetainsKeyBodyAndSeparateRuns(t *testing.T) {
	b := CommentOutbox{Dir: t.TempDir(), Scope: Scope{TargetKey: "target", ProjectUID: "project", Actor: "worker"}, Teammate: "child"}
	now := time.Now()
	o := &commentOracle{lose: true}
	item := Comment{UID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", IssueUID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", Body: "Historical result", CreatedAt: now}
	if err := b.Queue(item); err != nil {
		t.Fatal(err)
	}
	_ = b.Flush(t.Context(), o, now)
	if err := b.Flush(t.Context(), o, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(o.keys) != 2 || o.keys[0] != o.keys[1] || o.bodies[0] != o.bodies[1] {
		t.Fatal("ordinary retry changed key/body")
	}
	item.UID = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	if err := b.Queue(item); err != nil {
		t.Fatal(err)
	}
	if err := b.Flush(t.Context(), o, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(o.keys) != 3 || o.keys[1] == o.keys[2] {
		t.Fatal("independent run comments coalesced")
	}
}
