package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/katabridge"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func executeNativeNotification(ctx context.Context, s *store.Store, j store.Job, trigger string, def nativeDateDefinition) (*runner.Run, error) {
	if strings.HasPrefix(string(j.Schedule), "issue-") {
		projected, source, err := nativeDateJob(ctx, s, j)
		if err != nil {
			return nil, err
		}
		if source == "" {
			return nil, errors.New("notification date source is absent")
		}
		fire, err := sched.DateFire(projected)
		if err != nil {
			return nil, err
		}
		if time.Now().Before(fire) {
			return nil, errors.New("notification date source is not due")
		}
		return nil, deliverNativeDateNotification(ctx, s, projected, source, def)
	}
	if def.Issue == nil || def.Issue.Kind != "existing" {
		return nil, errors.New("ordinary notify requires an existing issue target")
	}
	target := def.Issue.UID
	recipient, err := nativeNotificationRecipient(ctx, s, target, def.Action.Recipient)
	if err != nil {
		return nil, err
	}
	uid, err := katacli.NewUID()
	if err != nil {
		return nil, err
	}
	t := s.Native.Client.Target
	t.Token = ""
	c := runner.NativeExecutionContext{Version: 1, RunUID: uid, Target: t, ProjectUID: s.Native.Binding.ProjectUID, ExecutorLabel: s.Native.Binding.ExecutorLabel, Runtime: j, IssueUID: target, Job: &katacli.Definition{UID: j.ID, DefinitionEventUID: j.NativeEventUID, Name: j.Name, Definition: append(json.RawMessage(nil), j.NativeDefinition...)}}
	if at, ok := ctx.Value(nativeFireTime{}).(time.Time); ok {
		c.Occurrence = at.UTC().Format(time.RFC3339Nano)
	}
	if err := c.Save(runDirFor(uid)); err != nil {
		return nil, err
	}
	start := time.Now()
	rec := store.Run{ID: uid, JobID: j.ID, Trigger: trigger, RunDir: runDirFor(uid), Ref: target, Outcome: "parked", ParkReason: "blocked", StartedAt: start, EndedAt: &start, Note: "Ordinary notification waiting for its exact recipient slot"}
	if err := s.PutRun(ctx, rec); err != nil {
		return nil, err
	}
	b, err := localBridge(s)
	if err != nil {
		return nil, err
	}
	out := katabridge.AttentionOutbox{Dir: b.Dir, Scope: b.Scope, Teammate: t.Teammate}
	item := katabridge.Attention{Key: "notify-job:" + j.ID, Source: uid, Issue: target, Recipient: recipient, Message: def.Action.Message, RecordRun: true}
	if err := out.Queue(item); err != nil {
		return nil, err
	}
	sendErr := out.FlushOne(ctx, item.Key, katabridge.CLIAttention{Client: *s.Native.Client})
	pending, err := out.IsPending(item.Key)
	if err != nil {
		return nil, err
	}
	if !pending {
		if err := settleNotificationAttention(ctx, s, out, uid); err != nil {
			return nil, err
		}
		stored, err := s.Run(ctx, uid)
		if err != nil {
			return nil, err
		}
		rec = *stored
	} else if err := queueNotificationEvidence(ctx, s, c, rec); err != nil {
		fmt.Fprintln(os.Stderr, "herdr-kata: notification evidence buffer:", err)
	}
	run := &runner.Run{RunID: uid, JobID: j.ID, RunDir: rec.RunDir, Outcome: runner.Outcome(rec.Outcome), ParkReason: runner.ParkReason(rec.ParkReason), StartedAt: rec.StartedAt, EndedAt: *rec.EndedAt, Result: &runner.Result{Note: rec.Note}}
	return run, sendErr
}

var errNotificationTargetClosed = errors.New("ordinary notification target is no longer open")

