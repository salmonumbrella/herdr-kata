//go:build live

package live

import (
	"context"
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
)

func liveWake(t *testing.T, bin, coreBin, script string) {
	f := newFixture(t, bin, coreBin, "")
	write(t, filepath.Join(f.work, "wait"), nil)
	f.verifyCore()
	term, err := f.h.Run(context.Background(), hc.RunSpec{Name: "worker", Cwd: f.work, Argv: []string{"/usr/bin/env", "python3", script}})
	if err != nil {
		t.Fatal(err)
	}
	f.verifyCore()
	run(t, f.work, nil, coreBin, "label", "--json", term.Address, "herdr-kata.agent=worker")
	r := journal.Run{Version: 1, Key: journal.ManualKey("executor", "example-project", "wake"), RunUID: "01J00000000000000000000001", Project: "example-project", Phase: "running", ExpiresAt: time.Now().Add(time.Minute), Plan: defs.Plan{Agent: "worker", Name: "example-job", Steps: []defs.Step{{Key: "main", Kind: "prompt", Text: "Complete the task."}}}, Attempts: []journal.Attempt{{Step: "main", N: 1, Status: "queued"}}}
	if err = f.store.Create(r); err != nil {
		t.Fatal(err)
	}
	dir := f.store.Layout.Attempt(r.Key, "main", 1)
	body, err := prompt.Build(r, r.Plan.Steps[0], 1, "issue", "Example issue", "", filepath.Join(dir, "result.json"), bin, "")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "prompt.md"), []byte(body))
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	for {
		f.verifyCore()
		out, err := attempt.Wake(context.Background(), f.h, f.store, r.Key, 0, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		saved, err := f.store.Load(r.Key)
		if err != nil {
			t.Fatal(err)
		}
		if saved.Attempts[0].Status == "running" {
			_ = os.Remove(filepath.Join(f.work, "wait"))
		}
		switch v := out.(type) {
		case attempt.Held:
			t.Fatalf("wake held: %s", v.Reason)
		case attempt.Done:
			if v.Status != "succeeded" {
				t.Fatalf("wake result: %+v", v)
			}
			b, err := os.ReadFile(filepath.Join(f.work, "deliveries"))
			if err != nil || strings.TrimSpace(string(b)) != "delivered" {
				t.Fatalf("deliveries: %q %v", b, err)
			}
			live, err := f.h.Get(context.Background(), term.Address)
			if err != nil || live.State != "live" {
				t.Fatalf("borrowed terminal: %+v %v", live, err)
			}
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("wake timed out")
		case <-poll.C:
		}
	}
}
