package worker

import (
	"context"
	"errors"
	"github.com/salmonumbrella/herdr-kata/internal/clock"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

func TestPublishedPreparationAcknowledgedAfterSyncError(t *testing.T) {
	w, h, k := fixture(t)
	tick(t, w, at)
	req := journal.Request{ID: "published", Kind: "run", JobUID: k.jobs[0].UID}
	request(t, w, req)
	plan, reasons := defs.Freeze(k.jobs[0], nil, w.Config, "example-project", exec.LookPath)
	if len(reasons) != 0 {
		t.Fatal(reasons)
	}
	key := journal.ManualKey(w.Config.Executor, "example-project", req.ID)
	if err := w.prepare(context.Background(), k, w.Config.Projects[0], plan, key, "manual:"+req.ID, req.ID, at); err != nil {
		t.Fatal(err)
	}
	saved, err := w.Store.Load(key)
	if err != nil {
		t.Fatal(err)
	}
	// Create may return an fsync error after publishing the complete journal.
	q := journal.Requests{Dir: w.Store.Layout.Requests()}
	consumed, _ := w.acknowledgePreparation(q, req, key, errors.New("directory sync failed"))
	done, reason, err := q.Result(req.ID)
	if err != nil || !consumed || !done || reason != "" {
		t.Fatalf("published request not acknowledged: consumed=%v done=%v reason=%q err=%v", consumed, done, reason, err)
	}
	restarted := New(w.Config, w.Store, h, w.Clients, w.Bin)
	tick(t, restarted, at.Add(time.Second))
	tick(t, restarted, at.Add(2*time.Second))
	r, err := w.Store.Load(key)
	if err != nil || r.RunUID != saved.RunUID || h.runs != 1 {
		t.Fatalf("published run did not recover exactly once: %+v %d %v", r, h.runs, err)
	}
}

func TestSkipSurvivesBriefUnavailableScan(t *testing.T) {
	for _, failure := range []string{"jobs", "workflows", "ready", "not-ready"} {
		t.Run(failure, func(t *testing.T) {
			w, h, k := fixture(t)
			k.jobs[0].CreatedAt = at.Add(-24 * time.Hour)
			k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "cron", Cron: new("0 9 * * *")}
			k.jobs[0].Definition.Catchup = "skip"
			k.jobs[0].Definition.GraceSeconds = new(int64(20))
			if failure == "not-ready" {
				k.jobs[0].Definition.Issue = &kata.IssuePolicy{Kind: "existing", UID: new("01J00000000000000000000020")}
				k.ready["01J00000000000000000000020"] = true
			}
			tick(t, w, at.Add(-40*time.Second))
			if failure == "not-ready" {
				k.ready["01J00000000000000000000020"] = false
			} else {
				k.failures = map[string]error{failure: &kata.Error{Code: 7}}
			}
			_ = w.Tick(clock.WithNow(context.Background(), at.Add(-10*time.Second)), at.Add(-10*time.Second))
			k.failures = nil
			k.ready["01J00000000000000000000020"] = true
			tick(t, w, at.Add(25*time.Second))
			tick(t, w, at.Add(30*time.Second))
			if h.runs != 1 {
				t.Fatalf("brief unavailable scan lost or repeated occurrence: %d launches", h.runs)
			}
		})
	}
}

func TestSkipDropsLongOutageWhileWorkerAlive(t *testing.T) {
	w, h, k := fixture(t)
	k.jobs[0].CreatedAt = at.Add(-24 * time.Hour)
	k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "cron", Cron: new("0 9 * * *")}
	k.jobs[0].Definition.Catchup = "skip"
	k.jobs[0].Definition.GraceSeconds = new(int64(20))
	tick(t, w, at.Add(-40*time.Second))
	k.err = &kata.Error{Code: 7}
	_ = w.Tick(clock.WithNow(context.Background(), at.Add(-10*time.Second)), at.Add(-10*time.Second))
	_ = w.Tick(clock.WithNow(context.Background(), at.Add(9*time.Minute)), at.Add(9*time.Minute))
	k.err = nil
	tick(t, w, at.Add(10*time.Minute))
	if h.runs != 0 {
		t.Fatalf("stale occurrence launched: %d", h.runs)
	}
}

