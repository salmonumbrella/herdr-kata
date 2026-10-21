//go:build kata_children

package inbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/testenv"
)

func integrationKata(t *testing.T) (*kata.CLI, config.Config, func(...string) []byte) {
	t.Helper()
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	home := t.TempDir()
	cfg := bindingConfig()
	cfg.Kata = config.Kata{Bin: bin, Project: "inbox-test", Actor: "worker", Inbox: "worker/inbox", Env: map[string]string{"KATA_HOME": home}}
	k := kata.New(cfg.Kata, home, nil, nil)
	raw := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, append(args, "--json", "--as", "worker")...)
		cmd.Env, cmd.Dir = append(k.Env(), "GOMAXPROCS=2"), home
		out, err := cmd.Output()
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				out = append(out, exit.Stderr...)
			}
			t.Fatalf("kata %v: %v\n%s", args, err, out)
		}
		return out
	}
	raw("daemon", "start")
	t.Cleanup(func() { raw("daemon", "stop") })
	raw("projects", "create", cfg.Kata.Project)
	return k, cfg, raw
}

func TestInteg540ValidateRealChild(t *testing.T) {
	k, cfg, raw := integrationKata(t)
	caps, err := k.Capabilities(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !New(cfg, k, caps, func(context.Context, sched.Due) {}, nil).Enabled() {
		t.Fatal("native child capability missing")
	}
	parent, err := k.CreateIssue(t.Context(), kata.CreateReq{Title: "Recurring integration parent", Body: "Exercise native child metadata and binding."})
	if err != nil {
		t.Fatal(err)
	}
	parent, err = k.Claim(t.Context(), parent.UID, true)
	if err != nil {
		t.Fatal(err)
	}
	wfPath := filepath.Join(t.TempDir(), "workflow.json")
	if err := os.WriteFile(wfPath, []byte(`{"name":"Child check","definition":{"version":1,"steps":[{"key":"check","kind":"command","command":"true"}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var wr struct {
		Workflow kata.Workflow `json:"workflow"`
	}
	if err := json.Unmarshal(raw("cron", "workflow", "create", "--project", cfg.Kata.Project, "--file", wfPath), &wr); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"herdr_kata.workflow": "Child check", "herdr_kata.profile": "fast", "herdr_kata.checkout": "repo"} {
		if _, err := k.MetaSet(t.Context(), parent.UID, key, value, kata.Cond{}); err != nil {
			t.Fatal(err)
		}
	}
	jobPath := filepath.Join(t.TempDir(), "job.json")
	jobJSON := fmt.Sprintf(`{"name":"Native inbox check","definition":{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"interval","interval_seconds":60},"action":{"kind":"notify","recipient":"worker/inbox","message":"Native child due"},"issue":{"kind":"per-run","parent_uid":%q,"title":"Recurring child integration","body":"Verify native occurrence binding."},"overlap":"allow","catchup":"latest"}}`, parent.UID)
	if err := os.WriteFile(jobPath, []byte(jobJSON), 0600); err != nil {
		t.Fatal(err)
	}
	var jr struct {
		Job kata.Job `json:"job"`
	}
	if err := json.Unmarshal(raw("cron", "job", "create", "--project", cfg.Kata.Project, "--file", jobPath), &jr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	for {
		requests, err := k.Inbox(ctx, cfg.Kata.Inbox)
		if err != nil {
			t.Fatal(err)
		}
		for _, request := range requests {
			child, err := k.ShowIssue(ctx, request.Ref)
			if err != nil {
				t.Fatal(err)
			}
			if child.ParentUID != parent.UID {
				continue
			}
			rows, err := k.Ready(ctx)
			if err != nil {
				t.Fatal(err)
			}
			ready := map[string]bool{}
			for _, i := range rows {
				ready[i.UID] = true
			}
			if !ready[child.UID] {
				continue
			}
			o, err := Validate(child, caps.ProjectUID)
			if err != nil || o.JobUID != jr.Job.UID || o.DefinitionEventUID != jr.Job.DefinitionEventUID {
				t.Fatalf("real occurrence %+v: %v", o, err)
			}
			parent, err := k.ShowIssue(ctx, parent.UID)
			if err != nil {
				t.Fatal(err)
			}
			b, err := ResolveBinding(ctx, k, parent, cfg)
			if err != nil || b.WorkflowUID != wr.Workflow.UID || b.WorkflowEventUID != wr.Workflow.DefinitionEventUID || b.WFDef == nil || b.Profile != "fast" || b.Cwd != "/tmp/example-checkout" {
				t.Fatalf("real binding %+v: %v", b, err)
			}
			if _, _, err := AckOp(child, cfg.Kata.Inbox); err != nil {
				t.Fatal(err)
			}
			if decision, claim, err := Takeover(child, cfg.Kata.Actor); err != nil || decision != "own-actor" || claim {
				t.Fatal(decision, claim, err)
			}
			return
		}
		timer := time.NewTimer(30 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			t.Fatal("no native child within 150 seconds")
		case <-timer.C:
		}
	}
}

func TestInteg540MergedMainCapability(t *testing.T) {
	k, cfg, _ := integrationKata(t)
	caps, err := k.Capabilities(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !New(cfg, k, caps, func(context.Context, sched.Due) {}, nil).Enabled() {
		t.Fatal("merged Kata main must support native child scanning")
	}
	// Capability absence is covered by TestScannerDisabled. Merged main has
	// the capability; an empty inbox must still disable all scanner work.
	cfg.Kata.Inbox = ""
	// Nil reader and callback prove that a disabled scanner performs no work.
	s := New(cfg, nil, caps, nil, nil)
	if s.Enabled() {
		t.Fatal("empty inbox unexpectedly enabled native child scanning")
	}
	s.Poke()
	if err := s.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
}
