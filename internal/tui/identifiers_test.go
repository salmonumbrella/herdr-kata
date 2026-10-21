package tui

import (
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

func TestDefinitionHeadersUseNames(t *testing.T) {
	m := jobDetailFixture(t, `{"enabled":true,"action":{"kind":"execute","prompt":"Work"}}`)
	body := workflowText(m.buildJobDetail().Body())
	if got, _, _ := strings.Cut(body, "\n"); got != "Example job" {
		t.Fatalf("job header: %q", got)
	}
	v, _ := m.selectedJobView()
	if body := workflowText(m.renderJobInspector(v, 64)); !strings.HasPrefix(trimIdentifierLines(body), "Example job\n\n") {
		t.Fatalf("job inspector header: %q", body)
	}
	w := workflowModel(t, workflowSnapshot())
	inspector := w.workflowInspector(w.selectedWorkflow(), 64)
	if strings.TrimSpace(inspector[1].Text) != "" {
		t.Fatalf("workflow inspector second line should separate sections: %q", inspector[1].Text)
	}
	send(w, "l")
	detail := workflowText(w.buildWorkflowDetail().Body())
	if !strings.HasPrefix(detail, "Example\n\n") {
		t.Fatalf("workflow header: %q", detail)
	}
}

func TestExistingIssueAndParentUseSavedQualifiedReference(t *testing.T) {
	const issueUID = "01HZNQ7VFPK1XGD8R5MABCD4EX"
	m := jobDetailFixture(t, `{"enabled":true,"action":{"kind":"execute","prompt":"Work"},"issue":{"kind":"existing","uid":"`+issueUID+`"},"options":{"run_parent":"`+issueUID+`"}}`)
	m.data.Runs = []runner.Run{{UID: "known", Target: runner.Target{Identity: kata.Identity{ProjectUID: "p"}}, IssueUID: issueUID, IssueRef: "example-project#cd4ex"}}
	body := workflowText(m.buildJobDetail().Body())
	if !strings.Contains(body, "existing example-project#cd4ex") {
		t.Fatalf("existing issue: %s", body)
	}
	v, _ := m.selectedJobView()
	body = workflowText(m.renderJobInspector(v, 64))
	if !strings.Contains(body, "example-project#cd4ex") {
		t.Fatalf("parent reference: %s", body)
	}
}

func TestMissingWorkflowUsesANameMessage(t *testing.T) {
	const uid = "01HZNQ7VFPK1XGD8R5MABCD4EX"
	m := jobDetailFixture(t, `{"enabled":true,"action":{"kind":"execute","workflow_uid":"`+uid+`"}}`)
	body := workflowText(m.buildJobDetail().Body())
	if strings.Contains(body, uid) || !strings.Contains(body, "workflow unavailable") {
		t.Fatal(body)
	}
}

func trimIdentifierLines(body string) string {
	lines := strings.Split(body, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.Join(lines, "\n")
}

func TestRunIdentityUsesNameOrShortRunID(t *testing.T) {
	for _, name := range []string{"Example job", ""} {
		m, _ := fixture(t)
		const uid = "01K00000000000000000000001"
		m.data.Runs = []runner.Run{{UID: uid, Phase: runner.Held, Job: kata.Job{UID: "01K00000000000000000000002", Name: name}}}
		m.Update(dataMsg{data: m.data})
		send(m, "3")
		send(m, "l")
		body := workflowText(m.renderRunDetail())
		want := name
		if want == "" {
			want = "run 01K00000"
		}
		if !strings.HasPrefix(body, want) || strings.Contains(body, uid) || strings.Contains(body, "01K00000000000000000000002") {
			t.Fatal(body)
		}
	}
}

func TestNativeParentReferenceNeedsNoLocalRun(t *testing.T) {
	const uid = "01K00000000000000000000001"
	m := jobDetailFixture(t, `{"action":{"kind":"notify"},"issue":{"kind":"per-run","parent_uid":"`+uid+`"}}`)
	m.data.Jobs[0].IssueRefs = map[string]string{uid: "example-project#abc45"}
	m.rebuildJobIndex()
	text := workflowText(m.buildJobDetail().Body())
	if !strings.Contains(text, "example-project#abc45") || strings.Contains(text, uid) {
		t.Fatal(text)
	}
}

func TestRemovedJobActionOmitsIdentifier(t *testing.T) {
	m := jobDetailFixture(t, `{"action":{"kind":"execute","prompt":"work"}}`)
	m.data.Jobs = nil
	m.rebuildJobIndex()
	m.jobKeyAction("p")
	if m.notice.Text != "job was removed" {
		t.Fatal(m.notice.Text)
	}
}

func TestUnnamedRunUsesItsSavedIssueIdentity(t *testing.T) {
	r := runner.Run{UID: "01K00000000000000000000001", ProjectName: "example-project", IssueUID: "issue", IssueRefs: map[string]string{"issue": "example-project#abc45"}}
	if got := runView(r).Job; got != "example-project#abc45" {
		t.Fatal(got)
	}
}
