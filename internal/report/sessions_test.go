package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/session"
)

func recorded(agent, id string) *session.Session {
	return &session.Session{Agent: agent, SessionID: id}
}

// Contract: past attempts precede current plan order, and the first exact pair wins.
func TestRunSessionsOrderAndDedup(t *testing.T) {
	for _, outcome := range []string{"succeeded", "held"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, st, k, r := setup(t)
			if err := st.Update(r.Key, func(r *journal.Run) error {
				r.Phase, r.Outcome, r.EndedAt = "settled", outcome, new(at)
				if outcome == "held" {
					r.Phase, r.Hold, r.Outcome = "held", "launch_ambiguous", "unknown"
				}
				r.Plan.Steps = []defs.Step{{Key: "first", Kind: "prompt", Retries: 1}, {Key: "second", Kind: "prompt", Retries: 1}, {Key: "cmd", Kind: "command"}}
				r.Attempts = []journal.Attempt{
					{Step: "second", N: 1, Status: "failed", LaunchedAt: at, Session: recorded("pi", "same")},
					{Step: "first", N: 1, Status: "failed", LaunchedAt: at, Session: recorded("pi", "same")},
					{Step: "first", N: 2, Status: "succeeded", LaunchedAt: at, Session: recorded("other", "same")},
					{Step: "cmd", N: 1, Nonce: "command", LaunchedAt: at, TaskStarted: new(false)},
					{Step: "second", N: 2, Status: "succeeded", LaunchedAt: at, Session: recorded("pi", "last")},
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			publish := Final
			if outcome == "held" {
				publish = Hold
			}
			if err := publish(WithRunSessions(ctx, true), k, st, r.Key); err != nil {
				t.Fatal(err)
			}
			s := body(t, k.bodies[0]).Summary
			want := []kata.SummarySession{{Session: *recorded("pi", "same"), Step: "second"}, {Session: *recorded("other", "same"), Step: "first"}, {Session: *recorded("pi", "last"), Step: "second"}}
			if !reflect.DeepEqual(s.Sessions, want) || s.SessionsIncomplete || len(s.Steps) != 0 {
				t.Fatalf("sessions: %+v", s)
			}
		})
	}
}

// Contract: recorded launch evidence without a valid identity makes cost a floor.
func TestRunSessionsIncompleteLaunchEvidence(t *testing.T) {
	for _, tt := range []struct {
		name    string
		kind    string
		attempt journal.Attempt
		want    bool
	}{
		{"unlaunched", "prompt", journal.Attempt{}, false},
		{"launched", "prompt", journal.Attempt{Nonce: "launch", LaunchedAt: at, TaskStarted: new(false)}, true},
		{"recovered", "prompt", journal.Attempt{LaunchedAt: at, TaskStarted: new(false)}, true},
		{"command-launched", "command", journal.Attempt{Nonce: "launch", LaunchedAt: at, TaskStarted: new(false)}, false},
	} {
		for _, hold := range []string{"", "launch_ambiguous"} {
			t.Run(tt.name+"/"+hold, func(t *testing.T) {
				ctx, st, k, r := setup(t)
				if err := st.Update(r.Key, func(r *journal.Run) error {
					r.Phase, r.Outcome, r.EndedAt = "settled", "succeeded", new(at)
					r.Plan.Steps = []defs.Step{{Key: "main", Kind: tt.kind}}
					a := tt.attempt
					a.Step, a.N = "main", 1
					r.Attempts = []journal.Attempt{a}
					if hold != "" {
						r.Phase, r.Hold, r.Outcome = "held", hold, "unknown"
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				publish := Final
				if hold != "" {
					publish = Hold
				}
				if err := publish(WithRunSessions(ctx, true), k, st, r.Key); err != nil {
					t.Fatal(err)
				}
				s := body(t, k.bodies[0]).Summary
				if s.SessionsIncomplete != tt.want || len(s.Sessions) != 0 {
					t.Fatalf("summary %+v", s)
				}
			})
		}
	}
}

// Contract: only distinct identities over the cap mark the report incomplete.
func TestRunSessionsCap(t *testing.T) {
	for _, distinct := range []bool{false, true} {
		t.Run(fmt.Sprint(distinct), func(t *testing.T) {
			ctx, st, k, r := setup(t)
			if err := st.Update(r.Key, func(r *journal.Run) error {
				r.Phase, r.Outcome, r.EndedAt = "settled", "succeeded", new(at)
				r.Plan.Steps = []defs.Step{{Key: "main", Kind: "prompt", Retries: 64}}
				for i := range 65 {
					id := i
					if i == 64 && !distinct {
						id = 0
					}
					status := "failed"
					if i == 64 {
						status = "succeeded"
					}
					r.Attempts = append(r.Attempts, journal.Attempt{Step: "main", N: i + 1, Status: status, LaunchedAt: at, Session: recorded("pi", fmt.Sprintf("session-%02d", id))})
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := Final(WithRunSessions(ctx, true), k, st, r.Key); err != nil {
				t.Fatal(err)
			}
			s := body(t, k.bodies[0]).Summary
			if len(s.Sessions) != 64 || s.SessionsIncomplete != distinct {
				t.Fatal(s)
			}
		})
	}
}

type stagedSessionObserver struct {
	*fakeKata
	t    *testing.T
	st   *journal.Store
	key  string
	errs []error
}

func (k *stagedSessionObserver) Observe(uid string, b []byte, rev int) (int, bool, error) {
	k.t.Helper()
	saved, err := journal.New(k.st.Layout).Load(k.key)
	if err != nil || !bytes.Equal(saved.ReportBody, b) {
		k.t.Fatal("send preceded durable save", err)
	}
	k.bodies = append(k.bodies, bytes.Clone(b))
	if len(k.bodies) <= len(k.errs) {
		return 0, false, k.errs[len(k.bodies)-1]
	}
	return rev + 1, false, nil
}

// Contract: structured refusals remove sessions first and persist before retry.
func TestRunSessionsStagedFallback(t *testing.T) {
	unsupported := &kata.Error{Code: 5, Message: `{"error":{"kind":"conflict","code":"unsupported_event_features"}}`}
	readonly := &kata.Error{Code: 5, Message: `{"error":{"kind":"conflict","code":"federated_read_only"}}`}
	for _, tt := range []struct {
		name       string
		errs       []error
		incomplete bool
		want       int
	}{
		{"two-stages", []error{unsupported, unsupported}, false, 3},
		{"read-only", []error{readonly}, false, 2},
		{"incomplete-only", []error{readonly}, true, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, st, base, r := setup(t)
			fleetFinal(t, st, r.Key)
			if err := st.Update(r.Key, func(r *journal.Run) error {
				r.Plan.Steps = []defs.Step{{Key: "main", Kind: "prompt"}}
				a := journal.Attempt{Step: "main", N: 1, Nonce: "launch", LaunchedAt: at, TaskStarted: new(false), Status: "failed", Session: recorded("pi", "one")}
				if tt.incomplete {
					a.Session = nil
				}
				r.Attempts = []journal.Attempt{a}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			k := &stagedSessionObserver{fakeKata: base, t: t, st: st, key: r.Key, errs: tt.errs}
			if err := Final(WithSummaryV2(WithRunSessions(ctx, true), true), k, st, r.Key); err != nil {
				t.Fatal(err)
			}
			if len(k.bodies) != tt.want {
				t.Fatal(len(k.bodies))
			}
			first, second := body(t, k.bodies[0]).Summary, body(t, k.bodies[1]).Summary
			if len(first.Sessions) == 0 && !first.SessionsIncomplete || len(second.Sessions) != 0 || second.SessionsIncomplete || second.Reason == nil || len(second.Steps) != 1 {
				t.Fatal(first, second)
			}
			if tt.want == 3 {
				last := body(t, k.bodies[2]).Summary
				if last.Reason != nil || len(last.Steps) != 0 {
					t.Fatal(last)
				}
			}
		})
	}
}

// Conflict readback compares recorded sessions as well as the other summary fields.
func TestRunSessionsConflictReadback(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprint(same), func(t *testing.T) {
			ctx, st, k, r := setup(t)
			if err := st.Update(r.Key, func(r *journal.Run) error {
				r.Phase, r.Outcome, r.EndedAt = "settled", "succeeded", new(at)
				r.Plan.Steps = []defs.Step{{Key: "main", Kind: "prompt"}}
				r.Attempts = []journal.Attempt{{Step: "main", N: 1, Session: recorded("pi", "one")}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			k.observeErr = &kata.Error{Code: 7}
			_ = Final(WithRunSessions(ctx, true), k, st, r.Key)
			remote := body(t, k.bodies[0])
			if !same {
				remote.Summary.Sessions[0].SessionID = "different"
			}
			raw, err := json.Marshal(remote)
			if err != nil {
				t.Fatal(err)
			}
			k.observed = map[string][]byte{r.RunUID: raw}
			k.observeErr = &kata.Error{Code: 5, Message: `{"error":{"kind":"conflict","code":"revision_conflict"}}`}
			err = Final(WithRunSessions(ctx, true), k, st, r.Key)
			saved, e := st.Load(r.Key)
			if e != nil || (err == nil) != same || saved.ReportDone != same || saved.ReportRefused == same {
				t.Fatal(saved, err, e)
			}
		})
	}
}
