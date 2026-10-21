package runner

import (
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/inbox"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

type RequestExecutor struct {
	InstanceUID   string `json:"instance_uid"`
	ProjectUID    string `json:"project_uid"`
	Actor         string `json:"actor"`
	ExecutorLabel string `json:"executor_label"`
}
type RequestAdmission struct {
	Version    int             `json:"version"`
	RequestUID string          `json:"request_uid"`
	RunUID     string          `json:"run_uid"`
	JournalID  string          `json:"journal_id"`
	Executor   RequestExecutor `json:"executor"`
}
type IssueRequestRun struct {
	Request    sched.ExecuteRequest `json:"request"`
	ParentUID  string               `json:"parent_uid"`
	Binding    sched.Binding        `json:"binding"`
	Ownership  sched.Ownership      `json:"ownership"`
	NotifyKey  string               `json:"notify_key"`
	NotifyJSON json.RawMessage      `json:"notify_json"`
	Admission  RequestAdmission     `json:"admission"`
}
type RequestOutcome struct {
	Version    int        `json:"version"`
	RequestUID string     `json:"request_uid"`
	RunUID     string     `json:"run_uid"`
	JournalID  string     `json:"journal_id"`
	Phase      Phase      `json:"phase"`
	Outcome    string     `json:"outcome"`
	Reason     string     `json:"reason"`
	Summary    string     `json:"summary"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}
func requestReceipt(r *Run) RequestAdmission {
	t := r.Target
	return RequestAdmission{Version: 1, RequestUID: r.IssueRequest.Request.RequestUID, RunUID: r.UID, JournalID: r.JournalID, Executor: RequestExecutor{InstanceUID: t.Identity.InstanceUID, ProjectUID: t.Identity.ProjectUID, Actor: t.Identity.Actor, ExecutorLabel: t.Executor}}
}
func requestReceiptMatches(r *Run, issue kata.IssueView) bool {
	b, _ := json.Marshal(r.IssueRequest.Admission)
	return issue.UID == r.IssueUID && issue.ProjectUID == r.Target.Identity.ProjectUID && jsonEqual(issue.Metadata["herdr_kata.admission"], b)
}
func takeover(r *Run) *sched.Ownership {
	if r.IssueRequest != nil {
		return &r.IssueRequest.Ownership
	}
	if r.Child != nil {
		return &r.Child.Ownership
	}
	return nil
}
func savedNotification(r *Run) (string, json.RawMessage) {
	if r.IssueRequest != nil {
		return r.IssueRequest.NotifyKey, r.IssueRequest.NotifyJSON
	}
	if r.Child != nil {
		return r.Child.NotifyKey, r.Child.NotifyJSON
	}
	return "", nil
}
func requestOutcomeOp(r *Run, id string, phase Phase, outcome, reason, summary string, ended *time.Time) Op {
	v := RequestOutcome{Version: 1, RequestUID: r.IssueRequest.Request.RequestUID, RunUID: r.UID, JournalID: r.JournalID, Phase: phase, Outcome: outcome, Reason: reason, Summary: capText(summary, maxSummaryBytes), StartedAt: r.StartedAt, EndedAt: ended}
	b, _ := json.Marshal(v)
	op := newOp(id, "meta.set", Payload{Ref: r.IssueRef, Name: "herdr_kata.outcome", Value: string(b), Owner: attentionOwner(r)}, "")
	op.Guard = &kata.Cond{JSONValue: true}
	return op
}

func validateRequestJournal(r *Run) error {
	c := r.IssueRequest
	if c == nil || r.Version != 2 || r.Admitted == nil || r.Child != nil || r.Admission != nil || c.Request.Version != 1 || c.Request.Kind != "execute" || c.Request.RequestedAt.IsZero() || c.NotifyKey == "" || !json.Valid(c.NotifyJSON) || r.JournalID == "" || c.Admission != requestReceipt(r) || r.Source != "issue-request:"+r.IssueUID {
		return errors.New("invalid issue-request journal")
	}
	uid, err := ident.IssueRequestUID(r.IssueUID)
	if err != nil || uid != r.UID {
		return errors.New("invalid issue-request run UID")
	}
	raw, _ := json.Marshal(c.Request)
	if _, err := inbox.ValidateRequest(kata.IssueView{UID: r.IssueUID, ProjectUID: r.Target.Identity.ProjectUID, Status: "open", ParentUID: c.ParentUID, Metadata: map[string]json.RawMessage{"herdr_kata.request": raw}}, r.Target.Identity.ProjectUID); err != nil {
		return err
	}
	if r.Target.Identity.InstanceUID == "" || r.Target.Identity.Actor == "" || r.Target.Executor == "" || c.Binding.Profile == "" || c.Ownership.Revision < 1 || c.Ownership.Decision != "claimed" && c.Ownership.Decision != "own-actor" || r.Job.UID != "" || r.Job.DefinitionEventUID != "" {
		return errors.New("invalid issue-request binding or provenance")
	}
	return nil
}
