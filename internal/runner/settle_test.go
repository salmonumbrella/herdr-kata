package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

var settleTime = time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC)

func settleFixture() *Run {
	start := settleTime.Add(-time.Minute)
	return &Run{Version: 1, UID: "run-1", IssueUID: "issue-1", IssueRef: "project#abcd", IssueOwned: true, Phase: Settled, Target: Target{Identity: kata.Identity{Actor: "worker"}, Executor: "executor-1"}, Job: kata.Job{UID: "job-1", DefinitionEventUID: "job-event-1"}, Occurrence: "2026-01-02T03:04:05Z", StartedAt: &start, Steps: []StepState{{Key: "main", Kind: "prompt", Attempt: 1, Status: "succeeded"}}}
}

func settlePayload(t *testing.T, ops []Op, kind string) (Op, Payload) {
	t.Helper()
	for _, op := range ops {
		if op.Kind == kind {
			var p Payload
			if err := json.Unmarshal(op.Payload, &p); err != nil {
				t.Fatal(err)
			}
			return op, p
		}
	}
	t.Fatalf("missing %s in %v", kind, settleKinds(ops))
	return Op{}, Payload{}
}
func settleKinds(ops []Op) []string {
	var kinds []string
	for _, op := range ops {
		kinds = append(kinds, op.Kind)
	}
	return kinds
}
func wantSettleKinds(t *testing.T, ops []Op, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(settleKinds(ops), want) {
		t.Fatalf("ops=%v, want=%v", settleKinds(ops), want)
	}
}

func TestSettleClosesWithEvidence(t *testing.T) {
	r, res := settleFixture(), resultFixture()
	r.Workflow = &kata.Workflow{UID: "wf-1", DefinitionEventUID: "wf-event-1"}
	before, _ := json.Marshal(r)
	resBefore, _ := json.Marshal(res)
	ops := SettleOps(r, res, settleTime)
	wantSettleKinds(t, ops, "close", "observe")
	op, p := settlePayload(t, ops, "close")
	if p.Close == nil || p.Close.Reason != "done" || p.Close.Ref != r.IssueRef || p.Close.Message != res.Summary || !reflect.DeepEqual(p.Close.Evidence, res.Evidence) || op.Key != "herdr-kata:run-1:close" || op.Guard == nil || op.Guard.IfMatch != "" {
		t.Fatalf("close=%+v payload=%+v", op, p)
	}
	_, p = settlePayload(t, ops, "observe")
	o := p.Observation
	if o == nil || o.Status != "succeeded" || o.Summary.Message != res.Summary || o.Summary.Version != 1 || o.JobUID != "job-1" || o.DefinitionEventUID != "job-event-1" || o.WorkflowUID != "wf-1" || o.WorkflowDefinitionEventUID != "wf-event-1" || o.Actor != "worker" || o.ExecutorLabel != "executor-1" || o.IssueUID != "issue-1" || o.ExpectedRevision != 1 || o.StartedAt == nil || !o.StartedAt.Equal(*r.StartedAt) || o.EndedAt == nil || !o.EndedAt.Equal(settleTime) {
		t.Fatalf("observation=%+v", o)
	}
	after, _ := json.Marshal(r)
	resAfter, _ := json.Marshal(res)
	if string(before) != string(after) || string(resBefore) != string(resAfter) || !reflect.DeepEqual(ops, SettleOps(r, res, settleTime)) {
		t.Fatal("settlement is not pure/deterministic")
	}
	for _, op := range ops {
		if op.Safety != kata.SafetyOf(op.Kind) || op.ID == "" {
			t.Fatalf("invalid op=%+v", op)
		}
	}
}

func TestSettleReviewModeForbidsValidClose(t *testing.T) {
	for _, mode := range []string{"prompt", "command", "mixed-audit"} {
		t.Run(mode, func(t *testing.T) {
			r, res := settleFixture(), resultFixture()
			switch mode {
			case "command":
				r, res = commandFixture(), nil
			case "mixed-audit":
				r = commandFixture()
				r.WFDef.Steps = append(r.WFDef.Steps, defs.Step{Key: "main", Kind: "prompt"})
				res.CloseReason = "audit-no-change"
				res.Evidence = []string{"no-change-audit:all callers already validate input"}
			}
			normal := SettleOps(r, res, settleTime)
			wantSettleKinds(t, normal, "close", "observe")
			_, closePayload := settlePayload(t, normal, "close")
			before, _ := json.Marshal(r)
			ops := settleOps(r, res, settleTime, true)
			wantSettleKinds(t, ops, "comment", "label.add", "meta.set", "meta.set", "observe")
			after, _ := json.Marshal(r)
			if string(before) != string(after) || !reflect.DeepEqual(ops, settleOps(r, res, settleTime, true)) {
				t.Fatal("review settlement mutated the run or changed on replay")
			}
			for i, suffix := range []string{"comment", "review-label", "attention", "attention-msg", "observe"} {
				if ops[i].ID != "herdr-kata:run-1:settle:"+suffix {
					t.Fatalf("unstable review operation ID: %s", ops[i].ID)
				}
			}
			_, comment := settlePayload(t, ops, "comment")
			if comment.Body != closePayload.Close.Message {
				t.Fatalf("review lost successful report: %q", comment.Body)
			}
			_, observation := settlePayload(t, ops, "observe")
			if observation.Observation.Status != "succeeded" {
				t.Fatal("review lost successful outcome", observation)
			}
		})
	}
}

