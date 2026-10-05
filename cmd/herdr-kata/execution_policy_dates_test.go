package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

func consumerDateJob(t *testing.T, s *store.Store, kind, action string, lead int64) store.Job {
	t.Helper()
	j := consumerShellJob(t, s)
	var raw map[string]any
	if err := katacli.Decode(j.NativeDefinition, &raw); err != nil {
		t.Fatal(err)
	}
	raw["trigger"] = map[string]any{"kind": kind, "issue_uid": policyIssueUID, "lead_seconds": lead, "timezone": "America/Los_Angeles"}
	if action == "notify" {
		raw["action"] = map[string]any{"kind": "notify", "recipient": "worker/child", "message": "Inspect due source"}
		raw["issue"] = map[string]any{"kind": "existing", "uid": policyIssueUID}
	}
	draft, err := katacli.NewDraft("job", j.ID, j.Name, policyJSON(t, raw), j.NativeEventUID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Native.Save(t.Context(), draft); err != nil {
		t.Fatal(err)
	}
	if err = s.SetEnabled(t.Context(), j.ID, true); err != nil {
		t.Fatal(err)
	}
	current, err := s.Job(t.Context(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	return *current
}

func dateProjection(t *testing.T, dir, value, zone, instant string, revision int) {
	t.Helper()
	var date any
	if value != "" {
		date = map[string]any{"field": "scheduled_on", "value": value, "timezone": zone, "instant": instant}
	}
	policyWrite(t, filepath.Join(dir, "planning-dates.json"), map[string]any{"project_id": 73, "issue_uid": policyIssueUID, "revision": revision, "scheduled_on": date, "deadline_on": nil})
}

func captureDateDiagnostics(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prior := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = prior; r.Close(); w.Close() }()
	fn()
	w.Close()
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestConsumerDateSourcesRetainCivilResolutionAndIgnoreUnrelatedEdits(t *testing.T) {
	for _, tc := range []struct{ name, value, zone, instant string }{
		{"DST gap", "2025-03-09T02:30", "America/New_York", "2025-03-09T07:00:00Z"},
		{"DST fold", "2025-11-02T01:30", "America/New_York", "2025-11-02T05:30:00Z"},
		{"recurrence civil date", "2000-01-01", "Asia/Tokyo", "1999-12-31T15:00:00Z"},
		{"authority Local", "2000-01-01", "Local", "1999-12-31T15:00:00Z"},
		{"explicit UTC", "2000-01-01T00:00:00Z", "UTC", "2000-01-01T00:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, dir, _ := policyProduct(t)
			j := consumerDateJob(t, s, "issue-scheduled", "execute", 0)
			d := &daemon{store: s, slots: make(chan struct{}, 1), inflight: map[string]bool{}}
			dateProjection(t, dir, tc.value, tc.zone, tc.instant, 1)
			d.sweep(t.Context())
			d.wg.Wait()
			last, err := s.LastRun(t.Context(), j.ID)
			if err != nil {
				t.Fatal(err)
			}
			c, err := runner.LoadNativeContext(last.RunDir)
			if err != nil {
				t.Fatal(err)
			}
			var source struct {
				IssueUID                        string `json:"issue_uid"`
				Field, Value, Timezone, Instant string
				OffsetSeconds                   int64 `json:"offset_seconds"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(c.Occurrence, "date:")), &source); err != nil {
				t.Fatal(err)
			}
			if source.IssueUID != policyIssueUID || source.Field != "scheduled_on" || source.Value != tc.value || source.Timezone != tc.zone || source.Instant != tc.instant || source.OffsetSeconds != 0 {
				t.Fatalf("native source evidence changed: %+v", source)
			}
			// Ordinary metadata revision does not identify another occurrence.
			dateProjection(t, dir, tc.value, tc.zone, tc.instant, 2)
			d.sweep(t.Context())
			d.wg.Wait()
			dateProjection(t, dir, "2100-01-01", "UTC", "2100-01-01T00:00:00Z", 3)
			d.sweep(t.Context())
			d.wg.Wait()
			dateProjection(t, dir, "", "", "", 4)
			d.sweep(t.Context())
			d.wg.Wait()
			raw, _ := os.ReadFile(filepath.Join(j.CWD, "consumer-count"))
			if string(raw) != "x" {
				t.Fatalf("same/moved/cleared source fired: %q", raw)
			}
			dateProjection(t, dir, "2001-01-01", "UTC", "2001-01-01T00:00:00Z", 5)
			d.sweep(t.Context())
			d.wg.Wait()
			raw, _ = os.ReadFile(filepath.Join(j.CWD, "consumer-count"))
			if string(raw) != "xx" {
				t.Fatalf("changed source did not earn a distinct local fire: %q", raw)
			}
			policyAssertNoHandshake(t, policyCalls(t, dir))
		})
	}
}

func TestConsumerQueuedDateSourceInvalidationStopsLaunch(t *testing.T) {
	s, dir, _ := policyProduct(t)
	j := consumerDateJob(t, s, "issue-scheduled", "execute", 0)
	dateProjection(t, dir, "2000-01-01", "UTC", "2000-01-01T00:00:00Z", 1)
	projected, key, err := nativeDateJob(t.Context(), s, j)
	if err != nil {
		t.Fatal(err)
	}
	dateProjection(t, dir, "", "", "", 2)
	ctx := context.WithValue(t.Context(), nativeSourceKey{}, key)
	if _, err := Execute(ctx, s, projected, "scheduled"); err == nil {
		t.Fatal("cleared queued source executed")
	}
	if raw, _ := os.ReadFile(filepath.Join(j.CWD, "consumer-count")); len(raw) != 0 {
		t.Fatal("invalidated source produced process effects")
	}
}

func TestConsumerDeadlineLeadNotificationFailureHasStableNamedDiagnostic(t *testing.T) {
	s, dir, _ := policyProduct(t)
	j := consumerDateJob(t, s, "issue-deadline", "notify", 1800)
	policyWrite(t, filepath.Join(dir, "planning-dates.json"), map[string]any{"project_id": 73, "issue_uid": policyIssueUID, "revision": 1, "scheduled_on": nil, "deadline_on": map[string]any{"field": "deadline_on", "value": "2000-01-01T01:00", "timezone": "Europe/Paris", "instant": "2000-01-01T00:00:00Z"}})
	projected, key, err := nativeDateJob(t.Context(), s, j)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(1999, 12, 31, 23, 30, 0, 0, time.UTC)
	var source struct {
		Offset int64 `json:"offset_seconds"`
	}
	json.Unmarshal([]byte(strings.TrimPrefix(key, "date:")), &source)
	if source.Offset != -1800 || projected.RunAt.UTC().Format(time.RFC3339) != "2000-01-01T00:00:00Z" {
		t.Fatal("deadline source/lead evidence was lost")
	}
	d := &daemon{store: s, slots: make(chan struct{}, 1), inflight: map[string]bool{}}
	_, fires, _, err := d.nativeDateDue(t.Context(), j, want.Add(-time.Second))
	if err != nil || len(fires) != 0 {
		t.Fatalf("lead became due too early: %v %v", fires, err)
	}
	text := captureDateDiagnostics(t, func() {
		for i := 0; i < 3; i++ {
			d.sweep(t.Context())
			d.wg.Wait()
		}
	})
	if strings.Count(text, "ordinary notification delivery") != 1 || !strings.Contains(text, j.ID) || strings.Contains(text, "unknown schedule") {
		t.Fatalf("missing/spammed ordinary delivery diagnostic: %s", text)
	}
	if raw, _ := os.ReadFile(filepath.Join(j.CWD, "consumer-count")); len(raw) != 0 {
		t.Fatal("notification deadline launched execution")
	}
	if err := s.SetEnabled(t.Context(), j.ID, false); err != nil {
		t.Fatal(err)
	}
	d.sweep(t.Context())
	if err := s.SetEnabled(t.Context(), j.ID, true); err != nil {
		t.Fatal(err)
	}
	text = captureDateDiagnostics(t, func() { d.sweep(t.Context()) })
	if strings.Count(text, "ordinary notification delivery") != 1 {
		t.Fatalf("reactivated job lost its pending-delivery diagnostic: %s", text)
	}
}

func TestConsumerDateHistoryRetainsLocalContextIdentityChecks(t *testing.T) {
	s, dir, _ := policyProduct(t)
	j := consumerDateJob(t, s, "issue-scheduled", "execute", 0)
	dateProjection(t, dir, "2000-01-01", "UTC", "2000-01-01T00:00:00Z", 1)
	d := &daemon{store: s, slots: make(chan struct{}, 1), inflight: map[string]bool{}}
	d.sweep(t.Context())
	d.wg.Wait()
	last, err := s.LastRun(t.Context(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := runner.LoadNativeContext(last.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	proof.Target.Actor = "other-worker"
	policyWrite(t, filepath.Join(last.RunDir, "native-context.json"), proof)
	text := captureDateDiagnostics(t, func() { d.sweep(t.Context()); d.wg.Wait() })
	if !strings.Contains(text, "saved execution actor") {
		t.Fatalf("foreign local context was accepted as source history: %s", text)
	}
	if raw, _ := os.ReadFile(filepath.Join(j.CWD, "consumer-count")); string(raw) != "x" {
		t.Fatal("foreign local provenance triggered work")
	}
}

func TestConsumerDateSourceDoesNotRefireAfterIndependentManualRun(t *testing.T) {
	s, dir, _ := policyProduct(t)
	j := consumerDateJob(t, s, "issue-scheduled", "execute", 0)
	dateProjection(t, dir, "2000-01-01", "UTC", "2000-01-01T00:00:00Z", 1)
	d := &daemon{store: s, slots: make(chan struct{}, 1), inflight: map[string]bool{}}
	d.sweep(t.Context())
	d.wg.Wait()
	manual, err := Execute(t.Context(), s, j, "manual")
	if err != nil || manual == nil || manual.Outcome != runner.OutcomeDone {
		t.Fatalf("independent manual invocation: %+v %v", manual, err)
	}
	d.sweep(t.Context())
	d.wg.Wait()
	if raw, _ := os.ReadFile(filepath.Join(j.CWD, "consumer-count")); string(raw) != "xx" {
		t.Fatalf("manual invocation reset the unchanged scheduled source: %q", raw)
	}
}

func TestConsumerIssueScheduledUsesOrdinarySource(t *testing.T) {
	s, dir, _ := policyProduct(t)
	j := consumerShellJob(t, s)
	def, err := s.Native.Client.Definition(t.Context(), "job", j.ID)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := katacli.Decode(def.Definition, &body); err != nil {
		t.Fatal(err)
	}
	body["trigger"] = map[string]any{"kind": "issue-scheduled", "issue_uid": policyIssueUID, "timezone": "UTC"}
	raw := policyJSON(t, body)
	draft, err := katacli.NewDraft("job", j.ID, j.Name, raw, def.DefinitionEventUID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Native.Save(t.Context(), draft); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEnabled(t.Context(), j.ID, true); err != nil {
		t.Fatal(err)
	}
	policyWrite(t, filepath.Join(dir, "planning-dates.json"), map[string]any{"project_id": 73, "issue_uid": policyIssueUID, "revision": 1, "scheduled_on": map[string]any{"field": "scheduled_on", "value": "2000-01-01", "timezone": "UTC", "instant": "2000-01-01T00:00:00Z"}, "deadline_on": nil})
	policyWrite(t, filepath.Join(dir, "issues.json"), map[string]any{policyIssueUID: map[string]any{"uid": policyIssueUID, "status": "open", "metadata": map[string]any{"scheduled_on": "2000-01-01", "timezone": "UTC"}}})
	d := &daemon{store: s, slots: make(chan struct{}, 1), inflight: map[string]bool{}}
	d.sweep(t.Context())
	d.wg.Wait()
	marker := filepath.Join(j.CWD, "consumer-count")
	rawEffect, _ := os.ReadFile(marker)
	if string(rawEffect) != "x" {
		t.Fatalf("ordinary issue date did not execute: effects=%q", rawEffect)
	}
	d.sweep(t.Context())
	d.wg.Wait()
	rawEffect, _ = os.ReadFile(marker)
	if string(rawEffect) != "x" {
		t.Fatal("same source date refired")
	}
	policyAssertNoHandshake(t, policyCalls(t, dir))
}
