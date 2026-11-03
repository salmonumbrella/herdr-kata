package worker

import (
	"bytes"
	"encoding/json"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/report"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

// These are literal wire expectations, independent of the report builder.
func assertUnsupportedObservation(t *testing.T, k *fakeKata, detail string) {
	t.Helper()
	want := kata.ObserveCronRunBody{
		Actor: new("worker"), DefinitionEventUID: new("01J00000000000000000000011"),
		EndedAt: new(at), ExecutorLabel: new("executor"), ExpectedRevision: 0,
		JobUID: new("01J00000000000000000000010"), OccurrenceKey: new("unsupported:01J00000000000000000000011"),
		StartedAt: new(at), Status: "failed", Summary: kata.Summary{Version: 1, Message: new("reason: unsupported\n" + detail)}, Teammate: new("herdr-kata"),
	}
	if len(k.bodies) != 1 {
		t.Fatalf("observations=%d, want 1", len(k.bodies))
	}
	var got kata.ObserveCronRunBody
	if err := json.Unmarshal(k.bodies[0], &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("observation %s; want %+v", k.bodies[0], want)
	}
}

func TestUnsupportedReasonsPreserveWhitespace(t *testing.T) {
	for _, tt := range []struct {
		name, agent, detail string
		emptyCheckout       bool
	}{
		{name: "profile spaces", agent: "missing  profile", detail: "profile not configured: missing  profile"},
		{name: "profile tabs", agent: "missing\tprofile ", detail: "profile not configured: missing\tprofile "},
		{name: "empty checkout", emptyCheckout: true, detail: "checkout not configured: "},
		{name: "line breaks", agent: "missing\r\nprofile\nnext\rlast", detail: "profile not configured: missing profile next last"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w, h, k := fixture(t)
			if tt.emptyCheckout {
				k.jobs[0].Definition.CheckoutKey = new("")
			} else {
				k.jobs[0].Definition.Options = map[string]any{"agent": tt.agent}
			}
			tick(t, w, at)
			assertUnsupportedObservation(t, k, tt.detail)
			if h.runs != 0 || k.creates != 0 {
				t.Fatal("unsupported definition launched or created an issue")
			}
		})
	}
}

func assertNoUnsupportedExecution(t *testing.T, w *Worker, h *fakeHC, k *fakeKata) {
	t.Helper()
	runs, err := w.Store.All()
	if err != nil || len(runs) != 0 || h.runs != 0 || k.creates != 0 {
		t.Fatalf("journals=%d launches=%d issues=%d error=%v", len(runs), h.runs, k.creates, err)
	}
	if _, err := os.Stat(w.Store.Layout.Job(k.jobs[0].UID)); !os.IsNotExist(err) {
		t.Fatalf("unexpected checkpoint: %v", err)
	}
}

func TestUnsupportedReportedThroughReadinessGate(t *testing.T) {
	for _, defect := range []bool{false, true} {
		t.Run(strconv.FormatBool(defect), func(t *testing.T) {
			w, h, k := fixture(t)
			k.jobs[0].Definition.Issue = &kata.IssuePolicy{Kind: "existing", UID: new("01J00000000000000000000020")}
			k.ready = map[string]bool{}
			if defect {
				k.jobs[0].Definition.Options = map[string]any{"agent": "missing"}
			}
			request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
			tick(t, w, at)
			records, err := (report.Outbox{Layout: w.Store.Layout}).All()
			if err != nil {
				t.Fatal(err)
			}
			done, reason, err := (journal.Requests{Dir: w.Store.Layout.Requests()}).Result("request")
			wantReason := "existing issue is not ready"
			if defect {
				wantReason = "unsupported job: profile not configured: missing"
				if len(records) != 1 || records[0].State != "done" {
					t.Fatal(records)
				}
				assertUnsupportedObservation(t, k, "profile not configured: missing")
			} else if len(records) != 0 || len(k.bodies) != 0 {
				t.Fatalf("gate created report: records=%v bodies=%s", records, k.bodies)
			}
			if err != nil || !done || reason != wantReason {
				t.Fatalf("request done=%v reason=%q error=%v", done, reason, err)
			}
			assertNoUnsupportedExecution(t, w, h, k)
		})
	}
}

