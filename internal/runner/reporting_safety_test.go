package runner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
)

func TestExistingNotifyAdmissionStoresQualifiedReference(t *testing.T) {
	f := newAdmission(t)
	f.d.Def.Action = defs.Action{Kind: "notify", Recipient: "worker", Message: "due"}
	f.d.Def.Issue = &defs.IssuePolicy{Kind: "existing", UID: "issue"}
	f.d.Workflow = nil
	f.d.WFDef = nil
	f.start(t)
	f.m.executions.Store(f.uid, struct{}{})
	r := f.admit(t)
	if r.IssueRef != "project#task" || r.IssueRefs["issue"] != "project#task" {
		t.Fatalf("notify journal lost qualified reference: %+v", r)
	}
}

func TestNativeChildRetainsWorkflowNameForClose(t *testing.T) {
	f := newChildAdmission(t)
	f.d.Child.Binding.WorkflowName = "Check service"
	f.start(t)
	r := f.admit(t)
	r.Phase, r.Outcome = Settled, "succeeded"
	r.Steps[0].Status = "succeeded"
	r.Steps[0].Receipt = &Receipt{Version: 1, Run: r.UID, Step: "main", Attempt: 1, StartedAt: time.Now(), EndedAt: time.Now(), ExitCode: 0}
	_, payload := settlePayload(t, SettleOps(r, nil, time.Now()), "close")
	if !strings.Contains(payload.Close.Message, "Workflow Check service:") {
		t.Fatal(payload.Close.Message)
	}
}

func TestResumedCloseClearsRunningAttentionMessageBeforeClosing(t *testing.T) {
	r := settleFixture()
	r.Resume = &ResumeState{UID: "request"}
	r.Outcome = "succeeded"
	ops := SettleOps(r, resultFixture(), settleTime)
	clear, closeIndex := -1, -1
	for i, op := range ops {
		p, _ := decodePayload(op)
		if op.Kind == "meta.unset" && p.Name == "work.attention_msg" {
			clear = i
			if op.Guard == nil {
				t.Fatal("unguarded cleanup")
			}
		}
		if op.Kind == "close" {
			closeIndex = i
		}
	}
	if clear < 0 || closeIndex <= clear {
		t.Fatal("running attention message survives close", settleKinds(ops))
	}
}

func TestCommandOnlyReleaseCommentDoesNotClaimTab(t *testing.T) {
	f := newAdmission(t)
	r := settleFixture()
	r.UID = f.uid
	r.Phase = Held
	r.Outcome = "failed"
	r.Steps = []StepState{{Key: "main", Kind: "command", Attempt: 1, Status: "failed"}}
	if err := f.s.Create(r); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Release(context.Background(), r.UID); err != nil {
		t.Fatal(err)
	}
	saved := loaded(t, f.s, r.UID)
	found := false
	for _, op := range saved.Ops {
		if op.Kind != "comment" {
			continue
		}
		var p Payload
		_ = json.Unmarshal(op.Payload, &p)
		found = true
		if strings.Contains(p.Body, "tab retained") || !strings.Contains(p.Body, "Automatic execution stopped") {
			t.Fatal(p.Body)
		}
	}
	if !found {
		t.Fatal("release has no handoff comment")
	}
}