func assertSettleReview(t *testing.T, res *Result) {
	t.Helper()
	ops := SettleOps(settleFixture(), res, settleTime)
	wantSettleKinds(t, ops, "comment", "label.add", "meta.set", "meta.set", "observe")
	op, p := settlePayload(t, ops, "comment")
	if p.Body != res.Summary || op.Key != "herdr-kata:run-1:comment:succeeded" {
		t.Fatalf("comment=%+v %s", p, op.Key)
	}
	_, p = settlePayload(t, ops, "label.add")
	if p.Name != "needs-review" {
		t.Fatal(p)
	}
	_, p = settlePayload(t, ops, "meta.set")
	if p.Value != "needs-human" {
		t.Fatal(p)
	}
	var msg Payload
	if err := json.Unmarshal(ops[3].Payload, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Name != "work.attention_msg" || msg.Value != "herdr-kata: review run run-1" {
		t.Fatal(msg)
	}
	_, p = settlePayload(t, ops, "observe")
	if p.Observation.Status != "succeeded" {
		t.Fatal(p.Observation)
	}
}
func TestSettleShortMessageReview(t *testing.T) {
	r := resultFixture()
	r.Summary = "Fixed it"
	assertSettleReview(t, r)
}
func TestSettleNoEvidenceReview(t *testing.T) {
	r := resultFixture()
	r.Evidence = nil
	assertSettleReview(t, r)
}

func TestSettleEvidenceMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		evidence     []string
		close        bool
	}{
		{"default-done", "", []string{"commit:abcdef0"}, true},
		{"bad-sha", "done", []string{"commit:fixed"}, false},
		{"empty-command", "done", []string{"test:"}, false},
		{"audit", "audit-no-change", []string{"no-change-audit:reviewed all callers", "reviewed-paths:parser.go"}, true},
		{"audit-test", "audit-no-change", []string{"no-change-audit:reviewed", "test:check"}, false},
		{"audit-two", "audit-no-change", []string{"no-change-audit:a", "no-change-audit:b"}, false},
		{"done-audit", "done", []string{"no-change-audit:reviewed"}, false},
		{"duplicate-agent-evidence", "done", []string{"test:check", "test:check"}, false},
		{"wontfix", "wontfix", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := resultFixture()
			res.CloseReason = tc.reason
			res.Evidence = tc.evidence
			ops := SettleOps(settleFixture(), res, settleTime)
			if (ops[0].Kind == "close") != tc.close {
				t.Fatalf("ops=%v", settleKinds(ops))
			}
		})
	}
}

func TestSettleFailedStuck(t *testing.T) {
	r, res := settleFixture(), resultFixture()
	r.Reason = "tests_failed"
	res.Status = "failed"
	ops := SettleOps(r, res, settleTime)
	wantSettleKinds(t, ops, "comment", "meta.set", "meta.set", "observe")
	_, p := settlePayload(t, ops, "meta.set")
	if p.Value != "stuck" || p.Owner != "" {
		t.Fatal(p)
	}
	var msg Payload
	json.Unmarshal(ops[2].Payload, &msg)
	if msg.Value != "herdr-kata: tests_failed" {
		t.Fatal(msg)
	}
	_, p = settlePayload(t, ops, "observe")
	if p.Observation.Status != "failed" || !strings.Contains(p.Observation.Summary.Message, "tests_failed") {
		t.Fatal(p.Observation)
	}
	for _, op := range ops {
		if op.Kind == "meta.set" && (op.Guard == nil || op.Guard.IfMatch != "" || op.Safety != kata.Guarded) {
			t.Fatalf("guard must be frozen by a fresh delivery read: %+v", op)
		}
	}
}

