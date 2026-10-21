package main

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestLogsMissingRunSummarizesLocalError(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	var out, stderr bytes.Buffer
	if code := run([]string{"logs", "01K00000000000000000000000"}, &out, &stderr); code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "doctor") || strings.Contains(stderr.String(), root) || strings.Contains(stderr.String(), "lstat") {
		t.Fatal(code, out.String(), stderr.String())
	}
}

type unreadableRunDirectory struct {
	state.OSFS
	path string
}

func (f unreadableRunDirectory) Lstat(path string) (fs.FileInfo, error) {
	if path == f.path {
		return nil, &os.PathError{Op: "lstat", Path: path, Err: fs.ErrPermission}
	}
	return f.OSFS.Lstat(path)
}

func TestAbandonUnreadableRunSummarizesWithoutAcknowledging(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	const uid = "01K00000000000000000000000"
	if err := os.MkdirAll(l.RunDir(uid), 0700); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := abandonWithFS([]string{uid, "--local"}, &out, &stderr, unreadableRunDirectory{path: l.RunDir(uid)})
	if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "doctor") || strings.Contains(stderr.String(), l.Root) || strings.Contains(stderr.String(), "lstat") {
		t.Fatal(code, out.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(l.QuarantineRunDir(uid), "acknowledged.json")); !os.IsNotExist(err) {
		t.Fatal("unreadable run acknowledged", err)
	}
}

func TestAmbiguousRunJobNamesTheJobBeforeRemedy(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	f := state.OSFS{}
	if err := state.InitializeLayout(f, l); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Projects: []config.Project{{Name: "example-project", Actor: "worker"}, {Name: "other-project", Actor: "worker"}}}
	for _, project := range cfg.Projects {
		pl := l.Project(project.Name)
		if err := state.WriteJSON(f, pl.StateFile(), kata.Installation{Version: 1, ProjectUID: project.Name, Name: project.Name}); err != nil {
			t.Fatal(err)
		}
		if err := state.WriteJSON(f, pl.DefsSnapshot(), defs.Snapshot{Version: 2, Jobs: []defs.JobEntry{{UID: "job-" + project.Name, Name: "shared", Supported: true, Owner: defs.Adapter}}}); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := resolveRunJob(context.Background(), cfg, f, l, "shared", "")
	if err == nil || err.Error() != `ambiguous job "shared"; select --project` {
		t.Fatal(err)
	}
}

func TestAbandonUnknownKataRunExplainsSelectedProject(t *testing.T) {
	for _, inbox := range []string{"", "worker/inbox"} {
		t.Run("inbox="+inbox, func(t *testing.T) {
			root := t.TempDir()
			l := state.Layout{Root: filepath.Join(root, "state")}
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", root)
			t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
			bin := filepath.Join(root, "kata")
			script := `#!/bin/sh
case "$*" in
 *" health"*) printf '%s\n' '{"version":"0.19.0","schema_version":33}' ;;
 *"cron capabilities"*) printf '%s\n' '{"project_uid":"project","event_features":["cron_v1"]}' ;;
 *"federation identity"*) printf '%s\n' '{"instance_uid":"instance","auth":{"actor":"worker"}}' ;;
 *"daemon diagnose"*) printf '%s\n' '{"state":"ready","endpoint":"local","project_uid":"project"}' ;;
 *"list --status all --label herdr-kata:lease"*) printf '%s\n' '{"issues":[]}' ;;
 *"cron run show 01K00000000000000000000000"*) printf '%s\n' '{"error":{"kind":"not_found","message":"cron not found","exit_code":4}}'; exit 4 ;;
 *"inbox --for worker/inbox"*) printf '%s\n' '{"requests":[]}' ;;
 *) printf '%s\n' "$*" >> "$0.unexpected"; exit 99 ;;
esac
`
			if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			cfgPath := filepath.Join(root, "config.toml")
			text := fmt.Sprintf("session=\"work\"\nexecutor=\"worker\"\n[kata]\nbin=%q\nproject=\"example-project\"\nactor=\"worker\"\ninbox=%q\n[profiles.default]\nkind=\"codex\"\n", bin, inbox)
			if err := os.WriteFile(cfgPath, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := state.InitializeLayout(state.OSFS{}, l); err != nil {
				t.Fatal(err)
			}
			k := kata.New(cfg.Kata, l.Root, nil, nil)
			if err := kata.NewRouteGuard(*cfg, k, state.OSFS{}, l.Project("project")).Initialize(t.Context()); err != nil {
				t.Fatal(err)
			}
			snapshot := func() map[string]string {
				t.Helper()
				files := map[string]string{}
				err := filepath.WalkDir(l.Root, func(path string, entry fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if entry.IsDir() {
						files[path] = "directory"
						return nil
					}
					body, err := os.ReadFile(path)
					files[path] = string(body)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				return files
			}
			before := snapshot()
			var out, stderr bytes.Buffer
			code := run([]string{"abandon", "01K00000000000000000000000", "--project", "example-project"}, &out, &stderr)
			if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), `run "01K00000000000000000000000" is unavailable in project "example-project"`) || strings.Contains(stderr.String(), "cron not found") {
				t.Error(code, out.String(), stderr.String())
			}
			if after := snapshot(); !reflect.DeepEqual(before, after) {
				t.Errorf("unknown run changed local state: before=%v after=%v", before, after)
			}
			if body, err := os.ReadFile(bin + ".unexpected"); !os.IsNotExist(err) {
				t.Errorf("unexpected Kata command: %s (%v)", body, err)
			}
		})
	}
}

func TestStatusUnreadableControlResultExplainsRecovery(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	if err := state.InitializeLayout(state.OSFS{}, l); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(l.Root, "request-results", "request.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	const body = `{"version":1,"run_uid":"run","future_field":true}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for _, asJSON := range []bool{false, true} {
		var out, stderr bytes.Buffer
		code := statusCommand(l, asJSON, false, &out, &stderr)
		if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "saved control results could not be read") || !strings.Contains(stderr.String(), "doctor") || !strings.Contains(stderr.String(), "preserve") {
			t.Fatal(code, out.String(), stderr.String())
		}
	}
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != body {
		t.Fatal("status changed control result", string(saved), err)
	}
}

func TestStatusControlResultsDirectoryErrorKeepsNeutralAdvice(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	if err := state.InitializeLayout(state.OSFS{}, l); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(l.Root, "request-results")
	if err := os.WriteFile(path, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, asJSON := range []bool{false, true} {
		var out, stderr bytes.Buffer
		code := statusCommand(l, asJSON, false, &out, &stderr)
		if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "doctor") || strings.Contains(stderr.String(), "newer build") {
			t.Fatal(code, out.String(), stderr.String())
		}
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "not a directory" {
		t.Fatal("status changed unreadable result directory", string(body), err)
	}
}
