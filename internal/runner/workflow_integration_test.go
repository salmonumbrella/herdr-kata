//go:build kata

package runner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"github.com/salmonumbrella/herdr-kata/internal/testenv"
)

func TestIntegCommandWorkflowEndToEnd(t *testing.T) {
	integrationWorkflow(t, false)
}
func TestIntegNotifyScheduledIssue(t *testing.T) {
	integrationWorkflow(t, true)
}
func TestIntegNotifyScheduledMessageAtLimit(t *testing.T) {
	integrationWorkflow(t, true, strings.Repeat("\"", 1024))
}
func integrationWorkflow(t *testing.T, notify bool, messages ...string) {
	t.Helper()
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	home := t.TempDir()
	l := state.Layout{Root: t.TempDir()}
	cfg := config.Config{Executor: "workflow-test", Kata: config.Kata{Bin: bin, Project: "workflow-test", Actor: "worker", Env: map[string]string{"KATA_HOME": home}}, Limits: config.Limits{MaxActiveRuns: 2, DefaultTimeout: 30 * time.Second}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}
	k := kata.New(cfg.Kata, l.Root, nil, nil)
	raw := startKataDaemon(t, bin, home, k.Env(), cfg.Kata.Project)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	def := defs.JobDef{Version: 1, Kind: "job", Enabled: true, Trigger: defs.Trigger{Kind: "manual"}, Action: defs.Action{Kind: "execute"}, Issue: &defs.IssuePolicy{Kind: "per-run", Title: "Verify command workflow"}, Overlap: "forbid", Catchup: "skip"}
	var workflow *kata.Workflow
	var wfdef *defs.WorkflowDef
	var existing kata.IssueView
	if notify {
		var reply struct {
			Issue kata.IssueView `json:"issue"`
		}
		if e := json.Unmarshal(raw("create", "Notify scheduled issue", "--project", "workflow-test"), &reply); e != nil {
			t.Fatal(e)
		}
		existing = reply.Issue
		raw("schedule", existing.UID, "2099-01-01", "--project", "workflow-test")
		existing, e := k.ShowIssue(ctx, existing.UID)
		if e != nil {
			t.Fatal(e)
		}
		if _, ok := existing.Metadata["scheduled_on"]; !ok {
			t.Fatal("issue not scheduled", existing)
		}
		def.Action = defs.Action{Kind: "notify", Recipient: "current-owner-or-author"}
		if len(messages) > 0 {
			def.Action.Message = messages[0]
		}
		def.Issue = &defs.IssuePolicy{Kind: "existing", UID: existing.UID}
	} else {
		wfdef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{
			{Key: "write", Kind: "command", Command: "printf first > marker"},
			{Key: "verify", Kind: "command", Command: `test "$(cat marker)" = first; printf second >> marker`, After: []string{"write"}},
		}}
		created := createWorkflow(t, raw, home, "Command verification", *wfdef)
		workflow = &created
		def.Action.WorkflowUID = workflow.UID
	}
	job := createJob(t, raw, home, "Workflow verification", def)
	g := kata.NewRouteGuard(cfg, k, state.OSFS{}, l)
	if e := g.Initialize(ctx); e != nil {
		t.Fatal(e)
	}
	s := NewStore(state.OSFS{}, l)
	q := NewQueue(k, s, g, RealClock{})
	m := NewManager(cfg, k, nil, s, q, g, RealClock{})
	queueCtx, queueCancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- q.Run(queueCtx) }()
	stopQueue := sync.OnceFunc(func() {
		queueCancel()
		m.Wait()
		if e := <-done; e != nil && !errors.Is(e, context.Canceled) {
			t.Error(e)
		}
	})
	defer stopQueue()
	at := time.Now().UTC()
	uid := ident.RunUID(at, ident.ScheduledSource(job.UID, ident.OccurrenceKey(at)))
	decision, e := m.Admit(ctx, sched.Due{Job: job, Def: def, Workflow: workflow, WFDef: wfdef, Occurrence: at})
	if e != nil || decision != sched.Admitted {
		t.Fatal(decision, e)
	}
	m.Wait()
	finalOp := "herdr-kata:" + uid + ":settle:observe"
	if notify {
		finalOp = "notify-observe"
	}
	if e = q.wait(ctx, uid, finalOp); e != nil {
		t.Fatal(e)
	}
	// Join the queue before checking the released gate: its final scan can
	// still hold the journal, which capacity probes count as occupied.
	stopQueue()
	r := loaded(t, s, uid)
	remote, e := k.ShowRun(ctx, uid)
	if e != nil || remote.Status != "succeeded" || remote.Revision != 2 || remote.StartedAt == nil || remote.EndedAt == nil || r.Herdr.TabID != "" || m.GateHeld(job.UID) {
		t.Fatal(remote, r, e)
	}
	issue, e := k.ShowIssue(ctx, r.IssueUID)
	if e != nil {
		t.Fatal(e)
	}
	if notify {
		if issue.Status != "open" || r.IssueOwned {
			t.Fatal(issue, r)
		}
		for key := range issue.Metadata {
			if strings.HasPrefix(key, "work.") {
				t.Fatal("notify wrote work metadata", key)
			}
		}
		var request struct {
			Message string `json:"message"`
		}
		e = json.Unmarshal(issue.Metadata["notify."+base64.RawURLEncoding.EncodeToString([]byte("worker"))], &request)
		expectedMessage := def.Action.Message
		if expectedMessage == "" {
			expectedMessage = "Kata cron: Workflow verification is due"
		}
		if e != nil || request.Message != expectedMessage {
			t.Fatal(request, e)
		}
	} else {
		if issue.Status != "closed" || !strings.Contains(remote.Summary.Message, "2 command steps exited 0") {
			t.Fatal(issue, remote)
		}
		_, p := settlePayload(t, r.Ops, "close")
		if len(p.Close.Evidence) != 2 {
			t.Fatal(p)
		}
		b, e := os.ReadFile(filepath.Join(r.Cwd, "marker"))
		if e != nil || string(b) != "firstsecond" {
			t.Fatal(string(b), e)
		}
	}
}