func TestManualRequestsWaitForCapacityAndOverlap(t *testing.T) {
	for _, gate := range []string{"global", "project", "overlap"} {
		t.Run(gate, func(t *testing.T) {
			w, h, k := fixture(t)
			k.jobs[0].Definition.Options = map[string]any{"input": "configured context"}
			w.Config.Projects[0].MaxActiveRuns = 2
			k.jobs[0].Definition.Overlap = "allow"
			switch gate {
			case "global":
				w.Config.Limits.MaxActiveRuns = 1
			case "project":
				w.Config.Projects[0].MaxActiveRuns = 1
			case "overlap":
				k.jobs[0].Definition.Overlap = "forbid"
			}
			request(t, w, journal.Request{ID: "first", Kind: "run", JobUID: k.jobs[0].UID})
			tick(t, w, at)
			first := active(t, w)
			request(t, w, journal.Request{ID: "waiting", Kind: "run", JobUID: k.jobs[0].UID, InputSet: true})
			tick(t, w, at.Add(time.Second))
			q := journal.Requests{Dir: w.Store.Layout.Requests()}
			done, pendingReason, err := q.Result("waiting")
			if err != nil || done || pendingReason == "" {
				t.Fatalf("temporary gate rejected request: done=%v err=%v", done, err)
			}
			finishAgent(t, w, h, first)
			tick(t, w, at.Add(2*time.Second))
			tick(t, w, at.Add(3*time.Second))
			tick(t, w, at.Add(4*time.Second))
			done, reason, err := q.Result("waiting")
			if err != nil || !done || reason != "" || h.runs != 2 {
				t.Fatalf("request did not run once after gate freed: done=%v reason=%q launches=%d err=%v", done, reason, h.runs, err)
			}
			if got := active(t, w); got.RequestID != "waiting" || got.Plan.Input != "" {
				t.Fatalf("pending input changed: %q", got.Plan.Input)
			}
		})
	}
}

func TestManualRequestsWaitForTransientReads(t *testing.T) {
	for _, failure := range []string{"jobs", "workflows", "ready"} {
		t.Run(failure, func(t *testing.T) {
			w, h, k := fixture(t)
			if failure == "ready" {
				// Only jobs bound to an existing issue read the ready list.
				issue := "01J00000000000000000000099"
				k.jobs[0].Definition.Issue = &kata.IssuePolicy{Kind: "existing", UID: &issue}
				k.ready = map[string]bool{issue: true}
			}
			request(t, w, journal.Request{ID: "waiting", Kind: "run", JobUID: k.jobs[0].UID})
			k.failures = map[string]error{failure: &kata.Error{Code: 7}}
			_ = w.Tick(clock.WithNow(context.Background(), at), at)
			q := journal.Requests{Dir: w.Store.Layout.Requests()}
			if done, _, err := q.Result("waiting"); err != nil || done {
				t.Fatalf("transient read rejected request: %v %v", done, err)
			}
			k.failures = nil
			tick(t, w, at.Add(30*time.Second))
			tick(t, w, at.Add(time.Minute))
			if done, reason, err := q.Result("waiting"); err != nil || !done || reason != "" || h.runs != 1 {
				t.Fatalf("request not admitted after recovery: %v %q %d %v", done, reason, h.runs, err)
			}
		})
	}
}

func TestUnknownRequestNeedsAllProjectsRead(t *testing.T) {
	w, h, k := fixture(t)
	w.Config.Projects = append(w.Config.Projects, config.Project{Name: "other-project", Actor: "worker", MaxActiveRuns: 1})
	w.Clients["other-project"] = &fakeKata{ready: map[string]bool{}}
	k.err = &kata.Error{Code: 7}
	request(t, w, journal.Request{ID: "unknown", Kind: "run", JobUID: "01J00000000000000000000099"})
	_ = w.Tick(clock.WithNow(context.Background(), at), at)
	q := journal.Requests{Dir: w.Store.Layout.Requests()}
	if done, _, err := q.Result("unknown"); err != nil || done {
		t.Fatalf("unknown rejected before all reads: %v %v", done, err)
	}
	k.err = nil
	tick(t, w, at.Add(30*time.Second))
	done, reason, err := q.Result("unknown")
	if err != nil || !done || !strings.Contains(reason, "unknown") || h.runs != 0 {
		t.Fatalf("unknown not rejected after complete scan: %v %q %v", done, reason, err)
	}
}

