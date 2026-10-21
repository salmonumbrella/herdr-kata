package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type startupRetryLog struct {
	slog.Handler
	entered, release chan struct{}
	once             sync.Once
}

func (h *startupRetryLog) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "project lane waiting" {
		h.once.Do(func() { close(h.entered); <-h.release })
	}
	return nil
}

// A failed initialization must not expose the lane to the active write queue.
// Hold the retry producer at its logger while the queue is free to make progress.
func TestWorkerPublishesQueueAfterPinInitialization(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(l.Root, "absent.sock"))
	cfg := config.Config{Session: "work", Executor: "worker", Kata: config.Kata{Bin: "/bin/kata", Project: "example-project", Actor: "worker"}, Limits: config.Limits{MaxActiveRuns: 2, DefaultTimeout: time.Hour}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}
	if err := state.InitializeLayout(state.OSFS{}, l); err != nil {
		t.Fatal(err)
	}
	route := kata.Route{Bin: "/bin/kata", Env: map[string]string{}, Endpoint: "local", DiagnoseState: "ready"}
	pin := kata.Installation{Version: 1, CreatedAt: time.Now(), Executor: cfg.Executor, InstanceUID: "instance", ProjectUID: "project", Actor: "worker", Route: route}
	if e := state.WriteJSON(state.OSFS{}, l.Project("project").StateFile(), pin); e != nil {
		t.Fatal(e)
	}
	payload, _ := json.Marshal(runner.Payload{Ref: "example-project#task", Body: "pending report"})
	now := time.Now()
	saved := &runner.Run{Version: 1, UID: "01K00000000000000000000000", Phase: runner.HandedOff, Admitted: new(true), SlotReleased: true, EndedAt: &now, Target: runner.Target{Identity: kata.Identity{InstanceUID: "instance", ProjectUID: "project", Actor: "worker"}, Executor: cfg.Executor, Route: route}, Ops: []runner.Op{{ID: "report", Kind: "comment", Key: "report-key", Payload: payload, Safety: kata.SafetyOf("comment")}}}
	if e := runner.NewStore(state.OSFS{}, l).Create(saved); e != nil {
		t.Fatal(e)
	}
	gate := &startupRetryLog{Handler: slog.NewTextHandler(io.Discard, nil), entered: make(chan struct{}), release: make(chan struct{})}
	original := slog.Default()
	slog.SetDefault(slog.New(gate))
	defer slog.SetDefault(original)
	var healthy atomic.Bool
	reports := make(chan string, 4)
	leases := &hubLeases{}
	cliRunner := func(_ context.Context, args, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
		command := strings.Join(args, " ")
		if out, code, ok := leases.reply("project", args[8:]); ok {
			return out, nil, code, nil
		}
		var response string
		switch {
		case strings.HasSuffix(command, " health"):
			response = `{"version":"test","schema_version":33}`
		case strings.Contains(command, "cron capabilities"):
			response = `{"project_uid":"project","event_features":["cron_v1"]}`
		case strings.Contains(command, "federation identity"):
			// A different instance resolves the lane but fails its pin.
			response = `{"instance_uid":"other-instance","auth":{"actor":"worker"}}`
			if healthy.Load() {
				response = `{"instance_uid":"instance","auth":{"actor":"worker"}}`
			}
		case strings.Contains(command, "daemon diagnose"):
			response = `{"state":"ready","endpoint":"local","project_uid":"project"}`
		case strings.Contains(command, " comment "):
			var current kata.Installation
			if e := state.ReadJSON(state.OSFS{}, l.Project("project").StateFile(), 65536, &current); e != nil {
				t.Error(e)
			}
			reports <- current.Name
			response = `{"comment":{"uid":"report"}}`
		case strings.Contains(command, "cron job list"):
			response = `{"jobs":[]}`
		case strings.Contains(command, "cron workflow list"):
			response = `{"workflows":[]}`
		default:
			t.Errorf("unexpected Kata call: %s", command)
			return nil, nil, 1, errors.New("unexpected call")
		}
		return []byte(response), nil, 0, nil
	}
	starter := func(ctx context.Context, _, _ []string, _ string) (kata.Proc, error) {
		r, w := io.Pipe()
		done := make(chan struct{})
		go func() { <-ctx.Done(); w.Close(); close(done) }()
		return kata.Proc{Stdout: r, Stderr: strings.NewReader(""), Wait: func() error { <-done; return nil }}, nil
	}
	k := kata.New(cfg.Kata, l.Root, cliRunner, starter)
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	var once sync.Once
	unblock := func() { once.Do(func() { close(gate.release) }) }
	done := make(chan error, 1)
	go func() { done <- workerWithCLI(ctx, cfg, l, k) }()
	defer func() {
		unblock()
		cancel()
		if e := <-done; e != nil {
			t.Error(e)
		}
	}()
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal("initialization did not fail")
	}
	healthy.Store(true)
	early := false
	select {
	case name := <-reports:
		early = true
		t.Errorf("queue delivered before initialization completed, pin name=%q", name)
	case <-time.After(3 * time.Second):
	}
	unblock()
	if !early {
		select {
		case name := <-reports:
			if name != "example-project" {
				t.Fatal("queue used an uninitialized pin", name)
			}
		case <-ctx.Done():
			t.Fatal("initialized lane did not deliver")
		}
	}
}
