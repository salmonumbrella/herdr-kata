package report

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"hegel.dev/go/hegel"
)

// Contract: final failures request attention before leaving the active run set.
// Removing the notification or moving it after Observe must fail these tests.
func finalFixture(t *testing.T, outcome string) (context.Context, *journal.Store, *fakeKata, journal.Run) {
	t.Helper()
	ctx, st, k, r := setup(t)
	if err := Admit(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	if err := st.Update(r.Key, func(r *journal.Run) error {
		r.Phase, r.Outcome = "settled", outcome
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	k.events, k.bodies = nil, nil
	return ctx, st, k, r
}

func TestFinalFailureAlerts(t *testing.T) {
	for _, tt := range []struct{ outcome, attempt, message string }{
		{"failed", "failed", "run failed: reason: failed; 01J00000000000000000000001"},
		{"unknown", "needs-human", "run unknown: reason: needs-human; 01J00000000000000000000001"},
		{"", "", "run unknown: reason: unknown; 01J00000000000000000000001"},
		{"succeeded", "succeeded", ""},
	} {
		t.Run(tt.outcome+tt.attempt, func(t *testing.T) {
			ctx, st, k, r := finalFixture(t, tt.outcome)
			if err := st.Update(r.Key, func(r *journal.Run) error {
				r.Attempts = []journal.Attempt{{Step: "main", Status: tt.attempt, Summary: "private output"}}
				r.Detail = "private detail"
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			k.onNotify = func() {
				saved, err := journal.New(st.Layout).Load(r.Key)
				if err != nil || !slices.Contains(saved.Notified, "settled") || len(saved.ReportBody) == 0 || saved.ReportDone {
					t.Fatalf("notification preceded durable intent/body: %+v %v", saved, err)
				}
			}
			if err := Final(ctx, k, st, r.Key); err != nil {
				t.Fatal(err)
			}
			if err := Final(ctx, k, journal.New(st.Layout), r.Key); err != nil {
				t.Fatal(err)
			}
			if tt.message == "" {
				if len(k.notifications) != 0 || !slices.Equal(k.events, []string{"observe"}) {
					t.Fatalf("success requested attention: %+v %v", k.notifications, k.events)
				}
			} else if !slices.Equal(k.notifications, []notification{{"abcd", "worker", tt.message}}) || !slices.Equal(k.events, []string{"notify", "observe"}) {
				t.Fatalf("attention request/order: %+v %v", k.notifications, k.events)
			}
			got, err := st.Load(r.Key)
			if err != nil || got.Phase != "reported" || !got.ReportDone || len(k.bodies) != 1 {
				t.Fatalf("final publication: %+v %v bodies=%d", got, err, len(k.bodies))
			}
		})
	}
}

func TestFinalAlertDeliveryErrors(t *testing.T) {
	for _, tt := range []struct {
		name      string
		err       error
		wantSends int
	}{
		{"unavailable", &kata.Error{Code: 7}, 2},
		{"uncertain", &kata.Error{Code: 1}, 1},
		{"unknown mutation", &kata.Error{Code: 7, Message: "mutation result may be unknown"}, 1},
		{"definitive", &kata.Error{Code: 3}, 1},
		{"conflict", &kata.Error{Code: 5}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, st, k, r := finalFixture(t, "failed")
			k.notifyErr = tt.err
			if err := Final(ctx, k, st, r.Key); err == nil {
				t.Fatal("notification error was hidden")
			}
			saved, err := st.Load(r.Key)
			if err != nil || saved.Phase != "settled" || len(k.bodies) != 0 {
				t.Fatalf("send failure changed settlement or observed early: %+v %v", saved, err)
			}
			k.notifyErr = nil
			if err := Final(WithNow(ctx, at.Add(time.Hour)), k, journal.New(st.Layout), r.Key); err != nil {
				t.Fatal(err)
			}
			got, _ := st.Load(r.Key)
			if len(k.notifications) != tt.wantSends || len(k.bodies) != 1 || got.Phase != "reported" || !bytes.Equal(saved.ReportBody, k.bodies[0]) {
				t.Fatalf("retry: notifications=%+v observations=%d phase=%s", k.notifications, len(k.bodies), got.Phase)
			}
		})
	}
}

func TestSettleAlertsExcludeEachOther(t *testing.T) {
	t.Run("final then refused observation then hold", func(t *testing.T) {
		ctx, st, k, r := finalFixture(t, "failed")
		k.observeErr = &kata.Error{Code: 3}
		if err := Final(ctx, k, st, r.Key); err == nil {
			t.Fatal("expected refused observation")
		}
		if err := Hold(ctx, k, journal.New(st.Layout), r.Key); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(k.notifications, []notification{{"abcd", "worker", "run failed: reason: failed; 01J00000000000000000000001"}}) {
			t.Fatalf("duplicate or replaced final attention: %+v", k.notifications)
		}
	})
	for _, intent := range []string{"held", "settled"} {
		t.Run("saved "+intent+" intent", func(t *testing.T) {
			ctx, st, k, r := finalFixture(t, "failed")
			if err := st.Update(r.Key, func(r *journal.Run) error { r.Notified = []string{intent}; return nil }); err != nil {
				t.Fatal(err)
			}
			if err := Final(ctx, k, journal.New(st.Layout), r.Key); err != nil {
				t.Fatal(err)
			}
			if len(k.notifications) != 0 || len(k.bodies) != 1 {
				t.Fatalf("saved intent resent: %+v observations=%d", k.notifications, len(k.bodies))
			}
		})
	}
}

func TestFinalAlertUsesFrozenReason(t *testing.T) {
	ctx, st, k, r := finalFixture(t, "unknown")
	if err := st.Update(r.Key, func(r *journal.Run) error {
		r.Attempts = []journal.Attempt{{Status: "needs-human"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	k.notifyErr = &kata.Error{Code: 7}
	if err := Final(ctx, k, st, r.Key); err == nil {
		t.Fatal("expected unavailable notification")
	}
	saved, _ := st.Load(r.Key)
	if err := st.Update(r.Key, func(r *journal.Run) error {
		r.Outcome, r.Detail = "succeeded", "changed after freeze"
		r.Attempts = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	k.notifyErr = nil
	if err := Final(ctx, k, journal.New(st.Layout), r.Key); err != nil {
		t.Fatal(err)
	}
	want := notification{"abcd", "worker", "run unknown: reason: needs-human; 01J00000000000000000000001"}
	if !slices.Equal(k.notifications, []notification{want, want}) || len(k.bodies) != 1 || !bytes.Equal(k.bodies[0], saved.ReportBody) {
		t.Fatalf("frozen alert/body changed: %+v observations=%q", k.notifications, k.bodies)
	}
}

func existingFixture(t *testing.T) (context.Context, *journal.Store, *fakeKata, journal.Run) {
	t.Helper()
	ctx, st, k, r := setup(t)
	if err := st.Update(r.Key, func(r *journal.Run) error {
		r.Plan.Name = "example-job"
		r.Plan.Issue.Kind, r.Plan.Issue.UID = "existing", "01J00000000000000000000030"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Admit(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	if err := st.Update(r.Key, func(r *journal.Run) error { r.Phase, r.Outcome = "settled", "failed"; return nil }); err != nil {
		t.Fatal(err)
	}
	r, err := st.Load(r.Key)
	if err != nil {
		t.Fatal(err)
	}
	k.events, k.bodies = nil, nil
	return ctx, st, k, r
}

func TestExistingFailureCarrier(t *testing.T) {
	ctx, st, k, r := existingFixture(t)
	if err := Final(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	want := createRequest{
		"cron alerts: 01J00000000000000000000010 2026-11-02",
		"herdr-kata requests attention here when a run of job 01J00000000000000000000010 settles failed or unknown in the 2026-11-02 UTC start-date group. Existing issues receive no adapter writes. Inspect runs with kata cron run list --job-uid 01J00000000000000000000010; close this issue when handled.",
		"cron-worker:01J00000000000000000000010:alert:2026-11-02", "",
	}
	if !slices.Equal(k.createRequests, []createRequest{want}) || !slices.Equal(k.notifications, []notification{{"abcd", "worker", "run failed: reason: failed; 01J00000000000000000000001"}}) {
		t.Fatalf("carrier request/notification: %+v %+v", k.createRequests, k.notifications)
	}
	got, _ := st.Load(r.Key)
	if got.IssueRef != "01J00000000000000000000030" || got.IssueUID != "01J00000000000000000000030" || *body(t, k.bodies[0]).IssueUID != "01J00000000000000000000030" {
		t.Fatalf("existing issue identity replaced: %+v %s", got, k.bodies[0])
	}
	// A renamed job can also attach a different existing issue within this day.
	r.Key, r.RunUID = journal.ManualKey("executor", "example-project", "second"), "01J00000000000000000000002"
	r.Plan.Name, r.Plan.Issue.UID = "renamed-job", "01J00000000000000000000031"
	r.IssueRef, r.IssueUID = r.Plan.Issue.UID, r.Plan.Issue.UID
	r.StartedAt = new(at.Add(time.Hour))
	if err := st.Create(r); err != nil {
		t.Fatal(err)
	}
	if err := Final(WithNow(ctx, at.Add(2*time.Hour)), k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(k.createRequests, []createRequest{want, want}) || len(k.notifications) != 2 || k.notifications[1] != (notification{"abcd", "worker", "run failed: reason: failed; 01J00000000000000000000002"}) {
		t.Fatalf("same-day carrier changed after edit: %+v %+v", k.createRequests, k.notifications)
	}
}

func TestCarrierUsesUTCStartDate(t *testing.T) {
	ctx, st, k, r := existingFixture(t)
	start := time.Date(2026, 11, 2, 0, 30, 0, 0, time.FixedZone("fixture", 14*60*60))
	if err := st.Update(r.Key, func(r *journal.Run) error { r.StartedAt = &start; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := Final(WithNow(ctx, start.Add(25*time.Hour)), k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	if len(k.createRequests) != 1 || k.createRequests[0].key != "cron-worker:01J00000000000000000000010:alert:2026-11-01" {
		t.Fatalf("carrier used local/end date: %+v", k.createRequests)
	}
	r.Key, r.RunUID, r.StartedAt = journal.ManualKey("executor", "example-project", "tomorrow"), "01J00000000000000000000002", new(start.Add(24*time.Hour))
	if err := st.Create(r); err != nil {
		t.Fatal(err)
	}
	if err := Final(WithNow(ctx, start.Add(25*time.Hour)), k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	if len(k.createRequests) != 2 || k.createRequests[1].key != "cron-worker:01J00000000000000000000010:alert:2026-11-02" {
		t.Fatalf("next start-date reused old carrier: %+v", k.createRequests)
	}
}

func TestExistingBlockedStaysSilent(t *testing.T) {
	ctx, st, k, r := existingFixture(t)
	if err := NotifyOnce(ctx, k, st, r.Key, "blocked:main:1:1", "blocked"); err != nil {
		t.Fatal(err)
	}
	if len(k.createRequests) != 0 || len(k.notifications) != 0 {
		t.Fatalf("existing blocked spell created attention: %+v %+v", k.createRequests, k.notifications)
	}
}

func TestCarrierCreateErrors(t *testing.T) {
	for _, tt := range []struct {
		name  string
		err   error
		retry bool
	}{
		{"unavailable", &kata.Error{Code: 7}, true},
		{"uncertain", &kata.Error{Code: 1}, true},
		{"unknown mutation", &kata.Error{Code: 7, Message: "mutation result may be unknown"}, true},
		{"definitive", &kata.Error{Code: 3}, false},
		{"conflict", &kata.Error{Code: 5}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, st, k, r := existingFixture(t)
			k.createErr = tt.err
			if err := Final(ctx, k, st, r.Key); err == nil {
				t.Fatal("carrier error was hidden")
			}
			saved, err := st.Load(r.Key)
			if err != nil || saved.Phase != "settled" || slices.Contains(saved.Notified, "settled") == tt.retry || len(k.notifications) != 0 || len(k.bodies) != 0 {
				t.Fatalf("carrier failure intent/progression: %+v %v", saved, err)
			}
			k.createErr = nil
			if err := Final(ctx, k, journal.New(st.Layout), r.Key); err != nil {
				t.Fatal(err)
			}
			creates, sends := 1, 0
			if tt.retry {
				creates, sends = 2, 1
			}
			if len(k.createRequests) != creates || len(k.notifications) != sends || len(k.bodies) != 1 {
				t.Fatalf("carrier replay: creates=%+v sends=%+v observations=%d", k.createRequests, k.notifications, len(k.bodies))
			}
			if tt.retry && k.createRequests[0] != k.createRequests[1] {
				t.Fatal("carrier retry changed bytes")
			}
		})
	}
}

func TestCarrierMissingIdentity(t *testing.T) {
	for _, missing := range []string{"job UID", "carrier ref"} {
		t.Run(missing, func(t *testing.T) {
			ctx, st, k, r := existingFixture(t)
			creates := 1
			if missing == "job UID" {
				creates = 0
				if err := st.Update(r.Key, func(r *journal.Run) error { r.Plan.JobUID = ""; return nil }); err != nil {
					t.Fatal(err)
				}
			} else {
				k.omitCreateRef = true
			}
			if err := Final(ctx, k, st, r.Key); err == nil {
				t.Fatal("missing carrier identity was silently accepted")
			}
			if err := Final(ctx, k, journal.New(st.Layout), r.Key); err != nil {
				t.Fatal(err)
			}
			got, _ := st.Load(r.Key)
			if len(k.notifications) != 0 || len(k.createRequests) != creates || len(k.bodies) != 1 || got.Phase != "reported" {
				t.Fatalf("missing identity sent attention or blocked publication: creates=%d sends=%+v phase=%s", len(k.createRequests), k.notifications, got.Phase)
			}
		})
	}
}

func TestCarrierCreateHorizon(t *testing.T) {
	for _, age := range []time.Duration{6*24*time.Hour - time.Nanosecond, 6 * 24 * time.Hour, 8 * 24 * time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			ctx, st, k, r := existingFixture(t)
			ctx = WithNow(ctx, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC).Add(age))
			err := Final(ctx, k, st, r.Key)
			if age < 6*24*time.Hour {
				if err != nil || len(k.createRequests) != 1 || len(k.notifications) != 1 {
					t.Fatalf("inside cutoff: %v %+v", err, k)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "expired") {
				t.Fatalf("missing expiry diagnostic: %v", err)
			}
			if err := Final(ctx, k, journal.New(st.Layout), r.Key); err != nil {
				t.Fatal(err)
			}
			got, _ := st.Load(r.Key)
			if len(k.createRequests) != 0 || len(k.notifications) != 0 || len(k.bodies) != 1 || got.Phase != "reported" {
				t.Fatalf("expired alert blocked reporting: %+v %+v", got, k)
			}
		})
	}
	for _, lost := range []string{"create", "notify"} {
		t.Run("lost "+lost, func(t *testing.T) {
			ctx, st, k, r := existingFixture(t)
			if lost == "create" {
				k.loseCreateReply = true
			} else {
				k.notifyErr = &kata.Error{Code: 7}
			}
			if err := Final(ctx, k, st, r.Key); err == nil {
				t.Fatal("expected lost result")
			}
			k.notifyErr = nil
			if err := Final(WithNow(ctx, at.Add(time.Hour)), k, journal.New(st.Layout), r.Key); err != nil {
				t.Fatal(err)
			}
			if len(k.createRequests) != 2 || k.createRequests[0] != k.createRequests[1] || len(k.bodies) != 1 {
				t.Fatalf("safe replay changed request: %+v", k)
			}
		})
		t.Run("lost "+lost+" then expiry", func(t *testing.T) {
			ctx, st, k, r := existingFixture(t)
			if lost == "create" {
				k.loseCreateReply = true
			} else {
				k.notifyErr = &kata.Error{Code: 7}
			}
			if err := Final(ctx, k, st, r.Key); err == nil {
				t.Fatal("expected lost result")
			}
			ctx = WithNow(ctx, at.Add(6*24*time.Hour))
			if err := Final(ctx, k, journal.New(st.Layout), r.Key); err == nil {
				t.Fatal("expected expiry diagnostic")
			}
			if err := Final(ctx, k, journal.New(st.Layout), r.Key); err != nil {
				t.Fatal(err)
			}
			if len(k.createRequests) != 1 || len(k.bodies) != 1 {
				t.Fatalf("expired key recreated carrier: %+v", k)
			}
		})
	}
}

func TestCarrierRejectsStaleTickBeforeDailyKeyExpires(t *testing.T) {
	ctx, st, k, r := existingFixture(t)
	start := time.Date(2026, 11, 2, 23, 59, 58, 0, time.UTC)
	if err := st.Update(r.Key, func(r *journal.Run) error { r.StartedAt = &start; return nil }); err != nil {
		t.Fatal(err)
	}
	k.notifyErr = &kata.Error{Code: 7}
	if err := Final(WithNow(ctx, start), k, st, r.Key); err == nil {
		t.Fatal("expected unavailable notification")
	}
	// The earliest peer may have created this group's carrier just after UTC
	// midnight. Even a late-starting run must leave a full day's replay margin.
	tickTime := time.Date(2026, 11, 7, 23, 59, 57, 0, time.UTC)
	serviceTime := time.Date(2026, 11, 8, 0, 0, 2, 0, time.UTC)
	ctx = WithCurrentTime(WithNow(ctx, tickTime), func() time.Time { return serviceTime })
	k.notifyErr = nil
	if err := Final(ctx, k, journal.New(st.Layout), r.Key); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("stale tick reopened daily carrier: %v", err)
	}
	if err := Final(ctx, k, journal.New(st.Layout), r.Key); err != nil {
		t.Fatal(err)
	}
	if len(k.createRequests) != 1 || len(k.notifications) != 1 || len(k.bodies) != 1 {
		t.Fatalf("expired group recreated or final reporting stopped: %+v", k)
	}
}

func TestUndatedExistingHold(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(map[bool]string{false: "admission", true: "already refused"}[refused], func(t *testing.T) {
			ctx, st, k, r := setup(t)
			if err := st.Update(r.Key, func(r *journal.Run) error {
				r.Plan.Issue.Kind, r.Plan.Issue.UID = "existing", "01J00000000000000000000030"
				r.StartedAt = nil
				if refused {
					r.Phase, r.Hold, r.ReportRefused = "held", "report_refused", true
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if !refused {
				if err := Admit(ctx, k, st, r.Key); err == nil {
					t.Fatal("undated admission accepted")
				}
			}
			if err := Hold(ctx, k, st, r.Key); err == nil || !strings.Contains(err.Error(), "start") {
				t.Fatalf("missing undated diagnostic: %v", err)
			}
			if err := Hold(WithNow(ctx, at.Add(24*time.Hour)), k, journal.New(st.Layout), r.Key); err != nil {
				t.Fatal(err)
			}
			got, _ := st.Load(r.Key)
			if got.Phase != "held" || got.Hold != "report_refused" || len(k.createRequests) != 0 || len(k.notifications) != 0 || !slices.Contains(got.Notified, "held") {
				t.Fatalf("undated hold changed: %+v %+v", got, k)
			}
		})
	}
}

func TestCarrierPayloadIndependentOfMutableJobText(t *testing.T) {
	ctx, st, k, r := existingFixture(t)
	if err := Final(ctx, k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	if len(k.createRequests) != 1 {
		t.Fatal("missing carrier")
	}
	want := k.createRequests[0]
	hegel.Test(t, func(ht *hegel.T) {
		name, target := hegel.Draw(ht, hegel.Text()), hegel.Draw(ht, hegel.Text())
		if err := st.Update(r.Key, func(r *journal.Run) error {
			r.Phase, r.ReportDone, r.ReportBody, r.Notified = "settled", false, nil, nil
			r.Plan.Name, r.Plan.Issue.UID = name, target
			return nil
		}); err != nil {
			ht.Fatal(err)
		}
		k.createRequests, k.notifications = nil, nil
		if err := Final(ctx, k, journal.New(st.Layout), r.Key); err != nil {
			ht.Fatal(err)
		}
		if !slices.Equal(k.createRequests, []createRequest{want}) || !slices.Equal(k.notifications, []notification{{"abcd", "worker", "run failed: reason: failed; 01J00000000000000000000001"}}) {
			ht.Fatalf("mutable text changed carrier: %+v %+v", k.createRequests, k.notifications)
		}
	})
}

func TestFinalAlertReasonIndependentOfDetails(t *testing.T) {
	ctx, st, k, r := finalFixture(t, "failed")
	hegel.Test(t, func(ht *hegel.T) {
		detail, summary := hegel.Draw(ht, hegel.Text()), hegel.Draw(ht, hegel.Text())
		if err := st.Update(r.Key, func(r *journal.Run) error {
			r.Phase, r.ReportDone, r.ReportBody, r.Notified = "settled", false, nil, nil
			r.Detail = detail
			r.Attempts = []journal.Attempt{{Step: "main", Status: "failed", Summary: summary}}
			return nil
		}); err != nil {
			ht.Fatal(err)
		}
		k.notifications = nil
		if err := Final(ctx, k, journal.New(st.Layout), r.Key); err != nil {
			ht.Fatal(err)
		}
		if !slices.Equal(k.notifications, []notification{{"abcd", "worker", "run failed: reason: failed; 01J00000000000000000000001"}}) {
			ht.Fatalf("detail leaked into attention: %+v", k.notifications)
		}
	})
}

// Contract: after any potentially effective send, a restart cannot send the
// other settlement intent. Only known unavailability leaves delivery open.
func TestSettleIntentSurvivesSequences(t *testing.T) {
	for _, policy := range []string{"per-run", "existing"} {
		t.Run(policy, func(t *testing.T) {
			ctx, st, k, r := finalFixture(t, "failed")
			hegel.Test(t, func(ht *hegel.T) {
				actor, prefix := hegel.Draw(ht, hegel.Text()), hegel.Draw(ht, hegel.Text())
				sequence := hegel.Draw(ht, hegel.Lists(hegel.Integers(0, 11)))
				// Bound filesystem work when materializing a generated sequence.
				sequence = append(sequence[:min(len(sequence), 16)], 0, 1)
				if err := st.Update(r.Key, func(r *journal.Run) error {
					r.Plan.Issue.Kind, r.Actor = policy, actor
					r.Notified = []string{"blocked:main:1:1"}
					return nil
				}); err != nil {
					ht.Fatal(err)
				}
				*k = fakeKata{}
				consumed := ""
				var want []notification
				for _, choice := range sequence {
					id := []string{"held", "settled"}[choice%2]
					result := choice / 2
					k.notifyErr = nil
					if result > 0 {
						code := []int{0, 7, 1, 3, 5, 7}[result]
						message := ""
						if result == 5 {
							message = "mutation result may be unknown"
						}
						k.notifyErr = fmt.Errorf("%s: %w", prefix, &kata.Error{Code: code, Message: message})
					}
					if consumed == "" {
						want = append(want, notification{"abcd", actor, id})
					}
					k.onNotify = func() {
						saved, err := journal.New(st.Layout).Load(r.Key)
						if err != nil || !slices.Contains(saved.Notified, id) {
							ht.Fatalf("send without durable intent: %+v %v", saved, err)
						}
					}
					err := NotifyOnce(ctx, k, journal.New(st.Layout), r.Key, id, id)
					if (err != nil) != (consumed == "" && result > 0) {
						ht.Fatalf("wrong error for result %d with consumed %q: %v", result, consumed, err)
					}
					if consumed == "" && result != 1 {
						consumed = id
					}
					saved, err := st.Load(r.Key)
					ids := []string{"blocked:main:1:1"}
					if consumed != "" {
						ids = append(ids, consumed)
					}
					if err != nil || !slices.Equal(saved.Notified, ids) || !slices.Equal(k.notifications, want) {
						ht.Fatalf("replay changed intent/delivery: saved=%+v sends=%+v want=%+v err=%v", saved.Notified, k.notifications, want, err)
					}
				}
			})
		})
	}
}

func TestCarrierEquivalentUTCRepresentations(t *testing.T) {
	ctx, st, k, r := existingFixture(t)
	hegel.Test(t, func(ht *hegel.T) {
		seconds := hegel.Draw(ht, hegel.Integers[int64](-62135510400, 253402214399))
		// Journal timestamps use RFC3339, whose offsets have minute precision.
		offsetMinutes := hegel.Draw(ht, hegel.Integers(-1439, 1439))
		start := time.Unix(seconds, 0).UTC()
		*k = fakeKata{}
		for _, represented := range []time.Time{start, start.In(time.FixedZone("fixture", offsetMinutes*60))} {
			if err := st.Update(r.Key, func(r *journal.Run) error {
				r.StartedAt, r.ReportBody, r.ReportDone, r.Notified = &represented, nil, false, nil
				return nil
			}); err != nil {
				ht.Fatal(err)
			}
			if err := Final(WithNow(ctx, start), k, journal.New(st.Layout), r.Key); err != nil {
				ht.Fatal(err)
			}
		}
		if len(k.createRequests) != 2 || k.createRequests[0] != k.createRequests[1] || len(k.notifications) != 2 {
			ht.Fatalf("equivalent instant changed carrier: %+v %+v", k.createRequests, k.notifications)
		}
	}, hegel.WithTestCases(1000))
}

func TestCarrierCutoffAcrossRestart(t *testing.T) {
	ctx, st, k, r := existingFixture(t)
	hegel.Test(t, func(ht *hegel.T) {
		// Fixture starts at 09:00 UTC, leaving 135 hours until its group cutoff.
		const window = 135 * time.Hour
		age := time.Duration(hegel.Draw(ht, hegel.OneOf(
			hegel.Integers[int64](0, math.MaxInt64),
			hegel.SampledFrom([]int64{int64(window - time.Nanosecond), int64(window), int64(window + time.Nanosecond)}),
		)))
		if err := st.Update(r.Key, func(r *journal.Run) error {
			r.Phase, r.ReportBody, r.ReportDone, r.Notified = "settled", nil, false, nil
			return nil
		}); err != nil {
			ht.Fatal(err)
		}
		*k = fakeKata{loseCreateReply: true}
		if err := Final(ctx, k, st, r.Key); err == nil {
			ht.Fatal("expected lost create result")
		}
		err := Final(WithNow(ctx, at.Add(age)), k, journal.New(st.Layout), r.Key)
		if age < window {
			if err != nil || len(k.createRequests) != 2 || k.createRequests[0] != k.createRequests[1] || len(k.notifications) != 1 {
				ht.Fatalf("retry inside window: %v %+v", err, k)
			}
		} else {
			if err == nil || !strings.Contains(err.Error(), "expired") || len(k.createRequests) != 1 || len(k.notifications) != 0 {
				ht.Fatalf("expired create replay: %v %+v", err, k)
			}
			if err := Final(WithNow(ctx, at.Add(age)), k, journal.New(st.Layout), r.Key); err != nil {
				ht.Fatal(err)
			}
		}
		saved, err := st.Load(r.Key)
		if err != nil || saved.Phase != "reported" || len(k.bodies) != 1 {
			ht.Fatalf("carrier recovery stopped final publication: %+v %v", saved, err)
		}
	})
}

func TestFrozenFinalSummaryBounded(t *testing.T) {
	ctx, st, k, r := finalFixture(t, "failed")
	hegel.Test(t, func(ht *hegel.T) {
		text := hegel.Draw(ht, hegel.Text()) + "界🙂"
		// Exercise multibyte boundaries and aggregate output beyond the byte cap.
		text = strings.Repeat(text, 1+4097/len(text))
		if err := st.Update(r.Key, func(r *journal.Run) error {
			r.Phase, r.ReportBody, r.ReportDone, r.Notified = "settled", nil, false, nil
			r.Detail = text
			r.Attempts = []journal.Attempt{{Step: "main", Summary: text, Note: text}}
			return nil
		}); err != nil {
			ht.Fatal(err)
		}
		*k = fakeKata{observeErr: &kata.Error{Code: 7}}
		if err := Final(ctx, k, st, r.Key); err == nil {
			ht.Fatal("expected unavailable observation")
		}
		first := bytes.Clone(k.bodies[0])
		message := body(t, first).Summary.Message
		if message == nil || !utf8.ValidString(*message) || len(*message) > 4096 || !strings.HasPrefix(*message, "reason: failed\n") {
			ht.Fatalf("unbounded or displaced failure reason: %v", message)
		}
		k.observeErr = nil
		if err := Final(WithNow(ctx, at.Add(48*time.Hour)), k, journal.New(st.Layout), r.Key); err != nil {
			ht.Fatal(err)
		}
		if len(k.bodies) != 2 || !bytes.Equal(first, k.bodies[1]) || !slices.Equal(k.notifications, []notification{{"abcd", "worker", "run failed: reason: failed; 01J00000000000000000000001"}}) {
			ht.Fatalf("frozen evidence changed on replay: %+v %q", k.notifications, k.bodies)
		}
	})
}

func TestHeldUnavailableAfterReportDone(t *testing.T) {
	ctx, st, k, r := existingFixture(t)
	hegel.Test(t, func(ht *hegel.T) {
		reason, address := hegel.Draw(ht, hegel.Text()), hegel.Draw(ht, hegel.Text())
		if err := st.Update(r.Key, func(r *journal.Run) error {
			r.Phase, r.Hold, r.ReportBody, r.ReportDone, r.Notified = "held", reason, nil, false, nil
			r.Attempts = []journal.Attempt{{Address: address}}
			return nil
		}); err != nil {
			ht.Fatal(err)
		}
		*k = fakeKata{notifyErr: &kata.Error{Code: 7}}
		if err := Hold(ctx, k, st, r.Key); err == nil {
			ht.Fatal("expected unavailable notification")
		}
		saved, err := st.Load(r.Key)
		if err != nil || !saved.ReportDone || saved.Phase != "held" || len(saved.Notified) != 0 || !slices.Equal(k.events, []string{"observe", "notify"}) {
			ht.Fatalf("hold publication/delivery order: %+v %v %+v", saved, err, k.events)
		}
		k.notifyErr = nil
		if err := Hold(ctx, k, journal.New(st.Layout), r.Key); err != nil {
			ht.Fatal(err)
		}
		want := notification{"abcd", "worker", "held: " + reason + "; hc " + address}
		if len(k.bodies) != 1 || !slices.Equal(k.notifications, []notification{want, want}) {
			ht.Fatalf("completed hold lost notification or reobserved: %+v %q", k.notifications, k.bodies)
		}
	})
}

func TestRejectedAlertsHaveNoSideEffects(t *testing.T) {
	ctx, st, k, r := existingFixture(t)
	hegel.Test(t, func(ht *hegel.T) {
		text := hegel.Draw(ht, hegel.Text())
		if err := st.Update(r.Key, func(r *journal.Run) error {
			r.ReportBody, r.ReportDone, r.Notified = []byte("!"+text), false, nil
			return nil
		}); err != nil {
			ht.Fatal(err)
		}
		*k = fakeKata{}
		if err := Final(ctx, k, st, r.Key); err == nil {
			ht.Fatal("malformed frozen body accepted")
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		for _, call := range []func() error{
			func() error { return Final(cancelled, k, st, r.Key) },
			func() error { return Hold(cancelled, k, st, r.Key) },
			func() error { return NotifyOnce(cancelled, k, st, r.Key, "settled", text) },
		} {
			if err := call(); err != context.Canceled {
				ht.Fatalf("cancelled entry: %v", err)
			}
		}
		if len(k.createRequests) != 0 || len(k.notifications) != 0 || len(k.bodies) != 0 {
			ht.Fatalf("rejected request caused side effects: %+v", k)
		}
	}, hegel.WithTestCases(1000))
}

func TestIntentSaveFailurePreventsSend(t *testing.T) {
	for _, policy := range []string{"per-run", "existing"} {
		t.Run(policy, func(t *testing.T) {
			ctx, st, k, r := finalFixture(t, "failed")
			dir := st.Layout.Run(r.Key)
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			hegel.Test(t, func(ht *hegel.T) {
				message := hegel.Draw(ht, hegel.Text())
				id := hegel.Draw(ht, hegel.SampledFrom([]string{"held", "settled"}))
				if err := st.Update(r.Key, func(r *journal.Run) error {
					r.Plan.Issue.Kind, r.Notified = policy, nil
					return nil
				}); err != nil {
					ht.Fatal(err)
				}
				*k = fakeKata{}
				if err := os.Chmod(dir, 0o500); err != nil {
					ht.Fatal(err)
				}
				err := NotifyOnce(ctx, k, st, r.Key, id, message)
				if restore := os.Chmod(dir, 0o700); restore != nil {
					ht.Fatal(restore)
				}
				if err == nil || len(k.notifications) != 0 {
					ht.Fatalf("failed intent persistence allowed send: %v %+v", err, k.notifications)
				}
			})
		})
	}
}
