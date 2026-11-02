package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestUnreadableExistingRunRejectsManualRequest(t *testing.T) {
	w, h, k := fixture(t)
	tick(t, w, at)
	req := journal.Request{ID: "unreadable", Kind: "run", JobUID: k.jobs[0].UID}
	request(t, w, req)
	key := journal.ManualKey(w.Config.Executor, "example-project", req.ID)
	if err := state.WriteFile(filepath.Join(w.Store.Layout.Run(key), "run.json"), []byte("{")); err != nil {
		t.Fatal(err)
	}
	_ = w.Tick(context.Background(), at.Add(time.Second))
	done, reason, err := (journal.Requests{Dir: w.Store.Layout.Requests()}).Result(req.ID)
	if err != nil || !done || reason != "existing run journal is unreadable" {
		t.Fatalf("unreadable journal acknowledged as success: done=%v reason=%q err=%v", done, reason, err)
	}
	if h.runs != 0 || !w.Store.Exists(key) {
		t.Fatal("unreadable run relaunched or lost its barrier")
	}
}

func TestUnreadablePublishedPreparationIsRejected(t *testing.T) {
	w, _, k := fixture(t)
	req := journal.Request{ID: "bad-publication", Kind: "run", JobUID: k.jobs[0].UID}
	request(t, w, req)
	key := journal.ManualKey(w.Config.Executor, "example-project", req.ID)
	if err := state.WriteFile(filepath.Join(w.Store.Layout.Run(key), "run.json"), []byte("{")); err != nil {
		t.Fatal(err)
	}
	q := journal.Requests{Dir: w.Store.Layout.Requests()}
	consumed, _ := w.acknowledgePreparation(q, req, key, errors.New("preparation failed"))
	done, reason, err := q.Result(req.ID)
	if err != nil || !consumed || !done || reason != "existing run journal is unreadable" {
		t.Fatalf("unreadable publication accepted: %v %v %q %v", consumed, done, reason, err)
	}
}

func TestMissingOrUnreadableReleaseTargetRejected(t *testing.T) {
	for _, kind := range []string{"missing", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			w, _, _ := fixture(t)
			tick(t, w, at)
			req := journal.Request{ID: "release-target", Kind: "release", Run: "01J00000000000000000000099"}
			want := "file does not exist"
			if kind == "unreadable" {
				key := journal.ManualKey("executor", "example-project", "target")
				r := journal.Run{Version: 1, Key: key, RunUID: "01J00000000000000000000001", Project: "example-project", Phase: "held"}
				if err := w.Store.Create(r); err != nil {
					t.Fatal(err)
				}
				req.Run = key
				if err := state.WriteFile(filepath.Join(w.Store.Layout.Run(key), "run.json"), []byte("{")); err != nil {
					t.Fatal(err)
				}
				want = "unexpected end of JSON input"
			}
			request(t, w, req)
			_ = w.Tick(context.Background(), at.Add(time.Second))
			q := journal.Requests{Dir: w.Store.Layout.Requests()}
			done, reason, err := q.Result(req.ID)
			if err != nil || !done || !strings.Contains(reason, want) {
				t.Fatalf("release still pending: done=%v reason=%q err=%v", done, reason, err)
			}
			if pending, err := q.Pending(); err != nil || len(pending) != 0 {
				t.Fatalf("rejected release remains queued: %v %v", pending, err)
			}
		})
	}
}

func TestTemporaryReleaseLookupFailureStaysPending(t *testing.T) {
	w, _, _ := fixture(t)
	req := journal.Request{ID: "temporary-release", Kind: "release", Run: "01J00000000000000000000099"}
	request(t, w, req)
	q := journal.Requests{Dir: w.Store.Layout.Requests()}
	err := &os.PathError{Op: "read", Path: "fixture", Err: syscall.EAGAIN}
	if e := w.releaseLookupFailure(q, req, err); e == nil {
		t.Fatal("transient lookup error not reported")
	}
	if done, _, e := q.Result(req.ID); e != nil || done {
		t.Fatalf("transient release rejected: %v %v", done, e)
	}
}
