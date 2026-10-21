//go:build kata

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/lease"
	"github.com/salmonumbrella/herdr-kata/internal/migration"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"github.com/salmonumbrella/herdr-kata/internal/testenv"
)

// The ID migration must preserve retirement and final-report recovery even
// when the last configured lane was removed before the first new-ID start.
func TestMigrationRecoversFinalReportsForRemovedLane(t *testing.T) {
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	root := t.TempDir()
	oldConfig := filepath.Join(root, "config", "salmonumbrella.herdr-kata")
	old := state.Layout{Root: filepath.Join(root, "state", "salmonumbrella.herdr-kata")}
	newConfig := filepath.Join(root, "config", "kata")
	migrated := state.Layout{Root: filepath.Join(root, "state", "kata")}
	if err := state.InitializeLayout(state.OSFS{}, old); err != nil {
		t.Fatal(err)
	}
	c := config.Config{Executor: "worker-runtime", Session: "work", Kata: config.Kata{Bin: bin, Project: "example-project", Actor: "worker", Env: map[string]string{"KATA_HOME": filepath.Join(root, "kata")}}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}
	k := kata.New(c.Kata, old.Root, nil, nil)
	raw := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, append(args, "--json", "--as", "worker")...)
		cmd.Env, cmd.Dir = append(k.Env(), "GOMAXPROCS=2"), root
		out, err := cmd.Output()
		if err != nil {
			var stderr []byte
			if exit, ok := err.(*exec.ExitError); ok {
				stderr = exit.Stderr
			}
			t.Fatalf("kata %v: %v %s %s", args, err, out, stderr)
		}
		return out
	}
	raw("daemon", "start")
	t.Cleanup(func() { raw("daemon", "stop") })
	raw("projects", "create", "example-project")
	identity, route, err := kata.ResolveIdentity(t.Context(), k)
	if err != nil {
		t.Fatal(err)
	}
	guard := kata.NewRouteGuard(c, k, state.OSFS{}, old.Project(identity.ProjectUID), identity.ProjectUID)
	if err := guard.InitializeResolved(t.Context(), identity, route); err != nil {
		t.Fatal(err)
	}
	hold, err := lease.Claim(t.Context(), k, c.Executor, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if hold.Lease().Generation != 1 {
		t.Fatalf("initial generation: %+v", hold.Lease())
	}
	path := filepath.Join(root, "job.json")
	if err := os.WriteFile(path, []byte(`{"name":"migration-job","definition":{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Do not execute removed work"},"issue":{"kind":"per-run","title":"Migration work"},"overlap":"forbid","catchup":"skip"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Job kata.Job `json:"job"`
	}
	if err := json.Unmarshal(raw("cron", "job", "create", "--project", "example-project", "--file", path), &reply); err != nil {
		t.Fatal(err)
	}
	store := runner.NewStore(state.OSFS{}, old)
	now := time.Now().UTC()
	finalUID := "01K00000000000000000000001"
	unfinishedUID := "01K00000000000000000000002"
	var finalPayload json.RawMessage
	for _, uid := range []string{finalUID, unfinishedUID} {
		issue, err := k.CreateIssue(t.Context(), kata.CreateReq{Title: "Migration work " + uid, IdempotencyKey: uid})
		if err != nil {
			t.Fatal(err)
		}
		running := kata.Observation{JobUID: reply.Job.UID, DefinitionEventUID: reply.Job.DefinitionEventUID, OccurrenceKey: "manual:" + uid, IssueUID: issue.UID, Actor: "worker", ExecutorLabel: c.Executor, Teammate: new("herdr-kata"), Status: "running", Summary: kata.Summary{Version: 1, Message: "admitted"}}
		if got, _, err := k.Observe(t.Context(), uid, running); err != nil || got.Revision != 1 || got.Status != "running" {
			t.Fatalf("admission: %+v %v", got, err)
		}
		r := &runner.Run{Version: 1, UID: uid, Target: runner.Target{Identity: identity, Route: route, Executor: c.Executor}, Job: reply.Job, Occurrence: running.OccurrenceKey, IssueUID: issue.UID, IssueRef: issue.QualifiedID, Phase: runner.Running, StartedAt: &now, PromptsSent: 1, LeaseGeneration: 1}
		if uid == finalUID {
			r.Phase, r.Outcome, r.EndedAt = runner.Settled, "failed", &now
			r.Inspect = "operation effect not confirmed"
			final := running
			final.Status, final.Summary, final.ExpectedRevision, final.StartedAt, final.EndedAt = "failed", kata.Summary{Version: 1, Message: "saved terminal result"}, 1, &now, &now
			finalPayload, err = json.Marshal(runner.Payload{Observation: &final})
			if err != nil {
				t.Fatal(err)
			}
			r.Ops = []runner.Op{{ID: "herdr-kata:" + uid + ":settle:observe", Kind: "observe", Payload: finalPayload, Safety: kata.SafetyOf("observe"), FirstSent: &now, Inspect: r.Inspect}}
		}
		if err := store.Create(r); err != nil {
			t.Fatal(err)
		}
	}
	// Preserve a real cached definition; it must migrate but remain absent from
	// the operator's active-lane view after removal.
	if err := state.WriteJSON(state.OSFS{}, old.Project(identity.ProjectUID).DefsSnapshot(), defs.Snapshot{Version: 1, Jobs: []defs.JobEntry{{UID: reply.Job.UID, Name: reply.Job.Name, Definition: reply.Job.Definition, DefinitionEventUID: reply.Job.DefinitionEventUID, Supported: true}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(oldConfig, 0700); err != nil {
		t.Fatal(err)
	}
	text := fmt.Sprintf("session=\"work\"\nexecutor=\"worker-runtime\"\n[kata]\nbin=%q\nenv={ KATA_HOME=%q }\n[profiles.default]\nkind=\"codex\"\n", bin, c.Kata.Env["KATA_HOME"])
	if err := os.WriteFile(filepath.Join(oldConfig, "config.toml"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", newConfig)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", migrated.Root)
	if err := migration.Ensure(newConfig, migrated.Root); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ProjectEntries()) != 0 {
		t.Fatal("removed lane reconfigured")
	}
	h := herdrtest.New(t)
	t.Setenv("HERDR_SOCKET_PATH", h.Path)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Worker(ctx, *cfg, migrated) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	})
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for {
		got, err := k.ShowRun(t.Context(), finalUID)
		if err != nil {
			t.Fatal(err)
		}
		var handed runner.Run
		if err := state.ReadJSON(state.OSFS{}, migrated.RunFile(unfinishedUID), 4<<20, &handed); err != nil {
			t.Fatal(err)
		}
		retired, err := k.ShowRun(t.Context(), unfinishedUID)
		if err != nil {
			t.Fatal(err)
		}
		current, found, err := lease.Read(t.Context(), k)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "failed" && retired.Status == "unknown" && handed.Phase == runner.HandedOff && found && current.Owner == "" {
			if got.Revision != 2 || retired.Revision != 2 || got.Summary.Message != "saved terminal result" || current.Generation != 1 || handed.PromptsSent != 1 || !handed.SlotReleased {
				t.Fatalf("migration changed recovery evidence: final=%+v lease=%+v unfinished=%+v", got, current, handed)
			}
			break
		}
		select {
		case <-deadline.C:
			t.Fatalf("retirement/final recovery did not complete: final=%+v lease=%+v unfinished=%+v", got, current, handed)
		case <-ticker.C:
		}
	}
	var saved runner.Run
	if err := state.ReadJSON(state.OSFS{}, old.RunFile(finalUID), 4<<20, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Phase != runner.Settled || saved.Outcome != "failed" || saved.PromptsSent != 1 || len(saved.Ops) != 1 || !saved.Ops[0].Landed() || string(saved.Ops[0].Payload) != string(finalPayload) {
		t.Fatalf("frozen terminal journal changed: %+v", saved)
	}
	status, err := ReadStatus(migrated, *cfg)
	if err != nil || len(status.Projects) != 0 || len(status.Definitions) != 0 || len(status.Runs) != 0 {
		t.Fatalf("removed lane still exposed: %+v %v", status, err)
	}
	if calls := h.Requests(); len(calls) != 0 {
		t.Fatalf("removed lane touched an executor: %+v", calls)
	}
}
