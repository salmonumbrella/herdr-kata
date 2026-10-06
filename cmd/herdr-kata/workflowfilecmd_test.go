package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/workflow"
)

// The read-only half of the workflow file verbs: list, show, edit.
//
// None of them changes anything on its own, which is exactly why a wrong answer
// here goes unnoticed: a workflow missing from `workflow list` reads as a workflow that was
// never made, and a `workflow show` that reformats the file is pasted back as a
// different workflow. Everything below points HERDR_KATA_HOME at a temporary
// directory, so nothing touches the real ~/.herdr-kata/workflows this machine runs on.

// workflowFilesIn points the workflow verbs at an empty temporary state directory.
func workflowFilesIn(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HERDR_KATA_HOME", dir)
	// An editor inherited from the surrounding shell would make `workflow edit`
	// spawn a real one in the middle of the test run.
	t.Setenv("HERDR_KATA_EDITOR", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	return dir
}

// An installation with no workflows yet has to say so in a way that names the verb
// that makes one. An empty table is indistinguishable from a listing that
// failed.
func TestWorkflowListOnAnEmptyInstallationSaysHowToMakeOne(t *testing.T) {
	workflowFilesIn(t)

	out, err := captureStdout(t, func() error { return workflowList(nil) })
	if err != nil {
		t.Fatalf("listing workflows before any exist failed: %v", err)
	}
	if !strings.Contains(out, "workflow new") {
		t.Errorf("said %q with no workflows, want the command that writes one", out)
	}
}

// The listing is what somebody reads to pick a workflow to run, so every column has
// to carry the value the caller decides on: how many steps it has, whether it
// needs an input, and what it is for.
func TestWorkflowEditWithoutAnEditorPrintsThePath(t *testing.T) {
	dir := workflowFilesIn(t)
	writeWorkflowDraft(t, "triage", "about: sort\nsteps:\n  - id: one\n    run: true\n")

	out, err := captureStdout(t, func() error { return workflowEdit([]string{"triage"}) })
	if err != nil {
		t.Fatalf("workflow edit with no editor failed: %v", err)
	}
	want := filepath.Join(filepath.Join(dir, "drafts", "workflows"), "triage"+workflow.Ext)
	if strings.TrimSpace(out) != want {
		t.Errorf("workflow edit printed %q, want the path %q", strings.TrimSpace(out), want)
	}
}

// The workflow somebody most needs to open is the one that no longer parses, so a
// broken file has to be editable. Refusing it is the state where the only way
// out is to know where herdr-kata keeps its workflows.
func TestWorkflowEditOpensAWorkflowThatDoesNotParse(t *testing.T) {
	dir := workflowFilesIn(t)
	writeWorkflowDraft(t, "broken", "steps:\n  - id: one\n    agnet: typo\n")

	out, err := captureStdout(t, func() error { return workflowEdit([]string{"broken"}) })
	if err != nil {
		t.Fatalf("workflow edit refused the broken workflow it exists to fix: %v", err)
	}
	want := filepath.Join(filepath.Join(dir, "drafts", "workflows"), "broken"+workflow.Ext)
	if strings.TrimSpace(out) != want {
		t.Errorf("workflow edit printed %q, want the path %q to the broken file", strings.TrimSpace(out), want)
	}
}

// A workflow that is not there is a different answer from one that is there and
// broken, and `workflow edit` must not turn the first into an empty file or a
// success.
func TestWorkflowEditRefusesWhatIsNotThere(t *testing.T) {
	dir := workflowFilesIn(t)

	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{"no id", nil, "usage"},
		{"unknown workflow", []string{"missing"}, "identity must be a ULID"},
		{"not an id", []string{"Not An Id"}, "identity must be a ULID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error { return workflowEdit(tc.argv) })
			if err == nil {
				t.Fatalf("workflow edit %v succeeded and printed %q", tc.argv, out)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("workflow edit %v said %q, want it to mention %q", tc.argv, err, tc.want)
			}
		})
	}

	if entries, err := os.ReadDir(filepath.Join(dir, "drafts", "workflows")); err == nil && len(entries) > 0 {
		t.Errorf("workflow edit created %d file(s) for workflows that do not exist", len(entries))
	}
}

// The re-read on the way out is the whole reason `workflow edit` is a command and
// not a printed path: a syntax error saved at 17:00 is otherwise first reported
// by the scheduler at 04:00, to nobody.
func TestWorkflowEditReportsASyntaxErrorSavedByTheEditor(t *testing.T) {
	workflowFilesIn(t)
	writeWorkflowDraft(t, "triage", "about: sort\nsteps:\n  - id: one\n    run: true\n")
	t.Setenv("HERDR_KATA_EDITOR", editorWriting(t, "steps:\n  - id: one\n    agnet: typo\n"))

	if err := workflowEdit([]string{"triage"}); err == nil {
		t.Fatal("workflow edit reported success after the editor saved a workflow that does not parse")
	}
}

// The ordinary case still has to succeed, or the check above would just be a
// command that always fails.
func TestWorkflowEditAcceptsAnEditThatStillParses(t *testing.T) {
	dir := workflowFilesIn(t)
	writeWorkflowDraft(t, "triage", "about: sort\nsteps:\n  - id: one\n    run: true\n")
	edited := "about: sort the inbox\nsteps:\n  - id: one\n    run: true\n  - id: two\n    run: true\n"
	t.Setenv("HERDR_KATA_EDITOR", editorWriting(t, edited))

	if err := workflowEdit([]string{"triage"}); err != nil {
		t.Fatalf("workflow edit rejected an edit that parses: %v", err)
	}

	f, err := workflow.Load(filepath.Join(dir, "drafts", "workflows"), "triage")
	if err != nil {
		t.Fatalf("the edited workflow no longer loads: %v", err)
	}
	if len(f.Steps) != 2 || f.About != "sort the inbox" {
		t.Errorf("the file on disk is %+v, want what the editor saved", f)
	}
}

// An editor that exits non-zero means the edit was abandoned, and reporting
// success would tell the caller their unsaved change is on disk.
func TestWorkflowEditFailsWhenTheEditorDoes(t *testing.T) {
	workflowFilesIn(t)
	writeWorkflowDraft(t, "triage", "about: sort\nsteps:\n  - id: one\n    run: true\n")
	t.Setenv("HERDR_KATA_EDITOR", "false")

	if err := workflowEdit([]string{"triage"}); err == nil {
		t.Fatal("workflow edit reported success after the editor exited non-zero")
	}
}

// editorWriting returns an editor command that replaces the file it is given
// with body. It stands in for a person saving a change.
func editorWriting(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "editor.sh")
	content := "#!/bin/sh\ncat > \"$1\" <<'HERDR_KATA_EOF'\n" + body + "HERDR_KATA_EOF\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return shellQuote(script)
}

func writeWorkflowDraft(t *testing.T, id, body string) {
	t.Helper()
	dir := workflowDraftDir()
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, id+workflow.Ext), []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
}
