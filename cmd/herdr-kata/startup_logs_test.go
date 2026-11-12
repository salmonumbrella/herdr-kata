package main

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type screenHC struct {
	hc.HC
	reads int
}

func (h *screenHC) CoreID(context.Context) (string, error) { return "fixture-core", nil }
func (h *screenHC) Get(_ context.Context, id string) (hc.Terminal, error) {
	if id != "t1" {
		return hc.Terminal{}, fmt.Errorf("unexpected terminal %s", id)
	}
	return hc.Terminal{Address: "t1", CreatedMs: 10, PID: 20, Labels: map[string]string{"kata.nonce": "fixture-nonce"}}, nil
}
func (h *screenHC) Read(_ context.Context, id string, lines int) (string, bool, error) {
	if id != "t1" || lines != 200 {
		return "", false, fmt.Errorf("unexpected screen request")
	}
	h.reads++
	return "Current task output", true, nil
}
func TestLogsUsesPromptSnapshotOnlyAfterStopIntent(t *testing.T) {
	for _, reason := range []string{"", "agent_not_started", "timeout"} {
		t.Run(reason, func(t *testing.T) {
			_, l := operatorFixture(t)
			r := localRun(t, l, "running")
			st := journal.New(l)
			if err := st.Update(r.Key, func(r *journal.Run) error {
				r.Plan = defs.Plan{Steps: []defs.Step{{Key: "main", Kind: "prompt"}}}
				r.Attempts = []journal.Attempt{{Step: "main", N: 1, Address: "t1", CoreID: "fixture-core", CreatedMs: 10, PID: 20, Nonce: "fixture-nonce", StopReason: reason}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := state.WriteFile(filepath.Join(l.Attempt(r.Key, "main", 1), "output.log"), []byte("Provisional startup screen\n")); err != nil {
				t.Fatal(err)
			}
			h := &screenHC{}
			var out bytes.Buffer
			code := logs(context.Background(), []string{"logs", r.RunUID}, st, h, &out)
			want, reads := "Provisional startup screen\n", 0
			if reason == "" {
				want, reads = "Current task output\n", 1
			}
			if code != 0 || out.String() != want || h.reads != reads {
				t.Fatalf("logs exit=%d output=%q reads=%d", code, out.String(), h.reads)
			}
		})
	}
}
