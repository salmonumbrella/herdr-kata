package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/katabridge"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/lockfile"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

// NativeDelivery contains ordinary write bookkeeping only. The pending DTO is
// immutable through reply loss; only its unsent successor may be coalesced.
type NativeDelivery = katabridge.NativeDelivery

func queueNativeObservation(ctx context.Context, path string, identity NativeDelivery, dto katacli.RunObservation) error {
	raw, err := json.Marshal(dto)
	if err != nil {
		return err
	}
	if len(raw) > 98304 || len(dto.Summary) > 65536 {
		return errors.New("local observation DTO exceeds byte bound")
	}
	lock, err := acquireExecutionQueueLock(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer lock.Release()
	d := identity
	err = statefs.ReadJSON(path, 262144, &d)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if d.Version != 1 || d.TargetKey != identity.TargetKey || d.ProjectUID != identity.ProjectUID || d.Actor != identity.Actor || d.Teammate != identity.Teammate || d.RunUID != identity.RunUID {
		return errors.New("local observation identity changed")
	}
	if d.Pending != nil && !sameObservationIdentity(*d.Pending, dto) {
		return errors.New("local observation references changed")
	}
	if d.Pending == nil {
		dto.ExpectedRevision = d.Revision
		d.Pending = &dto
	} else {
		dto.ExpectedRevision = 0
		d.Unsent = &dto
	}
	return statefs.WriteJSON(path, d, 262144)
}

// Ordinary observations are asynchronous; no result permits or prevents a process.
func deliverNativeObservations(ctx context.Context, client katacli.Client, path string) {
	deliverNativeBatch(ctx, client, path, 100)
}

func deliverNativeBatch(ctx context.Context, client katacli.Client, path string, budget int) int {
	client.Timeout = 3 * time.Second
	sent := 0
	for sent < budget && ctx.Err() == nil {
		lock, err := lockfile.Acquire(path + ".lock")
		if err != nil {
			return sent
		}
		var d NativeDelivery
		if statefs.ReadJSON(path, 262144, &d) != nil {
			lock.Release()
			return sent
		}
		if d.Version != 1 || d.TargetKey != nativeTargetKey(client.Target) || d.Actor != client.Target.Actor || d.Teammate != client.Target.Teammate {
			d.Error = "saved observation routing changed; select its recorded target and actor"
			_ = statefs.WriteJSON(path, d, 262144)
			lock.Release()
			return sent
		}
		if d.Pending == nil {
			lock.Release()
			return sent
		}
		if d.Attempts < 1000000 {
			d.Attempts++
		}
		if statefs.WriteJSON(path, d, 262144) != nil {
			lock.Release()
			return sent
		}
		// Freeze exactly this DTO, then release local storage before any remote I/O.
		pending, _ := json.Marshal(d.Pending)
		dto := *d.Pending
		lock.Release()
		sent++
		result, sendErr := client.ObserveRun(ctx, d.RunUID, dto)
		lock, err = lockfile.Acquire(path + ".lock")
		if err != nil {
			return sent
		}
		var current NativeDelivery
		if statefs.ReadJSON(path, 262144, &current) != nil {
			lock.Release()
			return sent
		}
		currentPending, _ := json.Marshal(current.Pending)
		if current.Version != d.Version || current.TargetKey != d.TargetKey || current.ProjectUID != d.ProjectUID || current.Actor != d.Actor || current.Teammate != d.Teammate || current.RunUID != d.RunUID || current.Revision != d.Revision || string(currentPending) != string(pending) {
			lock.Release()
			return sent
		}
		if sendErr == nil {
			sendErr = statefs.WriteJSON(filepath.Join(filepath.Dir(path), "native-observed.json"), nativeObserved{Version: 1, RunUID: d.RunUID, DTO: dto}, 262144)
		}
		if sendErr != nil {
			current.Error = "ordinary run observation delivery failed"
			current.NextRetry = time.Now().Add(katabridge.RetryDelay(current.Attempts)).UTC().Format(time.RFC3339Nano)
			_ = statefs.WriteJSON(path, current, 262144)
			lock.Release()
			return sent
		}
		current.Attempts = 0
		current.Revision = result.Run.Revision
		current.Pending = current.Unsent
		current.Unsent = nil
		current.Error = ""
		current.NextRetry = ""
		if current.Pending != nil {
			current.Pending.ExpectedRevision = current.Revision
		}
		err = statefs.WriteJSON(path, current, 262144)
		lock.Release()
		if err != nil {
			return sent
		}
	}
	return sent
}

func nativeDeliveryPath(dir string) string { return filepath.Join(dir, "native-delivery.json") }

// Only bounded local storage contention is waited out. Delivery never retains
// this lock while waiting for a remote command or acknowledgment.
func acquireExecutionQueueLock(ctx context.Context, path string) (*lockfile.Lock, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lock, err := lockfile.Acquire(path)
		if err == nil {
			return lock, nil
		}
		var held *lockfile.ErrHeld
		if !errors.As(err, &held) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Replay checks the saved immutable route before reusing the current credentials.
// At most 100 remote writes are attempted, independently of execution slots.
func flushNativeObservations(ctx context.Context, s *store.Store) error {
	if s.Native == nil || s.Native.Client == nil {
		return nil
	}
	recoveryErr := recoverMissingObservations(ctx, s)
	paths, err := katabridge.ObservationPaths(s.Native.StateDir)
	if err != nil {
		return err
	}
	budget := 100
	var problems []error
	if recoveryErr != nil {
		problems = append(problems, recoveryErr)
	}
	// First attempts precede retries; older retry times precede newer failures.
	// An unavailable run cannot consume every subsequent batch indefinitely.
	retryAt := map[string]string{}
	for _, path := range paths {
		var d NativeDelivery
		if statefs.ReadJSON(path, 262144, &d) == nil {
			retryAt[path] = d.NextRetry
		}
	}
	sort.SliceStable(paths, func(i, j int) bool { return retryAt[paths[i]] < retryAt[paths[j]] })
	checkedProject := false
	for _, path := range paths {
		if budget == 0 || ctx.Err() != nil {
			break
		}
		var d NativeDelivery
		if err := statefs.ReadJSON(path, 262144, &d); err != nil {
			problems = append(problems, err)
			continue
		}
		if d.Pending == nil {
			continue
		}
		if d.NextRetry != "" {
			at, err := time.Parse(time.RFC3339Nano, d.NextRetry)
			if err != nil {
				problems = append(problems, err)
				continue
			}
			if time.Now().Before(at) {
				continue
			}
		}
		c, err := runner.LoadNativeContext(filepath.Dir(path))
		if err == nil {
			err = c.Check(s.Native)
		}
		if err == nil && (c.RunUID != d.RunUID || c.ProjectUID != d.ProjectUID) {
			err = errors.New("local observation context identity mismatch")
		}
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if !checkedProject {
			if _, err := s.Native.Client.ProjectID(ctx, s.Native.Binding.ProjectUID); err != nil {
				return errors.Join(append(problems, err)...)
			}
			checkedProject = true
		}
		budget -= deliverNativeBatch(ctx, *s.Native.Client, path, budget)
	}
	return errors.Join(problems...)
}

// nativeObserved is a local copy of the last acknowledged evidence, allowing a
// failed tail write to be reconstructed from settled local history on restart.
// It changes neither the frozen v1 queue format nor execution policy.
type nativeObserved struct {
	Version int                    `json:"version"`
	RunUID  string                 `json:"run_uid"`
	DTO     katacli.RunObservation `json:"observation"`
}

func sameObservationEvidence(a, b katacli.RunObservation) bool {
	a.ExpectedRevision = 0
	b.ExpectedRevision = 0
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func sameObservationIdentity(a, b katacli.RunObservation) bool {
	return a.JobUID == b.JobUID && a.DefinitionEventUID == b.DefinitionEventUID && a.FlowUID == b.FlowUID && a.FlowDefinitionEventUID == b.FlowDefinitionEventUID && a.OccurrenceKey == b.OccurrenceKey && a.IssueUID == b.IssueUID && a.Teammate == b.Teammate && a.ExecutorLabel == b.ExecutorLabel
}

// Existing local SQL history has whole-second timestamps. Comparing that
// projection must not rewrite acknowledged or queued nanosecond evidence.
func sameLocalRunEvidence(a, b katacli.RunObservation) bool {
	normalize := func(value string) string {
		at, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return value
		}
		return at.UTC().Truncate(time.Second).Format(time.RFC3339)
	}
	a.StartedAt = normalize(a.StartedAt)
	b.StartedAt = normalize(b.StartedAt)
	a.EndedAt = normalize(a.EndedAt)
	b.EndedAt = normalize(b.EndedAt)
	return sameObservationEvidence(a, b)
}