func nativeNotificationRecipient(ctx context.Context, s *store.Store, uid, recipient string) (string, error) {
	projectID, err := s.Native.Client.ProjectID(ctx, s.Native.Binding.ProjectUID)
	if err != nil {
		return "", err
	}
	var out struct {
		Issue struct {
			UID       string  `json:"uid"`
			ProjectID int64   `json:"project_id"`
			Status    string  `json:"status"`
			Author    string  `json:"author"`
			Owner     *string `json:"owner"`
			DeletedAt *string `json:"deleted_at"`
		} `json:"issue"`
	}
	if err := s.Native.Client.Show(ctx, uid, &out); err != nil {
		return "", err
	}
	if out.Issue.UID != uid || out.Issue.ProjectID != projectID {
		return "", errors.New("ordinary notification target is not the configured open issue")
	}
	if out.Issue.Status == "closed" || out.Issue.DeletedAt != nil {
		return "", errNotificationTargetClosed
	}
	if out.Issue.Status != "open" {
		return "", errors.New("ordinary notification target has an unknown status")
	}
	if recipient == "current-owner-or-author" {
		recipient = out.Issue.Author
		if out.Issue.Owner != nil && *out.Issue.Owner != "" {
			recipient = *out.Issue.Owner
		}
	}
	if recipient == "" {
		return "", errors.New("exact notification recipient required")
	}
	return recipient, nil
}
func queueNotificationEvidence(ctx context.Context, s *store.Store, c runner.NativeExecutionContext, rec store.Run) error {
	status := "unknown"
	if rec.Outcome == "done" {
		status = "succeeded"
	}
	id := NativeDelivery{Version: 1, TargetKey: nativeTargetKey(c.Target), ProjectUID: c.ProjectUID, Actor: c.Target.Actor, Teammate: c.Target.Teammate, RunUID: c.RunUID}
	dto := nativeObservation(c, status, rec.StartedAt, *rec.EndedAt)
	path := nativeDeliveryPath(rec.RunDir)
	var saved NativeDelivery
	queued := statefs.ReadJSON(path, 262144, &saved) == nil && (saved.Pending != nil && sameLocalRunEvidence(*saved.Pending, dto) || saved.Unsent != nil && sameLocalRunEvidence(*saved.Unsent, dto))
	var ack nativeObserved
	observed := statefs.ReadJSON(filepath.Join(rec.RunDir, "native-observed.json"), 262144, &ack) == nil && ack.Version == 1 && ack.RunUID == c.RunUID && sameLocalRunEvidence(ack.DTO, dto)
	var observationErr error
	if !queued && !observed {
		observationErr = queueNativeObservation(ctx, path, id, dto)
	}
	return errors.Join(observationErr, queueHistoricalResult(s, c, rec))
}
func flushOrdinaryAttention(ctx context.Context, s *store.Store) error {
	b, err := localBridge(s)
	if errors.Is(err, store.ErrNativeUnconfigured) {
		return nil
	}
	if err != nil {
		return err
	}
	out := katabridge.AttentionOutbox{Dir: b.Dir, Scope: b.Scope, Teammate: s.Native.Client.Target.Teammate}
	// Retired ordinary attention may still need local history/evidence writes.
	// Recover it before any new send, without consulting current issue state or
	// resubmitting a request that its handler may already have cleared.
	settlementErr := settleNotificationAttention(ctx, s, out, "")
	items, err := out.PendingItems()
	if err != nil {
		return errors.Join(settlementErr, err)
	}
	var problems []error
	if settlementErr != nil {
		problems = append(problems, settlementErr)
	}
	// Date jobs have their own sweep. Rotate supported pending work durably so
	// an unavailable context or occupied recipient cannot monopolize each batch.
	keys := make([]string, 0, len(items))
	supported := make([]katabridge.Attention, 0, len(items))
	for _, item := range items {
		if strings.HasPrefix(item.Key, "run-job:") || strings.HasPrefix(item.Key, "notify-job:") {
			keys = append(keys, item.Key)
			supported = append(supported, item)
		}
	}
	selected, err := (katabridge.WorkBatch{Dir: out.Dir, Scope: out.Scope, Teammate: out.Teammate, Name: "pending-run-attention"}).Select(keys, 100)
	if err != nil {
		return errors.Join(settlementErr, err)
	}
	for _, index := range selected {
		item := supported[index]
		if ctx.Err() != nil {
			break
		}
		if strings.HasPrefix(item.Key, "run-job:") {
			if err := flushRunAttention(ctx, s, out, item); err != nil {
				problems = append(problems, err)
			}
			continue
		}
		if !strings.HasPrefix(item.Key, "notify-job:") {
			continue
		}
		c, err := runner.LoadNativeContext(runDirFor(item.Source))
		if err == nil {
			err = c.Check(s.Native)
		}
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if c.Job == nil || item.Key != "notify-job:"+c.Job.UID || c.IssueUID != item.Issue {
			problems = append(problems, errors.New("notification context identity mismatch"))
			continue
		}
		current, err := s.Native.Client.Definition(ctx, "job", c.Job.UID)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if current.DefinitionEventUID != c.Job.DefinitionEventUID {
			if err := out.CancelPending(item.Key); err != nil {
				problems = append(problems, err)
			}
			continue
		}
		var def nativeDateDefinition
		if err := katacli.Decode(c.Job.Definition, &def); err != nil {
			problems = append(problems, err)
			continue
		}
		recipient, err := nativeNotificationRecipient(ctx, s, item.Issue, def.Action.Recipient)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if recipient != item.Recipient {
			_ = out.CancelPending(item.Key)
			continue
		}
		client := *s.Native.Client
		client.Timeout = 3 * time.Second
		if err := out.FlushOne(ctx, item.Key, katabridge.CLIAttention{Client: client}); err != nil {
			problems = append(problems, err)
			continue
		}
		pending, err := out.IsPending(item.Key)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if !pending {
			if err := settleNotificationAttention(ctx, s, out, c.RunUID); err != nil {
				problems = append(problems, err)
			}
		}
	}
	return errors.Join(problems...)
}

