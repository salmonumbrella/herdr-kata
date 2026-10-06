package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"github.com/salmonumbrella/herdr-kata/internal/workflow"
)

// What the board's WORKFLOWS tab does, kept here in the command layer where workflows
// already live so the board never imports this package.
//
// They are deliberately the same two operations `herdr-kata workflow run` and
// `herdr-kata workflow resume` perform, against the same store and the same run
// directories: a workflow started from the board has to be indistinguishable from
// one started by hand, or the board becomes a second way of doing it with its
// own bugs.

// startWorkflowFromBoard calls a workflow with an input.
func startWorkflowFromBoard(s *store.Store, workflowID, input string) error {
	var def workflow.Workflow
	var err error
	if s.Native != nil {
		def, err = nativeWorkflowDefinition(context.Background(), s, workflowID)
	} else {
		def, err = workflow.Load(workflowDir(), workflowID)
	}
	if err != nil {
		return err
	}
	// The same refusal `workflow run` makes, and the one that counts: the board asks
	// for an input before it gets here, but a workflow whose prompts say {{input}}
	// started with a blank hands every agent in the sequence a hole where its
	// subject should be, and an agent handed that will invent something.
	if def.TakesInput() && strings.TrimSpace(input) == "" {
		return fmt.Errorf("workflow %s needs an input: %s", def.ID, def.Input)
	}
	// The board's own working directory, which is the directory the pane was
	// opened in — the same thing `workflow run` with no --cwd uses. A workflow declares
	// no directory of its own on purpose: that belongs to whoever calls it.
	dir, err := os.Getwd()
	if err != nil {
		dir = ""
	}
	runID := newRunID(def.ID)
	if s.Native != nil {
		runID, err = katacli.NewUID()
		if err != nil {
			return err
		}
	}
	rec := store.Run{
		ID: runID, JobID: def.ID, Trigger: "manual",
		Outcome: "running", StartedAt: time.Now(),
		Workflow: def.ID, Input: input,
	}
	_, err = runWorkflow(context.Background(), s, workflowJob(def, dir, "", ""), rec, workflowOpts{})
	return err
}

// resumeWorkflowRun picks a parked workflow run up at the step that stopped it.
func resumeWorkflowRun(s *store.Store, runID string) error {
	ctx := context.Background()
	rec, err := s.Run(ctx, runID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(rec.Workflow) == "" {
		return fmt.Errorf("run %s is not a workflow run; there is nothing to resume", rec.ID)
	}
	// The job is optional. A workflow called from the board has none at all, and one
	// called by a job may outlive it — but the run records which workflow ran and
	// what it was called with, so neither case needs the job to still exist.
	j := store.Job{ID: rec.JobID, Workflow: rec.Workflow, Enabled: true, Model: store.DefaultModel}
	if s.Native == nil {
		if stored, err := s.Job(ctx, rec.JobID); err == nil {
			j = *stored
		}
	}
	_, err = runWorkflow(ctx, s, j, *rec, workflowOpts{})
	return err
}
