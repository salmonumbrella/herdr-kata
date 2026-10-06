package main

import (
	"strings"
	"testing"
)

func TestWorkflowResumeUsageNamesTheReferenceOverride(t *testing.T) {
	t.Setenv("HERDR_KATA_HOME", t.TempDir())
	err := workflowResume(nil)
	if err == nil || !strings.Contains(err.Error(), "[--ref <value>]") {
		t.Fatalf("usage error = %v, want the reference override", err)
	}
}

func TestWorkflowRunUsageNamesTheReferenceFlag(t *testing.T) {
	t.Setenv("HERDR_KATA_HOME", t.TempDir())
	err := workflowRun(nil)
	if err == nil || !strings.Contains(err.Error(), "[--ref <value>]") {
		t.Fatalf("usage error = %v, want the reference flag", err)
	}
}
