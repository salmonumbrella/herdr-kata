//go:build live

package live

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/report"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

func (f *fixture) scheduledJob(uid, at string, options map[string]any) {
	f.t.Helper()
	b, err := json.Marshal(map[string]any{"name": "Scratch " + uid, "definition": map[string]any{
		"version": 1, "kind": "job", "enabled": true, "trigger": map[string]any{"kind": "once", "at": at},
		"action": map[string]any{"kind": "execute", "prompt": "Complete the scratch task."},
		"issue":  map[string]any{"kind": "per-run", "title": "Scratch scheduled execution"}, "catchup": "latest", "overlap": "forbid", "options": options,
	}})
	if err != nil {
		f.t.Fatal(err)
	}
	f.kata(b, "cron", "job", "create", "--uid", uid, "--file", "-")
}
func (f *fixture) jobRuns(uid string) []struct {
	UID, Status string
	Revision    int
	IssueUID    *string `json:"issue_uid"`
	Summary     kata.Summary
} {
	f.t.Helper()
	var v struct {
		Runs []struct {
			UID, Status string
			Revision    int
			IssueUID    *string `json:"issue_uid"`
			Summary     kata.Summary
		}
	}
	if err := json.Unmarshal(f.kata(nil, "cron", "run", "list", "--job-uid", uid, "--limit", "100"), &v); err != nil {
		f.t.Fatal(err)
	}
	return v.Runs
}
func eventually(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	for {
		if predicate() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("scratch condition timed out")
		case <-poll.C:
		}
	}
}
func zeroConfigJobs(t *testing.T, bin, coreBin, script string) {
	f := newFixture(t, bin, coreBin, "")
	f.configure([]string{"/bin/sh", script, "project-profile"})
	b, err := os.ReadFile(f.config)
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.Replace(b, []byte("commands=true"), []byte("commands=true\ndefault_checkout=\"example-project\"\ndefault_agent=\"project-agent\""), 1)
	b = bytes.Replace(b, []byte("[profiles.default]"), []byte("[profiles.project-agent]"), 1)
	write(t, f.config, b)
	good, bad, pinned := "01J00000000000000000000031", "01J00000000000000000000032", "01J00000000000000000000033"
	due := time.Now().UTC().Add(5 * time.Second).Format(time.RFC3339Nano)
	f.scheduledJob(good, due, nil)
	f.scheduledJob(bad, due, map[string]any{"agent": "missing"})
	f.scheduledJob(pinned, due, map[string]any{"executor": "another-worker"})
	f.cli("up")
	eventually(t, func() bool { return len(f.jobRuns(bad)) == 1 })
	unsupported := f.jobRuns(bad)[0]
	if unsupported.Status != "failed" || unsupported.Revision != 1 || unsupported.IssueUID != nil && *unsupported.IssueUID != "" || unsupported.Summary.Message == nil || *unsupported.Summary.Message != "reason: unsupported\nprofile not configured: missing" {
		t.Fatalf("unsupported observation %+v", unsupported)
	}
	r := f.await(good, func(r journal.Run) bool { return r.Phase == "reported" })
	f.assertEvidence(r, true)
	resolved, err := filepath.EvalSymlinks(f.work)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := filepath.EvalSymlinks(r.Plan.CheckoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if checkout != resolved || len(r.Plan.Steps) != 1 || !slices.Equal(r.Plan.Steps[0].Argv, []string{"/bin/sh", script, "project-profile"}) || strings.HasPrefix(r.Occurrence, "manual:") {
		t.Fatalf("defaults not frozen: %+v", r.Plan)
	}
	// Completion takes several worker ticks. The unsupported run stays one revision.
	assertUnsupported := func() {
		t.Helper()
		runs := f.jobRuns(bad)
		if len(runs) != 1 || runs[0].UID != unsupported.UID || runs[0].Revision != 1 || len(f.jobRuns(pinned)) != 0 {
			t.Fatal("unsupported/pinned observations changed")
		}
		all, err := f.store.All()
		if err != nil || len(all) != 1 || all[0].JobUID != good {
			t.Fatal(all, err)
		}
		for _, uid := range []string{bad, pinned} {
			if _, err := os.Stat(f.store.Layout.Job(uid)); !os.IsNotExist(err) {
				t.Fatalf("unexpected checkpoint for %s: %v", uid, err)
			}
		}
		records, err := (report.Outbox{Layout: f.store.Layout}).All()
		if err != nil || len(records) != 1 || records[0].State != "done" {
			t.Fatal(records, err)
		}
		f.verifyCore()
		ids, err := f.h.List(context.Background(), true, nil)
		if err != nil || len(ids) != 1 {
			t.Fatalf("unexpected job terminal: %v %v", ids, err)
		}
		var issues struct{ Issues []struct{ UID string } }
		if err := json.Unmarshal(f.kata(nil, "list", "--status", "all", "--limit", "0"), &issues); err != nil || len(issues.Issues) != 1 || issues.Issues[0].UID != r.IssueUID {
			t.Fatal("unexpected scratch issues", issues, err)
		}
	}
	assertUnsupported()
	f.cli("stop")
	restarted := time.Now().UTC()
	f.cli("up")
	eventually(t, func() bool {
		var cp sched.Checkpoint
		b, err := os.ReadFile(f.store.Layout.Job(good))
		return err == nil && json.Unmarshal(b, &cp) == nil && !cp.Through.Before(restarted)
	})
	assertUnsupported()
	t.Log("scheduled defaults succeeded without manual run; unsupported stayed one failed run across ticks/restart; foreign pin produced no run, terminal, issue or checkpoint")
}
