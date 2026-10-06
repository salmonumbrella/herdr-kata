package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/lockfile"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

func task9QueuedRun(t *testing.T, s *store.Store) (string, string) {
	t.Helper()
	uid, err := katacli.NewUID()
	if err != nil {
		t.Fatal(err)
	}
	dir := runDirFor(uid)
	target := s.Native.Client.Target
	target.Token = ""
	c := runner.NativeExecutionContext{Version: 1, RunUID: uid, Target: target, ProjectUID: s.Native.Binding.ProjectUID}
	if err := c.Save(dir); err != nil {
		t.Fatal(err)
	}
	path := nativeDeliveryPath(dir)
	id := NativeDelivery{Version: 1, TargetKey: nativeTargetKey(target), ProjectUID: c.ProjectUID, Actor: target.Actor, Teammate: target.Teammate, RunUID: uid}
	dto := katacli.RunObservation{JobUID: policyIssueUID, DefinitionEventUID: policyProjectUID, Status: "running", Summary: json.RawMessage(`{"version":1}`)}
	if err := queueNativeObservation(t.Context(), path, id, dto); err != nil {
		t.Fatal(err)
	}
	dto.Status = "succeeded"
	if err := queueNativeObservation(t.Context(), path, id, dto); err != nil {
		t.Fatal(err)
	}
	return uid, path
}

