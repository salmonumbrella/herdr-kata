package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/flow"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

func TestExecutionPolicySameSavedUIDCannotRunConcurrently(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("requires sh")
	}
	s, _, _ := policyProduct(t)
	draft, err := flow.NativeDraft(flow.Flow{NativeName: "Inspect", Steps: []store.Step{{ID: "inspect", Run: "printf x >> same-count; touch started; while test ! -f release; do sleep 0.01; done"}}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	fd, err := s.Native.Save(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := katacli.NewUID()
	if err != nil {
		t.Fatal(err)
	}
	cwd := s.Native.Binding.Checkouts["primary"]
	j := store.Job{ID: fd.UID, Flow: fd.UID, CWD: cwd}
	rec := store.Run{ID: uid, JobID: fd.UID, Flow: fd.UID, Trigger: "manual", RunDir: runDirFor(uid)}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type result struct {
		run *runner.Run
		err error
	}
	done := make(chan result, 1)
	go func() { run, err := runFlow(ctx, s, j, rec, flowOpts{}); done <- result{run, err} }()
	defer func() { os.WriteFile(filepath.Join(cwd, "release"), nil, 0600); cancel(); <-done }()
	started := false
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
		if _, err := os.Stat(filepath.Join(cwd, "started")); err == nil {
			started = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !started {
		t.Fatal("initial shell command did not start")
	}
	active, err := s.Run(t.Context(), uid)
	if err != nil {
		t.Fatal(err)
	}
	retryCtx, retryCancel := context.WithTimeout(t.Context(), time.Second)
	defer retryCancel()
	if _, err := runFlow(retryCtx, s, j, *active, flowOpts{}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("same saved UID did not return already-running: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(cwd, "same-count"))
	if err != nil || string(raw) != "x" {
		t.Errorf("same UID started duplicate side effects: %q %v", raw, err)
	}
	// A distinct UID is independent even while the first command is paused.
	otherCWD := t.TempDir()
	s.Native.Binding.Checkouts["other"] = otherCWD
	if err := os.WriteFile(filepath.Join(otherCWD, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	otherUID, err := katacli.NewUID()
	if err != nil {
		t.Fatal(err)
	}
	otherJob := j
	otherJob.CWD = otherCWD
	otherRec := store.Run{ID: otherUID, JobID: fd.UID, Flow: fd.UID, Trigger: "manual", RunDir: runDirFor(otherUID)}
	run, err := runFlow(t.Context(), s, otherJob, otherRec, flowOpts{})
	if err != nil || run == nil || run.Outcome != runner.OutcomeDone {
		t.Fatalf("distinct UID was excluded: %+v %v", run, err)
	}
	raw, err = os.ReadFile(filepath.Join(otherCWD, "same-count"))
	if err != nil || string(raw) != "x" {
		t.Fatalf("distinct UID missing side effect: %q %v", raw, err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	first := <-done
	done <- first
	if first.err != nil || first.run == nil || first.run.Outcome != runner.OutcomeDone {
		t.Fatalf("first execution unsettled: %+v %v", first.run, first.err)
	}
}
