package main

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/salmonumbrella/herdr-kata/internal/herdrcli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

// A settled local run records its tab independently of optional harness session
// capture. Its immutable context supplies routing provenance, including when
// KeepContext is false. Check before replacing the most recent run row, so a
// refused retarget cannot overwrite the evidence needed by the next invocation.
func nativePersistentProvenance(ctx context.Context, s *store.Store, c runner.NativeExecutionContext) (*store.Run, error) {
	previous, err := s.LastConversationRun(ctx, c.Runtime.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := nativeConversationContext(s, c, previous); err != nil {
		return nil, err
	}
	return previous, nil
}

// Selected attempts and session captures can be different runs. Each has its
// own immutable routing record; neither substitutes for the other's identity.
func nativeConversationContext(s *store.Store, c runner.NativeExecutionContext, record *store.Run) (runner.NativeExecutionContext, error) {
	var empty runner.NativeExecutionContext
	if record == nil || record.JobID != c.Runtime.ID || record.TabID == "" || record.AgentName == "" {
		return empty, errors.New("local conversation record does not match this job")
	}
	dir := record.RunDir
	if dir == "" {
		dir = runDirFor(record.ID)
	}
	old, err := runner.LoadNativeContext(dir)
	if err != nil {
		return empty, errors.New("prior live conversation has no readable local run provenance; inspect it before reuse")
	}
	if old.RunUID != record.ID || old.Job == nil || old.Job.UID != c.Runtime.ID || old.Runtime.ID != c.Runtime.ID {
		return empty, errors.New("prior live conversation provenance does not match this job")
	}
	if err := old.Check(s.Native); err != nil {
		return empty, err
	}
	if filepath.Clean(old.Runtime.CWD) != filepath.Clean(c.Runtime.CWD) {
		return empty, errors.New("persistent checkout changed; inspect local context before reuse")
	}
	return old, nil
}

// Direct named reuse requires the latest selected tab/name. Restoration instead
// follows the session's actual capturing run, even after an intervening blocked
// attempt. Its immutable context must independently prove compatible routing.
func checkNativeConversation(ctx context.Context, s *store.Store, c runner.NativeExecutionContext, previous *store.Run, ag *herdrcli.Agent, how string) error {
	if previous == nil || ag == nil {
		return errors.New("live conversation has no compatible local run provenance; inspect it before reuse")
	}
	if how == "kept" {
		if ag.TabID != previous.TabID || ag.Name != previous.AgentName {
			return errors.New("returned live conversation differs from the recorded local tab/name; inspect it before reuse")
		}
	} else if how != "adopted" && how != "resumed" {
		return errors.New("unknown local conversation selection")
	}
	session, err := s.JobSession(ctx, previous.JobID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	// Named reuse retains the existing handle policy. Optional session metadata
	// from that same selected run can detect a replacement; an older capture may
	// be stale after intentional KeepContext=false clears.
	if how == "kept" {
		if session != nil && session.RunID == previous.ID && ag.AgentSession != nil && (session.Kind != ag.AgentSession.Kind || session.Value != ag.AgentSession.Value) {
			return errors.New("returned live conversation differs from the recorded harness session")
		}
		return nil
	}
	// Cross-tab restoration must follow the actual capturing run, not the latest
	// selected attempt that might have parked before capturing any session.
	if session == nil || session.Value == "" || (ag.AgentSession == nil && how != "resumed") || (ag.AgentSession != nil && (session.Kind != ag.AgentSession.Kind || session.Value != ag.AgentSession.Value)) {
		return errors.New("returned conversation has no matching recorded local harness session")
	}
	captured, err := s.Run(ctx, session.RunID)
	if err != nil {
		return err
	}
	proof, err := nativeConversationContext(s, c, captured)
	if err != nil {
		return err
	}
	// Intentional resume may have unconfirmed optional metadata. Its actual
	// capturing record must still independently prove the requested session;
	// adoption never receives that exception.
	harness := ""
	if ag.AgentSession != nil {
		harness = ag.AgentSession.Agent
	}
	if harness == "" {
		harness = ag.Agent
	}
	canonical := func(h string) string {
		if h == "omp" {
			return "pi"
		}
		return h
	}
	if captured.ID != session.RunID || captured.ContextSession != session.Value || !proof.Runtime.Persistent || !proof.Runtime.KeepContext || canonical(proof.Runtime.Kind) != canonical(session.Harness) || canonical(harness) != canonical(session.Harness) {
		return errors.New("captured harness session does not match its local run provenance")
	}
	return nil
}
