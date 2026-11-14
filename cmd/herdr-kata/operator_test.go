package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
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

type requestReply struct {
	Request journal.Request
	Err     error
}

func respondToRequest(l state.Layout, reason string) <-chan requestReply {
	return respondToRequestWith(l, func(q journal.Requests, r journal.Request) error {
		if reason != "" {
			return q.Reject(r.ID, reason)
		}
		return q.Done(r.ID)
	})
}

func respondToRequestWith(l state.Layout, respond func(journal.Requests, journal.Request) error) <-chan requestReply {
	out := make(chan requestReply, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		poll := time.NewTicker(10 * time.Millisecond)
		defer poll.Stop()
		q := journal.Requests{Dir: l.Requests()}
		for {
			requests, err := q.Pending()
			if err != nil {
				out <- requestReply{Err: err}
				return
			}
			if len(requests) > 0 {
				r := requests[0]
				err = respond(q, r)
				out <- requestReply{Request: r, Err: err}
				return
			}
			select {
			case <-ctx.Done():
				out <- requestReply{Err: ctx.Err()}
				return
			case <-poll.C:
			}
		}
	}()
	return out
}

func TestRunReportsPendingReasonWhenWaitEnds(t *testing.T) {
	_, l := operatorFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reply := respondToRequestWith(l, func(q journal.Requests, r journal.Request) error {
		err := state.WriteJSON(filepath.Join(q.Dir, r.ID+".json"), map[string]any{"ID": r.ID, "Kind": r.Kind, "JobUID": r.JobUID, "PendingReason": "run capacity unavailable"})
		cancel()
		return err
	})
	var out bytes.Buffer
	code := postRequest(ctx, []string{"run", "01J00000000000000000000010"}, journal.New(l), &out)
	result := <-reply
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if code != 1 || !strings.Contains(out.String(), "pending: run capacity unavailable") {
		t.Fatalf("pending request: exit=%d output=%q", code, out.String())
	}
	if pending, err := (journal.Requests{Dir: l.Requests()}).Pending(); err != nil || len(pending) != 1 {
		t.Fatalf("pending request lost: %v %v", pending, err)
	}
}

func TestRunWaitsForRejection(t *testing.T) {
	config, l := operatorFixture(t)
	reply := respondToRequest(l, "unsupported job")
	var out bytes.Buffer
	code := run([]string{"--config", config, "--state", l.Root, "run", "01J00000000000000000000010"}, &out)
	result := <-reply
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if code != 3 || !strings.Contains(out.String(), "unsupported job") {
		t.Fatalf("rejected request: exit=%d output=%q", code, out.String())
	}
}

