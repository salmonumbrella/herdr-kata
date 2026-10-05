package main

import (
	"encoding/json"
	"github.com/salmonumbrella/herdr-kata/internal/katabridge"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"hegel.dev/go/hegel"
	"math"
	"strings"
	"testing"
	"time"
)

func TestTask9PendingEvidenceRemainsFrozenAcrossLatestTail(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		message := hegel.Draw(ht, hegel.Text())
		message = strings.ToValidUTF8(message, "\ufffd")
		raw, _ := json.Marshal(map[string]any{"version": 1, "message": message})
		first := katacli.RunObservation{JobUID: policyIssueUID, DefinitionEventUID: policyProjectUID, Status: "running", Summary: raw}
		path := nativeDeliveryPath(t.TempDir())
		id := NativeDelivery{Version: 1, RunUID: policyIssueUID, TargetKey: "target", ProjectUID: policyProjectUID, Actor: "worker"}
		err := queueNativeObservation(t.Context(), path, id, first)
		encoded, _ := json.Marshal(first)
		if len(raw) > 65536 || len(encoded) > 98304 {
			if err == nil {
				ht.Fatal("oversized evidence accepted")
			}
			return
		}
		if err != nil {
			ht.Fatal(err)
		}
		for _, status := range []string{"failed", "unknown", "succeeded"} {
			next := first
			next.Status = status
			if err := queueNativeObservation(t.Context(), path, id, next); err != nil {
				ht.Fatal(err)
			}
		}
		var d NativeDelivery
		if err := statefs.ReadJSON(path, 262144, &d); err != nil {
			ht.Fatal(err)
		}
		a, _ := json.Marshal(d.Pending)
		if string(a) != string(encoded) || d.Unsent == nil || d.Unsent.Status != "succeeded" {
			ht.Fatal("pending evidence changed or latest tail lost")
		}
	})
}

func TestTask9BackoffAlwaysFiniteAndBounded(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		attempt := int(hegel.Draw(ht, hegel.Integers(math.MinInt64, math.MaxInt64)))
		delay := katabridge.RetryDelay(attempt)
		if delay < time.Second || delay > time.Minute {
			ht.Fatalf("attempt %d unbounded delay %s", attempt, delay)
		}
	})
}

func TestTask9QueueRejectsChangedRunReferences(t *testing.T) {
	path := nativeDeliveryPath(t.TempDir())
	id := NativeDelivery{Version: 1, RunUID: policyIssueUID, TargetKey: "target", ProjectUID: policyProjectUID, Actor: "worker"}
	first := katacli.RunObservation{JobUID: policyIssueUID, DefinitionEventUID: policyProjectUID, Status: "running", Summary: json.RawMessage(`{"version":1}`)}
	if err := queueNativeObservation(t.Context(), path, id, first); err != nil {
		t.Fatal(err)
	}
	changed := first
	changed.JobUID = policyProjectUID
	if err := queueNativeObservation(t.Context(), path, id, changed); err == nil {
		t.Fatal("coalescing changed immutable definition identity")
	}
}
