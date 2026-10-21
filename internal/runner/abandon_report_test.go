package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestAbandonLostRunLeavesTruthfulIssueReport(t *testing.T) {
	for _, kind := range []string{"owned", "existing", "closed"} {
		t.Run(kind, func(t *testing.T) {
			f := newAdmission(t)
			remote := kata.Run{UID: f.uid, JobUID: "job", DefinitionEventUID: "event", OccurrenceKey: "manual:" + f.uid, IssueUID: "issue", Actor: "worker", ExecutorLabel: f.cfg.Executor, Status: "running", Revision: 3}
			observations, comments, attention, messages := 0, 0, 0, 0
			f.k.run = func(context.Context, string) (kata.Run, error) { return remote, nil }
			f.k.observe = func(_ context.Context, uid string, o kata.Observation) (kata.Run, bool, error) {
				observations++
				if uid != f.uid || o.Status != "unknown" || o.ExpectedRevision != 3 {
					t.Fatal(uid, o)
				}
				remote.Status, remote.Summary, remote.EndedAt, remote.Revision = o.Status, o.Summary, o.EndedAt, 4
				return remote, false, nil
			}
			revision := int64(7)
			f.k.show = func(_ context.Context, ref string) (kata.IssueView, error) {
				if ref != "issue" {
					t.Fatal(ref)
				}
				issue := kata.IssueView{UID: "issue", ProjectUID: "project", Status: "open", Revision: revision}
				if kind != "existing" {
					issue.Metadata = map[string]json.RawMessage{"herdr_kata.run": json.RawMessage(`"` + f.uid + `"`)}
				}
				if kind == "closed" {
					issue.Status = "closed"
				}
				return issue, nil
			}
			f.k.comment = func(_ context.Context, ref, body, key string) error {
				comments++
				if ref != "issue" || !strings.Contains(body, "local state lost") || !strings.Contains(body, f.uid) || key != "herdr-kata:"+f.uid+":abandon:comment" {
					t.Fatal(ref, body, key)
				}
				return nil
			}
			f.k.meta = func(_ context.Context, ref, key, value string, cond kata.Cond) (int64, error) {
				if kind != "owned" || ref != "issue" || cond.IfMatch == "" {
					t.Fatal("unguarded or unowned attention", kind, ref, key, cond)
				}
				switch key {
				case "work.attention":
					if value != "needs-human" {
						t.Fatal(value)
					}
					attention++
				case "work.attention_msg":
					if value != "herdr-kata: local state lost (run "+f.uid+")" {
						t.Fatal(value)
					}
					messages++
				default:
					t.Fatal(key)
				}
				revision++
				return revision, nil
			}
			for range 2 {
				if err := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); err != nil {
					t.Fatal(err)
				}
			}
			if observations != 1 || comments != 1 {
				t.Fatal("duplicate or missing report", observations, comments)
			}
			if kind == "owned" && (attention != 1 || messages != 1) {
				t.Fatal("lost issue still looks healthy", attention, messages)
			}
			if _, err := f.s.fsys.Lstat(f.s.layout.RunDir(f.uid)); err == nil {
				t.Fatal("abandon recreated execution journal")
			}
		})
	}
}