func TestMalformedExecutorReportedWithoutManualRequest(t *testing.T) {
	for _, tt := range []struct {
		name string
		pin  any
	}{{"null", nil}, {"empty", ""}, {"number", 7}, {"boolean", true}} {
		t.Run(tt.name, func(t *testing.T) {
			w, h, k := fixture(t)
			k.jobs[0].Definition.Options = map[string]any{"executor": tt.pin}
			tick(t, w, at)
			tick(t, w, at.Add(time.Second))
			assertUnsupportedObservation(t, k, "options.executor must be a non-empty string")
			records, err := (report.Outbox{Layout: w.Store.Layout}).All()
			if err != nil || len(records) != 1 || records[0].State != "done" || !bytes.Equal(records[0].Body, k.bodies[0]) {
				t.Fatalf("records %v error %v", records, err)
			}
			assertNoUnsupportedExecution(t, w, h, k)
		})
	}
}

func TestDoneUnsupportedCauseSurvivesSupportedTickAndRestart(t *testing.T) {
	w, h, k := fixture(t)
	k.jobs[0].Definition.Options = map[string]any{"agent": "available-later"}
	tick(t, w, at)
	box := report.Outbox{Layout: w.Store.Layout}
	first, err := box.All()
	if err != nil || len(first) != 1 || first[0].State != "done" {
		t.Fatal(first, err)
	}
	kEvent := k.jobs[0].DefinitionEventUID
	w.Config.Profiles["available-later"] = config.Profile{Argv: []string{"/bin/sh"}}
	tick(t, w, at.Add(time.Minute))
	if len(w.Unsupported) != 0 {
		t.Fatalf("profile availability did not repair the definition: %v", w.Unsupported)
	}
	delete(w.Config.Profiles, "available-later")
	restarted := New(w.Config, journal.New(w.Store.Layout), h, w.Clients, w.Bin)
	tick(t, restarted, at.Add(2*time.Minute))
	records, err := box.All()
	if err != nil || len(records) != 1 || records[0].State != "done" || records[0].RunUID != first[0].RunUID || !bytes.Equal(records[0].Body, first[0].Body) || k.jobs[0].DefinitionEventUID != kEvent {
		t.Fatalf("records %v error %v", records, err)
	}
	assertUnsupportedObservation(t, k, "profile not configured: available-later")
	assertNoUnsupportedExecution(t, restarted, h, k)
}

func TestPreparedRunKeepsFrozenDefaultsAfterRestart(t *testing.T) {
	w, h, k := fixture(t)
	original := w.Config.Checkouts["example-project"]
	w.Config.Projects[0].DefaultCheckout = "original"
	w.Config.Projects[0].DefaultAgent = "original"
	w.Config.Checkouts["original"] = original
	w.Config.Profiles["original"] = config.Profile{Argv: []string{"/bin/sh", "--original"}}
	k.jobs[0].Definition.CheckoutKey = nil
	if err := w.init(at); err != nil {
		t.Fatal(err)
	}
	p, reasons := defs.Freeze(k.jobs[0], nil, w.Config, "example-project", exec.LookPath)
	if len(reasons) != 0 {
		t.Fatal(reasons)
	}
	key := journal.ManualKey("executor", "example-project", "prepared")
	if err := w.prepare(k, w.Config.Projects[0], p, key, "manual:prepared", "prepared", at); err != nil {
		t.Fatal(err)
	}
	saved, err := w.Store.Load(key)
	if err != nil || saved.Phase != "prepared" || h.runs != 0 {
		t.Fatal(saved, err, h.runs)
	}
	w.Config.Projects[0].DefaultCheckout = "changed"
	w.Config.Projects[0].DefaultAgent = "changed"
	w.Config.Checkouts["changed"] = t.TempDir()
	w.Config.Profiles["changed"] = config.Profile{Argv: []string{"/bin/sh", "--changed"}}
	restarted := New(w.Config, journal.New(w.Store.Layout), h, w.Clients, w.Bin)
	tick(t, restarted, at.Add(time.Second))
	want := []string{"/bin/sh", "--original", "Read " + filepath.Join(w.Store.Layout.Attempt(key, "main", 1), "prompt.md") + " and do exactly what it says."}
	if len(h.specs) != 1 {
		t.Fatalf("launch count %d, want 1", len(h.specs))
	}
	launch := h.specs[0]
	if launch.Cwd != original || !slices.Equal(launch.Argv, want) {
		t.Fatalf("launch cwd %q argv %v; want cwd %q argv %v", launch.Cwd, launch.Argv, original, want)
	}
}