func settleNotificationAttention(ctx context.Context, s *store.Store, out katabridge.AttentionOutbox, source string) error {
	unfinished, err := out.Unsettled()
	if err != nil {
		return err
	}
	if source == "" {
		keys := make([]string, len(unfinished))
		for i, completion := range unfinished {
			raw, err := json.Marshal(completion)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(raw)
			keys[i] = hex.EncodeToString(sum[:])
		}
		selected, err := (katabridge.WorkBatch{Dir: out.Dir, Scope: out.Scope, Teammate: out.Teammate, Name: "accepted-run-settlements"}).Select(keys, 100)
		if err != nil {
			return err
		}
		batch := make([]katabridge.AttentionSettlement, len(selected))
		for i, index := range selected {
			batch[i] = unfinished[index]
		}
		unfinished = batch
	}
	var problems []error
	attempts := 0
	for _, completion := range unfinished {
		item := completion.Item
		if source != "" && item.Source != source {
			continue
		}
		if attempts >= 100 || ctx.Err() != nil {
			break
		}
		attempts++
		c, err := runner.LoadNativeContext(runDirFor(item.Source))
		if err == nil {
			err = c.Check(s.Native)
		}
		if err != nil {
			problems = append(problems, err)
			continue
		}
		var def nativeDateDefinition
		if c.Job == nil || c.RunUID != item.Source || item.Key != "notify-job:"+c.Job.UID || c.IssueUID != item.Issue {
			problems = append(problems, errors.New("notification settlement context identity mismatch"))
			continue
		}
		if err := katacli.Decode(c.Job.Definition, &def); err != nil {
			problems = append(problems, err)
			continue
		}
		if def.Action.Kind != "notify" || def.Action.Message != item.Message || def.Action.Recipient != "current-owner-or-author" && def.Action.Recipient != item.Recipient {
			problems = append(problems, errors.New("notification settlement differs from its saved definition"))
			continue
		}
		rec, err := s.Run(ctx, c.RunUID)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if rec.JobID != c.Job.UID || rec.Ref != item.Issue || rec.RunDir != runDirFor(c.RunUID) || rec.Outcome != "done" && (rec.Outcome != "parked" || rec.ParkReason != "blocked") {
			problems = append(problems, errors.New("notification settlement run identity mismatch"))
			continue
		}
		if rec.Outcome != "done" {
			rec.Outcome = "done"
			rec.ParkReason = ""
			rec.Note = "Ordinary notification submitted; recipient handler owns clearing"
			rec.EndedAt = &completion.CompletedAt
			if err := s.PutRun(ctx, *rec); err != nil {
				problems = append(problems, err)
				continue
			}
		}
		if rec.EndedAt == nil {
			problems = append(problems, errors.New("notification settlement lacks its local end time"))
			continue
		}
		if err := queueNotificationEvidence(ctx, s, c, *rec); err != nil {
			problems = append(problems, err)
			continue
		}
		if err := out.FinishSettlement(completion); err != nil {
			problems = append(problems, err)
		}
	}
	return errors.Join(problems...)
}

func queueRunAttention(s *store.Store, c runner.NativeExecutionContext, rec store.Run) error {
	if c.IssueUID == "" || rec.Outcome != "parked" || rec.EndedAt == nil {
		return nil
	}
	b, err := localBridge(s)
	if err != nil {
		return err
	}
	recipient := c.Target.Actor
	if c.Target.Teammate != "" {
		recipient += "/" + c.Target.Teammate
	}
	message := fmt.Sprintf("Needs human: local run %s parked (%s). Inspect its saved result and handle this exact inbox request.", c.RunUID, rec.ParkReason)
	return (katabridge.AttentionOutbox{Dir: b.Dir, Scope: b.Scope, Teammate: c.Target.Teammate}).Queue(katabridge.Attention{Key: "run-job:" + rec.JobID, Source: c.RunUID + ":" + rec.EndedAt.UTC().Format(time.RFC3339Nano), Issue: c.IssueUID, Recipient: recipient, Message: message})
}
func flushRunAttention(ctx context.Context, s *store.Store, out katabridge.AttentionOutbox, item katabridge.Attention) error {
	uid, _, _ := strings.Cut(item.Source, ":")
	c, err := runner.LoadNativeContext(runDirFor(uid))
	if err != nil {
		return err
	}
	if err := c.Check(s.Native); err != nil {
		return err
	}
	rec, err := s.Run(ctx, uid)
	if err != nil {
		return err
	}
	last, err := s.LastRun(ctx, rec.JobID)
	if err != nil {
		return err
	}
	if rec.Outcome != "parked" || last.ID != uid {
		return out.CancelPending(item.Key)
	}
	if item.Key != "run-job:"+rec.JobID || c.IssueUID != item.Issue {
		return errors.New("parked attention context identity mismatch")
	}
	recipient, err := nativeNotificationRecipient(ctx, s, item.Issue, item.Recipient)
	if errors.Is(err, errNotificationTargetClosed) {
		return out.CancelPending(item.Key)
	}
	if err != nil {
		return err
	}
	if recipient != item.Recipient {
		return errors.New("parked attention recipient changed")
	}
	client := *s.Native.Client
	client.Timeout = 3 * time.Second
	return out.FlushOne(ctx, item.Key, katabridge.CLIAttention{Client: client})
}