func TestAbandonReportReplaysFrozenCommentAfterLostReply(t *testing.T) {
	f := newAdmission(t)
	remote := kata.Run{UID: f.uid, JobUID: "job", DefinitionEventUID: "event", OccurrenceKey: "manual:" + f.uid, IssueUID: "issue", Actor: "worker", ExecutorLabel: f.cfg.Executor, Status: "running", Revision: 3}
	observations := 0
	f.k.run = func(context.Context, string) (kata.Run, error) { return remote, nil }
	f.k.observe = func(_ context.Context, _ string, o kata.Observation) (kata.Run, bool, error) {
		observations++
		remote.Status, remote.Summary, remote.EndedAt, remote.Revision = o.Status, o.Summary, o.EndedAt, 4
		return remote, false, nil
	}
	f.k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{UID: "issue", ProjectUID: "project", Status: "open", Revision: 7}, nil
	}
	var requests []kata.CommentReq
	f.k.typedComment = func(_ context.Context, req kata.CommentReq) (kata.Comment, error) {
		requests = append(requests, req)
		if len(requests) == 1 {
			return kata.Comment{}, lostReply
		}
		return kata.Comment{UID: "comment"}, nil
	}
	if err := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); err == nil {
		t.Fatal("lost reply was hidden")
	}
	reports := NewStore(f.s.fsys, state.Layout{Root: filepath.Join(f.s.layout.Root, "abandoned", "reports")})
	saved := loaded(t, reports, f.uid)
	if len(saved.Ops) != 1 || saved.Ops[0].FirstSent == nil || saved.Ops[0].Done {
		t.Fatal("missing frozen report", saved.Ops)
	}
	// Advance the durable retry schedule without sleeping on a real clock.
	if _, err := reports.Transition(f.uid, func(r *Run) error { r.Ops[0].NextAt = time.Time{}; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); err != nil {
		t.Fatal(err)
	}
	if observations != 1 || len(requests) != 2 || requests[0] != requests[1] {
		t.Fatal("report was changed or observation repeated", observations, requests)
	}
	if !loaded(t, reports, f.uid).Ops[0].Done {
		t.Fatal("report completion not saved")
	}
}

func TestAbandonCommentCompletesWhenAttentionChangesConcurrently(t *testing.T) {
	f := newAdmission(t)
	remote := kata.Run{UID: f.uid, JobUID: "job", DefinitionEventUID: "event", OccurrenceKey: "manual:" + f.uid, IssueUID: "issue", Actor: "worker", ExecutorLabel: f.cfg.Executor, Status: "running", Revision: 3}
	f.k.run = func(context.Context, string) (kata.Run, error) { return remote, nil }
	f.k.observe = func(_ context.Context, _ string, o kata.Observation) (kata.Run, bool, error) {
		remote.Status, remote.Summary, remote.EndedAt, remote.Revision = o.Status, o.Summary, o.EndedAt, 4
		return remote, false, nil
	}
	f.k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{UID: "issue", ProjectUID: "project", Status: "open", Revision: 8, Metadata: map[string]json.RawMessage{"herdr_kata.run": json.RawMessage(`"` + f.uid + `"`), "work.attention": json.RawMessage(`"stuck"`)}}, nil
	}
	comments, writes := 0, 0
	f.k.comment = func(context.Context, string, string, string) error { comments++; return nil }
	f.k.meta = func(_ context.Context, ref, key, value string, cond kata.Cond) (int64, error) {
		writes++
		if ref != "issue" || key != "work.attention" || cond.IfMatch != "8" {
			t.Fatal(ref, key, cond)
		}
		return 0, &kata.Error{Code: "revision_conflict"}
	}
	for range 2 {
		if err := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); err != nil {
			t.Fatal("delivered loss comment left abandon stuck", err)
		}
	}
	if comments != 1 || writes != 1 {
		t.Fatal("repeated comment or attention write", comments, writes)
	}
	reports := NewStore(f.s.fsys, state.Layout{Root: filepath.Join(f.s.layout.Root, "abandoned", "reports")})
	saved := loaded(t, reports, f.uid)
	if saved.Inspect != "" || head(saved) != nil || saved.Ops[1].FirstSent == nil || saved.Ops[1].Guard.IfMatch != "8" || saved.Ops[1].Inspect == "" {
		t.Fatal("conflicted intent was lost or still blocks completion", saved.Ops)
	}
	if _, err := f.s.fsys.Lstat(filepath.Join(f.s.layout.Root, "abandoned", f.uid+".json")); !os.IsNotExist(err) {
		t.Fatal("completed abandon retained its pending observation", err)
	}
}

