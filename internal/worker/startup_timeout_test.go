package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/attempt"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type stopKata struct {
	*fakeKata
	failFinal  bool
	writes     int
	finals     [][]byte
	createKeys []string
	notifyRefs []string
}

// CreateIssue records idempotency keys so the test can tell the daily failure
// alert carrier apart from a per-run or existing-issue write.
func (k *stopKata) CreateIssue(title, body, key, parent string) (string, string, error) {
	k.createKeys = append(k.createKeys, key)
	return k.fakeKata.CreateIssue(title, body, key, parent)
}
func (k *stopKata) Notify(ref, _, _ string) error {
	k.notifyRefs = append(k.notifyRefs, ref)
	return nil
}
func (k *stopKata) SetDeadline(string, time.Time) error { k.writes++; return nil }

// onlyAlertCarrier reports whether every create was the failure-alert carrier
// and every notification went to that carrier, never to the existing issue.
func (k *stopKata) onlyAlertCarrier(existingRef string) bool {
	for _, key := range k.createKeys {
		if !strings.Contains(key, ":alert:") {
			return false
		}
	}
	for _, ref := range k.notifyRefs {
		if ref == existingRef || len(k.createKeys) == 0 {
			return false
		}
	}
	return len(k.createKeys) == k.creates
}
func (k *stopKata) Observe(uid string, b []byte, rev int) (int, bool, error) {
	var body kata.ObserveCronRunBody
	if err := json.Unmarshal(b, &body); err != nil {
		return 0, false, err
	}
	if body.Status == "failed" {
		k.finals = append(k.finals, bytes.Clone(b))
		if k.failFinal {
			return 0, false, &kata.Error{Code: 7}
		}
	}
	return k.fakeKata.Observe(uid, b, rev)
}

type stopHC struct{ *fakeHC }

func (h *stopHC) Read(_ context.Context, id string, lines int) (string, bool, error) {
	if _, ok := h.terms[id]; !ok || lines != 200 {
		return "", false, fmt.Errorf("wrong screen request %s %d", id, lines)
	}
	return "Review hooks before starting", true, nil
}

func TestStopFailureFreesOverlapAcrossRestartAndReportRetry(t *testing.T) {
	for _, reason := range []string{"agent_not_started", "timeout"} {
		for _, restart := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restart=%t", reason, restart), func(t *testing.T) {
				w, h, base := fixture(t)
				k := &stopKata{fakeKata: base}
				base.jobs[0].Definition.Issue = &kata.IssuePolicy{Kind: "existing", UID: new("01J00000000000000000000020")}
				base.ready["01J00000000000000000000020"] = true
				w.HC = &stopHC{h}
				w.Clients["example-project"] = k
				request(t, w, journal.Request{ID: "first", Kind: "run", JobUID: k.jobs[0].UID})
				tick(t, w, at)
				r := active(t, w)
				// A frozen workflow with retry and independent follow-on work must
				// still end the entire run after a known stop.
				if err := w.Store.Update(r.Key, func(r *journal.Run) error {
					r.Plan.Steps[0].Retries = 3
					r.Plan.Steps = append(r.Plan.Steps, defs.Step{Key: "later", Kind: "prompt", Text: "Later task"})
					if reason == "timeout" {
						r.ExpiresAt = at.Add(2 * time.Minute)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				tick(t, w, at.Add(2*time.Minute))
				if restart {
					for range 5 {
						out, err := attempt.Settle(context.Background(), w.HC, w.Store, r.Key, 0, at.Add(130*time.Second))
						if err != nil {
							t.Fatal(err)
						}
						if _, ok := out.(attempt.Done); ok {
							break
						}
					}
					w = New(w.Config, journal.New(w.Store.Layout), w.HC, w.Clients, w.Bin)
				}
				k.failFinal = true
				if err := w.Tick(context.Background(), at.Add(130*time.Second)); err == nil {
					t.Fatal("expected transient final-report failure")
				}
				saved, err := w.Store.Load(r.Key)
				if err != nil || saved.Phase != "settled" || saved.Outcome != "failed" || saved.EndedAt == nil || len(h.terms) != 0 || h.runs != 1 {
					t.Fatalf("stop did not locally settle: %+v %v runs=%d terms=%v", saved, err, h.runs, h.terms)
				}
				var obs kata.ObserveCronRunBody
				if err := json.Unmarshal(saved.ReportBody, &obs); err != nil {
					t.Fatal(err)
				}
				if obs.Status != "failed" || obs.Summary.Message == nil || *obs.Summary.Message != "reason: "+reason {
					t.Fatalf("wrong stop report %s", saved.ReportBody)
				}
				request(t, w, journal.Request{ID: "second", Kind: "run", JobUID: k.jobs[0].UID})
				_ = w.Tick(context.Background(), at.Add(131*time.Second))
				if h.runs != 2 {
					t.Fatalf("settled run held capacity during report retry: %d", h.runs)
				}
				k.failFinal = false
				tick(t, w, at.Add(132*time.Second))
				saved, err = w.Store.Load(r.Key)
				if err != nil || saved.Phase != "reported" || len(k.finals) < 2 || !bytes.Equal(k.finals[0], k.finals[len(k.finals)-1]) || k.writes != 0 || !k.onlyAlertCarrier(saved.IssueRef) {
					t.Fatalf("report retry or existing issue writes: %+v %v finals=%d creates=%v notifies=%v writes=%d", saved, err, len(k.finals), k.createKeys, k.notifyRefs, k.writes)
				}
			})
		}
	}
}

// The same failed stop must release the scheduled occurrence gate as well.
func TestStopFailureAllowsNextScheduledOccurrence(t *testing.T) {
	w, h, k := fixture(t)
	w.HC = &stopHC{h}
	k.jobs[0].Definition.Trigger = kata.Trigger{Kind: "cron", Cron: new("*/5 * * * *"), Timezone: new("UTC")}
	k.jobs[0].CreatedAt = at.Add(-time.Hour)
	tick(t, w, at.Add(-time.Second))
	tick(t, w, at)
	r := active(t, w)
	tick(t, w, at.Add(120*time.Second))
	tick(t, w, at.Add(130*time.Second))
	saved, err := w.Store.Load(r.Key)
	if err != nil || saved.Phase != "reported" || saved.Outcome != "failed" {
		t.Fatalf("scheduled stop: %+v %v", saved, err)
	}
	tick(t, w, at.Add(5*time.Minute))
	if h.runs != 2 {
		t.Fatalf("next occurrence did not proceed: %d", h.runs)
	}
}
