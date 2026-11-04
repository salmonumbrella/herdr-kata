package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/report"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func droppedRows(t *testing.T, w *Worker) []report.Record {
	t.Helper()
	rows, err := (report.Outbox{Layout: w.Store.Layout, Dir: w.Store.Layout.Dropped()}).All()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
func savedCheckpoint(t *testing.T, w *Worker, uid string) sched.Checkpoint {
	t.Helper()
	b, err := os.ReadFile(w.Store.Layout.Job(uid))
	if err != nil {
		t.Fatal(err)
	}
	var c sched.Checkpoint
	if err = json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}
func heldOnce(t *testing.T, grace int64, setup ...func(*Worker)) (*Worker, *fakeHC, *fakeKata, journal.Run) {
	t.Helper()
	w, h, k := fixture(t)
	for _, configure := range setup {
		configure(w)
	}
	k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "once", At: new(at.Format(time.RFC3339))}
	k.jobs[0].Definition.GraceSeconds = &grace
	tick(t, w, at.Add(-time.Minute))
	request(t, w, journal.Request{ID: "occupant", Kind: "run", JobUID: k.jobs[0].UID})
	tick(t, w, at.Add(-30*time.Second))
	r := active(t, w)
	if err := w.Store.Update(r.Key, func(r *journal.Run) error { r.Phase = "held"; r.ReportDone = true; r.Outcome = "unknown"; return nil }); err != nil {
		t.Fatal(err)
	}
	tick(t, w, at)
	return w, h, k, r
}

func TestDroppedHostSupportFailureKeepsGate(t *testing.T) {
	for _, mode := range []string{"release", "settlement"} {
		t.Run(mode, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "worker")
			if err := os.Symlink("/bin/sh", bin); err != nil {
				t.Fatal(err)
			}
			w, h, k, r := heldOnce(t, 600, func(w *Worker) { w.Config.Profiles["default"].Argv[0] = bin })
			if mode == "release" {
				request(t, w, journal.Request{ID: "release", Kind: "release", Run: r.RunUID})
			} else {
				if err := w.Store.Update(r.Key, func(r *journal.Run) error { r.Phase = "running"; r.ReportDone = false; return nil }); err != nil {
					t.Fatal(err)
				}
				finishAgent(t, w, h, r)
			}
			if err := os.Remove(bin); err != nil {
				t.Fatal(err)
			}
			for n := range 12 {
				tick(t, w, at.Add(11*time.Minute+time.Duration(n)*time.Second))
			}
			saved, err := w.Store.Load(r.Key)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Phase == "released" || saved.Phase == "settled" || saved.Phase == "reported" {
				t.Fatal("host support failure removed gate before expiry was durable", saved.Phase)
			}
			if err = os.Symlink("/bin/sh", bin); err != nil {
				t.Fatal(err)
			}
			w = New(w.Config, w.Store, h, w.Clients, w.Bin)
			for n := range 12 {
				tick(t, w, at.Add(12*time.Minute+time.Duration(n)*time.Second))
			}
			rows := droppedRows(t, w)
			if h.runs != 1 || len(rows) != 1 || len(savedCheckpoint(t, w, k.jobs[0].UID).Pending) != 0 {
				t.Fatal("replayed overdue occurrence after support recovered", h.runs, rows)
			}
			var b kata.ObserveCronRunBody
			if err = json.Unmarshal(rows[0].Body, &b); err != nil {
				t.Fatal(err)
			}
			if *b.OccurrenceKey != at.Format(time.RFC3339) || !strings.HasPrefix(*b.Summary.Message, "reason: overlap_skipped\n1 occurrences ") {
				t.Fatalf("body=%s", rows[0].Body)
			}
		})
	}
}

