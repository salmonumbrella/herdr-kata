package main

import (
	"bytes"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
	var out bytes.Buffer
	code := run([]string{"--config", config, "--state", l.Root, "run", "01J00000000000000000000010", "--input", "manual context"}, &out)
	if code != 0 {
		t.Fatalf("run %d: %s", code, &out)
	}
	requests, err := (journal.Requests{Dir: l.Requests()}).Pending()
	if err != nil || len(requests) != 1 || requests[0].Input != "manual context" || requests[0].JobUID != "01J00000000000000000000010" {
		t.Fatal(requests, err)
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
func TestLogsReadsReportedRun(t *testing.T) {
	config, l := operatorFixture(t)
	r := localRun(t, l, "reported")
	r.Attempts = []journal.Attempt{{Step: "main", N: 1}}
	if err := journal.New(l).Update(r.Key, func(saved *journal.Run) error { saved.Attempts = r.Attempts; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteFile(filepath.Join(l.Attempt(r.Key, "main", 1), "output.log"), []byte("fixture output\n")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := run([]string{"--config", config, "--state", l.Root, "logs", r.RunUID}, &out); code != 0 || out.String() != "fixture output\n" {
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
	var out bytes.Buffer
	if code := run([]string{"--config", config, "--state", l.Root, "run", "01J00000000000000000000010", "--input", ""}, &out); code != 0 {
		t.Fatalf("run %d %s", code, &out)
	}
	requests, err := (journal.Requests{Dir: l.Requests()}).Pending()
	if err != nil || len(requests) != 1 || !requests[0].InputSet || requests[0].Input != "" {
		t.Fatal(requests, err)
	}
}
