//go:build kata_children

package runner

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/inbox"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"github.com/salmonumbrella/herdr-kata/internal/testenv"
)

type childIntegration struct {
	cfg     config.Config
	k       *kata.CLI
	m       *Manager
	s       *Store
	q       *Queue
	scanner *inbox.Scanner
	child   kata.IssueView
	raw     func(...string) []byte
}

func integrationChild(t *testing.T, claimParent bool) *childIntegration {
	t.Helper()
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	home := t.TempDir()
	root := t.TempDir()
	cfg := config.Config{Executor: "child-installation", Kata: config.Kata{Bin: bin, Project: "children-test", Actor: "worker", Inbox: "worker/inbox", Env: map[string]string{"KATA_HOME": home}}, Limits: config.Limits{MaxActiveRuns: 2, DefaultTimeout: time.Minute}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}
	k := kata.New(cfg.Kata, root, nil, nil)
	raw := startKataDaemon(t, bin, home, k.Env(), cfg.Kata.Project)
	parent, err := k.CreateIssue(t.Context(), kata.CreateReq{Title: "Recurring test parent", Body: "Run the bound command for native child requests."})
	if err != nil {
		t.Fatal(err)
	}
	if claimParent {
		if _, err = k.Claim(t.Context(), parent.UID, true); err != nil {
			t.Fatal(err)
		}
	}
	workflow := createWorkflow(t, raw, home, "Child commands", defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "check", Kind: "command", Command: "true"}}})
	if _, err = k.MetaSet(t.Context(), parent.UID, "herdr_kata.workflow", workflow.UID, kata.Cond{}); err != nil {
		t.Fatal(err)
	}
	createJob(t, raw, home, "Native children", defs.JobDef{Version: 1, Kind: "job", Enabled: true, Trigger: defs.Trigger{Kind: "interval", IntervalSeconds: 60}, Action: defs.Action{Kind: "notify", Recipient: "worker/inbox", Message: "Run this child"}, Issue: &defs.IssuePolicy{Kind: "per-run", ParentUID: parent.UID, Title: "Recurring command", Body: "Verify the command exits successfully."}, Overlap: "allow", Catchup: "latest"})
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	var child kata.IssueView
	for child.UID == "" {
		rows, err := k.Inbox(ctx, cfg.Kata.Inbox)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			i, err := k.ShowIssue(ctx, row.Ref)
			if err != nil {
				t.Fatal(err)
			}
			if i.ParentUID == parent.UID {
				child = i
				break
			}
		}
		if child.UID != "" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("native delivery", ctx.Err())
		case <-time.After(time.Second):
		}
	}
	l := state.Layout{Root: root}
	guard := kata.NewRouteGuard(cfg, k, state.OSFS{}, l)
	if err = guard.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := NewStore(state.OSFS{}, l)
	q := NewQueue(k, s, guard, RealClock{})
	m := NewManager(cfg, k, nil, s, q, guard, RealClock{})
	f := &childIntegration{cfg: cfg, k: k, m: m, s: s, q: q, child: child, raw: raw}
	caps, err := k.Capabilities(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	f.scanner = inbox.New(cfg, k, caps, func(ctx context.Context, d sched.Due) {
		if _, err := m.Admit(ctx, d); err != nil {
			t.Error(err)
		}
	}, nil)
	f.scanner.SaveSnapshot = func(v inbox.Snapshot) error { return inbox.WriteSnapshot(state.OSFS{}, root, v) }
	return f
}
func (f *childIntegration) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 2)
	go func() { done <- f.q.Run(ctx) }()
	go func() { done <- f.scanner.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		for range 2 {
			if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		}
		f.m.Wait()
	})
}
func (f *childIntegration) settled(t *testing.T) *Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for {
		ids, err := f.s.candidates()
		if err != nil {
			t.Fatal(err)
		}
		for _, uid := range ids {
			r, err := f.s.Load(uid)
			if err != nil {
				t.Fatal(err)
			}
			if r.IssueUID == f.child.UID && r.Phase == Settled && head(r) == nil {
				return r
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("child did not settle", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func TestInteg540PickedUpOnce(t *testing.T) {
	f := integrationChild(t, false)
	f.start(t)
	r := f.settled(t)
	if r.Outcome != "succeeded" || r.Child == nil || r.Child.Ownership.Decision != "claimed" || len(r.Steps) != 1 || r.Steps[0].Attempt != 1 {
		t.Fatal(r)
	}
	remote, err := f.k.ShowRun(t.Context(), r.UID)
	if err != nil || remote.Status != "succeeded" || remote.DefinitionEventUID != r.Child.DeliveryEventUID {
		t.Fatal(remote, err)
	}
	// Reinsert the same delivery request; the journal vetoes a second execution.
	if _, err = f.k.MetaSet(t.Context(), f.child.UID, r.Child.NotifyKey, string(r.Child.NotifyJSON), kata.Cond{JSONValue: true}); err != nil {
		t.Fatal(err)
	}
	f.scanner.Poke()
	time.Sleep(2300 * time.Millisecond)
	saved, err := f.s.Load(r.UID)
	if err != nil || saved.Steps[0].Attempt != 1 || len(saved.Ops) != len(r.Ops) {
		t.Fatal(saved, err)
	}
}
func TestInteg540NotReadyWaits(t *testing.T) {
	f := integrationChild(t, true)
	if _, err := f.k.MetaSet(t.Context(), f.child.UID, "someday", "true", kata.Cond{JSONValue: true}); err != nil {
		t.Fatal(err)
	}
	f.start(t)
	deadline := time.Now().Add(10 * time.Second)
	for {
		snap, err := inbox.ReadSnapshot(state.OSFS{}, f.s.layout.Root)
		if err == nil && len(snap.Requests) > 0 && snap.Requests[0].Status == inbox.WaitingReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("missing waiting-ready snapshot", snap, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	ids, err := f.s.candidates()
	if err != nil || len(ids) != 0 || f.m.FreeSlots() != 2 {
		t.Fatal(ids, err)
	}
	if _, err = f.k.MetaUnset(t.Context(), f.child.UID, "someday", kata.Cond{}); err != nil {
		t.Fatal(err)
	}
	f.scanner.Poke()
	f.settled(t)
}
func TestInteg540GuardedAck(t *testing.T) {
	f := integrationChild(t, true)
	// Queue the exact settlement acknowledgement without launching a model.
	key, saved, err := inbox.AckOp(f.child, f.cfg.Kata.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	value := string(saved)
	op := newOp("ack", "meta.unset", Payload{Ref: f.child.UID, Name: key}, "")
	op.Guard = &kata.Cond{JSONValue: true, IfValue: &value}
	uid := "01K00000000000000000000001"
	r := &Run{Version: 1, UID: uid, Phase: Settled, Child: &ChildRun{NotifyKey: key, NotifyJSON: saved}, Ops: []Op{op}}
	if err = f.s.Create(r); err != nil {
		t.Fatal(err)
	}
	// A newer request in the same slot must survive the saved --if-value.
	if err = f.k.Notify(t.Context(), f.child.UID, f.cfg.Kata.Inbox, "replacement request"); err != nil {
		t.Fatal(err)
	}
	deliver(t, f.q, uid)
	issue, err := f.k.ShowIssue(t.Context(), f.child.UID)
	if err != nil {
		t.Fatal(err)
	}
	var notification struct {
		Message string `json:"message"`
	}
	if err = json.Unmarshal(issue.Metadata[key], &notification); err != nil || notification.Message != "replacement request" {
		t.Fatal(issue, err)
	}
	if !loaded(t, f.s, uid).Ops[0].Done {
		t.Fatal("replacement disposition not recorded")
	}
}
