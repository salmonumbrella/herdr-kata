package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func logsFixture(t *testing.T) (state.Layout, *runner.Store, string, string) {
	t.Helper()
	l := state.Layout{Root: t.TempDir()}
	s := runner.NewStore(state.OSFS{}, l)
	uid := "01K00000000000000000000000"
	if e := s.Create(&runner.Run{Version: 1, UID: uid, Phase: runner.Running, Steps: []runner.StepState{{Key: "check", Kind: "command", Attempt: 1}}}); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(l.AttemptDir(uid, "check", 1), "output.log")
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte("first\n"), 0600); e != nil {
		t.Fatal(e)
	}
	return l, s, uid, path
}
func TestLogsFollow(t *testing.T) {
	l, s, uid, path := logsFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	done := make(chan error, 1)
	go func() { e := streamRunLogs(ctx, l, uid, true, writer); writer.Close(); done <- e }()
	b := make([]byte, len("[check · attempt 1]\nfirst\n"))
	if _, e := io.ReadFull(reader, b); e != nil || string(b) != "[check · attempt 1]\nfirst\n" {
		t.Fatal(string(b), e)
	}
	f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	f.WriteString("second\n")
	f.Close()
	// A later step becomes visible while following the same run.
	next := filepath.Join(l.AttemptDir(uid, "next", 1), "output.log")
	os.MkdirAll(filepath.Dir(next), 0700)
	os.WriteFile(next, []byte("third\n"), 0600)
	_, e = s.Transition(uid, func(r *runner.Run) error {
		r.Steps = append(r.Steps, runner.StepState{Key: "next", Kind: "command", Attempt: 1})
		r.Phase = runner.Settled
		now := time.Now()
		r.EndedAt = &now
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	rest, e := io.ReadAll(reader)
	if e != nil || string(rest) != "second\n[next · attempt 1]\nthird\n" {
		t.Fatal(string(rest), e)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}
func TestLogsSnapshotAndValidation(t *testing.T) {
	l, _, uid, _ := logsFixture(t)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
	var out, stderr bytes.Buffer
	if code := run([]string{"logs", uid}, &out, &stderr); code != 0 || out.String() != "[check · attempt 1]\nfirst\n" {
		t.Fatal(code, out.String(), stderr.String())
	}
	for _, args := range [][]string{{"logs"}, {"logs", "../outside"}, {"logs", uid, "extra"}, {"logs", uid, "--bad"}} {
		out.Reset()
		stderr.Reset()
		if code := run(args, &out, &stderr); code != 2 {
			t.Fatal(args, code, stderr.String())
		}
	}
}

func TestLogsFollowCappedTailMovement(t *testing.T) {
	l, s, uid, path := logsFixture(t)
	head := bytes.Repeat([]byte("h"), 1<<20)
	tail := bytes.Repeat([]byte("a"), 256<<10)
	initial := append(append([]byte{}, head...), tail...)
	if e := os.WriteFile(path, initial, 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	done := make(chan error, 1)
	go func() { e := streamRunLogs(ctx, l, uid, true, writer); writer.CloseWithError(e); done <- e }()
	read := func(want []byte) {
		t.Helper()
		got := make([]byte, len(want))
		if _, e := io.ReadFull(reader, got); e != nil || !bytes.Equal(got, want) {
			t.Fatalf("log delta: got %d bytes, want %d, err=%v", len(got), len(want), e)
		}
	}
	read(append([]byte("[check · attempt 1]\n"), initial...))
	marker := []byte("\n[output truncated]\n")
	for _, character := range []byte{'b', 'c'} {
		delta := append(append([]byte{}, marker...), bytes.Repeat([]byte{character}, 256<<10)...)
		f, e := os.OpenFile(path, os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		_, e = f.WriteAt(delta, int64(len(head)))
		f.Close()
		if e != nil {
			t.Fatal(e)
		}
		// The new log retains its head and replaces its capped tail in place.
		read(delta)
	}
	// A replacement without the truncation marker prints its complete contents.
	replacement := []byte("replacement output\n")
	if e := os.WriteFile(path, replacement, 0600); e != nil {
		t.Fatal(e)
	}
	read(replacement)
	if _, e := s.Transition(uid, func(r *runner.Run) error { r.Phase = runner.Settled; return nil }); e != nil {
		t.Fatal(e)
	}
	rest, e := io.ReadAll(reader)
	if e != nil || len(rest) != 0 {
		t.Fatal(string(rest), e)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}

func TestLogsFollowRetriesTransientCorruptRun(t *testing.T) {
	l, _, uid, path := logsFixture(t)
	saved, err := os.ReadFile(l.RunFile(uid))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.RunFile(uid), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	restored := make(chan error, 1)
	go func() {
		timer := time.NewTimer(300 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			restored <- ctx.Err()
			return
		case <-timer.C:
		}
		if err := os.WriteFile(path, []byte("later output\n"), 0600); err != nil {
			restored <- err
			return
		}
		var r runner.Run
		if err := json.Unmarshal(saved, &r); err != nil {
			restored <- err
			return
		}
		r.Phase = runner.Settled
		restored <- state.WriteJSON(state.OSFS{}, l.RunFile(uid), &r)
	}()
	var out bytes.Buffer
	err = streamRunLogs(ctx, l, uid, true, &out)
	if err != nil {
		t.Fatal("follow stopped during transient corruption:", err)
	}
	if err := <-restored; err != nil {
		t.Fatal(err)
	}
	if out.String() != "[check · attempt 1]\nlater output\n" {
		t.Fatal(out.String())
	}
}

func TestLogsCorruptionRetryIsBoundedAndFollowOnly(t *testing.T) {
	for _, follow := range []bool{false, true} {
		t.Run(fmt.Sprint(follow), func(t *testing.T) {
			l, _, uid, _ := logsFixture(t)
			if err := os.WriteFile(l.RunFile(uid), []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			start := time.Now()
			err := streamRunLogs(ctx, l, uid, follow, io.Discard)
			if _, ok := errors.AsType[*state.CorruptError](err); !ok {
				t.Fatalf("want corruption error, got %v", err)
			}
			elapsed := time.Since(start)
			if follow && elapsed < 800*time.Millisecond {
				t.Fatalf("no bounded retries: %v", elapsed)
			}
			if !follow && elapsed >= 200*time.Millisecond {
				t.Fatalf("snapshot retried: %v", elapsed)
			}
		})
	}
}

func TestLogsFollowCorruptionRetryHonorsCancellation(t *testing.T) {
	l, _, uid, _ := logsFixture(t)
	if err := os.WriteFile(l.RunFile(uid), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := streamRunLogs(ctx, l, uid, true, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestLogsShowEveryAttemptIncludingPrompts(t *testing.T) {
	l, s, uid, _ := logsFixture(t)
	for _, dir := range []string{l.AttemptDir(uid, "check", 2), l.AttemptDir(uid, "review", 1)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, e := s.Transition(uid, func(r *runner.Run) error {
		r.Steps[0].Attempt = 2
		r.Steps = append(r.Steps, runner.StepState{Key: "review", Kind: "prompt", Attempt: 1})
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	if e := streamRunLogs(t.Context(), l, uid, false, &out); e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"[check · attempt 1]", "[check · attempt 2]", "[review · attempt 1]", "first"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(want, out.String())
		}
	}
}

func TestLogsFollowLabelsOutputAfterFutureAttemptHeaders(t *testing.T) {
	l, s, uid, path := logsFixture(t)
	if err := os.MkdirAll(l.AttemptDir(uid, "future", 1), 0700); err != nil {
		t.Fatal(err)
	}
	if _, e := s.Transition(uid, func(r *runner.Run) error {
		r.Steps = append(r.Steps, runner.StepState{Key: "future", Kind: "prompt", Attempt: 1})
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	done := make(chan error, 1)
	go func() { e := streamRunLogs(ctx, l, uid, true, writer); writer.CloseWithError(e); done <- e }()
	expected := "[check · attempt 1]\nfirst\n[future · attempt 1]\n"
	b := make([]byte, len(expected))
	if _, e := io.ReadFull(reader, b); e != nil || string(b) != expected {
		t.Fatal(string(b), e)
	}
	if e := os.WriteFile(path, []byte("first\nsecond\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Transition(uid, func(r *runner.Run) error { r.Phase = runner.Settled; return nil }); e != nil {
		t.Fatal(e)
	}
	rest, e := io.ReadAll(reader)
	if e != nil || string(rest) != "[check · attempt 1]\nsecond\n" {
		t.Fatal("output attributed to future attempt", string(rest), e)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}

func TestLogsAcceptRequestJournal(t *testing.T) {
	l, _, uid, _ := logsFixture(t)
	var r runner.Run
	if err := state.ReadJSON(state.OSFS{}, l.RunFile(uid), 4<<20, &r); err != nil {
		t.Fatal(err)
	}
	r.Version, r.Source = 2, "issue-request:child"
	if err := state.WriteJSON(state.OSFS{}, l.RunFile(uid), r); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := streamRunLogs(t.Context(), l, uid, false, &out); err != nil || out.String() != "[check · attempt 1]\nfirst\n" {
		t.Fatalf("logs=%q err=%v", out.String(), err)
	}
}
