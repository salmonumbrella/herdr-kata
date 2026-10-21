package tui

import (
	"context"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

// unsupportedService makes any unexpected write or fetch fail at its boundary.
// A test that exercises one of these operations supplies its own collaborator.
type unsupportedService struct{}

func (unsupportedService) SaveJob(context.Context, kata.Job, string) (kata.Job, error) {
	panic("unexpected SaveJob")
}
func (unsupportedService) ShowJob(context.Context, string) (kata.Job, error) {
	panic("unexpected ShowJob")
}
func (unsupportedService) ShowIssue(context.Context, string) (kata.IssueView, error) {
	panic("unexpected ShowIssue")
}
func (unsupportedService) SaveWorkflow(context.Context, kata.Workflow) (kata.Workflow, error) {
	panic("unexpected SaveWorkflow")
}
func (unsupportedService) ShowWorkflow(context.Context, string) (kata.Workflow, error) {
	panic("unexpected ShowWorkflow")
}
func (unsupportedService) MetaSet(context.Context, string, string, string, kata.Cond) error {
	panic("unexpected MetaSet")
}
func (unsupportedService) MetaUnset(context.Context, string, string, kata.Cond) error {
	panic("unexpected MetaUnset")
}

type laneFormService struct {
	*fakeActions
	*fakeWriter
}

type bindingActionService struct {
	service
	ref string
}

func (s *bindingActionService) ShowIssue(_ context.Context, ref string) (kata.IssueView, error) {
	s.ref = ref
	return kata.IssueView{UID: "parent", ShortID: "abc45", ProjectUID: "p", Revision: 1}, nil
}
