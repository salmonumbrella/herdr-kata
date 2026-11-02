package journal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/state"
	"hegel.dev/go/hegel"
)

func sample() Run {
	return Run{Version: 1, Key: RunKey("executor", "example-project", "job", "2026-11-02T09:00:00Z"), RunUID: "01J00000000000000000000001", Project: "example-project", JobUID: "job", Phase: "prepared", StartedAt: new(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC))}
}
func TestKeys(t *testing.T) {
	if got := RunKey("executor", "example-project", "job", "2026-11-02T09:00:00Z"); got != "3df982acbf6eab245072c4a878c8c1fa" {
		t.Fatal(got)
	}
	if got := ManualKey("executor", "example-project", "request-one"); got != "588b165bbf186a26f3535d47f090fa03" {
		t.Fatal(got)
	}
}
func TestCreateIsDedupRecord(t *testing.T) {
	st := New(state.Layout{Root: t.TempDir()})
	r := sample()
	if err := st.Create(r); err != nil {
		t.Fatal(err)
	}
	r.RunUID = "01J00000000000000000000002"
	if err := st.Create(r); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	got, err := st.Load(r.Key)
	if err != nil || got.RunUID != "01J00000000000000000000001" || !st.Exists(r.Key) {
		t.Fatalf("saved: %+v %v", got, err)
	}
	// A directory without a journal is still a dedup barrier after a crash.
	key := ManualKey("executor", "example-project", "other")
	if err := os.Mkdir(st.Layout.Run(key), 0700); err != nil {
		t.Fatal(err)
	}
	r.Key = key
	if err := st.Create(r); !errors.Is(err, ErrExists) {
		t.Fatalf("missing journal was replaced: %v", err)
	}
	if !st.Exists(key) {
		t.Fatal("lost dedup barrier")
	}
}
func TestUpdateAtomic(t *testing.T) {
	st := New(state.Layout{Root: t.TempDir()})
	r := sample()
	if err := st.Create(r); err != nil {
		t.Fatal(err)
	}
	refused := errors.New("refused")
	err := st.Update(r.Key, func(r *Run) error { r.Phase = "running"; return refused })
	got, e := st.Load(r.Key)
	if !errors.Is(err, refused) || e != nil || got.Phase != "prepared" {
		t.Fatalf("failed update persisted: %+v %v %v", got, err, e)
	}
	if err := st.Update(r.Key, func(r *Run) error { r.Phase = "running"; r.AdmissionBody = []byte("frozen bytes\n"); return nil }); err != nil {
		t.Fatal(err)
	}
	// A fresh store reads the committed state and its exact frozen bytes.
	got, e = New(st.Layout).Load(r.Key)
	if e != nil || got.Phase != "running" || string(got.AdmissionBody) != "frozen bytes\n" {
		t.Fatalf("update: %+v %v", got, e)
	}
}

func TestFailedCreateDoesNotPublishPartialJournal(t *testing.T) {
	st := New(state.Layout{Root: t.TempDir()})
	first := sample()
	if err := st.Create(first); err != nil {
		t.Fatal(err)
	}
	bad := sample()
	bad.Key = ManualKey("executor", "example-project", "unpublished")
	bad.StartedAt = new(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
	if err := st.Create(bad); err == nil {
		t.Fatal("unencodable timestamp accepted")
	}
	runs, err := New(st.Layout).All()
	if err != nil || len(runs) != 1 || runs[0].Key != first.Key {
		t.Fatalf("failed publication damaged existing runs: %v %v", runs, err)
	}
	if st.Exists(bad.Key) {
		t.Fatal("incomplete journal published")
	}
	bad.StartedAt = first.StartedAt
	if err := st.Create(bad); err != nil {
		t.Fatal("unpublished occurrence cannot be prepared:", err)
	}
}

func TestUnpublishedPreparationDoesNotBlockRecovery(t *testing.T) {
	st := New(state.Layout{Root: t.TempDir()})
	r := sample()
	if err := st.Create(r); err != nil {
		t.Fatal(err)
	}
	// Crash before final directory publication, even before run.json exists.
	if err := os.Mkdir(filepath.Join(st.Layout.Root, "runs", ".prepare-interrupted"), 0700); err != nil {
		t.Fatal(err)
	}
	runs, err := New(st.Layout).Active()
	if err != nil || len(runs) != 1 || runs[0].RunUID != r.RunUID {
		t.Fatalf("unpublished preparation blocks recovery: %v %v", runs, err)
	}
}
func TestSecondLockFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve.lock")
	release, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if other, err := Lock(path); err == nil {
		other()
		t.Fatal("second writer admitted")
	}
	release()
	release()
	next, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	next()
}
func TestRequestRedeliveredAfterJournal(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	st := New(l)
	q := Requests{Dir: l.Requests()}
	req := Request{ID: "request-one", Kind: "run", Project: "example-project", JobUID: "job"}
	if err := q.Write(req); err != nil {
		t.Fatal(err)
	}
	pending, err := q.Pending()
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending %v %v", pending, err)
	}
	r := sample()
	r.Key = ManualKey("executor", req.Project, req.ID)
	r.RequestID = req.ID
	if err := st.Create(r); err != nil {
		t.Fatal(err)
	}
	// Crash before Done: the request redelivers, while the journal rejects replay.
	pending, err = q.Pending()
	if err != nil || len(pending) != 1 || !st.Exists(r.Key) {
		t.Fatalf("redelivery %v %v", pending, err)
	}
	if err := st.Create(r); !errors.Is(err, ErrExists) {
		t.Fatal(err)
	}
	if err := q.Done(req.ID); err != nil {
		t.Fatal(err)
	}
	pending, err = q.Pending()
	if err != nil || len(pending) != 0 {
		t.Fatalf("consumed %v %v", pending, err)
	}
}
func TestJournalRoundTrip(t *testing.T) {
	st := New(state.Layout{Root: t.TempDir()})
	hegel.Test(t, func(ht *hegel.T) {
		value := hegel.Draw(ht, hegel.Text())
		r := sample()
		r.Key = ManualKey("executor", r.Project, value)
		r.Hold = value
		if st.Exists(r.Key) {
			return
		}
		if err := st.Create(r); err != nil {
			ht.Fatal(err)
		}
		got, err := st.Load(r.Key)
		if err != nil || got.Hold != value {
			ht.Fatalf("round trip %q -> %q: %v", value, got.Hold, err)
		}
	})
}
