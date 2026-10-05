package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/salmonumbrella/herdr-kata/internal/katabridge"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

type nativeSourceKey struct{}
type nativeDateDefinition struct {
	Trigger struct {
		Kind        string `json:"kind"`
		IssueUID    string `json:"issue_uid"`
		LeadSeconds int64  `json:"lead_seconds"`
	} `json:"trigger"`
	Action struct {
		Kind      string `json:"kind"`
		Recipient string `json:"recipient"`
		Message   string `json:"message"`
	} `json:"action"`
	Issue *struct {
		Kind string `json:"kind"`
		UID  string `json:"uid"`
	} `json:"issue,omitempty"`
}

// Project into an ephemeral job copy. The shared definition remains a reference
// to the issue field; no copied schedule or first-observed state is written.
func nativeDateJob(ctx context.Context, s *store.Store, j store.Job) (store.Job, string, error) {
	var def nativeDateDefinition
	if err := katacli.Decode(j.NativeDefinition, &def); err != nil {
		return j, "", err
	}
	if def.Trigger.Kind != string(j.Schedule) {
		return j, "", errors.New("native date trigger differs from its local projection")
	}
	dates, err := s.Native.Client.PlanningDates(ctx, s.Native.Binding.ProjectUID, def.Trigger.IssueUID)
	if err != nil {
		return j, "", fmt.Errorf("ordinary planning-date read: %w", err)
	}
	date := dates.ScheduledOn
	if j.Schedule == "issue-deadline" {
		date = dates.DeadlineOn
	}
	j.RunAt = nil
	if date == nil {
		return j, "", nil
	}
	at := date.Instant.UTC()
	j.RunAt = &at
	if _, err := sched.DateFire(j); err != nil {
		return j, "", err
	}
	identity := struct {
		IssueUID string `json:"issue_uid"`
		katacli.PlanningDate
		OffsetSeconds int64 `json:"offset_seconds"`
	}{def.Trigger.IssueUID, *date, -def.Trigger.LeadSeconds}
	raw, err := json.Marshal(identity)
	if err != nil {
		return j, "", err
	}
	key := "date:" + string(raw)
	if utf8.RuneCountInString(key) > 1024 {
		return j, "", errors.New("planning-date occurrence exceeds the ordinary evidence bound")
	}
	return j, key, nil
}

