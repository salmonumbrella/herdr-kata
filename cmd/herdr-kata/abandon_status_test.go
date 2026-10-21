package main

import (
	"bytes"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestStatusSurfacesLossReportInspection(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	const uid = "01K00000000000000000000000"
	if err := state.InitializeLayout(state.OSFS{}, l); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(l.Root, "abandoned", "reports", "runs", uid, "run.json")
	r := runner.Run{Version: 1, UID: uid, Source: "abandon-report", Phase: runner.Settled, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project"}}, IssueUID: "issue", Inspect: "kata validation: comment rejected", Ops: []runner.Op{{ID: "abandon:comment", Kind: "comment", Inspect: "comment rejected"}}}
	if err := state.WriteJSON(state.OSFS{}, path, r); err != nil {
		t.Fatal(err)
	}
	for _, asJSON := range []bool{false, true} {
		var out, stderr bytes.Buffer
		if code := statusCommand(l, asJSON, false, &out, &stderr); code != 0 {
			t.Fatal(code, stderr.String())
		}
		for _, want := range []string{uid, "comment rejected", "herdr-kata stop", "abandoned/reports/quarantine/runs/", "resolve the issue separately"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("loss report missing %q in %s", want, out.String())
			}
		}
	}
}

func TestAbandonMissingIssueConfirmsRecordedLoss(t *testing.T) {
	root := t.TempDir()
	l := state.Layout{Root: filepath.Join(root, "state")}
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", root)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
	const uid = "01K00000000000000000000000"
	bin := filepath.Join(root, "kata")
	script := `#!/bin/sh
case "$*" in
 *" health"*) printf '%s\n' '{"version":"0.19.0","schema_version":33}' ;;
 *"cron capabilities"*) printf '%s\n' '{"project_uid":"project","event_features":["cron_v1"]}' ;;
 *"federation identity"*) printf '%s\n' '{"instance_uid":"instance","auth":{"actor":"worker"}}' ;;
 *"daemon diagnose"*) printf '%s\n' '{"state":"ready","endpoint":"local","project_uid":"project"}' ;;
 *"list --status all --label herdr-kata:lease"*) printf '%s\n' '{"issues":[]}' ;;
 *"cron run show 01K00000000000000000000000"*) printf '%s\n' '{"run":{"uid":"01K00000000000000000000000","job_uid":"job","definition_event_uid":"event","occurrence_key":"manual:01K00000000000000000000000","issue_uid":"issue","actor":"worker","executor_label":"installation","status":"unknown","revision":4,"summary":{"version":1,"message":"local state lost"}}}' ;;
 *"show -- issue"*) printf '%s\n' '{"error":{"kind":"issue_not_found","message":"issue gone","exit_code":4}}'; exit 4 ;;
 *) printf '%s\n' "$*" >&2; exit 9 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfgText := fmt.Sprintf("session=\"work\"\nexecutor=\"installation\"\n[kata]\nbin=%q\n[[projects]]\nname=\"example-project\"\nactor=\"worker\"\n[profiles.default]\nkind=\"codex\"\n", bin)
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(cfgText), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(root, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	view, err := cfg.SelectProject("example-project")
	if err != nil {
		t.Fatal(err)
	}
	k := kata.New(view.Kata, l.Root, nil, nil)
	if err := state.InitializeLayout(state.OSFS{}, l); err != nil {
		t.Fatal(err)
	}
	if err := kata.NewRouteGuard(view, k, state.OSFS{}, l.Project("project")).Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"abandon", uid, "--project", "example-project"}, &out, &stderr); code != 0 || !strings.Contains(out.String(), "unknown (local state lost)") || !strings.Contains(out.String(), "issue is gone") {
		t.Fatal(code, out.String(), stderr.String())
	}
	if _, err := os.Stat(l.RunFile(uid)); !os.IsNotExist(err) {
		t.Fatal("abandon recreated execution journal", err)
	}
}

func TestStatusSurfacesAutomaticallyQuarantinedLossReport(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	const uid = "01K00000000000000000000000"
	if err := state.InitializeLayout(state.OSFS{}, l); err != nil {
		t.Fatal(err)
	}
	reports := state.Layout{Root: filepath.Join(l.Root, "abandoned", "reports")}
	if err := os.MkdirAll(reports.RunDir(uid), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reports.RunFile(uid), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.NewStore(state.OSFS{}, reports).Load(uid); err == nil {
		t.Fatal("corruption hidden")
	}
	for _, asJSON := range []bool{false, true} {
		var out, stderr bytes.Buffer
		if code := statusCommand(l, asJSON, false, &out, &stderr); code != 0 {
			t.Fatal(code, stderr.String())
		}
		for _, want := range []string{uid, "automatically quarantined", "herdr-kata stop", reports.RunDir(uid), reports.QuarantineRunDir(uid), "resolve the issue separately"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("missing %q in %s", want, out.String())
			}
		}
	}
}
