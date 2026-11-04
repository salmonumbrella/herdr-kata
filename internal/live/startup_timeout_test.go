//go:build live

package live

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/worker"
)

func startupTimeout(t *testing.T, bin, coreBin string) {
	for _, reason := range []string{"agent_not_started", "timeout"} {
		t.Run(reason, func(t *testing.T) {
			f := newFixture(t, bin, coreBin, "")
			script := filepath.Join(f.work, "blocked-agent.sh")
			write(t, script, []byte("#!/bin/sh\ntrap '' INT\nprintf 'Hooks need review\\n'\nexec /bin/sleep 600\n"))
			f.configure([]string{"/bin/sh", script})
			uid := "01J00000000000000000000010"
			f.createJob(uid, map[string]any{"kind": "execute", "prompt": "Complete the scratch task."})
			cfg, err := config.Load(f.config)
			if err != nil {
				t.Fatal(err)
			}
			if reason == "timeout" {
				cfg.Limits.DefaultTimeout = time.Minute
			}
			client := kata.New(f.kataBin, "fixture", cfg.Kata.Env, "example-project", "worker")
			w := worker.New(*cfg, f.store, f.h, map[string]kata.Kata{"example-project": client}, f.bin)
			queue := journal.Requests{Dir: f.store.Layout.Requests()}
			if err = queue.Write(journal.Request{ID: "first", Kind: "run", JobUID: uid}); err != nil {
				t.Fatal(err)
			}
			at := time.Now().UTC()
			tick := func(at time.Time) {
				t.Helper()
				f.verifyCore()
				if err := w.Tick(context.Background(), at); err != nil {
					t.Fatal(err)
				}
			}
			tick(at)
			runs, err := f.store.All()
			if err != nil || len(runs) != 1 || len(runs[0].Attempts) != 1 {
				t.Fatalf("missing launch: %v %v", runs, err)
			}
			r := runs[0]
			// Wait only for the scratch script's observable output, never the
			// two-minute product window, which uses the injected tick clock.
			deadline := time.NewTimer(10 * time.Second)
			defer deadline.Stop()
			poll := time.NewTicker(time.Second)
			defer poll.Stop()
			for {
				text, _, err := f.h.Read(context.Background(), r.Attempts[0].Address, 200)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(text, "Hooks need review") {
					break
				}
				select {
				case <-deadline.C:
					t.Fatal("scratch startup dialog never appeared")
				case <-poll.C:
				}
			}
			// hc interrupt can lose its acknowledgement for an unreported
			// process. Durable stop intent must still allow the next tick to
			// confirm/kill and settle it.
			f.verifyCore()
			if err := w.Tick(context.Background(), at.Add(2*time.Minute)); err != nil {
				saved, loadErr := f.store.Load(r.Key)
				if loadErr != nil || saved.Attempts[0].StopReason != reason || saved.Phase != "running" {
					t.Fatalf("stop intent missing after interrupt error: %v %v", err, loadErr)
				}
				t.Log("interrupt acknowledgement unavailable; resuming durable stop intent")
			}
			tick(at.Add(130 * time.Second))
			tick(at.Add(131 * time.Second))
			saved, err := f.store.Load(r.Key)
			if err != nil || saved.Phase != "reported" || saved.Outcome != "failed" || saved.Attempts[0].StopReason != reason {
				t.Fatalf("stop not settled: %+v %v", saved, err)
			}
			var observed struct {
				Run struct {
					Status  string
					Summary struct{ Message string }
				}
			}
			if err := json.Unmarshal(f.kata(nil, "cron", "run", "show", r.RunUID), &observed); err != nil {
				t.Fatal(err)
			}
			if observed.Run.Status != "failed" || observed.Run.Summary.Message != "reason: "+reason {
				t.Fatalf("wrong Kata observation: %+v", observed)
			}
			var issue struct{ Issue struct{ Status string } }
			if err := json.Unmarshal(f.kata(nil, "show", saved.IssueUID), &issue); err != nil {
				t.Fatal(err)
			}
			if issue.Issue.Status != "open" {
				t.Fatalf("adapter changed issue status: %s", issue.Issue.Status)
			}
			if _, err := f.h.Get(context.Background(), saved.Attempts[0].Address); !errors.Is(err, hc.ErrNotFound) {
				t.Fatalf("stopped terminal not removed: %v", err)
			}
			if text := string(f.cli("logs", r.RunUID)); !strings.Contains(text, "Hooks need review") {
				t.Fatal("startup dialog missing from retained logs")
			}
			if err := queue.Write(journal.Request{ID: "second", Kind: "run", JobUID: uid}); err != nil {
				t.Fatal(err)
			}
			tick(at.Add(132 * time.Second))
			runs, err = f.store.All()
			if err != nil || len(runs) != 2 {
				t.Fatalf("next run not admitted: %v %v", runs, err)
			}
			found := false
			for _, next := range runs {
				if next.Key != r.Key && next.Phase == "running" && len(next.Attempts) == 1 && next.Attempts[0].Address != "" {
					found = true
				}
			}
			if !found {
				t.Fatal("next run did not launch")
			}
			t.Logf("%s: failed observation, retained dialog and next run launch verified", reason)
		})
	}
}
