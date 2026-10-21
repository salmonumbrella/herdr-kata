//go:build kata || kata_replies || kata_children

package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func startKataDaemon(t *testing.T, bin, home string, env []string, project string, extra ...string) func(...string) []byte {
	t.Helper()
	raw := func(args ...string) []byte {
		t.Helper()
		// Cleanup runs after t.Context is canceled. Setup and cleanup both use
		// fresh bounded contexts so cleanup can still stop the isolated daemon.
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		argv := append(args, "--json", "--as", "worker", "--project", project)
		argv = append(argv, extra...)
		cmd := exec.CommandContext(ctx, bin, argv...)
		cmd.Env = append(env, "GOMAXPROCS=2")
		cmd.Dir = home
		out, err := cmd.Output()
		if err != nil {
			var stderr []byte
			if exit, ok := errors.AsType[*exec.ExitError](err); ok {
				stderr = exit.Stderr
			}
			t.Fatalf("kata %v: %v\nstdout: %s\nstderr: %s", args, err, out, stderr)
		}
		return out
	}
	raw("daemon", "start")
	t.Cleanup(func() { raw("daemon", "stop") })
	raw("projects", "create", project)
	return raw
}

func createWorkflow(t *testing.T, raw func(...string) []byte, dir, name string, def defs.WorkflowDef) kata.Workflow {
	t.Helper()
	path := filepath.Join(dir, "workflow.json")
	envelope := struct {
		Name       string           `json:"name"`
		Definition defs.WorkflowDef `json:"definition"`
	}{Name: name, Definition: def}
	if err := state.WriteJSON(state.OSFS{}, path, envelope); err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Workflow kata.Workflow `json:"workflow"`
	}
	if err := json.Unmarshal(raw("cron", "workflow", "create", "--file", path), &reply); err != nil {
		t.Fatal(err)
	}
	return reply.Workflow
}

func createJob(t *testing.T, raw func(...string) []byte, dir, name string, def defs.JobDef) kata.Job {
	t.Helper()
	path := filepath.Join(dir, "job.json")
	envelope := struct {
		Name       string      `json:"name"`
		Definition defs.JobDef `json:"definition"`
	}{Name: name, Definition: def}
	if err := state.WriteJSON(state.OSFS{}, path, envelope); err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Job kata.Job `json:"job"`
	}
	if err := json.Unmarshal(raw("cron", "job", "create", "--file", path), &reply); err != nil {
		t.Fatal(err)
	}
	return reply.Job
}
