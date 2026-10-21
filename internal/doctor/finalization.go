package doctor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// Doctor is an explicit read-side action. Unlike status and the TUI it can
// compare retained terminal journals with Kata, without repairing either side.
func finalRunChecks(ctx context.Context, k *kata.CLI, f state.FS, l state.Layout, target runner.Target) []CheckResult {
	entries, err := f.ReadDir(filepath.Join(l.Root, "runs"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return []CheckResult{{Name: "final runs", Detail: err.Error()}}
	}
	var checks []CheckResult
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		var r runner.Run
		if err := state.ReadJSON(f, l.RunFile(entry.Name()), 4<<20, &r); err != nil {
			continue // LocalChecks owns unreadable-journal diagnostics.
		}
		if r.EndedAt == nil || r.IssueRequest != nil || r.Admitted != nil && !*r.Admitted || r.Target.Identity.InstanceUID != target.Identity.InstanceUID || r.Target.Identity.ProjectUID != target.Identity.ProjectUID || r.Target.Identity.Actor != target.Identity.Actor || r.Target.Executor != target.Executor {
			continue
		}
		check := CheckResult{Name: "final run " + r.UID}
		got, err := k.ShowRun(ctx, r.UID)
		switch {
		case err != nil:
			check.Detail = "cannot compare final run: " + err.Error()
		case got.UID != r.UID || got.Actor != target.Identity.Actor || got.ExecutorLabel != target.Executor:
			check.Detail = "Kata run identity differs; inspect the pinned route and retained journal"
		case got.Status != r.Outcome:
			check.Detail = fmt.Sprintf("locally %s/%s but Kata is %s at revision %d; herdr-kata up recovers saved final observations on the pinned route; inspect unresolved reporting operations with herdr-kata logs %s", r.Phase, r.Outcome, got.Status, got.Revision, r.UID)
		default:
			check.Passed, check.Detail = true, "Kata final status matches the local outcome"
		}
		checks = append(checks, check)
	}
	return checks
}
