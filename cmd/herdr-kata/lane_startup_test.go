package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/lease"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// A live lease of another installation stops up before it starts anything,
// with one read and no writes. up --takeover writes the next generation of
// the lease for this installation, on a checked route, before it starts.
func TestUpRespectsLiveForeignLeaseAndTakeoverOutranksIt(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", root)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(root, "sessions", "work", "herdr.sock"))
	bin := filepath.Join(root, "kata")
	heartbeat := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	calls := filepath.Join(root, "calls")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$*" in
 *" health") printf '%%s\n' '{"version":"test","schema_version":33}' ;;
 *"cron capabilities") printf '%%s\n' '{"project_uid":"01K00000000000000000000001","event_features":["cron_v1"]}' ;;
 *"federation identity") printf '%%s\n' '{"instance_uid":"instance","auth":{"actor":"worker"}}' ;;
 *"daemon diagnose"*) printf '%%s\n' '{"state":"ready","endpoint":"local","project_uid":"01K00000000000000000000001"}' ;;
 *"list --status all --label herdr-kata:lease"*) printf '%%s\n' '{"issues":[{"uid":"01K0000000000000000000LEAS","status":"closed","revision":3,"labels":["herdr-kata:lease"],"metadata":{"herdr_kata.lease":{"version":1,"owner":"elsewhere","generation":7,"heartbeat":"%s"}}}]}' ;;
 *"meta set --if-match 3 --json-value -- 01K0000000000000000000LEAS herdr_kata.lease"*) printf '%%s\n' '{"issue":{"uid":"01K0000000000000000000LEAS","revision":4}}' ;;
 *) exit 9 ;;
esac
`, calls, heartbeat)
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`session = "work"
executor = "here"
[kata]
bin = %q
[[projects]]
name = "example-project"
actor = "worker"
[profiles.default]
kind = "codex"
`, bin)
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"up"}, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "another installation (elsewhere) owns this lane") || !strings.Contains(stderr.String(), "up --takeover") {
		t.Fatal(code, stderr.String())
	}
	got, _ := os.ReadFile(calls)
	if lines := strings.Split(strings.TrimSpace(string(got)), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "list --status all --label herdr-kata:lease") {
		t.Fatalf("refusal made calls other than one lease read:\n%s", got)
	}
	l := state.Layout{Root: state.DefaultRoot()}
	if _, err := os.Stat(l.ServeInfo()); !os.IsNotExist(err) {
		t.Fatal("refused startup launched a supervisor", err)
	}

	// Prevent a supervisor from starting after the takeover; the staging
	// boundary refuses this non-directory once the lease is written.
	if err := os.MkdirAll(l.Root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.BinDir(), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	if code := run([]string{"up", "--takeover"}, &out, &stderr); code != 1 {
		t.Fatal("expected staging refusal after the takeover", code, stderr.String())
	}
	got, _ = os.ReadFile(calls)
	var written lease.Lease
	for line := range strings.SplitSeq(string(got), "\n") {
		if _, value, ok := strings.Cut(line, "herdr_kata.lease "); ok && strings.Contains(line, "meta set") {
			if err := json.Unmarshal([]byte(value), &written); err != nil {
				t.Fatal(err)
			}
		}
	}
	if written.Owner != "here" || written.Generation != 8 || !strings.Contains(string(got), "federation identity") {
		t.Fatalf("takeover lease %+v; calls:\n%s", written, got)
	}
}
