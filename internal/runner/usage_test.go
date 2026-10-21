package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/usage"
)

const usageSessionID = "00000000-0000-4000-8000-000000000001"

func usageFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func usageFixture(t *testing.T) (*Manager, *Store, *Run) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTSVIEW_DATA_DIR", t.TempDir())
	t.Setenv("AGENTSVIEW_AUTH_TOKEN", "")
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	s := testStore(t)
	r := settleFixture()
	at := settleTime.Add(-time.Minute)
	r.Phase = Running
	r.ActiveStep = "main"
	r.Cwd = "/srv/projects/example"
	r.Herdr = HerdrRef{Session: &herdr.AgentSession{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: usageSessionID}, LaunchedAt: at}
	r.Steps[0].SubmittedAt = &at
	if err := s.Create(r); err != nil {
		t.Fatal(err)
	}
	return NewManager(config.Config{}, nil, nil, s, nil, nil, &testClock{at: settleTime}), s, r
}
func savedUsage(t *testing.T, s *Store, uid string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(s.layout.RunFile(uid))
	if err != nil {
		t.Fatal(err)
	}
	var journal map[string]any
	if err := json.Unmarshal(data, &journal); err != nil {
		t.Fatal(err)
	}
	u, ok := journal["Usage"].(map[string]any)
	if !ok {
		t.Fatal("worker did not persist usage")
	}
	return u
}
func configureUsageServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	root := os.Getenv("AGENTSVIEW_DATA_DIR")
	usageFile(t, filepath.Join(root, "config.toml"), "host = \"127.0.0.1\"\nport = 1\nauth_token = \"example-token\"\n")
	record, _ := json.Marshal(map[string]any{"pid": 42, "service": "agentsview", "network": "tcp", "address": u.Host, "metadata": map[string]string{"browser_url": srv.URL + "/viewer"}})
	usageFile(t, filepath.Join(root, "daemon.42.json"), string(record))
}
func TestWorkerUsageAgentsview(t *testing.T) {
	m, s, r := usageFixture(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if req.Method != "GET" || req.URL.Path != "/viewer/api/v1/sessions/"+usageSessionID+"/usage" || req.URL.Query().Get("breakdown") != "true" || req.Header.Get("Authorization") != "Bearer example-token" {
			t.Errorf("wrong usage request: %s %s", req.Method, req.URL)
			w.WriteHeader(400)
			return
		}
		fmt.Fprint(w, `{"has_token_data":true,"has_cost":true,"cost":{"microdollars":1234567},"breakdown":[{"timestamp":"2026-01-02T03:59:30Z","input_tokens":120,"output_tokens":45,"cache_read_input_tokens":6,"cache_creation_input_tokens":7,"has_cost":true,"cost":{"microdollars":1234567}}]}`)
	}))
	defer srv.Close()
	configureUsageServer(t, srv)
	for range 2 {
		if err := m.settle(t.Context(), r.UID, &Result{Status: "succeeded"}); err != nil {
			t.Fatal(err)
		}
	}
	u := savedUsage(t, s, r.UID)
	if u["InputTokens"] != float64(120) || u["OutputTokens"] != float64(45) || u["CacheReadTokens"] != float64(6) || u["CacheCreationTokens"] != float64(7) || u["CostMicroUSD"] != float64(1234567) {
		t.Fatal(u)
	}
	if calls.Load() != 1 {
		t.Fatalf("collected %d times", calls.Load())
	}
}
func TestWorkerUsageClaudeFallback(t *testing.T) {
	m, s, r := usageFixture(t)
	_, err := s.Transition(r.UID, func(r *Run) error {
		r.Herdr.Session.Agent = "claude"
		r.Herdr.Session.Source = "herdr:claude"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(s.layout.AttemptDir(r.UID, "main", 1), "prompt.md")
	body := fmt.Sprintf("{\"type\":\"user\",\"message\":{\"content\":%q}}\n", "Read "+prompt)
	body += `{"type":"assistant","message":{"id":"one","usage":{"input_tokens":5,"output_tokens":13,"cache_read_input_tokens":21,"cache_creation_input_tokens":34}}}` + "\n"
	body += `{"type":"assistant","message":{"id":"one","usage":{"input_tokens":5,"output_tokens":13,"cache_read_input_tokens":21,"cache_creation_input_tokens":34}}}` + "\n"
	body += `{"type":"user","message":{"content":"Read /srv/other/runs/other/steps/main/1/prompt.md"}}` + "\n" + `{"type":"assistant","message":{"usage":{"input_tokens":999}}}` + "\n"
	usageFile(t, filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", "-srv-projects-example", usageSessionID+".jsonl"), body)
	if err := m.settle(t.Context(), r.UID, &Result{Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	u := savedUsage(t, s, r.UID)
	if u["InputTokens"] != float64(5) || u["OutputTokens"] != float64(13) || u["CacheReadTokens"] != float64(21) || u["CacheCreationTokens"] != float64(34) {
		t.Fatal(u)
	}
}
func writeUsageCodex(t *testing.T, s *Store, r *Run) {
	t.Helper()
	prompt := filepath.Join(s.layout.AttemptDir(r.UID, "main", 1), "prompt.md")
	body := fmt.Sprintf("{\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"message\":%q}}\n", "Read "+prompt)
	body += strings.Repeat(`{"type":"token_usage_record","payload":{"response_id":"one","usage":{"input_tokens":100,"cached_input_tokens":60,"cache_write_input_tokens":10,"output_tokens":17}}}`+"\n", 2)
	usageFile(t, filepath.Join(os.Getenv("CODEX_HOME"), "sessions", r.Herdr.LaunchedAt.In(time.Local).Format("2006/01/02"), "rollout-example-"+usageSessionID+".jsonl"), body)
}
func TestWorkerUsageCodexFallback(t *testing.T) {
	m, s, r := usageFixture(t)
	writeUsageCodex(t, s, r)
	if err := m.settle(t.Context(), r.UID, &Result{Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	u := savedUsage(t, s, r.UID)
	if u["InputTokens"] != float64(30) || u["OutputTokens"] != float64(17) || u["CacheReadTokens"] != float64(60) || u["CacheCreationTokens"] != float64(10) {
		t.Fatal(u)
	}
}
func TestWorkerUsageTimeoutFallsBack(t *testing.T) {
	m, s, r := usageFixture(t)
	writeUsageCodex(t, s, r)
	canceled := make(chan time.Duration, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		<-req.Context().Done()
		canceled <- time.Since(start)
	}))
	defer srv.Close()
	configureUsageServer(t, srv)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	if err := m.settle(ctx, r.UID, &Result{Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	u := savedUsage(t, s, r.UID)
	if u["InputTokens"] != float64(30) || u["OutputTokens"] != float64(17) {
		t.Fatal(u)
	}
	select {
	case elapsed := <-canceled:
		if elapsed > 4*time.Second {
			t.Fatalf("usage request exceeded its timeout: %s", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("usage request not canceled")
	}
}

func TestWorkerUsageCollectedBeforeRunFinishes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTSVIEW_DATA_DIR", t.TempDir())
	t.Setenv("AGENTSVIEW_AUTH_TOKEN", "")
	f := newExecution(t)
	f.accepted(t)
	r := loaded(t, f.s, f.uid)
	session := r.Herdr.Session.Value
	usageAt := r.Steps[0].SubmittedAt.UTC().Format(time.RFC3339Nano)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if req.URL.Path != "/viewer/api/v1/sessions/"+session+"/usage" {
			t.Error("wrong step session", req.URL)
			w.WriteHeader(400)
			return
		}
		fmt.Fprintf(w, `{"has_token_data":true,"breakdown":[{"timestamp":%q,"input_tokens":71,"output_tokens":19}]}`, usageAt)
	}))
	defer srv.Close()
	configureUsageServer(t, srv)
	f.result(t, "succeeded")
	res := f.await(t)
	r = loaded(t, f.s, f.uid)
	if r.EndedAt != nil || r.Phase != Running {
		t.Fatal("step accounting changed lifecycle", r.Phase)
	}
	u := savedUsage(t, f.s, f.uid)
	if u["InputTokens"] != float64(71) || u["OutputTokens"] != float64(19) {
		t.Fatal(u)
	}
	if err := f.m.settle(t.Context(), f.uid, res); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("step and run settlement made %d usage requests", calls.Load())
	}
}

func TestWorkerUsageDoesNotBlockCanceledSettlement(t *testing.T) {
	m, s, r := usageFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := m.settle(ctx, r.UID, &Result{Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if got := loaded(t, s, r.UID); got.Phase != Settled || got.EndedAt == nil {
		t.Fatal("usage prevented durable settlement", got.Phase)
	}
}

type usageCancellationContext struct {
	context.Context
	entered, canceled   chan struct{}
	entry, cancellation sync.Once
}

func (c *usageCancellationContext) Err() error {
	err := c.Context.Err()
	if err == nil {
		c.entry.Do(func() { close(c.entered) })
	} else {
		c.cancellation.Do(func() { close(c.canceled) })
	}
	return err
}

func TestWorkerUsageCancellationDuringCollectionStillSettles(t *testing.T) {
	m, s, r := usageFixture(t)
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &usageCancellationContext{Context: base, entered: make(chan struct{}), canceled: make(chan struct{})}
	lock := s.lock(r.UID)
	lock.Lock()
	unlock := sync.OnceFunc(lock.Unlock)
	defer unlock()
	done := make(chan error, 1)
	go func() { done <- m.settle(ctx, r.UID, &Result{Status: "succeeded"}) }()
	select {
	case <-ctx.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("settlement did not enter usage collection")
	}
	cancel()
	select {
	case <-ctx.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("usage collection did not observe cancellation")
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled usage collection blocked settlement")
	}
	if got := loaded(t, s, r.UID); got.Phase != Settled || got.EndedAt == nil {
		t.Fatal("optional accounting prevented durable settlement", got.Phase)
	}
}

func TestWorkerUsageDoesNotBackfillFinishedRun(t *testing.T) {
	m, s, r := usageFixture(t)
	writeUsageCodex(t, s, r)
	if _, err := s.Transition(r.UID, func(r *Run) error {
		r.Phase = Settled
		r.EndedAt = &settleTime
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.settle(t.Context(), r.UID, &Result{Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if got := loaded(t, s, r.UID); got.Usage != nil {
		t.Fatal("recovery recollected an already finished run", got.Usage)
	}
}

func TestWorkerUsagePreservesFrozenHandoff(t *testing.T) {
	m, s, r := usageFixture(t)
	writeUsageCodex(t, s, r)
	if _, err := s.Transition(r.UID, func(r *Run) error { r.Phase = HandedOff; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := m.settle(t.Context(), r.UID, nil); err != nil {
		t.Fatal(err)
	}
	got := loaded(t, s, r.UID)
	if got.Phase != HandedOff || got.EndedAt == nil || got.Steps[0].Usage != nil {
		t.Fatal("usage altered the frozen handoff", got)
	}
}

func TestWorkerUsageDiscardsLookupAfterHandoff(t *testing.T) {
	m, s, r := usageFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// A control request can hand off the run while optional I/O is in flight.
		if _, err := s.Transition(r.UID, func(r *Run) error {
			r.Phase = HandedOff
			r.EndedAt = &settleTime
			return nil
		}); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		fmt.Fprint(w, `{"has_token_data":true,"breakdown":[{"timestamp":"2026-01-02T03:59:30Z","input_tokens":120}]}`)
	}))
	defer srv.Close()
	configureUsageServer(t, srv)
	if err := m.settle(t.Context(), r.UID, nil); err != nil {
		t.Fatal(err)
	}
	got := loaded(t, s, r.UID)
	if got.Phase != HandedOff || got.EndedAt == nil || got.Steps[0].Usage != nil {
		t.Fatal("usage altered the frozen handoff", got)
	}
}

func observedSummary(t *testing.T, s *Store, uid string) map[string]any {
	t.Helper()
	for _, op := range loaded(t, s, uid).Ops {
		if op.Kind != "observe" {
			continue
		}
		var p struct {
			Observation struct {
				Summary map[string]any `json:"summary"`
			}
		}
		if err := json.Unmarshal(op.Payload, &p); err != nil {
			t.Fatal(err)
		}
		return p.Observation.Summary
	}
	t.Fatal("settlement queued no observation")
	return nil
}

func TestSettledObservationReportsRunTokens(t *testing.T) {
	m, s, r := usageFixture(t)
	writeUsageCodex(t, s, r)
	if err := m.settle(t.Context(), r.UID, &Result{Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	// Kata's input count covers every prompt token, cached or not.
	if got := observedSummary(t, s, r.UID); got["input_tokens"] != float64(100) || got["output_tokens"] != float64(17) {
		t.Fatal(got)
	}
}

func TestSettledObservationOmitsUnknownTokens(t *testing.T) {
	m, s, r := usageFixture(t)
	if err := m.settle(t.Context(), r.UID, &Result{Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	got := observedSummary(t, s, r.UID)
	if _, ok := got["input_tokens"]; ok {
		t.Fatal(got)
	}
	if _, ok := got["output_tokens"]; ok {
		t.Fatal(got)
	}
}

func TestSettledObservationOmitsPartialTokens(t *testing.T) {
	m, s, r := usageFixture(t)
	known := usageFixtureKnown()
	if _, err := s.Transition(r.UID, func(r *Run) error {
		r.Steps[0].Usage = &known
		r.Usage = &known
		at := *r.Steps[0].SubmittedAt
		r.Steps = append(r.Steps, StepState{Key: "check", Kind: "prompt", Attempt: 1, Submission: Accepted, SubmittedAt: &at, Status: "succeeded"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.settle(t.Context(), r.UID, &Result{Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	got := observedSummary(t, s, r.UID)
	if _, ok := got["input_tokens"]; ok {
		t.Fatal("reported a total missing an attempt", got)
	}
	if u := savedUsage(t, s, r.UID); u["InputTokens"] != float64(40) {
		t.Fatal("local usage total changed", u)
	}
}

func usageFixtureKnown() usage.Usage {
	return usage.Usage{InputTokens: 40, OutputTokens: 9, Found: true, Source: "codex"}
}

func TestHandoffObservationOmitsTokens(t *testing.T) {
	m, s, r := usageFixture(t)
	writeUsageCodex(t, s, r)
	if err := m.collectStepUsage(t.Context(), r.UID, 0); err != nil {
		t.Fatal(err)
	}
	if err := m.settle(t.Context(), r.UID, &Result{Status: "needs-human", Summary: "Choose the release branch."}); err != nil {
		t.Fatal(err)
	}
	if u := savedUsage(t, s, r.UID); u["InputTokens"] != float64(30) {
		t.Fatal("fixture usage was not collected", u)
	}
	got := observedSummary(t, s, r.UID)
	if _, ok := got["input_tokens"]; ok {
		t.Fatal("handoff reported tokens", got)
	}
	if _, ok := got["output_tokens"]; ok {
		t.Fatal("handoff reported tokens", got)
	}
}

// The agent's CODEX_HOME can come from the Herdr server alone. Usage follows
// the launch environment the probe saw, not the worker's own CODEX_HOME.
func TestWorkerUsageFollowsAgentCodexHome(t *testing.T) {
	m, s, r := usageFixture(t)
	writeUsageCodex(t, s, r)
	agentHome := os.Getenv("CODEX_HOME")
	t.Setenv("CODEX_HOME", t.TempDir())
	m.environment = homesLaunch{"CODEX_HOME": agentHome}
	if err := m.settle(t.Context(), r.UID, &Result{Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if u := savedUsage(t, s, r.UID); u["Found"] != true || u["OutputTokens"] != float64(17) {
		t.Fatal(u)
	}
}

type homesLaunch map[string]string

func (homesLaunch) Check(context.Context, *herdr.Client, func(context.Context) (string, error), map[string]string, []string, string, string) (herdr.ServerID, error) {
	return herdr.ServerID{}, nil
}
func (h homesLaunch) AgentHomes() map[string]string { return h }
