package defs

import (
	"strings"
	"testing"
)

func TestIssueTextRejectsNUL(t *testing.T) {
	for _, field := range []string{"title", "body"} {
		t.Run(field, func(t *testing.T) {
			job := fixture(t, "job")
			if field == "title" {
				job.Issue.Title = "task\x00title"
			} else {
				job.Issue.Body = "context\x00body"
			}
			_, support := Classify(job, nil, cfg())
			if support.Supported || !strings.Contains(strings.Join(support.Reasons, ";"), "issue."+field+" must be UTF-8 without NUL") {
				t.Fatalf("invalid issue accepted: %+v", support)
			}
		})
	}
}

func TestStaticPromptBudgetIncludesContextAndEnvelope(t *testing.T) {
	for _, field := range []string{"prompt", "body", "workflow prompt", "workflow about"} {
		t.Run(field, func(t *testing.T) {
			job := fixture(t, "job")
			var wf *WorkflowDef
			large := strings.Repeat("x", 255<<10)
			switch field {
			case "prompt":
				job.Action.Prompt = large
			case "body":
				job.Issue.Body = large
			default:
				job.Action.Prompt, job.Action.WorkflowUID = "", "workflow"
				wf = &WorkflowDef{Version: 1, Steps: []Step{{Key: "main", Kind: "prompt", Prompt: "check"}}}
				if field == "workflow prompt" {
					wf.Steps[0].Prompt = large
				} else {
					wf.About = large
				}
			}
			_, support := Classify(job, wf, cfg())
			if support.Supported || !strings.Contains(strings.Join(support.Reasons, ";"), "prompt exceeds 256 KiB") {
				t.Fatalf("oversize assembled prompt supported: %+v", support)
			}
		})
	}
}
