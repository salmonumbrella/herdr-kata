package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"hegel.dev/go/hegel"
)

func TestTask9Fix2RecoveryMarkerCannotBypassSavedScope(t *testing.T) {
	s, _, _ := policyProduct(t)
	j := fix1NotifyJob(t, s, policyIssueUID, map[string]any{"kind": "manual"})
	c, rec := fix2SavedRun(t, s, j, "done")
	b, err := localBridge(s)
	if err != nil {
		t.Fatal(err)
	}
	input, err := recoveryInput(s, rec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(rec.RunDir, "native-recovered.json")
	hegel.Test(t, func(ht *hegel.T) {
		text := strings.ToValidUTF8(hegel.Draw(ht, hegel.Text()), "\ufffd")
		m := nativeRecoveryMarker{Version: 1, Scope: b.Scope, Teammate: c.Target.Teammate, ExecutorLabel: s.Native.Binding.ExecutorLabel, RunUID: c.RunUID, Input: input}
		switch hegel.Draw(ht, hegel.Integers(0, 4)) {
		case 0:
			m.Scope.Actor += "other:" + text
		case 1:
			m.Scope.ProjectUID += "other:" + text
		case 2:
			m.Teammate += "other:" + text
		case 3:
			m.RunUID += "other:" + text
		case 4:
			m.Scope.TargetKey += "other:" + text
		}
		raw, _ := json.Marshal(m)
		if err := statefs.WriteJSON(path, m, 98304); err != nil {
			if len(raw) <= 98304 {
				ht.Fatal("bounded recovery marker rejected")
			}
			return
		}
		before, err := os.ReadFile(path)
		if err != nil {
			ht.Fatal(err)
		}
		if err := recoverMissingObservations(t.Context(), s); err == nil {
			ht.Fatal("same input digest bypassed changed recovery scope")
		}
		after, err := os.ReadFile(path)
		if err != nil || string(before) != string(after) {
			ht.Fatal("refusal changed frozen foreign recovery state")
		}
		if _, err := os.Stat(nativeDeliveryPath(rec.RunDir)); !os.IsNotExist(err) {
			ht.Fatal("foreign recovery marker queued evidence")
		}
	})
}