// Expiry must precede the release that would otherwise admit the overdue occurrence.
func TestDroppedForbidReleaseTiming(t *testing.T) {
	for _, tt := range []struct {
		name      string
		grace     int64
		releaseAt time.Duration
		drop      bool
	}{{"expired", 600, 11 * time.Minute, true}, {"no grace", 0, 11 * time.Minute, false}, {"before expiry", 600, 5 * time.Minute, false}, {"at grace", 600, 10 * time.Minute, false}} {
		t.Run(tt.name, func(t *testing.T) {
			w, h, k, r := heldOnce(t, tt.grace)
			if len(droppedRows(t, w)) != 0 {
				t.Fatal("dropped before grace")
			}
			request(t, w, journal.Request{ID: "release", Kind: "release", Run: r.RunUID})
			tick(t, w, at.Add(tt.releaseAt))
			tick(t, w, at.Add(tt.releaseAt+time.Second))
			rows := droppedRows(t, w)
			wantRuns := 2
			if tt.drop {
				wantRuns = 1
			}
			if h.runs != wantRuns || len(rows) != map[bool]int{true: 1, false: 0}[tt.drop] {
				t.Fatalf("launches=%d rows=%v", h.runs, rows)
			}
			if tt.drop {
				var b kata.ObserveCronRunBody
				if err := json.Unmarshal(rows[0].Body, &b); err != nil {
					t.Fatal(err)
				}
				if b.Status != "cancelled" || *b.OccurrenceKey != at.Format(time.RFC3339) || !strings.HasPrefix(*b.Summary.Message, "reason: overlap_skipped\n1 occurrences ") {
					t.Fatalf("body=%s", rows[0].Body)
				}
			}
			if len(savedCheckpoint(t, w, k.jobs[0].UID).Pending) != 0 {
				t.Fatal("unacknowledged occurrence")
			}
		})
	}
}
func TestDroppedForbidWaitsThenNeverReplays(t *testing.T) {
	w, h, _, r := heldOnce(t, 600)
	tick(t, w, at.Add(5*time.Minute))
	if len(droppedRows(t, w)) != 0 {
		t.Fatal("early drop")
	}
	tick(t, w, at.Add(11*time.Minute))
	if len(droppedRows(t, w)) != 1 {
		t.Fatal("missing expiry")
	}
	request(t, w, journal.Request{ID: "release", Kind: "release", Run: r.RunUID})
	tick(t, w, at.Add(12*time.Minute))
	tick(t, w, at.Add(13*time.Minute))
	if h.runs != 1 {
		t.Fatal("launched cancelled occurrence", h.runs)
	}
}
func TestDroppedDoesNotConfuseOtherJobCapacity(t *testing.T) {
	w, h, _, r := heldOnce(t, 600)
	if err := w.Store.Update(r.Key, func(r *journal.Run) error { r.JobUID = "another-job"; return nil }); err != nil {
		t.Fatal(err)
	}
	tick(t, w, at.Add(11*time.Minute))
	if len(droppedRows(t, w)) != 0 || h.runs != 1 {
		t.Fatal("capacity caused a drop")
	}
}
func TestDroppedLatestAndInitialHorizons(t *testing.T) {
	w, h, k := fixture(t)
	k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "interval", IntervalSeconds: new(int64(3600))}
	k.jobs[0].Definition.Catchup = "latest"
	tick(t, w, at)
	if len(droppedRows(t, w)) != 0 {
		t.Fatal("installation backfilled")
	}
	tick(t, w, at.Add(3*time.Hour))
	rows := droppedRows(t, w)
	if len(rows) != 1 || h.runs != 1 {
		t.Fatal(rows, h.runs)
	}
	var b kata.ObserveCronRunBody
	_ = json.Unmarshal(rows[0].Body, &b)
	if b.Status != "cancelled" || *b.OccurrenceKey != "2026-11-02T10:00:00Z" || !strings.HasPrefix(*b.Summary.Message, "reason: catchup_missed\n2 occurrences ") {
		t.Fatalf("body=%s", rows[0].Body)
	}
}