func TestAbandonReportNeverRetiresSigningRefusalEvidence(t *testing.T) {
	f := newAdmission(t)
	target, err := f.m.pin()
	if err != nil {
		t.Fatal(err)
	}
	remote := kata.Run{UID: f.uid, JobUID: "job", DefinitionEventUID: "event", OccurrenceKey: "manual:" + f.uid, IssueUID: "issue", Actor: "worker", ExecutorLabel: f.cfg.Executor, Status: "unknown", Summary: kata.Summary{Version: 1, Message: "local state lost"}, Revision: 4}
	f.k.run = func(context.Context, string) (kata.Run, error) { return remote, nil }
	f.k.comment = func(context.Context, string, string, string) error { t.Fatal("repeated delivered comment"); return nil }
	f.k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) {
		t.Fatal("retried inspected signing refusal")
		return 0, nil
	}
	reports := NewStore(f.s.fsys, state.Layout{Root: filepath.Join(f.s.layout.Root, "abandoned", "reports")})
	yes := true
	comment := newOp("abandon:comment", "comment", Payload{Ref: "issue", Body: "loss"}, "key")
	comment.Done = true
	attention := newOp("abandon:attention", "meta.set", Payload{Ref: "issue", Name: "work.attention", Value: "needs-human"}, "")
	attention.SigningRefused, attention.Inspect = true, "operation effect not confirmed"
	r := &Run{Version: 1, UID: f.uid, Source: "abandon-report", Target: target, Admitted: &yes, Phase: Settled, IssueUID: "issue", Ops: []Op{comment, attention}, Inspect: attention.Inspect}
	if err := reports.Create(r); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(reports.layout.RunFile(f.uid))
	if err != nil {
		t.Fatal(err)
	}
	if err := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); err == nil || !strings.Contains(err.Error(), "signing refusal evidence must be resolved") {
		t.Fatal(err)
	}
	after, err := os.ReadFile(reports.layout.RunFile(f.uid))
	if err != nil || string(before) != string(after) {
		t.Fatal("signing evidence changed", err)
	}
}

func TestAbandonSigningRefusalBlocksAdmissionUntilReportSucceeds(t *testing.T) {
	f := newAdmission(t)
	lost := kata.Run{UID: f.uid, JobUID: "job", DefinitionEventUID: "event", OccurrenceKey: "manual:" + f.uid, IssueUID: "issue", Actor: "worker", ExecutorLabel: f.cfg.Executor, Status: "running", Revision: 3}
	f.k.run = func(_ context.Context, uid string) (kata.Run, error) {
		if uid == f.uid {
			return lost, nil
		}
		return kata.Run{}, &kata.Error{Code: "not_found"}
	}
	f.k.observe = func(_ context.Context, uid string, o kata.Observation) (kata.Run, bool, error) {
		if uid == f.uid {
			lost.Status, lost.Summary, lost.EndedAt, lost.Revision = o.Status, o.Summary, o.EndedAt, 4
		}
		return kata.Run{}, false, nil
	}
	f.k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{UID: "issue", ProjectUID: "project", Status: "open", Revision: 7}, nil
	}
	commentCalls := 0
	f.k.comment = func(context.Context, string, string, string) error {
		commentCalls++
		if commentCalls == 1 {
			return &kata.Error{Code: "root_signing_key_unavailable", Exit: 5}
		}
		return nil
	}
	if err := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); err == nil {
		t.Fatal("signing refusal was hidden")
	}
	reports := NewStore(f.s.fsys, state.Layout{Root: filepath.Join(f.s.layout.Root, "abandoned", "reports")})
	if !loaded(t, reports, f.uid).Ops[0].SigningRefused {
		t.Fatal("missing signing evidence")
	}
	// The route diagnosis now passes; the durable refusal must still gate work.
	f.start(t)
	another := f.d
	another.Manual = true
	another.ManualRunUID = ident.RunUID(time.Now(), "another-run")
	f.m.executions.Store(another.ManualRunUID, struct{}{})
	if result, err := f.m.Admit(t.Context(), another); result != sched.NotAdmitted || err == nil || !strings.Contains(err.Error(), "signing_blocked") {
		t.Fatal("recovered diagnosis bypassed unresolved report", result, err)
	}
	if blocked, err := f.s.SigningBlockedFor("other-project"); err != nil || blocked {
		t.Fatal("report blocked an unrelated lane", blocked, err)
	}
	if _, err := reports.Transition(f.uid, func(r *Run) error { r.Ops[0].NextAt = time.Time{}; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); err != nil {
		t.Fatal(err)
	}
	if result, err := f.m.Admit(t.Context(), another); result != sched.Admitted || err != nil {
		t.Fatal("successful report did not unblock admission", result, err)
	}
}