func TestSettleUnknownNeedsHumanNotify(t *testing.T) {
	r := settleFixture()
	r.Outcome = "unknown"
	r.Reason = "no_result"
	ops := SettleOps(r, nil, settleTime)
	wantSettleKinds(t, ops, "comment", "meta.set", "meta.set", "resolve-recipient", "notify", "observe")
	op, p := settlePayload(t, ops, "resolve-recipient")
	if op.Safety != kata.ReadBack || p.Ref != r.IssueRef {
		t.Fatalf("resolve=%+v %+v", op, p)
	}
	_, p = settlePayload(t, ops, "notify")
	if p.To != "" || p.Message != "herdr-kata: no_result" {
		t.Fatal(p)
	}
	_, p = settlePayload(t, ops, "observe")
	if p.Observation.Status != "unknown" || !strings.Contains(p.Observation.Summary.Message, "no_result") {
		t.Fatal(p.Observation)
	}
}

func TestSettleHandedOff(t *testing.T) {
	for _, known := range []string{"", "succeeded", "failed"} {
		r := settleFixture()
		r.Phase = HandedOff
		r.Outcome = known
		r.Reason = "released"
		ops := SettleOps(r, resultFixture(), settleTime)
		wantSettleKinds(t, ops, "comment", "meta.set", "meta.set", "resolve-recipient", "notify", "observe")
		_, p := settlePayload(t, ops, "observe")
		want := known
		if want == "" {
			want = "unknown"
		}
		if p.Observation.Status != want {
			t.Fatalf("observation=%+v", p.Observation)
		}
	}
	r, res := settleFixture(), resultFixture()
	res.Status = "needs-human"
	res.Summary = "Please review the API change before continuing."
	ops := SettleOps(r, res, settleTime)
	wantSettleKinds(t, ops, "comment", "meta.set", "meta.set", "resolve-recipient", "notify", "observe")
	_, p := settlePayload(t, ops, "notify")
	if !strings.Contains(p.Message, res.Summary) {
		t.Fatal(p)
	}
	_, p = settlePayload(t, ops, "observe")
	if p.Observation.Status != "unknown" {
		t.Fatal(p.Observation)
	}
}

func TestSettleExistingCommentOnly(t *testing.T) {
	for _, status := range []string{"succeeded", "failed", "needs-human", "unknown"} {
		r, res := settleFixture(), resultFixture()
		r.IssueOwned = false
		res.Status = status
		if status == "unknown" {
			res = nil
			r.Outcome = "unknown"
		}
		ops := SettleOps(r, res, settleTime)
		if status == "needs-human" || status == "unknown" {
			wantSettleKinds(t, ops, "comment", "resolve-recipient", "notify", "observe")
		} else {
			wantSettleKinds(t, ops, "comment", "observe")
		}
	}
}

func commandFixture() *Run {
	r := settleFixture()
	r.Steps = nil
	r.Workflow = &kata.Workflow{UID: "wf-1", DefinitionEventUID: "wf-event-1", Name: "Checks"}
	r.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "lint", Kind: "command", Command: "go vet ./parser"}, {Key: "test", Kind: "command", Command: "go test ./parser", After: []string{"lint"}}}}
	for _, d := range r.WFDef.Steps {
		r.Steps = append(r.Steps, StepState{Key: d.Key, Kind: "command", Attempt: 1, Status: "succeeded", Receipt: &Receipt{Version: 1, Run: r.UID, Step: d.Key, Attempt: 1, ExitCode: 0, StartedAt: settleTime.Add(-time.Minute), EndedAt: settleTime}})
	}
	return r
}

func TestSettleCommandEvidence(t *testing.T) {
	r := commandFixture()
	ops := SettleOps(r, nil, settleTime)
	wantSettleKinds(t, ops, "close", "observe")
	_, p := settlePayload(t, ops, "close")
	want := "Workflow Checks: 2 command steps exited 0 (run run-1, occurrence 2026-01-02T03:04:05Z)"
	if p.Close.Message != want || !reflect.DeepEqual(p.Close.Evidence, []string{"test:go vet ./parser", "test:go test ./parser"}) {
		t.Fatal(p.Close)
	}
	_, p = settlePayload(t, ops, "observe")
	if p.Observation.Summary.Message != want {
		t.Fatal(p.Observation)
	}
	// Mixed workflows retain the agent's message and add command evidence.
	r.WFDef.Steps = append(r.WFDef.Steps, defs.Step{Key: "main", Kind: "prompt"})
	r.Steps = append(r.Steps, StepState{Key: "main", Kind: "prompt", Attempt: 1, Status: "succeeded"})
	res := resultFixture()
	ops = SettleOps(r, res, settleTime)
	_, p = settlePayload(t, ops, "close")
	if p.Close.Message != res.Summary || !reflect.DeepEqual(p.Close.Evidence, []string{"test:go test ./parser", "test:go vet ./parser"}) {
		t.Fatal(p.Close)
	}
}

