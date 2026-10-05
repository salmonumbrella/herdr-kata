package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"math"
	"time"
)

type nativeIssuePolicy struct {
	Kind, UID, Title, Body string
	Scheduled              *int64 `json:"scheduled_offset_seconds"`
	Deadline               *int64 `json:"deadline_offset_seconds"`
}

func nativeRunIssuePolicy(c runner.NativeExecutionContext) (nativeIssuePolicy, error) {
	var body struct {
		Issue nativeIssuePolicy `json:"issue"`
	}
	if c.Job == nil {
		return body.Issue, nil
	}
	err := katacli.Decode(c.Job.Definition, &body)
	if err == nil && body.Issue.Kind == "existing" {
		uid, uidErr := katacli.NormalizeUID(body.Issue.UID)
		if uidErr != nil || uid != body.Issue.UID {
			return body.Issue, errors.New("existing issue policy requires a canonical UID")
		}
	}
	return body.Issue, err
}
func prepareNativeRunIssue(ctx context.Context, s *store.Store, c *runner.NativeExecutionContext) error {
	policy, err := nativeRunIssuePolicy(*c)
	if err != nil {
		return err
	}
	if policy.Kind != "per-run" || c.IssuePreparedAt.IsZero() && c.IssueUID != "" {
		return nil
	}
	if c.IssueUID == "" {
		if c.IssuePreparedAt.IsZero() {
			return errors.New("saved per-run issue intent lacks its creation time; inspect this local run")
		}
		// The ordinary producer retains creation idempotency for seven days. Never
		// silently repeat an ambiguous creation outside that supported replay window.
		if !time.Now().Before(c.IssuePreparedAt.Add(7 * 24 * time.Hour)) {
			return errors.New("per-run issue creation replay window expired; inspect the retained local run before retrying")
		}
		metadata := map[string]string{}
		for key, offset := range map[string]*int64{"scheduled_on": policy.Scheduled, "deadline_on": policy.Deadline} {
			if offset == nil {
				continue
			}
			if *offset > math.MaxInt64/int64(time.Second) || *offset < -math.MaxInt64/int64(time.Second) {
				return fmt.Errorf("unsupported per-run %s offset", key)
			}
			metadata[key] = c.IssuePreparedAt.Add(time.Duration(*offset) * time.Second).UTC().Format(time.RFC3339Nano)
		}
		project, err := s.Native.Client.ProjectID(ctx, c.ProjectUID)
		if err != nil {
			return err
		}
		var out struct {
			Issue struct {
				UID       string `json:"uid"`
				ProjectID int64  `json:"project_id"`
			} `json:"issue"`
		}
		if err := s.Native.Client.CreateRunIssue(ctx, policy.Title, policy.Body, "herdr-run:"+c.RunUID, metadata, &out); err != nil {
			return err
		}
		uid, err := katacli.NormalizeUID(out.Issue.UID)
		if err != nil || uid != out.Issue.UID || out.Issue.ProjectID != project {
			return errors.New("created issue identity does not match the configured project")
		}
		if err := c.BindIssue(runDirFor(c.RunUID), uid); err != nil {
			return err
		}
		c.IssueUID = uid
	}
	c.Runtime.Ref = c.IssueUID
	return nativeIssueReady(ctx, s, c.IssueUID)
}
