package main

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"github.com/salmonumbrella/herdr-kata/internal/workflow"
)

func nativeWorkflowDefinition(ctx context.Context, s *store.Store, uid string) (workflow.Workflow, error) {
	if s.Native.Client == nil {
		return workflow.Workflow{}, store.ErrNativeUnconfigured
	}
	fd, err := s.Native.Client.Definition(ctx, "workflow", uid)
	if err != nil {
		return workflow.Workflow{}, err
	}
	return workflow.FromNative(fd)
}
func runNativeWorkflow(ctx context.Context, s *store.Store, j store.Job, rec store.Run, opts workflowOpts) (*runner.Run, error) {
	if s.Native.Client == nil {
		return nil, store.ErrNativeUnconfigured
	}
	if _, err := katacli.NormalizeUID(rec.ID); err != nil {
		return nil, errors.New("legacy or authority-era run is unsupported for automatic recovery; retain artifacts for inspection")
	}
	dir := rec.RunDir
	if dir == "" {
		dir = runDirFor(rec.ID)
	}
	c, err := runner.LoadNativeContext(dir)
	if err == nil {
		if c.Workflow == nil {
			return nil, errors.New("saved run is not a native workflow")
		}
		ref := c.Runtime.Ref
		// A supplied override must not masquerade as an older row's raw ref.
		// Existing v1 rows are compared before projecting their linked issue.
		if saved, e := s.Run(ctx, rec.ID); e == nil {
			if rec.Input != saved.Input || rec.Ref != saved.Ref {
				return nil, errors.New("saved run input/reference is immutable; start a new run to change it")
			}
		} else if !errors.Is(e, store.ErrNotFound) {
			return nil, e
		}
		if c.Job != nil && c.IssueUID != "" {
			// Earlier ordinary v1 binaries saved the raw Runtime.Ref in BOTH
			// context and row. Either matching old row or already projected row
			// can resume, without altering the immutable context's bytes.
			ref = c.IssueUID
		}
		compatibleRef := rec.Ref == ref || (c.Job != nil && c.IssueUID != "" && rec.Ref == c.Runtime.Ref)
		if rec.Input != c.Runtime.Input || !compatibleRef {
			return nil, errors.New("saved run input/reference is immutable; start a new run to change it")
		}
		rec.Input = c.Runtime.Input
		c.Runtime.Ref = ref
		rec.Ref = ref
		rec.Workflow = c.Workflow.UID
		return runNativeContext(ctx, s, c, rec, opts)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if _, err := s.Run(ctx, rec.ID); !errors.Is(err, store.ErrNotFound) {
		return nil, errors.New("saved run has no compatible local snapshot; retain authority-era artifacts for inspection")
	}
	mapped := false
	for _, checkout := range s.Native.Binding.Checkouts {
		if j.CWD != "" && resolvePath(j.CWD) == resolvePath(checkout) {
			mapped = true
		}
	}
	if !mapped {
		return nil, errors.New("map this checkout before native workflow execution")
	}
	fd, err := s.Native.Client.Definition(ctx, "workflow", firstNonEmpty(rec.Workflow, j.Workflow))
	if err != nil {
		return nil, err
	}
	projected, err := workflow.FromNative(fd)
	if err != nil {
		return nil, err
	}
	if projected.TakesInput() && strings.TrimSpace(rec.Input) == "" {
		return nil, errors.New("workflow needs input before local execution")
	}
	target := s.Native.Client.Target
	target.Token = ""
	j.Workflow = fd.UID
	j.Input = rec.Input
	j.Ref = rec.Ref
	c = runner.NativeExecutionContext{Version: 1, RunUID: rec.ID, Target: target, ProjectUID: s.Native.Binding.ProjectUID, ExecutorLabel: s.Native.Binding.ExecutorLabel, Workflow: &fd, Runtime: j}
	if id, err := katacli.NormalizeUID(rec.Ref); err == nil {
		c.IssueUID = id
		if err := nativeIssueReady(ctx, s, id); err != nil {
			return nil, err
		}
	}
	if err := c.Save(dir); err != nil {
		return nil, err
	}
	return runNativeContext(ctx, s, c, rec, opts)
}
