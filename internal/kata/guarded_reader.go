package kata

import (
	"context"
	"errors"
)

// GuardedReader fences lane reads and access probes through its route guard.
// A refused lane keeps snapshots and pending checkpoints until its next
// passing route check. Refusal must also fence definition and readiness reads.
type GuardedReader struct {
	*CLI
	Guard *RouteGuard
}

func (r GuardedReader) allowed() error {
	if !r.Guard.Allowed() {
		return errors.New("project route refused")
	}
	return nil
}
func (r GuardedReader) Capabilities(ctx context.Context) (Capabilities, error) {
	if e := r.allowed(); e != nil {
		return Capabilities{}, e
	}
	return r.Guard.ProbeCapabilities(ctx)
}
func (r GuardedReader) ProbeAccess(ctx context.Context) error {
	return r.Guard.ProbeAccess(ctx)
}
func (r GuardedReader) ShowJob(ctx context.Context, uid string) (Job, error) {
	if e := r.allowed(); e != nil {
		return Job{}, e
	}
	return r.CLI.ShowJob(ctx, uid)
}
func (r GuardedReader) ShowWorkflow(ctx context.Context, uid string) (Workflow, error) {
	if e := r.allowed(); e != nil {
		return Workflow{}, e
	}
	return r.CLI.ShowWorkflow(ctx, uid)
}
func (r GuardedReader) ListJobs(ctx context.Context) ([]Job, error) {
	if e := r.allowed(); e != nil {
		return nil, e
	}
	return r.CLI.ListJobs(ctx)
}
func (r GuardedReader) ListWorkflows(ctx context.Context) ([]Workflow, error) {
	if e := r.allowed(); e != nil {
		return nil, e
	}
	return r.CLI.ListWorkflows(ctx)
}
func (r GuardedReader) Ready(ctx context.Context) ([]IssueView, error) {
	if e := r.allowed(); e != nil {
		return nil, e
	}
	return r.CLI.Ready(ctx)
}
func (r GuardedReader) Inbox(ctx context.Context, user string) ([]InboxRequest, error) {
	if e := r.allowed(); e != nil {
		return nil, e
	}
	return r.CLI.Inbox(ctx, user)
}
func (r GuardedReader) ShowIssue(ctx context.Context, ref string) (IssueView, error) {
	if e := r.allowed(); e != nil {
		return IssueView{}, e
	}
	return r.CLI.ShowIssue(ctx, ref)
}
func (r GuardedReader) PlanningDates(ctx context.Context, ref string) (PlanningDates, error) {
	if e := r.allowed(); e != nil {
		return PlanningDates{}, e
	}
	return r.CLI.PlanningDates(ctx, ref)
}
