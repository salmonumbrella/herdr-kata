package main

import (
	"strings"
	"testing"
)

func TestFlowResumeUsageNamesTheReferenceOverride(t *testing.T) {
	t.Setenv("HERDR_KATA_HOME", t.TempDir())
	err := flowResume(nil)
	if err == nil || !strings.Contains(err.Error(), "[--ref <value>]") {
		t.Fatalf("usage error = %v, want the reference override", err)
	}
}

func TestFlowRunUsageNamesTheReferenceFlag(t *testing.T) {
	t.Setenv("HERDR_KATA_HOME", t.TempDir())
	err := flowRun(nil)
	if err == nil || !strings.Contains(err.Error(), "[--ref <value>]") {
		t.Fatalf("usage error = %v, want the reference flag", err)
	}
}
