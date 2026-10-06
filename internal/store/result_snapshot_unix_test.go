//go:build unix

package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSettlementPipeHonorsCancellationWithoutBlockingOtherWrites(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_KATA_HOME", dir)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	runDir := filepath.Join(dir, "runs", "workflow-local")
	if err = os.MkdirAll(runDir, 0700); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(runDir, "result.json")
	if err = syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Open(fifo, syscall.O_RDWR|syscall.O_NONBLOCK, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if fd >= 0 {
			syscall.Close(fd)
		}
	}()
	now := time.Now()
	r := Run{ID: "workflow-local", JobID: "local", Workflow: "local", Outcome: "running", StartedAt: now, RunDir: runDir}
	if err = s.PutRun(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	settled := make(chan error, 1)
	r.Outcome = "done"
	r.EndedAt = &now
	go func() { settled <- s.PutRun(ctx, r) }()
	// Wait for cancellation/pipe rejection explicitly. On Unix a FIFO is
	// nonregular and rejected before a read; its producer can remain pending
	// without retaining a SQLite connection or transaction.
	settlementReturned := false
	select {
	case err = <-settled:
		settlementReturned = true
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			t.Errorf("unexpected settlement error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("settlement did not return after context expired while rejecting result.json FIFO")
	}
	written := make(chan error, 1)
	go func() { written <- s.PutJob(context.Background(), Job{ID: "unrelated", Prompt: "independent write"}) }()
	writeReturned := false
	select {
	case err = <-written:
		writeReturned = true
		if err != nil {
			t.Error(err)
		}
	case <-time.After(10 * time.Second):
		t.Error("unrelated job write blocked behind settlement's SQLite write transaction")
	}
	// Unblock the FIFO read before waiting for every goroutine; no leak even on failure.
	if err = syscall.Close(fd); err != nil {
		t.Error(err)
	}
	fd = -1
	if !settlementReturned {
		select {
		case <-settled:
		case <-time.After(10 * time.Second):
			t.Fatal("settlement did not unblock during cleanup")
		}
	}
	if !writeReturned {
		select {
		case err = <-written:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("unrelated write did not finish during cleanup")
		}
	}
}

func TestOpenResultSnapshotDoesNotBlockOnAReplacementPipe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	// Simulate a regular file replaced after the initial path check. No writer
	// exists, so a blocking open would hang even before the descriptor check.
	done := make(chan error, 1)
	go func() {
		f, err := openResultSnapshot(path)
		if err == nil {
			err = f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		// Release a blocking Open before joining the worker on a regression.
		fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NONBLOCK, 0600)
		if err == nil {
			<-done
			syscall.Close(fd)
		}
		t.Fatal("opening a replacement FIFO blocked")
	}
}

func TestSettlementPipeProducesANullSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.json")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	r := Run{ID: "pipe-result", Outcome: "done", RunDir: dir, StartedAt: now, EndedAt: &now}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.PutRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	events, err := s.RunEvents(context.Background(), r.ID)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	var payload struct{ Result json.RawMessage }
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if string(payload.Result) != "null" {
		t.Fatalf("pipe result=%s", payload.Result)
	}
}
