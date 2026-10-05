package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

const nativeIssuePreparationNote = "Per-run issue preparation is pending."

func nativeIssuePreparationPending(c runner.NativeExecutionContext, rec store.Run) bool {
	policy, err := nativeRunIssuePolicy(c)
	return err == nil && policy.Kind == "per-run" && !c.IssuePreparedAt.IsZero() &&
		c.Job != nil && rec.JobID == c.Job.UID && rec.RunDir == runDirFor(c.RunUID) &&
		rec.Input == c.Runtime.Input && rec.Outcome == "parked" && rec.Note == nativeIssuePreparationNote
}

func resumeNativeIssuePreparation(argv []string) error {
	if len(argv) != 1 {
		return errors.New("usage: herdr-kata run resume <run-uid>")
	}
	uid, err := katacli.NormalizeUID(argv[0])
	if err != nil || uid != argv[0] {
		return errors.New("run resume requires a canonical run UID")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	if s.Native == nil || s.Native.Client == nil {
		return store.ErrNativeUnconfigured
	}
	c, err := runner.LoadNativeContext(runDirFor(uid))
	if err != nil {
		return err
	}
	run, err := runNativeContext(context.Background(), s, c, store.Run{ID: uid}, flowOpts{OnlyIssuePreparation: true})
	if run != nil {
		printRun(run)
	}
	if err != nil {
		return err
	}
	if run != nil && run.Outcome == runner.OutcomeFailed {
		return fmt.Errorf("resumed run %s failed", uid)
	}
	return nil
}
