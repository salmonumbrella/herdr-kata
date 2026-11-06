package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"hegel.dev/go/hegel"
)

func fleetFinal(t *testing.T, st *journal.Store, key string) {
	t.Helper()
	if err := st.Update(key, func(r *journal.Run) error { r.Phase = "settled"; r.Outcome = "failed"; r.EndedAt = new(at); return nil }); err != nil {
		t.Fatal(err)
	}
}
func TestFleetSummaryLegacyExact(t *testing.T) {
	ctx, st, k, r := setup(t)
	fleetFinal(t, st, r.Key)
	if err := Final(WithSummaryV2(ctx, false), k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	want := `{"actor":"worker","definition_event_uid":"01J00000000000000000000011","ended_at":"2026-11-02T09:00:00Z","executor_label":"executor","expected_revision":0,"job_uid":"01J00000000000000000000010","started_at":"2026-11-02T09:00:00Z","status":"failed","summary":{"message":"reason: failed","version":1},"teammate":"herdr-kata"}`
	if string(k.bodies[0]) != want {
		t.Fatal(string(k.bodies[0]))
	}
}
func TestFleetSummaryLatestAttempts(t *testing.T) {
	ctx, st, k, r := setup(t)
	fleetFinal(t, st, r.Key)
	if err := st.Update(r.Key, func(r *journal.Run) error {
		r.Plan.Steps = []defs.Step{{Key: "gate"}, {Key: "work"}, {Key: "pending"}, {Key: "unattempted"}}
		r.Attempts = []journal.Attempt{{Step: "work", N: 1, Status: "failed", Summary: "old"}, {Step: "work", N: 2, Status: "needs-human", Summary: "latest"}, {Step: "gate", N: 1, Status: "succeeded", Receipt: &journal.Receipt{ExitCode: 0}}, {Step: "pending", N: 1, Status: "running"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Final(WithSummaryV2(ctx, true), k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	summary := body(t, k.bodies[0]).Summary
	if summary.Version != 1 || summary.Reason == nil || *summary.Reason != "failed" || len(summary.Steps) != 3 {
		t.Fatal(summary)
	}
	gate, work, pending := summary.Steps[0], summary.Steps[1], summary.Steps[2]
	if gate.Key != "gate" || gate.Status != "succeeded" || gate.ExitCode == nil || *gate.ExitCode != 0 || gate.Attempts != 1 {
		t.Fatal(gate)
	}
	if work.Key != "work" || work.Status != "unknown" || work.Attempts != 2 || work.Reason == nil || *work.Reason != "needs-human" || work.Message != "latest" {
		t.Fatal(work)
	}
	if pending.Key != "pending" || pending.Status != "unknown" {
		t.Fatal(pending)
	}
}

// Inspect the actual wire keys: omitted timing is different from an empty value.
func fleetSummarySteps(t *testing.T, observation []byte) []map[string]json.RawMessage {
	t.Helper()
	var wire struct {
		Summary struct {
			Steps []map[string]json.RawMessage `json:"steps"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(observation, &wire); err != nil {
		t.Fatal(err)
	}
	return wire.Summary.Steps
}

func fleetReceipt(t *testing.T, start, end string) *journal.Receipt {
	t.Helper()
	parse := func(s string) time.Time {
		if s == "" {
			return time.Time{}
		}
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	return &journal.Receipt{StartedAt: parse(start), EndedAt: parse(end)}
}

// Contract: only the selected attempt's valid receipt supplies shared timing,
// for both final and held reports; capability-disabled reports remain v1.
func TestFleetSummaryReceiptTimes(t *testing.T) {
	timed := fleetReceipt(t, "2026-05-01T10:00:00+01:00", "2026-05-01T10:00:02+01:00")
	old := journal.Attempt{Step: "build", N: 1, Status: "failed", Summary: "old", Receipt: fleetReceipt(t, "2026-05-01T09:00:00+01:00", "2026-05-01T09:01:00+01:00")}
	old.Receipt.ExitCode = 1
	latest := journal.Attempt{Step: "build", N: 2, Status: "succeeded", Summary: "latest", Receipt: timed, LaunchedAt: at, IdleSince: new(at), TurnEndedAt: new(at)}
	for _, tt := range []struct {
		name       string
		receipt    *journal.Receipt
		previous   bool
		equalN     bool
		start, end string
	}{
		{name: "latest", receipt: timed, previous: true, start: "2026-05-01T09:00:00Z", end: "2026-05-01T09:00:02Z"},
		{name: "equal-N-later-entry", receipt: timed, previous: true, equalN: true, start: "2026-05-01T09:00:00Z", end: "2026-05-01T09:00:02Z"},
		{name: "equal-N-receiptless-latest", previous: true, equalN: true},
		{name: "mixed-offset-ordered", receipt: fleetReceipt(t, "2026-05-01T09:30:00+01:00", "2026-05-01T09:00:00Z"), start: "2026-05-01T08:30:00Z", end: "2026-05-01T09:00:00Z"},
		{name: "mixed-offset-reversed", receipt: fleetReceipt(t, "2026-05-01T09:00:00Z", "2026-05-01T09:30:00+01:00")},
		{name: "offset-zero-instant", receipt: fleetReceipt(t, "0001-01-01T01:00:00+01:00", "0001-01-01T01:00:02+01:00")},
		{name: "long-valid-interval", receipt: fleetReceipt(t, "0001-01-01T00:00:00.000000001Z", "9999-12-31T23:59:59.999999999Z"), start: "0001-01-01T00:00:00.000000001Z", end: "9999-12-31T23:59:59.999999999Z"},
		{name: "prompt-no-receipt"},
		{name: "receiptless-latest", previous: true},
		{name: "zero-start", receipt: fleetReceipt(t, "", "2026-05-01T09:00:02Z")},
		{name: "zero-end", receipt: fleetReceipt(t, "2026-05-01T09:00:00Z", "")},
		{name: "reversed", receipt: fleetReceipt(t, "2026-05-01T09:00:02Z", "2026-05-01T09:00:00Z")},
		{name: "equal-times", receipt: fleetReceipt(t, "2026-05-01T09:00:00Z", "2026-05-01T09:00:00Z"), start: "2026-05-01T09:00:00Z", end: "2026-05-01T09:00:00Z"},
		{name: "nanoseconds", receipt: fleetReceipt(t, "2026-05-01T10:00:00.123456789+01:00", "2026-05-01T10:00:02.000000001+01:00"), start: "2026-05-01T09:00:00.123456789Z", end: "2026-05-01T09:00:02.000000001Z"},
		{name: "UTC-year-overflow", receipt: fleetReceipt(t, "9999-12-31T23:30:00-01:00", "9999-12-31T23:30:02-01:00")},
		{name: "UTC-year-underflow", receipt: fleetReceipt(t, "0001-01-01T00:30:00+01:00", "0001-01-01T00:30:02+01:00")},
		{name: "UTC-end-overflow", receipt: fleetReceipt(t, "9999-12-31T22:59:59-01:00", "9999-12-31T23:00:01-01:00")},
		{name: "UTC-year-upper-valid", receipt: fleetReceipt(t, "9999-12-31T22:30:00-01:00", "9999-12-31T22:30:02-01:00"), start: "9999-12-31T23:30:00Z", end: "9999-12-31T23:30:02Z"},
		{name: "UTC-year-lower-valid", receipt: fleetReceipt(t, "0001-01-01T01:30:00+01:00", "0001-01-01T01:30:02+01:00"), start: "0001-01-01T00:30:00Z", end: "0001-01-01T00:30:02Z"},
	} {
		for _, holding := range []bool{false, true} {
			for _, enabled := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/hold=%t/v2=%t", tt.name, holding, enabled), func(t *testing.T) {
					ctx, st, k, r := setup(t)
					fleetFinal(t, st, r.Key)
					if err := st.Update(r.Key, func(r *journal.Run) error {
						r.Plan.Steps = []defs.Step{{Key: "build", Kind: "command"}, {Key: "unattempted", Kind: "command"}}
						if tt.receipt == nil && !tt.previous {
							r.Plan.Steps[0].Kind = "prompt"
						}
						a := latest
						a.Receipt = tt.receipt
						if tt.previous {
							previous := old
							if tt.equalN {
								previous.N = a.N
							}
							r.Attempts = append(r.Attempts, previous)
						}
						r.Attempts = append(r.Attempts, a)
						if holding {
							r.Phase, r.Hold = "held", "needs-human"
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					ctx = WithSummaryV2(ctx, enabled)
					var err error
					if holding {
						err = Hold(ctx, k, st, r.Key)
					} else {
						err = Final(ctx, k, st, r.Key)
					}
					if err != nil || len(k.bodies) != 1 {
						t.Fatal("publish", err, len(k.bodies))
					}
					summary := body(t, k.bodies[0]).Summary
					wantMessage := "reason: failed\nbuild: latest"
					if holding {
						wantMessage = "reason: needs-human\nbuild: latest"
					}
					if tt.previous {
						wantMessage = strings.Replace(wantMessage, "\nbuild: latest", "\nbuild: old\nbuild: latest", 1)
					}
					if summary.Version != 1 || summary.Message == nil || *summary.Message != wantMessage {
						t.Fatalf("legacy message changed: %+v", summary)
					}
					steps := fleetSummarySteps(t, k.bodies[0])
					if !enabled {
						if len(steps) != 0 || summary.Reason != nil || bytes.Contains(k.bodies[0], []byte(`"steps"`)) {
							t.Fatal("capability-disabled report enriched", string(k.bodies[0]))
						}
						return
					}
					if len(steps) != 1 || len(summary.Steps) != 1 {
						t.Fatal("attempted coverage", steps)
					}
					s := summary.Steps[0]
					if s.Key != "build" || s.Attempts != 2 || s.Status != "succeeded" || s.Message != "latest" || (s.ExitCode != nil) != (tt.receipt != nil) || s.ExitCode != nil && *s.ExitCode != 0 {
						t.Fatalf("selected evidence: %+v", s)
					}
					for key, want := range map[string]string{"started_at": tt.start, "ended_at": tt.end} {
						got, exists := steps[0][key]
						if want == "" {
							if exists {
								t.Fatalf("%s must be omitted: %s", key, got)
							}
						} else if string(got) != strconv.Quote(want) {
							t.Fatalf("%s = %s, want %q", key, got, want)
						}
					}
				})
			}
		}
	}
}

func TestFleetSummaryReceiptTimesPlanOrder(t *testing.T) {
	ctx, st, k, r := setup(t)
	fleetFinal(t, st, r.Key)
	if err := st.Update(r.Key, func(r *journal.Run) error {
		r.Plan.Steps = []defs.Step{{Key: "z"}, {Key: "a"}, {Key: "unattempted"}}
		r.Attempts = []journal.Attempt{
			{Step: "a", N: 1, Status: "succeeded", Receipt: fleetReceipt(t, "2026-05-01T09:00:00Z", "2026-05-01T09:00:02Z")},
			{Step: "z", N: 1, Status: "succeeded", Receipt: fleetReceipt(t, "2026-05-01T10:00:00Z", "2026-05-01T10:00:02Z")},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Final(WithSummaryV2(ctx, true), k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	steps := body(t, k.bodies[0]).Summary.Steps
	if len(steps) != 2 || steps[0].Key != "z" || steps[1].Key != "a" || steps[0].StartedAt != "2026-05-01T10:00:00Z" || steps[1].StartedAt != "2026-05-01T09:00:00Z" {
		t.Fatal("steps reordered or reconstructed", steps)
	}
}

// Test out-of-range years directly: year 10000 cannot be stored as journal JSON.
func TestFleetSummaryReceiptTimesUnpersistable(t *testing.T) {
	for _, year := range []int{0, 10000} {
		t.Run(strconv.Itoa(year), func(t *testing.T) {
			start := time.Date(year, 5, 1, 9, 0, 0, 0, time.UTC)
			r := journal.Run{Plan: defs.Plan{Steps: []defs.Step{{Key: "build"}}}, Attempts: []journal.Attempt{{Step: "build", N: 1, Status: "succeeded", Receipt: &journal.Receipt{StartedAt: start, EndedAt: start.Add(time.Second)}}}}
			var summary kata.Summary
			if err := enrich(&summary, r); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(kata.ObserveCronRunBody{Summary: summary})
			if err != nil {
				t.Fatal(err)
			}
			steps := fleetSummarySteps(t, encoded)
			if len(steps) != 1 || steps[0]["started_at"] != nil || steps[0]["ended_at"] != nil {
				t.Fatal("invalid UTC year leaked", string(encoded))
			}
		})
	}
}

// An offset is a spelling of an instant, not a change to the receipt interval.
func TestFleetSummaryReceiptTimesOffsetProperty(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		startOffset := hegel.Draw(ht, hegel.Integers(-23*60-59, 23*60+59))
		endOffset := hegel.Draw(ht, hegel.Integers(-23*60-59, 23*60+59))
		holding := hegel.Draw(ht, hegel.Integers(0, 1)) == 1
		nanos := hegel.Draw(ht, hegel.Integers(0, 999999999))
		seconds := hegel.Draw(ht, hegel.Integers(0, 86400))
		start := time.Date(2026, 5, 1, 9, 0, 0, nanos, time.UTC)
		end := start.Add(time.Duration(seconds) * time.Second)
		startZone, endZone := time.FixedZone("receipt-start", startOffset*60), time.FixedZone("receipt-end", endOffset*60)
		ctx, st, k, r := setup(t)
		fleetFinal(t, st, r.Key)
		if err := st.Update(r.Key, func(r *journal.Run) error {
			r.Plan.Steps = []defs.Step{{Key: "build", Kind: "command"}}
			r.Attempts = []journal.Attempt{{Step: "build", N: 1, Status: "succeeded", Receipt: &journal.Receipt{StartedAt: start.In(startZone), EndedAt: end.In(endZone)}}}
			return nil
		}); err != nil {
			ht.Fatal(err)
		}
		publish := Final
		if holding {
			publish = Hold
		}
		if err := publish(WithSummaryV2(ctx, true), k, st, r.Key); err != nil {
			ht.Fatal(err)
		}
		steps := fleetSummarySteps(t, k.bodies[0])
		if len(steps) != 1 {
			ht.Fatal("missing receipt step", steps)
		}
		for key, want := range map[string]time.Time{"started_at": start, "ended_at": end} {
			var raw string
			if err := json.Unmarshal(steps[0][key], &raw); err != nil {
				ht.Fatal(key, err)
			}
			got, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil || !got.Equal(want) || !strings.HasSuffix(raw, "Z") {
				ht.Fatal("UTC instant changed", key, raw, want, err)
			}
		}
	}, hegel.WithTestCases(32))
}
func TestFleetSummaryEncodedBoundAndRetry(t *testing.T) {
	ctx, st, k, r := setup(t)
	fleetFinal(t, st, r.Key)
	if err := st.Update(r.Key, func(r *journal.Run) error {
		for n := range 64 {
			key := fmt.Sprintf("step-%03d", n)
			r.Plan.Steps = append(r.Plan.Steps, defs.Step{Key: key, Kind: "command"})
			r.Attempts = append(r.Attempts, journal.Attempt{Step: key, N: 1, Status: "failed", Summary: strings.Repeat("\x01界", 1600), Receipt: fleetReceipt(t, "2026-05-01T10:00:00+01:00", "2026-05-01T10:00:02+01:00")})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	k.observeErr = &kata.Error{Code: 7}
	_ = Final(WithSummaryV2(ctx, true), k, st, r.Key)
	first := bytes.Clone(k.bodies[0])
	summary := body(t, first).Summary
	encoded, err := json.Marshal(summary)
	if err != nil || len(encoded) > 64*1024 || len(summary.Steps) != 64 {
		t.Fatal("unbounded", len(encoded), len(summary.Steps), err)
	}
	for _, step := range summary.Steps {
		if !utf8.ValidString(step.Message) || len(step.Message) > 4096 || step.StartedAt != "2026-05-01T09:00:00Z" || step.EndedAt != "2026-05-01T09:00:02Z" {
			t.Fatal("invalid step message/timing", step)
		}
	}
	if err := st.Update(r.Key, func(r *journal.Run) error { r.Attempts = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	k.observeErr = nil
	if err := Final(WithSummaryV2(ctx, false), k, journal.New(st.Layout), r.Key); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, k.bodies[1]) {
		t.Fatal("retry changed frozen bytes")
	}
}

func fleetTimedStep(t *testing.T, st *journal.Store, key string) {
	t.Helper()
	if err := st.Update(key, func(r *journal.Run) error {
		r.Plan.Steps = []defs.Step{{Key: "build", Kind: "command"}}
		r.Attempts = []journal.Attempt{{Step: "build", N: 2, Status: "succeeded", Receipt: fleetReceipt(t, "2026-05-01T10:00:00+01:00", "2026-05-01T10:00:02+01:00")}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFleetSummaryFrozenLegacy(t *testing.T) {
	ctx, st, k, r := setup(t)
	fleetFinal(t, st, r.Key)
	k.observeErr = &kata.Error{Code: 7}
	if err := Final(WithSummaryV2(ctx, false), k, st, r.Key); err == nil {
		t.Fatal("uncertain write hidden")
	}
	first := bytes.Clone(k.bodies[0])
	fleetTimedStep(t, st, r.Key)
	k.observeErr = nil
	if err := Final(WithSummaryV2(ctx, true), k, journal.New(st.Layout), r.Key); err != nil {
		t.Fatal(err)
	}
	if len(k.bodies) != 2 || !bytes.Equal(first, k.bodies[1]) || bytes.Contains(k.bodies[1], []byte(`"steps"`)) {
		t.Fatal("capability change backfilled frozen legacy report", k.bodies)
	}
}

type fleetObserver struct {
	*fakeKata
	st           *journal.Store
	key          string
	first        error
	beforeReturn func()
	t            *testing.T
	uids         []string
	revisions    []int
}

func (k *fleetObserver) Observe(uid string, b []byte, rev int) (int, bool, error) {
	k.bodies = append(k.bodies, bytes.Clone(b))
	k.uids = append(k.uids, uid)
	k.revisions = append(k.revisions, rev)
	if len(k.bodies) == 1 {
		if k.beforeReturn != nil {
			k.beforeReturn()
		}
		return 0, false, k.first
	}
	saved, err := journal.New(k.st.Layout).Load(k.key)
	if err != nil || !bytes.Equal(saved.ReportBody, b) {
		k.t.Fatal("retry before durable projection", err)
	}
	return 0, false, &kata.Error{Code: 7}
}
func TestFleetSummaryFallback(t *testing.T) {
	for _, tt := range []struct {
		name     string
		err      error
		fallback bool
	}{
		{"unsupported", &kata.Error{Code: 5, Message: `{"error":{"kind":"conflict","code":"unsupported_event_features","exit_code":5}}`}, true},
		{"wrong-kind", &kata.Error{Code: 5, Message: `{"error":{"kind":"validation","code":"unsupported_event_features"}}`}, false},
		{"malformed", &kata.Error{Code: 5, Message: `{"error":`}, false},
		{"plain", &kata.Error{Code: 5, Message: "unsupported_event_features"}, false},
		{"uncertain", &kata.Error{Code: 7, Message: `{"error":{"kind":"conflict","code":"unsupported_event_features"}}`}, false},
		{"conflict", &kata.Error{Code: 5, Message: `{"error":{"kind":"conflict","code":"revision_conflict"}}`}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, st, base, r := setup(t)
			fleetFinal(t, st, r.Key)
			fleetTimedStep(t, st, r.Key)
			k := &fleetObserver{fakeKata: base, st: st, key: r.Key, first: tt.err, t: t}
			_ = Final(WithSummaryV2(ctx, true), k, st, r.Key)
			want := 1
			if tt.fallback {
				want = 2
			}
			if len(k.bodies) != want {
				t.Fatal("observations", len(k.bodies))
			}
			saved, err := st.Load(r.Key)
			if err != nil {
				t.Fatal(err)
			}
			got := body(t, saved.ReportBody)
			before := body(t, k.bodies[0]).Summary
			if len(before.Steps) != 1 || before.Steps[0].StartedAt != "2026-05-01T09:00:00Z" || before.Steps[0].EndedAt != "2026-05-01T09:00:02Z" {
				t.Fatal("fallback did not start with timed evidence", before)
			}
			if (got.Summary.Reason == nil) != tt.fallback || saved.ReportRefused {
				t.Fatal("projection/refusal", got.Summary, saved.ReportRefused)
			}
			if tt.fallback {
				if len(got.Summary.Steps) != 0 || bytes.Contains(saved.ReportBody, []byte(`"steps"`)) || before.Message == nil || got.Summary.Message == nil || *before.Message != *got.Summary.Message {
					t.Fatal("projection retained steps or changed message", got.Summary)
				}
				if k.shows != 0 || k.uids[0] != r.RunUID || k.uids[0] != k.uids[1] || k.revisions[0] != r.ObsRev || k.revisions[0] != k.revisions[1] {
					t.Fatal("fallback identity/readback", k.shows)
				}
				projected := bytes.Clone(saved.ReportBody)
				k2 := &fakeKata{}
				if err := Final(WithSummaryV2(ctx, true), k2, journal.New(st.Layout), r.Key); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(projected, k2.bodies[0]) {
					t.Fatal("restart enriched projected bytes")
				}
			} else if !bytes.Equal(k.bodies[0], saved.ReportBody) {
				t.Fatal("ordinary error rewrote bytes")
			}
		})
	}
}
func TestFleetSummaryFailedProjectionSavePreventsRetry(t *testing.T) {
	ctx, st, base, r := setup(t)
	fleetFinal(t, st, r.Key)
	fleetTimedStep(t, st, r.Key)
	path := filepath.Join(st.Layout.Run(r.Key), "run.json")
	backup := path + ".saved"
	k := &fleetObserver{fakeKata: base, st: st, key: r.Key, t: t, first: &kata.Error{Code: 5, Message: `{"error":{"kind":"conflict","code":"unsupported_event_features"}}`}}
	k.beforeReturn = func() {
		if err := os.Rename(path, backup); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	err := Final(WithSummaryV2(ctx, true), k, st, r.Key)
	if err == nil || len(k.bodies) != 1 {
		t.Fatal("retry without save", err, len(k.bodies))
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, path); err != nil {
		t.Fatal(err)
	}
	saved, err := st.Load(r.Key)
	if err != nil {
		t.Fatal(err)
	}
	summary := body(t, saved.ReportBody).Summary
	if summary.Reason == nil || len(summary.Steps) != 1 || summary.Steps[0].StartedAt != "2026-05-01T09:00:00Z" || summary.Steps[0].EndedAt != "2026-05-01T09:00:02Z" || !bytes.Equal(k.bodies[0], saved.ReportBody) {
		t.Fatal("lost original bytes", err)
	}
}
func TestFleetSummaryUTF8Property(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		count := hegel.Draw(ht, hegel.Integers(0, 64))
		attempts := hegel.Draw(ht, hegel.Integers(1, 4))
		message := strings.Repeat(hegel.Draw(ht, hegel.Text()), 64)
		ctx, st, k, r := setup(t)
		fleetFinal(t, st, r.Key)
		if err := st.Update(r.Key, func(r *journal.Run) error {
			for n := range count {
				key := fmt.Sprintf("step-%02d", n)
				r.Plan.Steps = append(r.Plan.Steps, defs.Step{Key: key, Kind: "command", Retries: 3})
				r.Attempts = append([]journal.Attempt{{Step: key, N: attempts, Status: "succeeded", Summary: message, Receipt: &journal.Receipt{ExitCode: 0}}}, r.Attempts...)
				if attempts > 1 {
					r.Attempts = append(r.Attempts, journal.Attempt{Step: key, N: attempts - 1, Status: "failed", Summary: "old"})
				}
			}
			return nil
		}); err != nil {
			ht.Fatal(err)
		}
		if err := Final(WithSummaryV2(ctx, true), k, st, r.Key); err != nil {
			ht.Fatal(err)
		}
		got := body(t, k.bodies[0]).Summary
		encoded, err := json.Marshal(got)
		if err != nil || len(encoded) > 65536 || len(got.Steps) != count {
			ht.Fatal("summary bound/evidence", len(encoded), len(got.Steps), count, err)
		}
		for i, step := range got.Steps {
			if step.Key != fmt.Sprintf("step-%02d", i) || step.Status != "succeeded" || step.Attempts != attempts || step.ExitCode == nil || *step.ExitCode != 0 || step.Message == "old" || !utf8.ValidString(step.Message) || len(step.Message) > 4096 {
				ht.Fatal("latest step evidence", i, step)
			}
		}
	}, hegel.WithTestCases(32))
}
func TestFleetFallbackPreservesFrozenValuesProperty(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		revision := hegel.Draw(ht, hegel.Integers[int64](9007199254740993, 18014398509481983))
		ctx, st, base, r := setup(t)
		fleetFinal(t, st, r.Key)
		if err := st.Update(r.Key, func(r *journal.Run) error { r.ObsRev = int(revision); return nil }); err != nil {
			ht.Fatal(err)
		}
		k := &fleetObserver{fakeKata: base, st: st, key: r.Key, t: t, first: &kata.Error{Code: 5, Message: `{"error":{"kind":"conflict","code":"unsupported_event_features"}}`}}
		_ = Final(WithSummaryV2(ctx, true), k, st, r.Key)
		if len(k.bodies) != 2 {
			ht.Fatal("missing fallback", len(k.bodies))
		}
		var before, after map[string]json.RawMessage
		if err := json.Unmarshal(k.bodies[0], &before); err != nil {
			ht.Fatal(err)
		}
		if err := json.Unmarshal(k.bodies[1], &after); err != nil {
			ht.Fatal(err)
		}
		if string(after["expected_revision"]) != strconv.FormatInt(revision, 10) {
			ht.Fatal("number rounded", string(after["expected_revision"]))
		}
		delete(before, "summary")
		delete(after, "summary")
		if !reflect.DeepEqual(before, after) || body(t, k.bodies[1]).Summary.Message == nil || *body(t, k.bodies[1]).Summary.Message != "reason: failed" {
			ht.Fatal("projection changed frozen evidence")
		}
	}, hegel.WithTestCases(24))
}

// Contract: summary fallback and restart replay retain the first settlement
// alert intent, frozen bytes and run identity, including existing-issue carriers.
type fallbackAlertKata struct {
	*fakeKata
	t     *testing.T
	store *journal.Store
	key   string
	uids  []string
	revs  []int
}

func (k *fallbackAlertKata) Observe(uid string, b []byte, rev int) (int, bool, error) {
	k.t.Helper()
	saved, err := journal.New(k.store.Layout).Load(k.key)
	if err != nil || !bytes.Equal(saved.ReportBody, b) || !slices.Contains(saved.Notified, "settled") || len(k.notifications) != 1 {
		k.t.Fatalf("observation preceded durable body/one alert: %v sends=%+v", err, k.notifications)
	}
	k.events = append(k.events, "observe")
	k.bodies = append(k.bodies, bytes.Clone(b))
	k.uids = append(k.uids, uid)
	k.revs = append(k.revs, rev)
	switch len(k.bodies) {
	case 1:
		return 0, false, &kata.Error{Code: 5, Message: `{"error":{"kind":"conflict","code":"unsupported_event_features"}}`}
	case 2:
		return 0, false, &kata.Error{Code: 7}
	default:
		return rev + 1, false, nil
	}
}

func TestFleetFallbackKeepsOneAlertAcrossRestart(t *testing.T) {
	for _, policy := range []string{"per-run", "existing"} {
		for _, outcome := range []string{"failed", "unknown"} {
			t.Run(policy+"/"+outcome, func(t *testing.T) {
				ctx, st, base, r := finalFixture(t, outcome)
				if policy == "existing" {
					ctx, st, base, r = existingFixture(t)
				}
				if err := st.Update(r.Key, func(r *journal.Run) error {
					r.Outcome = outcome
					r.Plan.Steps = []defs.Step{{Key: "main"}}
					r.Attempts = []journal.Attempt{{Step: "main", N: 1, Status: outcome, Summary: "private detail", Receipt: fleetReceipt(t, "2026-05-01T10:00:00+01:00", "2026-05-01T10:00:02+01:00")}}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				k := &fallbackAlertKata{fakeKata: base, t: t, store: st, key: r.Key}
				if err := Final(WithSummaryV2(ctx, true), k, st, r.Key); err == nil {
					t.Fatal("fallback retry unavailability hidden")
				}
				saved, err := st.Load(r.Key)
				if err != nil || len(k.bodies) != 2 || saved.ReportDone || saved.ReportRefused {
					t.Fatalf("fallback state: %+v %v bodies=%d", saved, err, len(k.bodies))
				}
				before, projected := body(t, k.bodies[0]), body(t, k.bodies[1])
				if before.Summary.Reason == nil || *before.Summary.Reason != outcome || len(before.Summary.Steps) != 1 || projected.Summary.Reason != nil || len(projected.Summary.Steps) != 0 || projected.Summary.Message == nil || !bytes.Equal(saved.ReportBody, k.bodies[1]) {
					t.Fatalf("enrichment/projection: %+v %+v", before.Summary, projected.Summary)
				}
				if before.Summary.Steps[0].StartedAt != "2026-05-01T09:00:00Z" || before.Summary.Steps[0].EndedAt != "2026-05-01T09:00:02Z" || before.Summary.Message == nil || *before.Summary.Message != *projected.Summary.Message || bytes.Contains(k.bodies[1], []byte(`"steps"`)) {
					t.Fatal("timed fallback lost message or retained steps", before.Summary, projected.Summary)
				}
				if err := st.Update(r.Key, func(r *journal.Run) error {
					r.Outcome, r.Detail, r.Attempts = "succeeded", "changed after freeze", nil
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				ctx = WithSummaryV2(WithNow(ctx, at.Add(time.Hour)), true)
				for range 2 {
					if err := Final(ctx, k, journal.New(st.Layout), r.Key); err != nil {
						t.Fatal(err)
					}
				}
				want := notification{"abcd", "worker", "run " + outcome + ": reason: " + outcome + "; 01J00000000000000000000001"}
				if !slices.Equal(k.notifications, []notification{want}) || !slices.Equal(k.events, []string{"notify", "observe", "observe", "observe"}) || len(k.bodies) != 3 || !bytes.Equal(k.bodies[1], k.bodies[2]) {
					t.Fatalf("replay changed alert/order/bytes: %+v %v %q", k.notifications, k.events, k.bodies)
				}
				for i := range k.uids {
					if k.uids[i] != r.RunUID || k.revs[i] != 1 {
						t.Fatal("fallback changed run/revision", k.uids, k.revs)
					}
				}
				creates := 1 // Per-run admission already created its issue.
				if len(k.createRequests) != creates {
					t.Fatal("duplicate issue/carrier create", k.createRequests)
				}
				final, err := st.Load(r.Key)
				if err != nil || !final.ReportDone || final.Phase != "reported" || !bytes.Equal(final.ReportBody, saved.ReportBody) {
					t.Fatalf("restart did not publish frozen projection: %+v %v", final, err)
				}
			})
		}
	}
}
