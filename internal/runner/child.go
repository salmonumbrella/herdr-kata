package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/inbox"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

// childInputs synthesizes execution inputs from the frozen parent binding.
// Native delivery definitions are historical evidence, never adapter jobs.
func (m *Manager) childInputs(ctx context.Context, d sched.Due, projectUID string) (sched.Due, error) {
	c := *d.Child
	issue, err := m.k.ShowIssue(ctx, c.IssueRef)
	if err != nil {
		return d, err
	}
	if err := inbox.ValidateNativeSource(issue); err != nil {
		return d, err
	}
	if issue.UID != c.IssueUID || issue.ProjectUID != projectUID || issue.Status != "open" {
		return d, fmt.Errorf("%w: child changed project or closed", inbox.ErrUnsupported)
	}
	decision, _, err := inbox.Takeover(issue, m.cfg.Kata.Actor)
	if err != nil {
		return d, err
	}
	if d.IssueRefs == nil {
		d.IssueRefs = map[string]string{}
	}
	if ref := kata.IssueReference(m.cfg.Kata.Project, issue); ref != "" {
		d.IssueRefs[issue.UID] = ref
	}
	c.Ownership = sched.Ownership{Decision: decision, Revision: issue.Revision}
	if c.Binding.Profile == "" {
		c.Binding.Profile = "default"
	}
	if _, ok := m.cfg.Profiles[c.Binding.Profile]; !ok {
		return d, fmt.Errorf("%w: unknown profile", inbox.ErrUnsupported)
	}
	if c.Binding.WFDef != nil {
		for _, s := range c.Binding.WFDef.Steps {
			if _, ok := m.cfg.Profiles[defs.StepProfile(s, c.Binding.Profile)]; s.Kind == "prompt" && !ok {
				return d, fmt.Errorf("%w: unknown step profile", inbox.ErrUnsupported)
			}
		}
	}
	if c.Scheduled.IsZero() || c.DeliveryJobUID == "" || c.DeliveryEventUID == "" || c.NotifyKey == "" || !json.Valid(c.NotifyJSON) {
		return d, errors.New("invalid child admission inputs")
	}
	options, _ := json.Marshal(map[string]string{"agent": c.Binding.Profile})
	d.Job = kata.Job{UID: c.DeliveryJobUID, DefinitionEventUID: c.DeliveryEventUID, Name: issue.Title}
	d.Def = defs.JobDef{Version: 1, Kind: "job", Enabled: true, Trigger: defs.Trigger{Kind: "manual"}, Action: defs.Action{Kind: "execute", Prompt: "Work Kata issue " + c.IssueRef}, Issue: &defs.IssuePolicy{Kind: "existing", UID: c.IssueUID, Title: issue.Title, Body: issue.Body}, Overlap: "allow", Catchup: "skip", Options: options}
	d.Workflow, d.WFDef = nil, c.Binding.WFDef
	if d.WFDef != nil {
		raw, err := json.Marshal(d.WFDef)
		if err != nil {
			return d, err
		}
		d.Workflow = &kata.Workflow{Name: c.Binding.WorkflowName, UID: c.Binding.WorkflowUID, DefinitionEventUID: c.Binding.WorkflowEventUID, Definition: raw}
		d.Def.Action.WorkflowUID = c.Binding.WorkflowUID
	}
	d.Child, d.Occurrence = &c, c.Scheduled
	return d, nil
}

func childTracking(r *Run) Op {
	op := newOp("child-tracked", "meta.set", Payload{Ref: r.IssueRef, Name: "work.attention", Value: "ok", Owner: r.Target.Identity.Actor}, "")
	op.Guard = &kata.Cond{IfMatch: strconv.FormatInt(takeover(r).Revision, 10)}
	return op
}

func attentionOwner(r *Run) string {
	if takeover(r) != nil {
		return r.Target.Identity.Actor
	}
	return ""
}

func childIssueMatches(r *Run, issue kata.IssueView) bool {
	return takeover(r) == nil || issue.UID == r.IssueUID && issue.ProjectUID == r.Target.Identity.ProjectUID
}