func TestSkipRecoveryLatenessBoundary(t *testing.T) {
	for _, late := range []time.Duration{140 * time.Second, 140*time.Second + time.Nanosecond} {
		t.Run(late.String(), func(t *testing.T) {
			w, h, k := fixture(t)
			k.jobs[0].CreatedAt = at.Add(-24 * time.Hour)
			k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "cron", Cron: new("0 9 * * *")}
			k.jobs[0].Definition.Catchup = "skip"
			k.jobs[0].Definition.GraceSeconds = new(int64(20))
			tick(t, w, at.Add(-40*time.Second))
			k.err = &kata.Error{Code: 7}
			_ = w.Tick(clock.WithNow(context.Background(), at.Add(-10*time.Second)), at.Add(-10*time.Second))
			k.err = nil
			tick(t, w, at.Add(late))
			want := 0
			if late == 140*time.Second {
				want = 1
			}
			if h.runs != want {
				t.Fatalf("lateness %s: launches=%d want=%d", late, h.runs, want)
			}
		})
	}
}

func TestSkipRestartNeedsStartupGrace(t *testing.T) {
	w, h, k := fixture(t)
	k.jobs[0].CreatedAt = at.Add(-24 * time.Hour)
	k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "cron", Cron: new("0 9 * * *")}
	k.jobs[0].Definition.Catchup = "skip"
	k.jobs[0].Definition.GraceSeconds = new(int64(20))
	tick(t, w, at.Add(-40*time.Second))
	restarted := New(w.Config, w.Store, h, w.Clients, w.Bin)
	tick(t, restarted, at.Add(25*time.Second))
	if h.runs != 0 {
		t.Fatalf("restart ran occurrence older than startup grace: %d", h.runs)
	}
}

func TestPermanentReasonNotOverwrittenByOtherProjectFailure(t *testing.T) {
	w, _, k := fixture(t)
	job := k.jobs[0]
	job.UID = "01J00000000000000000000030"
	job.Definition.Options = map[string]any{"adapter": "other"}
	w.Config.Projects = append([]config.Project{{Name: "other-project", Actor: "worker", MaxActiveRuns: 1}}, w.Config.Projects...)
	w.Clients["other-project"] = &fakeKata{jobs: []kata.CronJob{job}, ready: map[string]bool{}}
	k.err = &kata.Error{Code: 7}
	request(t, w, journal.Request{ID: "unsupported", Kind: "run", JobUID: job.UID})
	_ = w.Tick(clock.WithNow(context.Background(), at), at)
	done, reason, err := (journal.Requests{Dir: w.Store.Layout.Requests()}).Result("unsupported")
	if err != nil || !done || !strings.Contains(reason, "unsupported") || strings.Contains(reason, "unavailable") {
		t.Fatalf("wrong cross-project rejection: %v %q %v", done, reason, err)
	}
}

func TestPendingReasonBelongsToMatchedProject(t *testing.T) {
	w, _, k := fixture(t)
	job := k.jobs[0]
	job.UID = "01J00000000000000000000030"
	w.Config.Projects = append([]config.Project{{Name: "other-project", Actor: "worker", MaxActiveRuns: 1}}, w.Config.Projects...)
	w.Clients["other-project"] = &fakeKata{jobs: []kata.CronJob{job}, ready: map[string]bool{}}
	k.err = &kata.Error{Code: 7}
	request(t, w, journal.Request{ID: "first", Kind: "run", JobUID: job.UID})
	_ = w.Tick(clock.WithNow(context.Background(), at), at)
	request(t, w, journal.Request{ID: "waiting", Kind: "run", JobUID: job.UID})
	_ = w.Tick(clock.WithNow(context.Background(), at.Add(time.Second)), at.Add(time.Second))
	done, reason, err := (journal.Requests{Dir: w.Store.Layout.Requests()}).Result("waiting")
	if err != nil || done || !strings.Contains(reason, "capacity") {
		t.Fatalf("unrelated failure replaced pending reason: %v %q %v", done, reason, err)
	}
}