// Frozen checkpoint recovery must apply even if network delivery already completed.
func TestDroppedRecoverFrozenDecisionBeforeFreeGate(t *testing.T) {
	for _, delivery := range []bool{false, true} {
		for _, delay := range []time.Duration{time.Minute, 9 * 24 * time.Hour} {
			t.Run(map[bool]string{false: "pending", true: "delivered"}[delivery], func(t *testing.T) {
				w, h, k := fixture(t)
				k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "once", At: new(at.Format(time.RFC3339))}
				tick(t, w, at.Add(-time.Minute))
				cp := savedCheckpoint(t, w, k.jobs[0].UID)
				cp.Through = at.Add(11 * time.Minute)
				cp.Pending = nil
				p, reasons := defs.Freeze(k.jobs[0], nil, w.Config, "example-project", exec.LookPath)
				if len(reasons) != 0 {
					t.Fatal(reasons)
				}
				o := report.Outbox{Layout: w.Store.Layout, Dir: w.Store.Layout.Dropped()}
				original := savedCheckpoint(t, w, k.jobs[0].UID)
				path := w.Store.Layout.Job(k.jobs[0].UID)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				d := sched.Dropped{Count: 1, First: at, Last: at}
				if err := w.commitCheckpoint(w.Config.Projects[0], p, cp, d, true, cp.Through); err == nil {
					t.Fatal("expected checkpoint publication failure")
				}
				first := droppedRows(t, w)[0]
				if delivery {
					if err := o.Deliver(w.Clients, cp.Through); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := state.WriteJSON(path, original); err != nil {
					t.Fatal(err)
				}
				w = New(w.Config, w.Store, h, w.Clients, w.Bin)
				tick(t, w, cp.Through.Add(delay))
				after := droppedRows(t, w)
				if h.runs != 0 || len(after) != 1 || after[0].RunUID != first.RunUID || !bytes.Equal(after[0].Body, first.Body) || !savedCheckpoint(t, w, k.jobs[0].UID).Through.After(cp.Through) {
					t.Fatal("replayed a cancelled occurrence", h.runs, after)
				}
			})
		}
	}
}
func TestDroppedReconcilePreparedPendingBeforeCounting(t *testing.T) {
	for _, policy := range []string{"all", "latest", "skip"} {
		t.Run(policy, func(t *testing.T) {
			w, h, k := fixture(t)
			k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "once", At: new(at.Format(time.RFC3339))}
			k.jobs[0].Definition.Catchup = policy
			k.jobs[0].Definition.GraceSeconds = new(int64(600))
			tick(t, w, at.Add(-time.Minute))
			tick(t, w, at)
			r := active(t, w)
			cp := savedCheckpoint(t, w, k.jobs[0].UID)
			cp.Pending = []sched.Occurrence{{Key: at.Format(time.RFC3339), At: at}}
			if err := state.WriteJSON(w.Store.Layout.Job(k.jobs[0].UID), cp); err != nil {
				t.Fatal(err)
			}
			if err := w.Store.Update(r.Key, func(r *journal.Run) error { r.Phase = "held"; r.ReportDone = true; return nil }); err != nil {
				t.Fatal(err)
			}
			tick(t, w, at.Add(9*24*time.Hour))
			if len(droppedRows(t, w)) != 0 || h.runs != 1 {
				t.Fatal("cancelled prepared work", droppedRows(t, w))
			}
		})
	}
}