func operatorFixture(t *testing.T) (string, state.Layout) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	text := `executor="executor"
hc_bin="/bin/false"
[kata]
bin="/bin/false"
daemon="fixture"
[[projects]]
name="example-project"
actor="worker"
`
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path, state.Layout{Root: filepath.Join(root, "state")}
}
func TestRunOnlyPostsLocalRequest(t *testing.T) {
	config, l := operatorFixture(t)
	reply := respondToRequest(l, "")
	var out bytes.Buffer
	code := run([]string{"--config", config, "--state", l.Root, "run", "01J00000000000000000000010", "--input", "manual context"}, &out)
	if code != 0 {
		t.Fatalf("run %d: %s", code, &out)
	}
	result := <-reply
	if result.Err != nil || result.Request.Input != "manual context" || result.Request.JobUID != "01J00000000000000000000010" {
		t.Fatal(result)
	}
}
func localRun(t *testing.T, l state.Layout, phase string) journal.Run {
	t.Helper()
	r := journal.Run{Version: 1, Key: journal.ManualKey("executor", "example-project", "request"), RunUID: "01J00000000000000000000001", Project: "example-project", Phase: phase, StartedAt: new(time.Now().UTC())}
	if err := journal.New(l).Create(r); err != nil {
		t.Fatal(err)
	}
	return r
}
func TestReleaseOnlyPostsLocalRequest(t *testing.T) {
	config, l := operatorFixture(t)
	r := localRun(t, l, "held")
	var out bytes.Buffer
	if code := run([]string{"--config", config, "--state", l.Root, "release", r.RunUID}, &out); code != 0 {
		t.Fatalf("release %d: %s", code, &out)
	}
	requests, err := (journal.Requests{Dir: l.Requests()}).Pending()
	if err != nil || len(requests) != 1 || requests[0].Kind != "release" {
		t.Fatal(requests, err)
	}
	saved, _ := journal.New(l).Load(r.Key)
	if saved.Phase != "held" {
		t.Fatal("control command mutated worker state")
	}
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
	if err := prompt.WriteResult(filepath.Join(l.Attempt(r.Key, "main", 1), "result.json"), prompt.Result{RunUID: r.RunUID, Step: "main", Attempt: 1, Status: "succeeded", Summary: "done"}); err != nil {
		t.Fatal(err)
	}
	h := &settledHC{HC: hc.New("/bin/false", "fixture")}
	if _, err := attempt.Settle(context.Background(), h, st, r.Key, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !h.removed {
		t.Fatal("settlement did not remove terminal")
	}
	var out bytes.Buffer
	if code := logs(context.Background(), []string{"logs", r.RunUID}, st, h, &out); code != 0 || out.String() != "[terminal output unavailable after cleanup]\n" {
		t.Fatalf("logs exit=%d output=%q", code, out.String())
	}
	if err := st.Update(r.Key, func(r *journal.Run) error {
		r.Attempts[0].SettlementStatus = ""
		r.Attempts[0].Status = "held"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := logs(context.Background(), []string{"logs", r.RunUID}, st, h, &out); code != 1 || !strings.Contains(out.String(), "terminal not found") {
		t.Fatalf("unproven cleanup exit=%d output=%q", code, out.String())
	}
	if err := st.Update(r.Key, func(r *journal.Run) error {
		r.Attempts[0].SettlementStatus = "succeeded"
		r.Attempts[0].Status = "succeeded"
		return nil
	}); err != nil {
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

func TestLogsReadsReportedRun(t *testing.T) {
	config, l := operatorFixture(t)
	r := localRun(t, l, "reported")
	r.Attempts = []journal.Attempt{{Step: "main", N: 1, Session: &session.Session{Agent: "pi", SessionID: "ses_1"}}, {Step: "main", N: 2}}
	if err := journal.New(l).Update(r.Key, func(saved *journal.Run) error { saved.Attempts = r.Attempts; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteFile(filepath.Join(l.Attempt(r.Key, "main", 2), "output.log"), []byte("fixture output\n")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := run([]string{"--config", config, "--state", l.Root, "logs", r.RunUID}, &out); code != 0 || out.String() != "attempt main/1 session pi ses_1\nfixture output\n" {
		t.Fatalf("logs %d: %s", code, &out)
	}
}
func TestServeSecondLockRejected(t *testing.T) {
	config, l := operatorFixture(t)
	release, err := journal.Lock(l.Lock())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var out bytes.Buffer
	if code := run([]string{"--config", config, "--state", l.Root, "serve"}, &out); code != 1 {
		t.Fatalf("second serve %d: %s", code, &out)
	}
}

func TestExplicitEmptyInputPreserved(t *testing.T) {
	config, l := operatorFixture(t)
	reply := respondToRequest(l, "")
	var out bytes.Buffer
	if code := run([]string{"--config", config, "--state", l.Root, "run", "01J00000000000000000000010", "--input", ""}, &out); code != 0 {
		t.Fatalf("run %d %s", code, &out)
	}
	result := <-reply
	if result.Err != nil || !result.Request.InputSet || result.Request.Input != "" {
		t.Fatal(result)
	}
}

func TestWakeLogsState(t *testing.T) {
	config, l := operatorFixture(t)
	r := localRun(t, l, "reported")
	if err := journal.New(l).Update(r.Key, func(saved *journal.Run) error {
		saved.Plan.Agent = "worker"
		saved.Plan.Steps = []defs.Step{{Key: "main", Kind: "prompt"}}
		saved.Attempts = []journal.Attempt{{Step: "main", N: 1, Status: "succeeded", SettlementStatus: "succeeded", Address: "t1", Session: &session.Session{Agent: "pi", SessionID: "ses_1"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	run([]string{"--config", config, "--state", l.Root, "logs", r.RunUID}, &out)
	if !strings.HasPrefix(out.String(), "agent worker: completed\nattempt main/1 session pi ses_1\n") {
		t.Fatalf("logs: %s", &out)
	}
	for _, tc := range []struct{ status, reason, hold, want string }{
		{"failed", "not_delivered", "", "not delivered"},
		{"held", "", "occupant_changed", "occupant_changed"},
		{"held", "", "wake_uncertain", "uncertain (wake_uncertain)"},
	} {
		if err := journal.New(l).Update(r.Key, func(saved *journal.Run) error {
			saved.Attempts[0] = journal.Attempt{Step: "main", N: 1, Status: tc.status, StopReason: tc.reason, Hold: tc.hold}
			if tc.hold != "" {
				saved.Attempts[0].Address = "t1"
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		run([]string{"--config", config, "--state", l.Root, "logs", r.RunUID}, &out)
		if !strings.HasPrefix(out.String(), "agent worker: "+tc.want+"\n") {
			t.Fatalf("%s: logs: %s", tc.want, &out)
		}
	}
	if err := journal.New(l).Update(r.Key, func(saved *journal.Run) error {
		saved.Phase, saved.Outcome, saved.Hold = "settled", "cancelled", "timeout"
		saved.Attempts[0] = journal.Attempt{Step: "main", N: 1, Status: "running", Address: "t1"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	run([]string{"--config", config, "--state", l.Root, "logs", r.RunUID}, &out)
	if !strings.HasPrefix(out.String(), "agent worker: cancelled\n") {
		t.Fatalf("cancelled: logs: %s", &out)
	}
}