func TestSettleCommandReceiptProof(t *testing.T) {
	for _, mode := range []string{"missing", "stale", "foreign", "wrong-step", "wrong-version", "nonzero", "skipped", "missing-step"} {
		t.Run(mode, func(t *testing.T) {
			r := commandFixture()
			s := &r.Steps[1]
			switch mode {
			case "missing":
				s.Receipt = nil
			case "stale":
				s.Receipt.Attempt = 2
			case "foreign":
				s.Receipt.Run = "other"
			case "wrong-step":
				s.Receipt.Step = "other"
			case "wrong-version":
				s.Receipt.Version = 2
			case "nonzero":
				s.Receipt.ExitCode = 1
				s.Status = "failed"
			case "skipped":
				s.Status = "skipped"
			case "missing-step":
				r.Steps = r.Steps[:1]
			}
			ops := SettleOps(r, nil, settleTime)
			for _, op := range ops {
				if op.Kind == "close" {
					t.Fatal("closed without proof")
				}
			}
			_, p := settlePayload(t, ops, "observe")
			if p.Observation.Status == "succeeded" {
				t.Fatal("reported success without proof")
			}
		})
	}
}

func TestWriteBudgets(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     string
		owned      bool
		noEvidence bool
		writes     int
	}{
		{"success", "succeeded", true, false, 4},
		{"review", "succeeded", true, true, 7},
		{"failed", "failed", true, false, 6},
		{"unknown", "unknown", true, false, 7},
		{"existing-success", "succeeded", false, false, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, res := settleFixture(), resultFixture()
			r.IssueOwned = tc.owned
			res.Status = tc.status
			if tc.noEvidence {
				res.Evidence = nil
			}
			if tc.status == "unknown" {
				res = nil
				r.Outcome = "unknown"
			}
			writes := 2
			if !tc.owned {
				writes = 1
			}
			for _, op := range SettleOps(r, res, settleTime) {
				if op.Safety != kata.ReadBack {
					writes++
				}
			}
			if writes != tc.writes {
				t.Fatalf("writes=%d, want %d", writes, tc.writes)
			}
		})
	}
}

func TestSettleCapsSummaryAndPreservesEnd(t *testing.T) {
	r, res := settleFixture(), resultFixture()
	res.Summary = strings.Repeat("界", 22000)
	ended := settleTime.Add(-time.Second)
	r.EndedAt = &ended
	ops := SettleOps(r, res, settleTime)
	_, p := settlePayload(t, ops, "observe")
	if len(p.Observation.Summary.Message) > 60<<10 || !utf8.ValidString(p.Observation.Summary.Message) || !p.Observation.EndedAt.Equal(ended) {
		t.Fatal("summary cap or end changed")
	}
	// Mutating returned ops must not alias either input.
	_, p = settlePayload(t, ops, "close")
	p.Close.Evidence[0] = "test:other"
	if res.Evidence[0] != "test:go test ./parser" {
		t.Fatal("result mutated")
	}
}

func TestSettleBoundsEscapedTextAndNotify(t *testing.T) {
	r, res := settleFixture(), resultFixture()
	res.Status = "needs-human"
	res.Summary = strings.Repeat("\"<&\n", 16000)
	ops := SettleOps(r, res, settleTime)
	_, p := settlePayload(t, ops, "observe")
	b, _ := json.Marshal(p.Observation.Summary)
	if len(b) > 64<<10 {
		t.Fatalf("encoded summary is %d bytes", len(b))
	}
	_, p = settlePayload(t, ops, "notify")
	if len(p.Message) > 1024 || !utf8.ValidString(p.Message) || p.Message == "" {
		t.Fatalf("invalid notify length: %d", len(p.Message))
	}
}

func TestSettleCommandResultCannotOverrideMissingProof(t *testing.T) {
	r := commandFixture()
	r.Steps[1].Receipt = nil
	ops := SettleOps(r, resultFixture(), settleTime)
	for _, op := range ops {
		if op.Kind == "close" {
			t.Fatal("closed despite missing command receipt")
		}
	}
	_, p := settlePayload(t, ops, "observe")
	if p.Observation.Status != "unknown" {
		t.Fatal(p.Observation)
	}
}

func TestSettleBlockedIsNotSettlement(t *testing.T) {
	r := settleFixture()
	r.Phase = Blocked
	if ops := SettleOps(r, nil, settleTime); len(ops) != 0 {
		t.Fatalf("settled blocked run: %v", settleKinds(ops))
	}
}

func TestSettleCommandFailureSummary(t *testing.T) {
	r := commandFixture()
	r.Steps[1].Receipt.ExitCode = 7
	r.Steps[1].Status = "failed"
	r.Reason = "command_failed"
	ops := SettleOps(r, nil, settleTime)
	_, p := settlePayload(t, ops, "observe")
	if p.Observation.Status != "failed" || !strings.Contains(p.Observation.Summary.Message, "test exited 7") || !strings.Contains(p.Observation.Summary.Message, "command_failed") {
		t.Fatalf("summary=%+v", p.Observation)
	}
}