func TestDroppedFailureKeepsReleaseAndSettlementGates(t *testing.T) {
	for _, mode := range []string{"release", "settlement"} {
		t.Run(mode, func(t *testing.T) {
			w, h, _, r := heldOnce(t, 600)
			if mode == "release" {
				request(t, w, journal.Request{ID: "release", Kind: "release", Run: r.RunUID})
			} else {
				if err := w.Store.Update(r.Key, func(r *journal.Run) error { r.Phase = "running"; r.ReportDone = false; return nil }); err != nil {
					t.Fatal(err)
				}
				finishAgent(t, w, h, r)
			}
			if err := os.WriteFile(w.Store.Layout.Dropped(), []byte("blocked"), 0600); err != nil {
				t.Fatal(err)
			}
			for n := range 10 {
				if err := w.Tick(t.Context(), at.Add(11*time.Minute+time.Duration(n)*time.Second)); err == nil {
					t.Fatal("ignored failed drop intent")
				}
			}
			saved, err := w.Store.Load(r.Key)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Phase == "released" || saved.Phase == "settled" || saved.Phase == "reported" {
				t.Fatal("removed gate before durable decision", saved.Phase)
			}
			if err = os.Remove(w.Store.Layout.Dropped()); err != nil {
				t.Fatal(err)
			}
			w = New(w.Config, w.Store, h, w.Clients, w.Bin)
			for n := range 4 {
				tick(t, w, at.Add(12*time.Minute+time.Duration(n)*time.Second))
			}
			if h.runs != 1 || len(droppedRows(t, w)) != 1 {
				t.Fatal("failed decision replayed", h.runs, droppedRows(t, w))
			}
		})
	}
}
func TestDroppedEqualWatermarkDoesNotResurrect(t *testing.T) {
	w, h, k, r := heldOnce(t, 600)
	cp := savedCheckpoint(t, w, k.jobs[0].UID)
	cp.Pending = nil
	p, why := defs.Freeze(k.jobs[0], nil, w.Config, "example-project", exec.LookPath)
	if len(why) > 0 {
		t.Fatal(why)
	}
	o := report.Outbox{Layout: w.Store.Layout, Dir: w.Store.Layout.Dropped()}
	if err := o.Drop(p, "example-project", "worker", w.Config.Executor, "overlap_skipped", "detail", at.Format(time.RFC3339), cp, at); err != nil {
		t.Fatal(err)
	}
	request(t, w, journal.Request{ID: "release", Kind: "release", Run: r.RunUID})
	tick(t, w, at.Add(time.Minute))
	tick(t, w, at.Add(2*time.Minute))
	if h.runs != 1 || len(savedCheckpoint(t, w, k.jobs[0].UID).Pending) != 0 {
		t.Fatal("resurrected cancelled pending")
	}
	// A later prepared removal at the same Through must also survive recovery.
	cp.Pending = []sched.Occurrence{{Key: at.Format(time.RFC3339), At: at}}
	if err := o.Drop(p, "example-project", "worker", w.Config.Executor, "catchup_missed", "other", "older-key", cp, at); err != nil {
		t.Fatal(err)
	}
	tick(t, w, at.Add(3*time.Minute))
	if h.runs != 1 {
		t.Fatal("resurrected prepared removal")
	}
}

