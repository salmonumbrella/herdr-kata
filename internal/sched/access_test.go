package sched

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type accessReader struct {
	*fakeKata
	jobs                   []kata.Job
	workflows              []kata.Workflow
	listErr, errorWorkflow error
}

func (k *accessReader) ListJobs(context.Context) ([]kata.Job, error) { return k.jobs, k.listErr }
func (k *accessReader) ListWorkflows(context.Context) ([]kata.Workflow, error) {
	return k.workflows, k.listErr
}
func (k *accessReader) ShowWorkflow(context.Context, string) (kata.Workflow, error) {
	return k.workflow, k.errorWorkflow
}
func TestHiddenJob404NeedsCompleteListProof(t *testing.T) {
	for _, proof := range []string{"present", "absent", "failed"} {
		t.Run(proof, func(t *testing.T) {
			s, k, a, c := setup(t)
			a.result = NotAdmitted
			c.advance(time.Minute)
			tick(t, s)
			r := &accessReader{fakeKata: k, jobs: []kata.Job{k.job}, workflows: []kata.Workflow{k.workflow}}
			k.showErr = &kata.Error{Code: "not_found"}
			if proof == "absent" {
				r.jobs = nil
			}
			if proof == "failed" {
				r.listErr = errors.New("offline")
			}
			s.k = r
			tick(t, s)
			if got := len(pending(t, s)); (proof == "absent" && got != 0) || (proof != "absent" && got != 1) {
				t.Fatal("404 dropped without proof", got)
			}
		})
	}
}
func TestHiddenWorkflow404StaysPending(t *testing.T) {
	s, k, a, c := setup(t)
	a.result = NotAdmitted
	c.advance(time.Minute)
	tick(t, s)
	r := &accessReader{fakeKata: k, jobs: []kata.Job{k.job}, workflows: []kata.Workflow{k.workflow}, errorWorkflow: &kata.Error{Code: "not_found"}}
	// Exercise the revalidation boundary with a job naming this workflow.
	d, _ := defs.DecodeJob(k.job.Definition)
	d.Action.WorkflowUID = "wf"
	d.Action.Prompt = ""
	b, _ := json.Marshal(d)
	k.job.Definition = b
	r.workflow.UID = "wf"
	r.workflows = []kata.Workflow{r.workflow}
	s.k = r
	_, _, _, err := s.current(t.Context(), jobID)
	if err == nil {
		t.Fatal("hidden workflow marked unsupported")
	}
}
