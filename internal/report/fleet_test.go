package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
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
func TestFleetSummaryEncodedBoundAndRetry(t *testing.T) {
	ctx, st, k, r := setup(t)
	fleetFinal(t, st, r.Key)
	if err := st.Update(r.Key, func(r *journal.Run) error {
		for n := range 64 {
			key := fmt.Sprintf("step-%03d", n)
			r.Plan.Steps = append(r.Plan.Steps, defs.Step{Key: key, Kind: "prompt"})
			r.Attempts = append(r.Attempts, journal.Attempt{Step: key, N: 1, Status: "failed", Summary: strings.Repeat("\x01界", 1600)})
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
		if !utf8.ValidString(step.Message) || len(step.Message) > 4096 {
			t.Fatal("invalid step message")
		}
	}
	if err := st.Update(r.Key, func(r *journal.Run) error { r.Attempts = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	k.observeErr = nil
	if err := Final(WithSummaryV2(ctx, false), k, st, r.Key); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, k.bodies[1]) {
		t.Fatal("retry changed frozen bytes")
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
	saved, err := k.st.Load(k.key)
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
			if (got.Summary.Reason == nil) != tt.fallback || saved.ReportRefused {
				t.Fatal("projection/refusal", got.Summary, saved.ReportRefused)
			}
			if tt.fallback {
				if k.shows != 0 || k.uids[0] != k.uids[1] || k.revisions[0] != k.revisions[1] {
					t.Fatal("fallback identity/readback", k.shows)
				}
				projected := bytes.Clone(saved.ReportBody)
				k2 := &fakeKata{}
				if err := Final(WithSummaryV2(ctx, true), k2, st, r.Key); err != nil {
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
	if err != nil || body(t, saved.ReportBody).Summary.Reason == nil {
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