func TestHeldCommandCommentDistinguishesUnstartedSteps(t *testing.T) {
	for _, parked := range []bool{false, true} {
		t.Run(fmt.Sprint(parked), func(t *testing.T) {
			r := commandFixture()
			r.Phase, r.Outcome, r.Reason = Held, "unknown", "secret_unavailable"
			for i := range r.Steps {
				r.Steps[i] = StepState{Key: r.Steps[i].Key, Kind: "command", Attempt: 1, Submission: None}
			}
			if parked {
				r.Outcome, r.Reason, r.ActiveStep = "failed", "step_failed", "lint"
				r.Steps[0].Submission, r.Steps[0].Status, r.Steps[0].Note = Accepted, "failed", "Repair the fixture"
				r.Steps[0].Receipt = &Receipt{Version: 1, Run: r.UID, Step: "lint", Attempt: 1, ExitCode: 7}
			}
			ops := SettleOps(r, nil, settleTime)
			_, p := settlePayload(t, ops, "comment")
			if !strings.HasPrefix(p.Body, "Reason: "+r.Reason+"\n") || !strings.Contains(p.Body, "test: not started") || strings.Contains(p.Body, "no valid command receipt") {
				t.Fatalf("misleading held comment: %s", p.Body)
			}
			_, observed := settlePayload(t, ops, "observe")
			if observed.Observation.Status != r.Outcome || strings.Contains(strings.Join(settleKinds(ops), ","), "close") {
				t.Fatal("unstarted steps authorized success", observed, settleKinds(ops))
			}
		})
	}
}

func TestSettleNeedsHumanResultIsUnknown(t *testing.T) {
	r, res := settleFixture(), resultFixture()
	r.Outcome = "succeeded"
	res.Status = "needs-human"
	ops := SettleOps(r, res, settleTime)
	_, p := settlePayload(t, ops, "observe")
	if p.Observation.Status != "unknown" {
		t.Fatalf("needs-human is a new handoff: %+v", p.Observation)
	}
}

func TestSettleEmptySummaryNeverPadsForClose(t *testing.T) {
	r, res := settleFixture(), resultFixture()
	r.UID = "01JGFJJZ00ABCDEFGHJKMNPQRSTV"
	res.Run = r.UID
	res.Summary = ""
	ops := SettleOps(r, res, settleTime)
	wantSettleKinds(t, ops, "comment", "label.add", "meta.set", "meta.set", "observe")
}

func TestSettleLongSummaryRetainsReason(t *testing.T) {
	r, res := settleFixture(), resultFixture()
	r.Reason = "verification_failed"
	res.Status = "failed"
	res.Summary = strings.Repeat("x", 64000)
	ops := SettleOps(r, res, settleTime)
	_, p := settlePayload(t, ops, "observe")
	if !strings.Contains(p.Observation.Summary.Message, r.Reason) || len(p.Observation.Summary.Message) > 60<<10 {
		t.Fatal("reason was lost to summary cap")
	}
}

func TestOwnerChangedStopsWork(t *testing.T) {
	r, res := settleFixture(), resultFixture()
	res.Status = "failed"
	ops := SettleOps(r, res, settleTime)
	q, s, uid, k, _, _ := queueFixture(t, ops...)
	if _, err := s.Transition(uid, func(r *Run) error { r.IssueOwned = true; return nil }); err != nil {
		t.Fatal(err)
	}
	k.comment = func(context.Context, string, string, string) error { return nil }
	k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{Status: "open", Owner: "another-worker", Revision: 2}, nil
	}
	k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) {
		t.Fatal("wrote work.* after ownership changed")
		return 0, nil
	}
	deliver(t, q, uid)
	deliver(t, q, uid)
	got := loaded(t, s, uid)
	if got.Ops[1].Done || got.Ops[1].Inspect == "" || got.Ops[2].FirstSent != nil {
		t.Fatal("ownership change did not stop attention writes")
	}
}