func task9RestartDaemon(t *testing.T, s *store.Store, until func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	d := &daemon{store: s, tick: 10 * time.Millisecond, slots: make(chan struct{}, 1), deliver: func(context.Context, *store.Store) error { return nil }}
	done := make(chan struct{})
	go func() { defer close(done); d.run(ctx) }()
	defer func() { cancel(); <-done }()
	// Reply-loss replay includes a one-second backoff and ordinary CLI calls
	// with three-second timeouts. Assert eventual evidence, not a shorter SLA.
	for end := time.Now().Add(15 * time.Second); time.Now().Before(end); {
		if until() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("restart daemon did not replay ordinary local outbox")
}

func TestTask9RestartRetriesFrozenObservationAndKeepsIndependentRuns(t *testing.T) {
	s, dir, _ := policyProduct(t)
	os.WriteFile(filepath.Join(dir, "observe-mode"), []byte("ack-loss"), 0600)
	_, p1 := task9QueuedRun(t, s)
	_, p2 := task9QueuedRun(t, s)
	deliverNativeObservations(t.Context(), *s.Native.Client, p1)
	var old NativeDelivery
	if err := statefs.ReadJSON(p1, 262144, &old); err != nil {
		t.Fatal(err)
	}
	if old.Pending == nil || old.Error == "" {
		t.Fatal("fixture did not lose accepted reply")
	}
	// A restart is a new scheduler, with no in-memory worker or receipt state.
	task9RestartDaemon(t, s, func() bool {
		var a, b NativeDelivery
		return statefs.ReadJSON(p1, 262144, &a) == nil && statefs.ReadJSON(p2, 262144, &b) == nil && a.Pending == nil && b.Pending == nil && a.Revision == 2 && b.Revision == 2
	})
	var bodies []string
	for _, call := range policyCalls(t, dir) {
		if strings.Contains(strings.Join(call.Args, " "), "run observe") {
			bodies = append(bodies, call.Body)
		}
	}
	if len(bodies) < 3 || bodies[0] != bodies[1] {
		t.Fatalf("restart changed frozen retry body: %v", bodies)
	}
}

func TestTask9NotifyOnlyActivationNeedsNoCheckout(t *testing.T) {
	s, _, _ := policyProduct(t)
	j := consumerDateJob(t, s, "issue-deadline", "notify", 1800)
	s.Native.Binding.Checkouts = nil
	if err := s.SetEnabled(t.Context(), j.ID, true); err != nil {
		t.Fatalf("notification-only activation requires process checkout: %v", err)
	}
	execute := consumerShellJob(t, s)
	if err := s.SetEnabled(t.Context(), execute.ID, true); err == nil {
		t.Fatal("execute activation accepted missing checkout")
	}
}

func TestTask9LoggingFailureBeforeExecutionDoesNotBlockProcess(t *testing.T) {
	s, _, _ := policyProduct(t)
	j := consumerShellJob(t, s)
	uid, err := katacli.NewUID()
	if err != nil {
		t.Fatal(err)
	}
	target := s.Native.Client.Target
	target.Token = ""
	def, err := s.Native.Client.Definition(t.Context(), "job", j.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := runner.NativeExecutionContext{Version: 1, RunUID: uid, Target: target, ProjectUID: s.Native.Binding.ProjectUID, Job: &def, Runtime: j, IssueUID: policyIssueUID}
	fd, err := s.Native.Client.Definition(t.Context(), "workflow", j.Workflow)
	if err != nil {
		t.Fatal(err)
	}
	c.Workflow = &fd
	dir := runDirFor(uid)
	if err := c.Save(dir); err != nil {
		t.Fatal(err)
	}
	path := nativeDeliveryPath(dir)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	} // owned disk fault at buffer path
	rec := store.Run{ID: uid, JobID: j.ID, RunDir: dir, Trigger: "manual"}
	run, err := runNativeContext(t.Context(), s, c, rec, workflowOpts{})
	if err != nil || run == nil || run.Outcome != runner.OutcomeDone {
		t.Fatalf("log buffer failure prohibited execution: %+v %v", run, err)
	}
	raw, _ := os.ReadFile(filepath.Join(j.CWD, "consumer-count"))
	if string(raw) != "x" {
		t.Fatalf("no actual process effect: %q", raw)
	}
	os.Remove(path)
	// Durable ordinary local run + immutable context recover after repairing disk.
	os.WriteFile(filepath.Join(filepath.Dir(s.Native.Client.Executable), "observe-mode"), []byte("ack-loss"), 0600)
	task9RestartDaemon(t, s, func() bool {
		var d NativeDelivery
		return statefs.ReadJSON(path, 262144, &d) == nil && d.Pending == nil && d.Revision > 0
	})
}

func TestTask9ReplayRespectsBackoff(t *testing.T) {
	s, dir, _ := policyProduct(t)
	_, path := task9QueuedRun(t, s)
	deliverNativeObservations(t.Context(), *s.Native.Client, path)
	before := len(policyCalls(t, dir))
	var d NativeDelivery
	statefs.ReadJSON(path, 262144, &d)
	if d.NextRetry == "" {
		t.Fatal("failed write has no bounded retry time")
	}
	if err := flushNativeObservations(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if len(policyCalls(t, dir)) != before {
		t.Fatal("replay ignored backoff")
	}
}

func TestTask9FlushBoundsWritesAndDoesNotStarveAnotherRun(t *testing.T) {
	s, dir, _ := policyProduct(t)
	ids := map[string]bool{}
	for i := 0; i < 102; i++ {
		uid, _ := task9QueuedRun(t, s)
		ids[uid] = true
	}
	if err := flushNativeObservations(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	calls := func() map[string]bool {
		out := map[string]bool{}
		count := 0
		for _, call := range policyCalls(t, dir) {
			for i, a := range call.Args {
				if a == "observe" {
					count++
					out[call.Args[i+1]] = true
				}
			}
		}
		if count > 100 && len(out) <= 100 {
			t.Fatal("one flush exceeded100 writes")
		}
		return out
	}
	if n := len(calls()); n != 100 {
		t.Fatalf("batch wrote %d runs, want100", n)
	}
	// Make failed older runs due, as on a slower daemon tick. New runs must still
	// receive their first attempt before retries consume the whole batch.
	for uid := range calls() {
		path := nativeDeliveryPath(runDirFor(uid))
		var d NativeDelivery
		statefs.ReadJSON(path, 262144, &d)
		d.NextRetry = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
		if err := statefs.WriteJSON(path, d, 262144); err != nil {
			t.Fatal(err)
		}
	}
	if err := flushNativeObservations(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if n := len(calls()); n != 102 {
		t.Fatalf("failed earlier runs starved another run: %d/102", n)
	}
}

func TestTask9FinalQueueFailureRecoversSettledTailWithoutChangingPending(t *testing.T) {
	s, dir, _ := policyProduct(t)
	j := consumerShellJob(t, s)
	uid, err := katacli.NewUID()
	if err != nil {
		t.Fatal(err)
	}
	target := s.Native.Client.Target
	target.Token = ""
	c := runner.NativeExecutionContext{Version: 1, RunUID: uid, Target: target, ProjectUID: s.Native.Binding.ProjectUID, Job: &katacli.Definition{UID: j.ID, DefinitionEventUID: j.NativeEventUID}, Runtime: j, IssueUID: policyIssueUID}
	runDir := runDirFor(uid)
	if err := c.Save(runDir); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	end := now.Add(time.Second)
	rec := store.Run{ID: uid, JobID: j.ID, Trigger: "manual", RunDir: runDir, Outcome: "done", StartedAt: now, EndedAt: &end}
	if err := s.PutRun(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Run(t.Context(), uid)
	if err != nil {
		t.Fatal(err)
	}
	rec = *stored
	path := nativeDeliveryPath(runDir)
	id := NativeDelivery{Version: 1, TargetKey: nativeTargetKey(target), ProjectUID: c.ProjectUID, Actor: target.Actor, Teammate: target.Teammate, RunUID: uid}
	initial := nativeObservation(c, "running", now, time.Time{})
	if err := queueNativeObservation(t.Context(), path, id, initial); err != nil {
		t.Fatal(err)
	}
	lock, err := lockfile.Acquire(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	failed, cancel := context.WithCancel(t.Context())
	cancel()
	err = queueNativeObservation(failed, path, id, nativeObservation(c, "succeeded", now, end))
	lock.Release()
	if err == nil {
		t.Fatal("fixture did not fail final queue write")
	}
	os.WriteFile(filepath.Join(dir, "observe-mode"), []byte("ack-loss"), 0600)
	task9RestartDaemon(t, s, func() bool {
		var d NativeDelivery
		return statefs.ReadJSON(path, 262144, &d) == nil && d.Pending == nil && d.Revision == 2
	})
	var bodies []string
	for _, call := range policyCalls(t, dir) {
		if strings.Contains(strings.Join(call.Args, " "), "run observe") {
			bodies = append(bodies, call.Body)
		}
	}
	expected, _ := json.Marshal(initial)
	if len(bodies) != 3 || bodies[0] != string(expected) || bodies[1] != string(expected) {
		t.Fatalf("frozen pending lost through settled repair: %+v", bodies)
	}
	var final katacli.RunObservation
	json.Unmarshal([]byte(bodies[2]), &final)
	if final.Status != "succeeded" || final.ExpectedRevision != 1 || final.EndedAt != rec.EndedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("settled evidence lost: %+v", final)
	}
}

func TestTask9RestartDoesNotRewriteAcknowledgedTimestampPrecision(t *testing.T) {
	s, dir, _ := policyProduct(t)
	j := consumerShellJob(t, s)
	uid, _ := katacli.NewUID()
	target := s.Native.Client.Target
	target.Token = ""
	c := runner.NativeExecutionContext{Version: 1, RunUID: uid, Target: target, ProjectUID: s.Native.Binding.ProjectUID, Job: &katacli.Definition{UID: j.ID, DefinitionEventUID: j.NativeEventUID}, Runtime: j}
	runDir := runDirFor(uid)
	if err := c.Save(runDir); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)
	end := start.Add(time.Second)
	rec := store.Run{ID: uid, JobID: j.ID, Trigger: "manual", RunDir: runDir, Outcome: "done", StartedAt: start, EndedAt: &end}
	if err := s.PutRun(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	id := NativeDelivery{Version: 1, TargetKey: nativeTargetKey(target), ProjectUID: c.ProjectUID, Actor: target.Actor, Teammate: target.Teammate, RunUID: uid}
	path := nativeDeliveryPath(runDir)
	if err := queueNativeObservation(t.Context(), path, id, nativeObservation(c, "succeeded", start, end)); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "observe-mode"), []byte("ack-loss"), 0600)
	deliverNativeObservations(t.Context(), *s.Native.Client, path)
	deliverNativeObservations(t.Context(), *s.Native.Client, path)
	before := len(policyCalls(t, dir))
	if err := flushNativeObservations(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if after := len(policyCalls(t, dir)); after != before {
		t.Fatal("restart rewrote already acknowledged evidence from lower-precision local history")
	}
}
