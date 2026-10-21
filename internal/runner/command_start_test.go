package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestCommandPersistsGroupBeforeCompletion(t *testing.T) {
	spec := commandSpec(t, `printf ready > ready; while [ ! -f finish ]; do sleep 0.01; done`)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := RunCommand(ctx, spec); done <- err }()
	defer func() { _ = os.WriteFile(filepath.Join(spec.Cwd, "finish"), nil, 0600); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(spec.AttemptDir, "process.json")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command never started")
		}
		<-ticker.C
	}
	var started Receipt
	if err := state.ReadJSON(state.OSFS{}, filepath.Join(spec.AttemptDir, "process.json"), 4096, &started); err != nil {
		t.Fatal("running command has no durable process identity", err)
	}
	if started.PGID <= 0 || started.Run != "run-1" || started.Step != "check" || started.Attempt != 1 || !started.Uncertain || !started.EndedAt.IsZero() {
		t.Fatalf("invalid running proof: %+v", started)
	}
	if GroupGone(started.PGID) {
		t.Fatal("live command group was considered gone")
	}
}

func TestHeldCommandWithoutFinalReceiptReturnsCapacityAfterGroupExits(t *testing.T) {
	spec := commandSpec(t, "true")
	spec.AttemptDir = (state.Layout{Root: spec.RunDir}).AttemptDir(spec.Run, spec.Step, spec.Attempt)
	receipt, err := RunCommand(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce a worker lost before its final receipt was committed.
	if err := os.Remove(filepath.Join(spec.AttemptDir, "receipt.json")); err != nil {
		t.Fatal(err)
	}
	s := NewStore(state.OSFS{}, state.Layout{Root: spec.RunDir})
	r := &Run{Version: 1, UID: spec.Run, Phase: Held, Outcome: "unknown", Reason: "command_no_receipt", Steps: []StepState{{Key: spec.Step, Kind: "command", Attempt: 1, Submission: Sending}}}
	if err := s.Create(r); err != nil {
		t.Fatal(err)
	}
	m := NewManager(config.Config{}, nil, nil, s, nil, nil, RealClock{})
	if err := m.RestoreGates(); err != nil {
		t.Fatal(err)
	}
	if err := m.ReconcileHeld(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := loaded(t, s, spec.Run)
	if !got.SlotReleased || got.Phase != Held || got.Outcome != "unknown" || !m.resumeStopped(t.Context(), got) {
		t.Fatalf("stopped group %d cannot be recovered: %+v", receipt.PGID, got)
	}
}