func TestSettleMixedAuditNoChange(t *testing.T) {
	r := settleFixture()
	r.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "test", Kind: "command", Command: "make test"}, {Key: "main", Kind: "prompt", Prompt: "audit"}}}
	r.Steps = []StepState{
		{Key: "test", Kind: "command", Attempt: 1, Status: "succeeded", Receipt: &Receipt{Version: 1, Run: r.UID, Step: "test", Attempt: 1, ExitCode: 0, PGID: 1}},
		{Key: "main", Kind: "prompt", Attempt: 1, Status: "succeeded"},
	}
	res := &Result{Version: 1, Run: r.UID, Step: "main", Attempt: 1, Status: "succeeded", Summary: "Audited the parser callers and found nothing that needs to change.", Evidence: []string{"no-change-audit:all callers already validate input"}, CloseReason: "audit-no-change"}
	ops := SettleOps(r, res, settleTime)
	if settleKinds(ops)[0] != "close" {
		t.Fatalf("valid audit-no-change routed to %v", settleKinds(ops))
	}
	p, err := decodePayload(ops[0])
	if err != nil || p.Close == nil || !reflect.DeepEqual(p.Close.Evidence, []string{"no-change-audit:all callers already validate input"}) {
		t.Fatal("audit evidence changed", p, err)
	}
}

// Settlement and release reports stay plain even after typed findings. Their
// preparation does not wait for an uncertain chain write to recover its UID.
func TestLoopSettlementAndReleaseRemainPlain(t *testing.T) {
	for _, phase := range []Phase{Settled, HandedOff} {
		for _, pending := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pending=%t", phase, pending), func(t *testing.T) {
				r := settleFixture()
				r.Phase, r.Outcome = phase, "failed"
				r.Findings = Findings{RootUID: "root", HeadUID: makerClaim, MakerUID: "maker"}
				op := operation("head", "comment", Payload{Mode: "typed", Chain: &ChainComment{Purpose: "checker", Mode: "typed"}})
				op.Done, op.CommentUID = true, makerClaim
				r.Ops = []Op{op}
				if pending {
					r.Ops = append(r.Ops, operation("pending", "comment", Payload{Mode: "typed", ReplyTo: makerClaim, ReplyKind: "reply", Chain: &ChainComment{Purpose: "maker"}}))
				}
				res := resultFixture()
				res.Status = "failed"
				ops := SettleOps(r, res, settleTime)
				comment, p := settlePayload(t, ops, "comment")
				if p.Body != res.Summary || p.ReplyTo != "" || p.ReplyKind != "" || p.Chain != nil {
					t.Fatal("report changed the finding chain", p)
				}
				q, store, uid, k, _, _ := queueFixture(t, comment)
				k.typedComment = func(_ context.Context, req kata.CommentReq) (kata.Comment, error) {
					if req.ReplyTo != "" || req.Kind != "" || req.Body != res.Summary {
						t.Fatal("report was sent as a typed reply", req)
					}
					return kata.Comment{UID: "report"}, nil
				}
				if _, err := store.Transition(uid, func(saved *Run) error { saved.Findings = r.Findings; return nil }); err != nil {
					t.Fatal(err)
				}
				if err := q.send(t.Context(), uid); err != nil {
					t.Fatal(err)
				}
				saved := loaded(t, store, uid)
				if !saved.Ops[0].Done || saved.Ops[0].CommentUID != "report" || !reflect.DeepEqual(saved.Findings, r.Findings) {
					t.Fatal("report delivery changed the finding head", saved)
				}
			})
		}
	}
}

func TestSettleFailureNote(t *testing.T) {
	for _, phase := range []Phase{Held, Settled} {
		for _, kind := range []string{"prompt", "command"} {
			t.Run(string(phase)+"/"+kind, func(t *testing.T) {
				r := settleFixture()
				r.Phase, r.Outcome, r.Reason, r.ActiveStep = phase, "failed", "step_failed", "review"
				r.Steps = []StepState{{Key: "review", Kind: kind, Attempt: 1, Status: "failed", Note: "Inspect digest.md, approve its content, then resume."}}
				ops := SettleOps(r, nil, settleTime)
				_, comment := settlePayload(t, ops, "comment")
				if !strings.Contains(comment.Body, r.Steps[0].Note) || !strings.Contains(comment.Body, "review") {
					t.Fatal(comment.Body)
				}
				found := false
				for _, op := range ops {
					var p Payload
					if err := json.Unmarshal(op.Payload, &p); err != nil {
						t.Fatal(err)
					}
					if p.Name == "work.attention_msg" {
						found = true
						if !strings.Contains(p.Value, r.Steps[0].Note) {
							t.Fatal(p.Value)
						}
					}
				}
				if !found {
					t.Fatal("missing attention message")
				}
			})
		}
	}
}

func TestSettleResumedSuccessComment(t *testing.T) {
	r, res := settleFixture(), resultFixture()
	r.Outcome = "succeeded"
	r.Resume = &ResumeState{UID: "approval-1", Generation: 1}
	ops := SettleOps(r, res, settleTime)
	wantSettleKinds(t, ops, "meta.unset", "comment", "close", "observe")
	op, p := settlePayload(t, ops, "comment")
	if p.Body != "Run run-1 resumed and succeeded." || op.Key != "herdr-kata:run-1:resume:approval-1:comment:resumed-succeeded" {
		t.Fatalf("%+v %+v", op, p)
	}
	if !reflect.DeepEqual(ops, SettleOps(r, res, settleTime)) {
		t.Fatal("recovery changed the frozen comment")
	}
	r.Resume = nil
	wantSettleKinds(t, SettleOps(r, res, settleTime), "close", "observe")
}

