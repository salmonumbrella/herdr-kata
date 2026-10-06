package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/katabridge"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

// Local repair progress only; neither an observation nor execution authority.
// Frozen v1 context, delivery, and acknowledged evidence remain unchanged.
type nativeRecoveryMarker struct {
	Version       int              `json:"version"`
	Scope         katabridge.Scope `json:"scope"`
	Teammate      string           `json:"teammate,omitempty"`
	ExecutorLabel string           `json:"executor_label"`
	RunUID        string           `json:"run_uid"`
	Input         string           `json:"input"`
	Error         string           `json:"error,omitempty"`
	NextRetry     time.Time        `json:"next_retry,omitempty"`
}
type nativeRecoveryIdentityFailure struct{ error }

func (e nativeRecoveryIdentityFailure) Unwrap() error { return e.error }

type recoveryFileStamp struct {
	Exists   bool  `json:"exists"`
	Size     int64 `json:"size"`
	Modified int64 `json:"modified"`
}

func recoveryInput(s *store.Store, rec store.Run) (string, error) {
	var stamps []recoveryFileStamp
	for _, name := range []string{"native-context.json", "native-delivery.json", "native-observed.json"} {
		info, err := os.Stat(filepath.Join(rec.RunDir, name))
		if errors.Is(err, os.ErrNotExist) {
			stamps = append(stamps, recoveryFileStamp{})
			continue
		}
		if err != nil {
			return "", err
		}
		stamps = append(stamps, recoveryFileStamp{true, info.Size(), info.ModTime().UnixNano()})
	}
	raw, err := json.Marshal(struct {
		Run      store.Run
		Stamps   []recoveryFileStamp
		Target   string
		Project  string
		Actor    string
		Teammate string
		Executor string
	}{rec, stamps, nativeTargetKey(s.Native.Client.Target), s.Native.Binding.ProjectUID, s.Native.Client.Target.Actor, s.Native.Client.Target.Teammate, s.Native.Binding.ExecutorLabel})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Catalogue enumeration reads directory names, but context reads, per-run SQL,
// evidence reads, locks, and repair writes are limited to 100 rotating entries.
func recoverMissingObservations(ctx context.Context, s *store.Store) error {
	entries, err := os.ReadDir(filepath.Join(s.Native.StateDir, "runs"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() {
			paths = append(paths, filepath.Join(s.Native.StateDir, "runs", entry.Name(), "native-context.json"))
		}
	}
	b, err := localBridge(s)
	if err != nil {
		return err
	}
	indices, err := (katabridge.WorkBatch{Dir: b.Dir, Scope: b.Scope, Teammate: s.Native.Client.Target.Teammate, Name: "observation-history"}).Select(paths, 100)
	if err != nil {
		return err
	}
	var problems []error
	for _, index := range indices {
		if ctx.Err() != nil {
			return errors.Join(append(problems, ctx.Err())...)
		}
		if _, err := os.Stat(paths[index]); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			problems = append(problems, err)
			continue
		}
		dir := filepath.Dir(paths[index])
		uid := filepath.Base(dir)
		rec, err := s.Run(ctx, uid)
		if errors.Is(err, store.ErrNotFound) {
			// Context-only ordinary outboxes have no local execution history
			// to reconstruct. Their frozen pending evidence still replays.
			c, readErr := runner.LoadNativeContext(dir)
			if readErr != nil {
				err = errors.Join(err, fmt.Errorf("load saved context %s: %w", uid, readErr))
			}
			if readErr == nil && c.Job == nil && c.Workflow == nil {
				if checkErr := c.Check(s.Native); checkErr != nil {
					problems = append(problems, checkErr)
				} else if c.RunUID != uid {
					problems = append(problems, errors.New("local observation context identity mismatch"))
				}
				continue
			}
		}
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if rec.RunDir != dir {
			problems = append(problems, errors.New("local observation run directory identity mismatch"))
			continue
		}
		input, err := recoveryInput(s, *rec)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		marker := nativeRecoveryMarker{Version: 1, Scope: b.Scope, Teammate: s.Native.Client.Target.Teammate, ExecutorLabel: s.Native.Binding.ExecutorLabel, RunUID: uid}
		path := filepath.Join(dir, "native-recovered.json")
		if err := statefs.ReadJSON(path, 98304, &marker); err != nil && !errors.Is(err, os.ErrNotExist) {
			problems = append(problems, err)
			continue
		}
		if marker.Version != 1 || marker.Scope != b.Scope || marker.Teammate != s.Native.Client.Target.Teammate || marker.RunUID != uid {
			problems = append(problems, errors.New("saved observation recovery scope changed"))
			continue
		}
		// A configured display label is cache metadata. A rename must
		// revalidate the frozen context, never reinterpret its attribution.
		if marker.ExecutorLabel == s.Native.Binding.ExecutorLabel && marker.Input == input && (marker.Error == "" || time.Now().Before(marker.NextRetry)) {
			continue
		}
		previousInput, previousError := marker.Input, marker.Error
		err = recoverOneObservation(ctx, s, *rec)
		var identityFailure nativeRecoveryIdentityFailure
		if errors.As(err, &identityFailure) {
			problems = append(problems, err)
			continue
		}
		// Successful buffer writes change the fingerprint; save their current state.
		current, fingerprintErr := recoveryInput(s, *rec)
		if fingerprintErr != nil {
			problems = append(problems, errors.Join(err, fingerprintErr))
			continue
		}
		marker.ExecutorLabel = s.Native.Binding.ExecutorLabel
		marker.Input = current
		marker.Error = ""
		marker.NextRetry = time.Time{}
		if err != nil {
			marker.Error = err.Error()
			marker.NextRetry = time.Now().Add(time.Minute)
			if previousInput != input || previousError != marker.Error {
				problems = append(problems, err)
			}
		}
		if writeErr := statefs.WriteJSON(path, marker, 98304); writeErr != nil {
			problems = append(problems, errors.Join(err, writeErr))
		}
	}
	return errors.Join(problems...)
}

func recoverOneObservation(ctx context.Context, s *store.Store, rec store.Run) error {
	c, err := runner.LoadNativeContext(rec.RunDir)
	if err != nil {
		var syntax *json.SyntaxError
		var decode *json.UnmarshalTypeError
		var disk *os.PathError
		if !errors.As(err, &syntax) && !errors.As(err, &decode) && !errors.As(err, &disk) {
			return nativeRecoveryIdentityFailure{err}
		}
		return err
	}
	if err = c.Check(s.Native); err != nil {
		return nativeRecoveryIdentityFailure{err}
	}
	if c.RunUID != rec.ID {
		return nativeRecoveryIdentityFailure{errors.New("local observation context identity mismatch")}
	}
	if c.Job == nil && c.Workflow == nil {
		return nil
	}
	// Preparing the ordinary issue has not started a run. Publishing this local
	// park would freeze an absent issue UID and a provisional creation timestamp.
	if nativeIssuePreparationPending(c, rec) {
		return nil
	}
	historyErr := queueHistoricalResult(s, c, rec)
	path := nativeDeliveryPath(rec.RunDir)
	var d NativeDelivery
	existing := statefs.ReadJSON(path, 262144, &d)
	if existing != nil && !errors.Is(existing, os.ErrNotExist) {
		return errors.Join(historyErr, existing)
	}
	status := "unknown"
	switch rec.Outcome {
	case "running":
		status = "running"
	case "done":
		status = "succeeded"
	case "failed":
		status = "failed"
	}
	var end time.Time
	if rec.EndedAt != nil {
		end = *rec.EndedAt
	}
	dto := nativeObservation(c, status, rec.StartedAt, end)
	if existing == nil {
		if d.Unsent != nil && sameLocalRunEvidence(*d.Unsent, dto) || d.Pending != nil && sameLocalRunEvidence(*d.Pending, dto) {
			return historyErr
		}
		if d.Pending == nil {
			var ack nativeObserved
			if statefs.ReadJSON(filepath.Join(rec.RunDir, "native-observed.json"), 262144, &ack) == nil && ack.Version == 1 && ack.RunUID == c.RunUID && sameLocalRunEvidence(ack.DTO, dto) {
				return historyErr
			}
		}
	}
	id := NativeDelivery{Version: 1, TargetKey: nativeTargetKey(c.Target), ProjectUID: c.ProjectUID, Actor: c.Target.Actor, Teammate: c.Target.Teammate, RunUID: c.RunUID}
	return errors.Join(historyErr, queueNativeObservation(ctx, path, id, dto))
}
