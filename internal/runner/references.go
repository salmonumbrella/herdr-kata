package runner

import (
	"context"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"strings"
)

// Display evidence only: these refs never replace frozen mutation arguments.
func rememberReference(r *Run, issue kata.IssueView) {
	if issue.UID == "" || issue.ProjectUID != "" && issue.ProjectUID != r.Target.Identity.ProjectUID {
		return
	}
	ref := kata.IssueReference(r.ProjectName, issue)
	if ref == "" {
		return
	}
	if r.IssueRefs == nil {
		r.IssueRefs = map[string]string{}
	}
	r.IssueRefs[issue.UID] = ref
	if r.ProjectName == "" {
		r.ProjectName, _, _ = strings.Cut(ref, "#")
	}
}
func (s *Store) rememberIssue(ctx context.Context, uid string, issue kata.IssueView) error {
	r, err := s.LoadContext(ctx, uid)
	if err != nil {
		return err
	}
	ref := kata.IssueReference(r.ProjectName, issue)
	if ref == "" || r.IssueRefs[issue.UID] == ref {
		return nil
	}
	_, err = s.TransitionContext(ctx, uid, func(current *Run) error { rememberReference(current, issue); return nil })
	return err
}
func (q *Queue) showIssue(ctx context.Context, uid, ref string) (kata.IssueView, error) {
	issue, err := q.k.ShowIssue(ctx, ref)
	if err == nil {
		err = q.store.rememberIssue(ctx, uid, issue)
	}
	return issue, err
}
func (m *Manager) showIssue(ctx context.Context, uid, ref string) (kata.IssueView, error) {
	issue, err := m.k.ShowIssue(ctx, ref)
	if err == nil {
		err = m.store.rememberIssue(ctx, uid, issue)
	}
	return issue, err
}