// Slice F: the resumed review's disposition must reach both the issue thread
// and run history, even when a final command follows the last prompt report.
func TestSettleResumedSuccessNote(t *testing.T) {
	for _, kind := range []string{"prompt", "command", "mixed"} {
		for _, owned := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/owned=%t", kind, owned), func(t *testing.T) {
				r, res := settleFixture(), resultFixture()
				r.Outcome, r.IssueOwned = "succeeded", owned
				r.Resume = &ResumeState{UID: "approval-1", Generation: 1}
				r.Job.Name = "Review workflow"
				const note = "Approved digest complete; next action: wait for maintainer response"
				r.Steps = []StepState{{Key: "draft", Kind: "prompt", Attempt: 1, Status: "succeeded", Note: "Approve and resume."}, {Key: "review", Kind: "prompt", Attempt: 2, Status: "succeeded", Note: note}}
				r.WFDef = &defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "draft", Kind: "prompt", Prompt: "Write the draft"}, {Key: "review", Kind: "prompt", Prompt: "Review the draft", After: []string{"draft"}}}}
				wantSummary := "Updated the parser and verified the focused tests pass."
				if kind != "prompt" {
					r.Steps[1].Kind = "command"
					r.Steps[1].Receipt = &Receipt{Version: 1, Run: "run-1", Step: "review", Attempt: 2}
					r.WFDef.Steps[1] = defs.Step{Key: "review", Kind: "command", Command: "true", After: []string{"draft"}}
					if kind == "command" {
						r.Steps[0].Kind = "command"
						r.Steps[0].Receipt = &Receipt{Version: 1, Run: "run-1", Step: "draft", Attempt: 1}
						r.WFDef.Steps[0] = defs.Step{Key: "draft", Kind: "command", Command: "true"}
						wantSummary = "Workflow Review workflow: 2 command steps exited 0 (run run-1, occurrence 2026-01-02T03:04:05Z)"
					}
				}
				ops := SettleOps(r, res, settleTime)
				_, comment := settlePayload(t, ops, "comment")
				_, observed := settlePayload(t, ops, "observe")
				for _, text := range []string{comment.Body, observed.Observation.Summary.Message} {
					if !strings.Contains(text, "Step review: "+note) || strings.Contains(text, "Approve and resume.") {
						t.Fatalf("final handoff note missing or stale: %q", text)
					}
				}
				if !strings.HasPrefix(comment.Body, "Run run-1 resumed and succeeded.\n\nStep review: "+note) {
					t.Fatal(comment.Body)
				}
				if !strings.Contains(observed.Observation.Summary.Message, wantSummary) {
					t.Fatal("original outcome summary lost", observed.Observation.Summary.Message)
				}
				if owned {
					_, closed := settlePayload(t, ops, "close")
					if closed.Close.Message != wantSummary {
						t.Fatal("note changed the close payload", closed.Close)
					}
				}
				r.Resume = nil
				_, ordinary := settlePayload(t, SettleOps(r, res, settleTime), "observe")
				if ordinary.Observation.Summary.Message != wantSummary {
					t.Fatal("ordinary success changed", ordinary.Observation.Summary.Message)
				}
			})
		}
	}
}

func TestSettleResumedSuccessNoteBoundary(t *testing.T) {
	for _, tc := range []struct {
		note string
		want bool
	}{{"", false}, {strings.Repeat("é", 140), true}, {strings.Repeat("x", 281), false}} {
		r, res := settleFixture(), resultFixture()
		r.Outcome, r.Resume = "succeeded", &ResumeState{UID: "approval-1", Generation: 1}
		r.Steps = []StepState{{Key: "draft", Status: "succeeded", Note: "Approve and resume."}, {Key: "review", Status: "succeeded", Note: tc.note}}
		ops := SettleOps(r, res, settleTime)
		_, comment := settlePayload(t, ops, "comment")
		_, observed := settlePayload(t, ops, "observe")
		if !tc.want {
			if comment.Body != "Run run-1 resumed and succeeded." || observed.Observation.Summary.Message != res.Summary {
				t.Fatal("empty or invalid final note changed the report", comment, observed)
			}
		} else if !strings.Contains(comment.Body, tc.note) || !strings.Contains(observed.Observation.Summary.Message, tc.note) {
			t.Fatal("valid 280-byte note was shortened", comment, observed)
		}
	}
}

