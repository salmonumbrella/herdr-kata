package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	for _, outcome := range []string{"succeeded", "cancelled", "held"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, st, k, r := setup(t)
			if err := st.Update(r.Key, func(r *journal.Run) error {
				r.Phase, r.Outcome, r.EndedAt = "settled", outcome, new(at)
				if outcome == "held" {
					r.Phase, r.Hold, r.Outcome = "held", "launch_ambiguous", "unknown"
				}
				r.Plan.Steps = []defs.Step{{Key: "first", Kind: "prompt"}, {Key: "second", Kind: "prompt"}, {Key: "cmd", Kind: "command"}}
				r.Attempts = []journal.Attempt{
					{Step: "second", N: 1, Session: recorded("pi", "same")},
					{Step: "first", N: 2, Session: recorded("pi", "same")},
					{Step: "first", N: 2, Session: recorded("other", "same")},
					{Step: "cmd", N: 1, Nonce: "command", Session: recorded("pi", "command")},
					{Step: "second", N: 2, Session: recorded("pi", "last")},
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
		attempt journal.Attempt
		want    bool
	}{
		{"unlaunched", journal.Attempt{}, false},
		{"nonce", journal.Attempt{Nonce: "launch"}, true},
		{"false-start", journal.Attempt{TaskStarted: new(false)}, true},
		{"true-start", journal.Attempt{TaskStarted: new(true)}, true},
		{"legacy-time", journal.Attempt{LaunchedAt: at}, true},
		{"invalid", journal.Attempt{Nonce: "launch", Session: recorded("codex", "invalid")}, true},
	} {
		for _, hold := range []string{"", "launch_ambiguous", "launch_unproven"} {
			t.Run(tt.name+"/"+hold, func(t *testing.T) {
				ctx, st, k, r := setup(t)
				if err := st.Update(r.Key, func(r *journal.Run) error {
					r.Phase, r.Outcome, r.EndedAt = "settled", "succeeded", new(at)
					r.Plan.Steps = []defs.Step{{Key: "main", Kind: "prompt"}}
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
func TestRunSessionsCapAndFrozenRetry(t *testing.T) {
	for _, distinct := range []bool{false, true} {
		t.Run(fmt.Sprint(distinct), func(t *testing.T) {
			ctx, st, k, r := setup(t)
			if err := st.Update(r.Key, func(r *journal.Run) error {
				r.Phase, r.Outcome, r.EndedAt = "settled", "succeeded", new(at)
				r.Plan.Steps = []defs.Step{{Key: "main", Kind: "prompt"}}
				for i := range 65 {
					id := i
					if i == 64 && !distinct {
						id = 0
					}
					r.Attempts = append(r.Attempts, journal.Attempt{Step: "main", N: i + 1, Session: recorded("pi", fmt.Sprintf("session-%02d", id))})
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			k.observeErr = &kata.Error{Code: 7}
			if err := Final(WithRunSessions(ctx, true), k, st, r.Key); err == nil {
				t.Fatal("missing uncertain failure")
			}
			first := bytes.Clone(k.bodies[0])
			s := body(t, first).Summary
			if len(s.Sessions) != 64 || s.SessionsIncomplete != distinct {
				t.Fatal(s)
			}
			if err := st.Update(r.Key, func(r *journal.Run) error { r.Attempts = nil; return nil }); err != nil {
				t.Fatal(err)
			}
			k.observeErr = nil
			if err := Final(WithRunSessions(ctx, false), k, journal.New(st.Layout), r.Key); err != nil || !bytes.Equal(first, k.bodies[1]) {
				t.Fatal("frozen retry changed", err)
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
				a := journal.Attempt{Step: "main", N: 1, Nonce: "launch", Status: "failed", Session: recorded("pi", "one")}
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

func TestRunSessionsNoProjectionOnOtherErrors(t *testing.T) {
	for _, err := range []error{&kata.Error{Code: 5, Message: `{"error":{"kind":"conflict","code":"revision_conflict"}}`}, &kata.Error{Code: 5, Message: "unsupported_event_features"}, &kata.Error{Code: 7, Message: `{"error":{"kind":"conflict","code":"unsupported_event_features"}}`}} {
		ctx, st, base, r := setup(t)
		fleetFinal(t, st, r.Key)
		if e := st.Update(r.Key, func(r *journal.Run) error {
			r.Plan.Steps = []defs.Step{{Key: "main", Kind: "prompt"}}
			r.Attempts = []journal.Attempt{{Step: "main", N: 1, Session: recorded("pi", "one")}}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		k := &fleetObserver{fakeKata: base, st: st, key: r.Key, first: err, t: t}
		_ = Final(WithRunSessions(ctx, true), k, st, r.Key)
		saved, e := st.Load(r.Key)
		if e != nil || len(k.bodies) != 1 || !bytes.Equal(saved.ReportBody, k.bodies[0]) {
			t.Fatal(e, len(k.bodies))
		}
	}
}

func TestRunSessionsProjectionSaveFailure(t *testing.T) {
	ctx, st, base, r := setup(t)
	fleetFinal(t, st, r.Key)
	if err := st.Update(r.Key, func(r *journal.Run) error {
		r.Plan.Steps = []defs.Step{{Key: "main", Kind: "prompt"}}
		r.Attempts = []journal.Attempt{{Step: "main", N: 1, Session: recorded("pi", "one")}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(st.Layout.Run(r.Key), "run.json")
	backup := path + ".saved"
	k := &fleetObserver{fakeKata: base, st: st, key: r.Key, t: t, first: &kata.Error{Code: 5, Message: `{"error":{"kind":"conflict","code":"federated_read_only"}}`}}
	k.beforeReturn = func() {
		if err := os.Rename(path, backup); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := Final(WithRunSessions(ctx, true), k, st, r.Key); err == nil || len(k.bodies) != 1 {
		t.Fatal(err, len(k.bodies))
	}
}

func TestRunSessionsCommandOnlyLegacyBytes(t *testing.T) {
	ctx, st, k, r := setup(t)
	fleetFinal(t, st, r.Key)
	if err := st.Update(r.Key, func(r *journal.Run) error {
		r.Plan.Steps = []defs.Step{{Key: "main", Kind: "command"}}
		r.Attempts = []journal.Attempt{{Step: "main", N: 1, Nonce: "launch", Session: recorded("pi", "one")}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	k.observeErr = &kata.Error{Code: 7}
	_ = Final(WithRunSessions(ctx, true), k, st, r.Key)
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(k.bodies[0], &wire); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire["summary"]), "sessions") {
		t.Fatal(string(wire["summary"]))
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

func TestRunSessionsReadOnlyWithoutSessionsDoesNotProject(t *testing.T) {
	ctx, st, base, r := setup(t)
	fleetFinal(t, st, r.Key)
	fleetTimedStep(t, st, r.Key)
	k := &fleetObserver{fakeKata: base, st: st, key: r.Key, t: t, first: &kata.Error{Code: 5, Message: `{"error":{"kind":"conflict","code":"federated_read_only"}}`}}
	_ = Final(WithSummaryV2(ctx, true), k, st, r.Key)
	saved, err := st.Load(r.Key)
	if err != nil || len(k.bodies) != 1 || !bytes.Equal(saved.ReportBody, k.bodies[0]) {
		t.Fatal(err, len(k.bodies))
	}
}

func TestRunSessionsBoundWithLargeStepMessages(t *testing.T) {
	ctx, st, k, r := setup(t)
	if err := st.Update(r.Key, func(r *journal.Run) error {
		r.Phase, r.Outcome, r.EndedAt = "settled", "succeeded", new(at)
		for i := range 64 {
			key := fmt.Sprintf("step-%02d", i)
			r.Plan.Steps = append(r.Plan.Steps, defs.Step{Key: key, Kind: "prompt"})
			r.Attempts = append(r.Attempts, journal.Attempt{Step: key, N: 1, Status: "succeeded", Summary: strings.Repeat("界", 1400), Session: recorded("pi", fmt.Sprintf("session-%02d", i))})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Final(WithSummaryV2(WithRunSessions(ctx, true), true), k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	s := body(t, k.bodies[0]).Summary
	raw, err := json.Marshal(s)
	if err != nil || len(raw) > 64*1024 || len(s.Sessions) != 64 || len(s.Steps) != 64 || s.SessionsIncomplete {
		t.Fatal(len(raw), len(s.Sessions), len(s.Steps), err)
	}
}