func TestAbandonReportQuarantinePreservesSigningEvidenceAndVeto(t *testing.T) {
	f := newAdmission(t)
	target, err := f.m.pin()
	if err != nil {
		t.Fatal(err)
	}
	reports := NewStore(f.s.fsys, state.Layout{Root: filepath.Join(f.s.layout.Root, "abandoned", "reports")})
	op := newOp("abandon:comment", "comment", Payload{Ref: "issue", Body: "loss"}, "loss-key")
	op.SigningRefused = true
	if err := reports.Create(&Run{Version: 1, UID: f.uid, Source: "abandon-report", Target: target, Phase: Settled, IssueUID: "issue", Ops: []Op{op}}); err != nil {
		t.Fatal(err)
	}
	if blocked, err := f.s.SigningBlockedFor("project"); err != nil || !blocked {
		t.Fatal("report did not block its lane", blocked, err)
	}
	if err := state.QuarantineRunDirectory(f.s.fsys, reports.layout, f.uid); err != nil {
		t.Fatal(err)
	}
	if blocked, err := f.s.SigningBlockedFor("project"); err != nil || blocked {
		t.Fatal("whole-report quarantine did not unblock its lane", blocked, err)
	}
	var archived Run
	if err := state.ReadJSON(f.s.fsys, filepath.Join(reports.layout.Root, "quarantine", "runs", f.uid, "run.json"), 4<<20, &archived); err != nil || !archived.Ops[0].SigningRefused || archived.Ops[0].Done {
		t.Fatal("quarantine lost signing evidence", archived.Ops, err)
	}
	f.k.run = func(context.Context, string) (kata.Run, error) {
		return kata.Run{UID: f.uid, JobUID: "job", DefinitionEventUID: "event", OccurrenceKey: "manual:" + f.uid, IssueUID: "issue", Actor: "worker", ExecutorLabel: f.cfg.Executor, Status: "unknown", Summary: kata.Summary{Version: 1, Message: "local state lost"}, Revision: 4}, nil
	}
	f.k.comment = func(context.Context, string, string, string) error {
		t.Fatal("replaced a quarantined report")
		return nil
	}
	if err := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); err == nil || !strings.Contains(err.Error(), "quarantined") {
		t.Fatal("quarantine veto was lost", err)
	}
}