func (d *daemon) nativeDateDue(ctx context.Context, j store.Job, now time.Time) (store.Job, []time.Time, string, error) {
	projected, key, err := nativeDateJob(ctx, d.store, j)
	if err != nil {
		return j, nil, "", err
	}
	if key == "" {
		if err := cancelNativeAttention(d.store, "date-job:"+j.ID); err != nil {
			return j, nil, "", err
		}
		return projected, nil, "", nil
	}
	var notification nativeDateDefinition
	if err := katacli.Decode(j.NativeDefinition, &notification); err != nil {
		return j, nil, "", err
	}
	if notification.Action.Kind == "notify" {
		fire, err := sched.DateFire(projected)
		if err != nil {
			return j, nil, "", err
		}
		if now.Before(fire) {
			if err := cancelNativeAttention(d.store, "date-job:"+j.ID); err != nil {
				return j, nil, "", err
			}
			return projected, nil, key, nil
		}
		err = deliverNativeDateNotification(ctx, d.store, projected, key, notification)
		return projected, nil, key, err
	}
	// Suppression belongs only to this installation's most recent source run.
	// Explicit independent invocations still allocate different run UIDs.
	last, err := d.store.LastScheduledRun(ctx, j.ID)
	if err == nil {
		proof, e := runner.LoadNativeContext(last.RunDir)
		if e != nil {
			return j, nil, "", fmt.Errorf("read local date-run context: %w", e)
		}
		if e := proof.Check(d.store.Native); e != nil {
			return j, nil, "", e
		}
		if proof.RunUID != last.ID || proof.Job == nil || proof.Job.UID != j.ID {
			return j, nil, "", errors.New("local date-run context does not match its saved run/job identity")
		}
		if proof.Occurrence == key {
			return projected, nil, key, nil
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return j, nil, "", err
	}
	fires, err := sched.Due(projected, time.Time{}, now)
	if err != nil || len(fires) == 0 {
		return projected, fires, key, err
	}
	var def nativeDateDefinition
	if err := katacli.Decode(j.NativeDefinition, &def); err != nil {
		return j, nil, "", err
	}

	if err := nativeIssueReady(ctx, d.store, def.Trigger.IssueUID); err != nil {
		return j, nil, "", err
	}
	return projected, fires, key, nil
}

// Transient diagnostics describe a changed problem once, instead of emitting
// an unknown-schedule error on each tick. No scheduler checkpoint is persisted.
func (d *daemon) nativeDateDiagnostic(j store.Job, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dateProblems == nil {
		d.dateProblems = map[string]string{}
	}
	if err == nil {
		delete(d.dateProblems, j.ID)
		return
	}
	message := fmt.Sprintf("native job %s (%s): %v", j.ID, strings.TrimSpace(j.Name), err)
	if d.dateProblems[j.ID] != message {
		fmt.Fprintln(os.Stderr, "herdr-kata:", message)
		d.dateProblems[j.ID] = message
	}
}

func cancelNativeAttention(s *store.Store, key string) error {
	b, err := localBridge(s)
	if err != nil {
		return err
	}
	return (katabridge.AttentionOutbox{Dir: b.Dir, Scope: b.Scope, Teammate: s.Native.Client.Target.Teammate}).CancelPending(key)
}
func deliverNativeDateNotification(ctx context.Context, s *store.Store, j store.Job, source string, def nativeDateDefinition) error {
	target := def.Trigger.IssueUID
	if def.Issue != nil {
		if def.Issue.Kind != "existing" {
			return errors.New("notify requires an existing issue target")
		}
		target = def.Issue.UID
	}
	// The existing native sweeper remains the sole default date producer.
	if target == def.Trigger.IssueUID && def.Trigger.LeadSeconds == 0 && def.Action.Recipient == "current-owner-or-author" {
		return cancelNativeAttention(s, "date-job:"+j.ID)
	}
	recipient := def.Action.Recipient
	b, err := localBridge(s)
	if err != nil {
		return err
	}
	out := katabridge.AttentionOutbox{Dir: b.Dir, Scope: b.Scope, Teammate: s.Native.Client.Target.Teammate}
	key := "date-job:" + j.ID
	ambiguous := func(err error) error {
		return errors.Join(err, out.RetainError(key, "ordinary notification source/target could not be confirmed"))
	}
	projectID, err := s.Native.Client.ProjectID(ctx, s.Native.Binding.ProjectUID)
	if err != nil {
		return ambiguous(err)
	}
	issues := []string{def.Trigger.IssueUID}
	if target != def.Trigger.IssueUID {
		issues = append(issues, target)
	}
	for _, uid := range issues {
		var out struct {
			Issue struct {
				UID       string     `json:"uid"`
				ProjectID int64      `json:"project_id"`
				Status    string     `json:"status"`
				Owner     *string    `json:"owner"`
				Author    string     `json:"author"`
				DeletedAt *time.Time `json:"deleted_at"`
			} `json:"issue"`
		}
		if err := s.Native.Client.Show(ctx, uid, &out); err != nil {
			return ambiguous(err)
		}
		if out.Issue.UID != uid || out.Issue.ProjectID != projectID {
			return ambiguous(errors.New("ordinary notification source/target identity mismatch"))
		}
		if out.Issue.Status == "closed" || out.Issue.DeletedAt != nil {
			return errors.Join(errors.New("ordinary notification source/target is closed or deleted"), cancelNativeAttention(s, key))
		}
		if out.Issue.Status != "open" {
			return ambiguous(errors.New("ordinary notification source/target status is unknown"))
		}
		if uid == target && recipient == "current-owner-or-author" {
			recipient = out.Issue.Author
			if out.Issue.Owner != nil && *out.Issue.Owner != "" {
				recipient = *out.Issue.Owner
			}
		}
	}
	if recipient == "" {
		return errors.New("ordinary notification requires a recipient")
	}
	message := def.Action.Message
	if message == "" {
		if j.Schedule == "issue-deadline" {
			var evidence struct {
				Value string `json:"value"`
			}
			json.Unmarshal([]byte(strings.TrimPrefix(source, "date:")), &evidence)
			message = "Deadline reached: " + evidence.Value
		} else {
			var evidence struct {
				Value string `json:"value"`
			}
			json.Unmarshal([]byte(strings.TrimPrefix(source, "date:")), &evidence)
			message = "Scheduled date reached: " + evidence.Value
		}
	}
	item := katabridge.Attention{Key: "date-job:" + j.ID, Source: j.NativeEventUID + ":" + source, Issue: target, Recipient: recipient, Message: message}
	if err := out.Queue(item); err != nil {
		return err
	}
	_, current, err := nativeDateJob(ctx, s, j)
	if err != nil {
		return err
	}
	if current != source {
		return errors.Join(errors.New("ordinary notification source changed before delivery"), out.CancelPending(item.Key))
	}
	client := *s.Native.Client
	client.Timeout = 3 * time.Second
	if err := out.FlushOne(ctx, item.Key, katabridge.CLIAttention{Client: client}); err != nil {
		return errors.New("ordinary notification delivery failed; retrying locally")
	}
	return nil
}