func TestSettleUncertainCommandHasNoSuccessEvidence(t *testing.T) {
	for _, flag := range []string{"uncertain", "alive", "interrupted"} {
		t.Run(flag, func(t *testing.T) {
			r := commandFixture()
			for i := range r.Steps {
				receipt := r.Steps[i].Receipt
				receipt.Uncertain = flag == "uncertain"
				receipt.GroupAlive = flag == "alive"
				receipt.Interrupted = flag == "interrupted"
			}
			ops := SettleOps(r, nil, settleTime)
			for _, op := range ops {
				if op.Kind == "close" {
					t.Fatal("closed uncertain command")
				}
			}
			_, results, _, _ := settlementCommands(r)
			evidence := CommandEvidence(results)
			if len(evidence) != 0 {
				t.Fatalf("uncertain success evidence: %v", evidence)
			}
			_, p := settlePayload(t, ops, "observe")
			if p.Observation.Status != "unknown" || !strings.Contains(p.Observation.Summary.Message, "command outcome unknown") {
				t.Fatal(p.Observation)
			}
		})
	}
}

func TestSettleCommandEvidenceUsesDefinitionOrderWithoutTimes(t *testing.T) {
	r := commandFixture()
	for i := range r.Steps {
		r.Steps[i].Receipt.StartedAt = time.Time{}
		r.Steps[i].Receipt.EndedAt = time.Time{}
	}
	r.Steps[0], r.Steps[1] = r.Steps[1], r.Steps[0]
	ops := SettleOps(r, nil, settleTime)
	_, p := settlePayload(t, ops, "close")
	if !reflect.DeepEqual(p.Close.Evidence, []string{"test:go vet ./parser", "test:go test ./parser"}) || !strings.Contains(p.Close.Message, "2 command steps exited 0") {
		t.Fatal(p.Close)
	}
}

// Spec §23.4: a command-only success on an existing issue leaves the thread
// alone; its observation still carries the command receipt message. A run
// that already wrote to the thread, by a finding or the hold a resume follows,
// still closes that story with a comment.
func TestSettleExistingCommandSuccessIsQuiet(t *testing.T) {
	receipt := "Workflow Checks: 2 command steps exited 0 (run run-1, occurrence 2026-01-02T03:04:05Z)"
	for _, tc := range []struct {
		name  string
		setup func(*Run) *Result
		want  []string
	}{
		{"success", func(*Run) *Result { return nil }, []string{"observe"}},
		{"resumed-success", func(r *Run) *Result {
			r.Resume = &ResumeState{UID: "approval-1", Generation: 1}
			return nil
		}, []string{"comment", "observe"}},
		{"command-failure", func(r *Run) *Result {
			r.Steps[1].Receipt.ExitCode, r.Steps[1].Status = 1, "failed"
			return nil
		}, []string{"comment", "observe"}},
		{"held", func(r *Run) *Result {
			r.Phase, r.Reason = Held, "lost_state"
			return nil
		}, []string{"comment", "observe"}},
		{"handed-off", func(r *Run) *Result {
			r.Phase, r.Outcome, r.Reason = HandedOff, "succeeded", "operator asked"
			return nil
		}, []string{"comment", "resolve-recipient", "notify", "observe"}},
		{"published-finding", func(r *Run) *Result {
			r.Ops = append(r.Ops, newOp("herdr-kata:run-1:finding", "comment", Payload{Ref: r.IssueRef, Body: "Check failed", Chain: &ChainComment{Step: "test", Attempt: 1}}, "herdr-kata:run-1:finding"))
			return nil
		}, []string{"comment", "observe"}},
		{"native-child", func(r *Run) *Result {
			r.Child = &ChildRun{}
			return nil
		}, []string{"comment", "observe"}},
		{"prompt-success", func(r *Run) *Result {
			r.WFDef.Steps = append(r.WFDef.Steps, defs.Step{Key: "main", Kind: "prompt"})
			r.Steps = append(r.Steps, StepState{Key: "main", Kind: "prompt", Attempt: 1, Status: "succeeded"})
			return resultFixture()
		}, []string{"comment", "observe"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := commandFixture()
			r.IssueOwned = false
			res := tc.setup(r)
			ops := SettleOps(r, res, settleTime)
			wantSettleKinds(t, ops, tc.want...)
			if len(tc.want) == 1 {
				_, p := settlePayload(t, ops, "observe")
				if p.Observation.Status != "succeeded" || !strings.HasSuffix(p.Observation.Summary.Message, receipt) {
					t.Fatalf("observation=%+v", p.Observation)
				}
			}
		})
	}
	// A per-run command-only success still closes its own issue.
	wantSettleKinds(t, SettleOps(commandFixture(), nil, settleTime), "close", "observe")
}