func TestDroppedRecoveryCountsDisjointAndNewInstants(t *testing.T) {
	w, h, k := fixture(t)
	k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "interval", IntervalSeconds: new(int64(3600))}
	k.jobs[0].Definition.Catchup = "latest"
	tick(t, w, at)
	old := savedCheckpoint(t, w, k.jobs[0].UID)
	old.Pending = []sched.Occurrence{{Key: "old-pending", At: at.Add(-2 * time.Hour)}}
	if err := state.WriteJSON(w.Store.Layout.Job(k.jobs[0].UID), old); err != nil {
		t.Fatal(err)
	}
	p, why := defs.Freeze(k.jobs[0], nil, w.Config, "example-project", exec.LookPath)
	if len(why) > 0 {
		t.Fatal(why)
	}
	cp, d, err := w.checkpoint(w.Config.Projects[0], k.jobs[0], p, false, at.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if d.Count != 3 || !d.First.Equal(at.Add(-2*time.Hour)) || !d.Last.Equal(at.Add(2*time.Hour)) {
		t.Fatal("counted gaps", d)
	}
	path := w.Store.Layout.Job(k.jobs[0].UID)
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err = w.commitCheckpoint(w.Config.Projects[0], p, cp, d, false, cp.Through); err == nil {
		t.Fatal("missing write failure")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = state.WriteJSON(path, old); err != nil {
		t.Fatal(err)
	}
	first := droppedRows(t, w)[0]
	w = New(w.Config, w.Store, h, w.Clients, w.Bin)
	tick(t, w, at.Add(4*time.Hour))
	rows := droppedRows(t, w)
	if len(rows) != 2 || h.runs != 1 {
		t.Fatal(rows, h.runs)
	}
	var original, extra *report.Record
	for i := range rows {
		if rows[i].RunUID == first.RunUID {
			original = &rows[i]
		} else {
			extra = &rows[i]
		}
	}
	if original == nil || extra == nil || !bytes.Equal(original.Body, first.Body) {
		t.Fatal("changed frozen evidence")
	}
	var b kata.ObserveCronRunBody
	_ = json.Unmarshal(extra.Body, &b)
	if *b.OccurrenceKey != "2026-11-02T12:00:00Z" || !strings.HasPrefix(*b.Summary.Message, "reason: catchup_missed\n1 occurrences ") {
		t.Fatalf("new body=%s", extra.Body)
	}
	if active(t, w).Occurrence != "2026-11-02T13:00:00Z" {
		t.Fatal("wrong retained launch")
	}
}
func TestDroppedRecoveryPreservesNewDefinitionAndPreparation(t *testing.T) {
	w, h, k := fixture(t)
	tick(t, w, at)
	p, why := defs.Freeze(k.jobs[0], nil, w.Config, "example-project", exec.LookPath)
	if len(why) > 0 {
		t.Fatal(why)
	}
	old := sched.Checkpoint{Version: 1, JobUID: p.JobUID, EventUID: p.JobEventUID, Through: at.Add(2 * time.Minute), Pending: []sched.Occurrence{{Key: "old", At: at}}}
	o := report.Outbox{Layout: w.Store.Layout, Dir: w.Store.Layout.Dropped()}
	if err := o.Drop(p, "example-project", "worker", w.Config.Executor, "catchup_missed", "detail", "dropped-old", old, old.Through); err != nil {
		t.Fatal(err)
	}
	k.jobs[0].DefinitionEventUID = "new-event"
	k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "once", At: new(at.Add(time.Minute).Format(time.RFC3339))}
	key := at.Add(time.Minute).Format(time.RFC3339)
	current := sched.Checkpoint{Version: 1, JobUID: p.JobUID, EventUID: "new-event", Through: at.Add(time.Minute), Pending: []sched.Occurrence{{Key: key, At: at.Add(time.Minute)}}}
	if err := state.WriteJSON(w.Store.Layout.Job(p.JobUID), current); err != nil {
		t.Fatal(err)
	}
	tick(t, w, at.Add(2*time.Minute))
	if h.runs != 1 || active(t, w).Occurrence != key || savedCheckpoint(t, w, p.JobUID).EventUID != "new-event" {
		t.Fatal("overwrote newer event")
	}
	// Equal-watermark recovery must not resurrect an occurrence prepared after the snapshot.
	old.EventUID = "new-event"
	old.Pending = []sched.Occurrence{{Key: key, At: at.Add(time.Minute)}}
	p.JobEventUID = "new-event"
	if err := o.Drop(p, "example-project", "worker", w.Config.Executor, "catchup_missed", "detail", "other-drop", old, old.Through); err != nil {
		t.Fatal(err)
	}
	tick(t, w, at.Add(2*time.Minute))
	if h.runs != 1 || len(savedCheckpoint(t, w, p.JobUID).Pending) != 0 {
		t.Fatal("resurrected prepared pending")
	}
}

type dropOnlyKata struct {
	*fakeKata
	notifications int
}

func (k *dropOnlyKata) Notify(string, string, string) error { k.notifications++; return nil }
func TestDroppedExistingIssueIsObservationOnly(t *testing.T) {
	w, h, k := fixture(t)
	k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "once", At: new(at.Format(time.RFC3339))}
	k.jobs[0].Definition.Catchup = "skip"
	k.jobs[0].Definition.GraceSeconds = new(int64(20))
	k.jobs[0].Definition.Issue = &kata.IssuePolicy{Kind: "existing", UID: new("issue")}
	k.ready["issue"] = true
	boundary := &dropOnlyKata{fakeKata: k}
	w.Clients["example-project"] = boundary
	tick(t, w, at.Add(-time.Minute))
	tick(t, w, at.Add(10*time.Minute))
	rows := droppedRows(t, w)
	if len(rows) != 1 || h.runs != 0 || k.creates != 0 || boundary.notifications != 0 || len(k.bodies) != 1 {
		t.Fatal(rows, h.runs, k.creates, boundary.notifications)
	}
	var b kata.ObserveCronRunBody
	_ = json.Unmarshal(rows[0].Body, &b)
	if b.IssueUID != nil || b.Status != "cancelled" || !strings.HasPrefix(*b.Summary.Message, "reason: catchup_missed\n") {
		t.Fatalf("body=%s", rows[0].Body)
	}
}

