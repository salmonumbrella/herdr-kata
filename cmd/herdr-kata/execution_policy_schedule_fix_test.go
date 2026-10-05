package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/store"
)

func TestConsumerOldActivatedSchedulesLaunchFirstRun(t *testing.T) {
	for _, kind := range []string{"cron", "interval"} {
		t.Run(kind, func(t *testing.T) {
			s, dir, _ := policyProduct(t)
			j := consumerShellJob(t, s)
			var state map[string]any
			raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &state); err != nil {
				t.Fatal(err)
			}
			row := state["Resources"].(map[string]any)["job"].(map[string]any)[j.ID].(map[string]any)
			row["created_at"] = time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339Nano)
			row["definition"].(map[string]any)["trigger"] = map[string]any{"kind": kind, "cron": "* * * * *", "interval_seconds": 60}
			row["definition"].(map[string]any)["catchup"] = "latest"
			policyWrite(t, filepath.Join(dir, "state.json"), state)
			if err := s.SetEnabled(t.Context(), j.ID, true); err != nil {
				t.Fatal(err)
			}
			d := &daemon{store: s, slots: make(chan struct{}, 1), inflight: map[string]bool{}}
			d.sweep(t.Context())
			d.wg.Wait()
			if effects, _ := os.ReadFile(filepath.Join(j.CWD, "consumer-count")); string(effects) != "x" {
				t.Fatalf("old activated %s never launched: %q", kind, effects)
			}
			d.sweep(t.Context())
			d.wg.Wait()
			if effects, _ := os.ReadFile(filepath.Join(j.CWD, "consumer-count")); string(effects) != "x" {
				t.Fatalf("immediate subsequent tick duplicated %s: %q", kind, effects)
			}
		})
	}
}

func TestConsumerActivationReadFailureIsVisibleAndSafe(t *testing.T) {
	for _, mode := range []string{"malformed", "unsupported", "unreadable", "one bad identity"} {
		t.Run(mode, func(t *testing.T) {
			s, _, _ := policyProduct(t)
			j := consumerShellJob(t, s)
			past := time.Now().Add(-time.Hour)
			j.Schedule = store.ScheduleOnce
			j.RunAt = &past
			// Save a one-shot that would otherwise launch on the next tick.
			j = policyJob(t, s, j)
			if err := s.SetEnabled(t.Context(), j.ID, true); err != nil {
				t.Fatal(err)
			}
			healthy := consumerShellJob(t, s)
			healthy.Schedule = store.ScheduleOnce
			healthy.RunAt = &past
			healthy = policyJob(t, s, healthy)
			if err := s.SetEnabled(t.Context(), healthy.ID, true); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.Native.StateDir, "native-activation.json")
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "malformed":
				if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unsupported":
				var v map[string]any
				json.Unmarshal(original, &v)
				v["version"] = 2
				policyWrite(t, path, v)
			case "unreadable":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "one bad identity":
				var v map[string]any
				json.Unmarshal(original, &v)
				for _, entry := range v["entries"].(map[string]any) {
					a := entry.(map[string]any)
					if a["job_uid"] == j.ID {
						a["job_uid"] = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
					}
				}
				policyWrite(t, path, v)
			}
			d := &daemon{store: s, slots: make(chan struct{}, 1), inflight: map[string]bool{}}
			diagnostic := captureDateDiagnostics(t, func() { d.sweep(t.Context()); d.wg.Wait() })
			if !strings.Contains(diagnostic, j.ID) || !strings.Contains(diagnostic, "local activation") {
				t.Fatalf("local disk failure hidden from daemon: %q", diagnostic)
			}
			want := ""
			if mode == "one bad identity" {
				want = "x"
			}
			if effects, _ := os.ReadFile(filepath.Join(j.CWD, "consumer-count")); string(effects) != want {
				t.Fatalf("unsafe launch/healthy-row isolation failed: %q want %q", effects, want)
			}
			// Repairing the file resumes ordinary local work; a held row was not run.
			if mode == "unreadable" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			d.sweep(t.Context())
			d.wg.Wait()
			if effects, _ := os.ReadFile(filepath.Join(j.CWD, "consumer-count")); string(effects) != "xx" {
				t.Fatalf("repair did not release held rows: %q", effects)
			}
		})
	}
}

func TestConsumerOldActivatedRelativeCronLaunches(t *testing.T) {
	s, dir, _ := policyProduct(t)
	j := consumerShellJob(t, s)
	var state map[string]any
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	row := state["Resources"].(map[string]any)["job"].(map[string]any)[j.ID].(map[string]any)
	row["created_at"] = time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339Nano)
	row["definition"].(map[string]any)["trigger"] = map[string]any{"kind": "cron", "cron": "@every 5m"}
	row["definition"].(map[string]any)["catchup"] = "latest"
	policyWrite(t, filepath.Join(dir, "state.json"), state)
	if err := s.SetEnabled(t.Context(), j.ID, true); err != nil {
		t.Fatal(err)
	}
	d := &daemon{store: s, slots: make(chan struct{}, 1), inflight: map[string]bool{}}
	d.sweep(t.Context())
	d.wg.Wait()
	if effects, _ := os.ReadFile(filepath.Join(j.CWD, "consumer-count")); string(effects) != "x" {
		t.Fatalf("locally activated relative cron never launched: %q", effects)
	}
	d.sweep(t.Context())
	d.wg.Wait()
	if effects, _ := os.ReadFile(filepath.Join(j.CWD, "consumer-count")); string(effects) != "x" {
		t.Fatalf("subsequent relative tick duplicated process: %q", effects)
	}
}