func TestAbandonCompletesWhenIssueIsGone(t *testing.T) {
	for _, when := range []string{"before-report", "after-fatal-comment", "during-comment"} {
		t.Run(when, func(t *testing.T) {
			f := newAdmission(t)
			remote := kata.Run{UID: f.uid, JobUID: "job", DefinitionEventUID: "event", OccurrenceKey: "manual:" + f.uid, IssueUID: "issue", Actor: "worker", ExecutorLabel: f.cfg.Executor, Status: "running", Revision: 3}
			observations, comments := 0, 0
			gone := when == "before-report"
			f.k.run = func(context.Context, string) (kata.Run, error) { return remote, nil }
			f.k.observe = func(_ context.Context, uid string, o kata.Observation) (kata.Run, bool, error) {
				observations++
				if uid != f.uid || o.Status != "unknown" || o.ExpectedRevision != 3 {
					t.Fatal(uid, o)
				}
				remote.Status, remote.Summary, remote.EndedAt, remote.Revision = o.Status, o.Summary, o.EndedAt, 4
				return remote, false, nil
			}
			f.k.show = func(_ context.Context, ref string) (kata.IssueView, error) {
				if ref != "issue" {
					t.Fatal(ref)
				}
				if gone {
					return kata.IssueView{}, &kata.Error{Code: "issue_not_found"}
				}
				return kata.IssueView{UID: "issue", ProjectUID: "project", Status: "open", Revision: 7}, nil
			}
			f.k.comment = func(_ context.Context, ref, body, key string) error {
				comments++
				if ref != "issue" || key != "herdr-kata:"+f.uid+":abandon:comment" || !strings.Contains(body, "local state lost") {
					t.Fatal(ref, body, key)
				}
				if when == "after-fatal-comment" {
					return &kata.Error{Code: "validation", Message: "comment rejected"}
				}
				if when == "during-comment" {
					gone = true
					return &kata.Error{Code: "issue_not_found"}
				}
				t.Fatal("comment sent to missing issue")
				return nil
			}
			f.k.meta = func(context.Context, string, string, string, kata.Cond) (int64, error) {
				t.Fatal("metadata sent to missing issue")
				return 0, nil
			}
			if when == "after-fatal-comment" {
				if err := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); err == nil {
					t.Fatal("fatal report hidden")
				}
				gone = true
			}
			for range 2 {
				if err := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); err != nil {
					t.Fatal("missing issue trapped abandonment", err)
				}
			}
			wantComments := 1
			if when == "before-report" {
				wantComments = 0
			}
			if observations != 1 || comments != wantComments || remote.Status != "unknown" || remote.Summary.Message != "local state lost" {
				t.Fatal(observations, comments, remote)
			}
			if _, err := os.Stat(filepath.Join(f.s.layout.Root, "abandoned", f.uid+".json")); !os.IsNotExist(err) {
				t.Fatal("pending observation retained", err)
			}
			if when != "before-report" {
				saved := loaded(t, NewStore(f.s.fsys, abandonReportsLayout(f.s.layout)), f.uid)
				if head(saved) != nil || saved.Inspect != "" || saved.Ops[0].FirstSent == nil || !strings.Contains(saved.Ops[0].Inspect, "issue gone") {
					t.Fatal("report not retired with evidence", saved)
				}
			}
		})
	}
}

func TestMissingIssueCannotRetireSigningRefusalEvidence(t *testing.T) {
	f := newAdmission(t)
	target, err := f.m.pin()
	if err != nil {
		t.Fatal(err)
	}
	remote := kata.Run{UID: f.uid, JobUID: "job", DefinitionEventUID: "event", OccurrenceKey: "manual:" + f.uid, IssueUID: "issue", Actor: "worker", ExecutorLabel: f.cfg.Executor, Status: "unknown", Summary: kata.Summary{Version: 1, Message: "local state lost"}, Revision: 4}
	f.k.run = func(context.Context, string) (kata.Run, error) { return remote, nil }
	f.k.show = func(context.Context, string) (kata.IssueView, error) {
		return kata.IssueView{}, &kata.Error{Code: "issue_not_found"}
	}
	reports := NewStore(f.s.fsys, abandonReportsLayout(f.s.layout))
	op := newOp("abandon:comment", "comment", Payload{Ref: "issue", Body: "loss"}, "loss-key")
	op.SigningRefused, op.Inspect = true, "signing refusal requires inspection"
	if err := reports.Create(&Run{Version: 1, UID: f.uid, Source: "abandon-report", Target: target, Phase: Settled, IssueUID: "issue", Ops: []Op{op}, Inspect: op.Inspect}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(reports.layout.RunFile(f.uid))
	if err != nil {
		t.Fatal(err)
	}
	if err := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); err == nil || !strings.Contains(err.Error(), "signing refusal evidence must be resolved") {
		t.Fatal("missing issue retired signing evidence", err)
	}
	after, err := os.ReadFile(reports.layout.RunFile(f.uid))
	if err != nil || string(before) != string(after) {
		t.Fatal("changed signing evidence", err)
	}
}