func TestDroppedCurrentGraceProtectsOlderOccupantPlan(t *testing.T) {
	w, h, k, r := heldOnce(t, 0)
	k.jobs[0].DefinitionEventUID = "new-event"
	k.jobs[0].Definition.GraceSeconds = new(int64(600))
	k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "once", At: new(at.Add(time.Minute).Format(time.RFC3339))}
	tick(t, w, at.Add(time.Minute))
	if err := os.WriteFile(w.Store.Layout.Dropped(), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	request(t, w, journal.Request{ID: "release", Kind: "release", Run: r.RunUID})
	_ = w.Tick(t.Context(), at.Add(12*time.Minute))
	saved, err := w.Store.Load(r.Key)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Phase == "released" {
		t.Fatal("used occupant's old zero grace to remove current gate")
	}
	if err = os.Remove(w.Store.Layout.Dropped()); err != nil {
		t.Fatal(err)
	}
	w = New(w.Config, w.Store, h, w.Clients, w.Bin)
	tick(t, w, at.Add(13*time.Minute))
	tick(t, w, at.Add(14*time.Minute))
	if h.runs != 1 || len(droppedRows(t, w)) != 1 {
		t.Fatal("replayed current definition's expired occurrence")
	}
}

func TestDroppedReadOutageDoesNotProtectUnaffectedRuns(t *testing.T) {
	for _, trigger := range []string{"manual", "issue-scheduled", "once-zero-grace", "once-allow"} {
		for _, mode := range []string{"release", "settlement"} {
			for _, failure := range []string{"workflows", "ready"} {
				t.Run(trigger+"/"+mode+"/"+failure, func(t *testing.T) {
					w, h, k := fixture(t)
					k.jobs[0].Definition.GraceSeconds = new(int64(600))
					if trigger == "issue-scheduled" {
						k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "issue-scheduled", IssueUID: new("issue")}
					} else if strings.HasPrefix(trigger, "once") {
						k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "once", At: new(at.Add(time.Hour).Format(time.RFC3339))}
					}
					if trigger == "once-zero-grace" {
						k.jobs[0].Definition.GraceSeconds = new(int64(0))
					}
					if trigger == "once-allow" {
						k.jobs[0].Definition.Overlap = "allow"
					}
					if failure == "ready" {
						k.jobs[0].Definition.Issue = &kata.IssuePolicy{Kind: "existing", UID: new("issue")}
						k.ready["issue"] = true
					}
					request(t, w, journal.Request{ID: "occupant", Kind: "run", JobUID: k.jobs[0].UID})
					tick(t, w, at)
					r := active(t, w)
					if mode == "release" {
						if err := w.Store.Update(r.Key, func(r *journal.Run) error { r.Phase = "held"; r.ReportDone = true; return nil }); err != nil {
							t.Fatal(err)
						}
						request(t, w, journal.Request{ID: "release", Kind: "release", Run: r.RunUID})
					} else {
						finishAgent(t, w, h, r)
					}
					k.failures = map[string]error{failure: errors.New("read unavailable")}
					for n := range 12 {
						_ = w.Tick(t.Context(), at.Add(time.Duration(n+1)*time.Second))
					}
					saved, err := w.Store.Load(r.Key)
					if err != nil {
						t.Fatal(err)
					}
					if mode == "release" && saved.Phase != "released" || mode == "settlement" && saved.Phase != "reported" {
						t.Fatal("unrelated read failure retained capacity", saved.Phase)
					}
					if len(droppedRows(t, w)) != 0 {
						t.Fatal("unaffected trigger produced drops")
					}
				})
			}
		}
	}
}
