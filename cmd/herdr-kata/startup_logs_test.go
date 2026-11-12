package main

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/attempt"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/prompt"
	"github.com/salmonumbrella/herdr-kata/internal/session"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type screenHC struct {
	hc.HC
	reads int
}

type settledHC struct {
	hc.HC
	removed bool
	getErr  error
}

func (h *settledHC) CoreID(context.Context) (string, error) { return "fixture-core", nil }
func (h *settledHC) Get(context.Context, string) (hc.Terminal, error) {
	if h.getErr != nil {
		return hc.Terminal{}, h.getErr
	}
	if h.removed {
		return hc.Terminal{}, hc.ErrNotFound
	}
	return hc.Terminal{Address: "t1", State: "finished", Reason: "exited", CreatedMs: 10, PID: 20, Labels: map[string]string{"kata.nonce": "fixture-nonce"}}, nil
}
func (h *settledHC) Remove(context.Context, string) error { h.removed = true; return nil }
func (h *settledHC) Read(context.Context, string, int) (string, bool, error) {
	return "", false, fmt.Errorf("screen permission denied")
}

func TestLogsAfterPromptSettlement(t *testing.T) {
	_, l := operatorFixture(t)
	r := localRun(t, l, "running")
	st := journal.New(l)
	if err := st.Update(r.Key, func(r *journal.Run) error {
		r.Plan = defs.Plan{Steps: []defs.Step{{Key: "main", Kind: "prompt"}}}
		r.Attempts = []journal.Attempt{{Step: "main", N: 1, Status: "running", Address: "t1", CoreID: "fixture-core", CreatedMs: 10, PID: 20, Nonce: "fixture-nonce"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := prompt.WriteResult(filepath.Join(l.Attempt(r.Key, "main", 1), "result.json"), prompt.Result{RunUID: r.RunUID, Step: "main", Attempt: 1, Status: "succeeded", Summary: "done", Session: &session.Session{Agent: "pi", SessionID: "ses_1"}}); err != nil {
		t.Fatal(err)
	}
	h := &settledHC{}
	if _, err := attempt.Settle(context.Background(), h, st, r.Key, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !h.removed {
		t.Fatal("settlement did not remove terminal")
	}
	var out bytes.Buffer
	if code := logs(context.Background(), []string{"logs", r.RunUID}, st, h, &out); code != 0 || out.String() != "attempt main/1 session pi ses_1\n[terminal output unavailable after cleanup]\n" {
		t.Fatalf("logs exit=%d output=%q", code, out.String())
	}
	if err := st.Update(r.Key, func(r *journal.Run) error { r.Attempts[0].SettlementStatus = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := logs(context.Background(), []string{"logs", r.RunUID}, st, h, &out); code != 1 || !strings.Contains(out.String(), "terminal not found") {
		t.Fatalf("unproven cleanup exit=%d output=%q", code, out.String())
	}
	if err := st.Update(r.Key, func(r *journal.Run) error { r.Attempts[0].SettlementStatus = "succeeded"; return nil }); err != nil {
		t.Fatal(err)
	}
	h.getErr = fmt.Errorf("permission denied")
	out.Reset()
	if code := logs(context.Background(), []string{"logs", r.RunUID}, st, h, &out); code != 1 || !strings.Contains(out.String(), "permission denied") {
		t.Fatalf("logs exit=%d output=%q", code, out.String())
	}
	h.getErr = nil
	h.removed = false
	out.Reset()
	if code := logs(context.Background(), []string{"logs", r.RunUID}, st, h, &out); code != 1 || !strings.Contains(out.String(), "screen permission denied") {
		t.Fatalf("live logs exit=%d output=%q", code, out.String())
	}
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
