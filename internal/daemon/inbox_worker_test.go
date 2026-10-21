package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/inbox"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestWorkerInboxCapabilityGate(t *testing.T) {
	for _, tc := range []struct {
		name, inbox string
		features    []string
		want        int32
		wantStartup string
	}{
		{"transient-capability", "worker/inbox", []string{"cron_v1"}, 1, ""},
		{"configured", "worker/inbox", []string{"cron_v1"}, 1, ""},
		{"additional-features", "worker/inbox", []string{"cron_v1", "unrelated_feature"}, 1, ""},
		{"missing-capability", "worker/inbox", []string{"unrelated_feature"}, 0, "cron_v1"},
		{"unconfigured", "", []string{"cron_v1"}, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := state.Layout{Root: t.TempDir()}
			cfg := config.Config{Session: "test", Executor: "test", Kata: config.Kata{Bin: "/bin/kata", Project: "test", Actor: "worker", Inbox: tc.inbox}, Limits: config.Limits{MaxActiveRuns: 2, DefaultTimeout: time.Hour}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}
			var calls atomic.Int32
			var failedCaps atomic.Bool
			leases := &hubLeases{}
			runner := func(_ context.Context, argv, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
				cmd := strings.Join(argv[8:], " ")
				if out, code, ok := leases.reply("01K00000000000000000000001", argv[8:]); ok {
					return out, nil, code, nil
				}
				var v any
				switch {
				case cmd == "health":
					v = kata.Health{Version: "test", SchemaVersion: 33}
				case cmd == "cron capabilities":
					if tc.name == "transient-capability" {
						if _, err := os.Stat(l.Project("01K00000000000000000000001").StateFile()); err == nil && !failedCaps.Swap(true) {
							return []byte(`{"error":{"code":"transport","message":"temporary capability failure"}}`), nil, 7, nil
						}
					}
					v = kata.Capabilities{ProjectUID: "01K00000000000000000000001", EventFeatures: tc.features}
				case cmd == "federation identity":
					v = map[string]any{"instance_uid": "instance", "auth": map[string]string{"actor": "worker"}}
				case strings.HasPrefix(cmd, "daemon diagnose"):
					v = map[string]string{"state": "ready", "endpoint": "local", "project_uid": "01K00000000000000000000001", "federation_role": "hub"}
				case cmd == "cron job list":
					v = map[string]any{"jobs": []kata.Job{}}
				case cmd == "cron workflow list":
					v = map[string]any{"workflows": []kata.Workflow{}}
				case strings.HasPrefix(cmd, "inbox --for"):
					calls.Add(1)
					v = map[string]any{"requests": []kata.InboxRequest{}}
				default:
					t.Errorf("unexpected call: %s", cmd)
					return nil, nil, 1, errors.New("unexpected call")
				}
				b, err := json.Marshal(v)
				return b, nil, 0, err
			}
			starter := func(ctx context.Context, _, _ []string, _ string) (kata.Proc, error) {
				r, w := io.Pipe()
				done := make(chan struct{})
				go func() { <-ctx.Done(); w.Close(); close(done) }()
				return kata.Proc{Stdout: r, Stderr: strings.NewReader(""), Wait: func() error { <-done; return nil }}, nil
			}
			k := kata.New(cfg.Kata, l.Root, runner, starter)
			duration := 2400 * time.Millisecond
			if tc.want > 0 {
				duration = 10 * time.Second
			}
			ctx, cancel := context.WithTimeout(t.Context(), duration)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- workerWithCLI(ctx, cfg, l, k) }()
			if tc.want > 0 {
				for ctx.Err() == nil {
					snapshot, err := inbox.ReadSnapshot(state.OSFS{}, l.Project("01K00000000000000000000001").Root)
					if err == nil && !snapshot.LastSuccessfulScan.IsZero() {
						cancel()
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			if err := <-done; err != nil && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if tc.wantStartup != "" {
				problems, err := readStartupErrors(l)
				if err != nil || !strings.Contains(problems[cfg.Kata.Project], tc.wantStartup) {
					t.Fatalf("startup refusal = %v, %v; want %q", problems, err, tc.wantStartup)
				}
			}
			if calls.Load() != tc.want {
				t.Fatal("inbox scans", calls.Load(), "want", tc.want)
			}
			if tc.want > 0 {
				v, err := inbox.ReadSnapshot(state.OSFS{}, l.Project("01K00000000000000000000001").Root)
				if err != nil || v.LastSuccessfulScan.IsZero() {
					t.Fatal(v, err)
				}
			}
		})
	}
}

func TestStatusReadsInboxSnapshot(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	pl := statusTestLane(t, l)
	v := inbox.Snapshot{Version: 1, LastSuccessfulScan: time.Now().UTC(), Error: "last scan failed", Requests: []inbox.Request{{Ref: "test#task", Status: inbox.Unsupported, Reason: "foreign owner"}}}
	if err := inbox.WriteSnapshot(state.OSFS{}, pl.Root, v); err != nil {
		t.Fatal(err)
	}
	s, err := ReadStatus(l)
	if err != nil || s.Inbox == nil || s.Inbox.Error != v.Error || len(s.Inbox.Requests) != 1 || s.Inbox.Requests[0].Reason != "foreign owner" {
		t.Fatal(s, err)
	}
}