func TestCorruptAbandonRetryRemainsVisibleUntilWholeReportResolution(t *testing.T) {
	f := newAdmission(t)
	remote := kata.Run{UID: f.uid, JobUID: "job", DefinitionEventUID: "event", OccurrenceKey: "manual:" + f.uid, IssueUID: "issue", Actor: "worker", ExecutorLabel: f.cfg.Executor, Status: "running", Revision: 3}
	observations, comments := 0, 0
	f.k.run = func(context.Context, string) (kata.Run, error) { return remote, nil }
	f.k.observe = func(_ context.Context, uid string, o kata.Observation) (kata.Run, bool, error) {
		observations++
		if uid != f.uid || o.Status != "unknown" || o.ExpectedRevision != 3 {
			t.Fatal(uid, o)
		}
		remote.Status, remote.Summary, remote.EndedAt, remote.Revision = o.Status, o.Summary, o.EndedAt, 4
		return remote, false, nil
	}
	f.k.show = func(_ context.Context, ref string) (kata.IssueView, error) {
		if ref != "issue" {
			t.Fatal(ref)
		}
		return kata.IssueView{UID: "issue", ProjectUID: "project", Status: "open", Revision: 7}, nil
	}
	f.k.comment = func(_ context.Context, ref, body, key string) error {
		comments++
		if ref != "issue" || key != "herdr-kata:"+f.uid+":abandon:comment" || !strings.Contains(body, "local state lost") {
			t.Fatal(ref, body, key)
		}
		return lostReply
	}
	if err := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); err == nil {
		t.Fatal("lost reply hidden")
	}
	reports := abandonReportsLayout(f.s.layout)
	if err := os.WriteFile(reports.RunFile(f.uid), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Abandon(t.Context(), f.cfg, f.k, f.g, f.s.fsys, f.s.layout, f.uid); err == nil {
		t.Fatal("corruption hidden")
	}
	entries, err := os.ReadDir(reports.QuarantineRunDir(f.uid))
	if err != nil || len(entries) != 1 {
		t.Fatal("journal not preserved", entries, err)
	}
	archived := filepath.Join(reports.QuarantineRunDir(f.uid), entries[0].Name())
	if data, err := os.ReadFile(archived); err != nil || string(data) != "broken" {
		t.Fatal("corrupt evidence changed", err)
	}
	problems, err := AbandonReportProblems(f.s.fsys, f.s.layout)
	if err != nil || len(problems) != 1 || !strings.Contains(problems[0].Detail, "automatically quarantined") || !strings.Contains(problems[0].Detail, reports.RunDir(f.uid)) || !strings.Contains(problems[0].Detail, reports.QuarantineRunDir(f.uid)) {
		t.Fatal("automatic quarantine hid unfinished loss report", problems, err)
	}
	if observations != 1 || comments != 1 {
		t.Fatal("retry repeated remote writes", observations, comments)
	}
	// Preserve the remaining source directory alongside the quarantined journal.
	if err := os.Rename(reports.RunDir(f.uid), filepath.Join(reports.QuarantineRunDir(f.uid), "remaining")); err != nil {
		t.Fatal(err)
	}
	problems, err = AbandonReportProblems(f.s.fsys, f.s.layout)
	if err != nil || len(problems) != 0 {
		t.Fatal("whole-report resolution still unhealthy", problems, err)
	}
	if data, err := os.ReadFile(archived); err != nil || string(data) != "broken" {
		t.Fatal("resolution removed evidence", err)
	}
}
