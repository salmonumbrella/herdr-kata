package runner

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/store"
)

// These tests cover shared workspace placement and the retained artifact contract.

// Every step of a flow run belongs in that run's workspace.
func TestEveryStepLandsInTheFlowsSpace(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{results: map[string]Result{"author": ok("wrote"), "verify": ok("checked")}}
	w := &Flow{Launch: agent.launch, Space: &FlowSpace{WorkspaceID: "ws-7"}}

	job, def := flowJob(t), flowDef(
		store.Step{ID: "author", Agent: "write the thing"},
		store.Step{ID: "verify", Agent: "check the thing"},
	)
	if _, err := w.Execute(context.Background(), job, def, "", "run1", dir); err != nil {
		t.Fatalf("flow failed: %v", err)
	}
	if len(agent.calls) != 2 {
		t.Fatalf("launched %d agents, want 2", len(agent.calls))
	}
	for _, c := range agent.calls {
		if c.job.WorkspaceID != "ws-7" {
			t.Errorf("step %s went to space %q, want ws-7", c.stepID, c.job.WorkspaceID)
		}

	}
}

// Steps receive artifact paths without a removed collaboration prompt contract.
func TestAFlowWithNoSpaceStillRuns(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{results: map[string]Result{"author": ok("wrote")}}
	w := &Flow{Launch: agent.launch} // Space is nil

	job, def := flowJob(t), flowDef(store.Step{ID: "author", Agent: "write the thing"})
	wr, err := w.Execute(context.Background(), job, def, "", "run1", dir)
	if err != nil {
		t.Fatalf("flow failed: %v", err)
	}
	if wr.Outcome != OutcomeDone {
		t.Fatalf("outcome %s (%s), want done", wr.Outcome, wr.ParkReason)
	}
	call := agent.calls[0]
	if call.job.WorkspaceID != "" {
		t.Errorf("step named space %q with no flow space; it must fall back to herdr-kata's own",
			call.job.WorkspaceID)
	}
	if _, ok := call.job.Env["HERDR_KATA_THREAD"]; ok {
		t.Errorf("step was handed %s with no thread to write to", "HERDR_KATA_THREAD")
	}
	if strings.Contains(call.job.Prompt, "thread post") {
		t.Errorf("step was told to post into a thread that does not exist:\n%s", call.job.Prompt)
	}
}

// A usable workspace needs no additional collaboration state.
func TestASpaceWithNoThreadStillGroupsTheSteps(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{results: map[string]Result{"author": ok("wrote")}}
	w := &Flow{Launch: agent.launch, Space: &FlowSpace{WorkspaceID: "ws-7"}}

	job, def := flowJob(t), flowDef(store.Step{ID: "author", Agent: "write the thing"})
	if _, err := w.Execute(context.Background(), job, def, "", "run1", dir); err != nil {
		t.Fatalf("flow failed: %v", err)
	}
	call := agent.calls[0]
	if call.job.WorkspaceID != "ws-7" {
		t.Errorf("step went to space %q, want ws-7", call.job.WorkspaceID)
	}
	if strings.Contains(call.job.Prompt, "thread post") {
		t.Errorf("step told to post with no thread named:\n%s", call.job.Prompt)
	}
}

// A flow's name makes its spaces recognizable, while the random suffix keeps
// two runs of that flow from asking herdr for the same label. The suffix is
// checked by shape because making it predictable for a test would also make it
// predictable for concurrent runs.
func TestASpaceIsNamedAfterItsFlow(t *testing.T) {
	cases := []struct{ flow, prefix string }{
		{"triage", "FLOWS:triage:"},
		{" release-check ", "FLOWS:release-check:"},
		// Empty flow ids should remain recognizable rather than producing an
		// empty segment that looks like a formatting bug.
		{" \t", "FLOWS:flow:"},
	}
	for _, c := range cases {
		got := SpaceLabel(c.flow)
		want := regexp.MustCompile("^" + regexp.QuoteMeta(c.prefix) + `[A-Za-z0-9_-]{6}$`)
		if !want.MatchString(got) {
			t.Errorf("SpaceLabel(%q) = %q, want shape %s", c.flow, got, want)
		}
	}
	// Two runs of one flow must not ask herdr for the same name.
	if a, b := SpaceLabel("triage"), SpaceLabel("triage"); a == b {
		t.Errorf("two spaces for one flow got the same label %q", a)
	}
}

// The prompt tells the step to run herdr-kata. Herdr Kata is commonly run from a
// checkout and is not necessarily on any PATH, so the bare word is an
// instruction that fails silently — the class of failure flows exist to remove.
func hasEnv(env []string, key, want string) bool {
	for _, e := range env {
		if e == key+"="+want {
			return true
		}
	}
	return false
}

// The room an operator is looking at has to say why it is still open. A space
// named exactly as it was while running says only that something happened.
func TestASpaceLabelCarriesTheParkAndGivesItBack(t *testing.T) {
	label := SpaceLabel("triage")
	parked := LabelParked(label, "verify", ParkLoopExhausted)
	for _, want := range []string{label, "verify", "loop_exhausted"} {
		if !strings.Contains(parked, want) {
			t.Errorf("the parked label %q does not mention %q", parked, want)
		}
	}
	// A resume renames it back, and it has to be the same room: SpaceLabel's
	// suffix is random, so a name that could not be recovered would have to be
	// reinvented.
	if got := LabelBase(parked); got != label {
		t.Errorf("stripping the park gives %q, want the original %q", got, label)
	}
	if got := LabelBase(label); got != label {
		t.Errorf("a label that was never parked came back as %q", got)
	}
	// Parking twice must not stack verdicts.
	if got := LabelBase(LabelParked(parked, "build", ParkStepFailed)); got != label {
		t.Errorf("a second park left %q behind", got)
	}
}

// A park with nothing to say still has to be readable: the reason is missing
// exactly when the flow died in a way nobody classified.
func TestAParkedLabelWithoutAVerdictStillNamesTheRoom(t *testing.T) {
	label := SpaceLabel("triage")
	got := LabelParked(label, "", "")
	if !strings.HasPrefix(got, label) || !strings.Contains(got, "parked") {
		t.Errorf("bare parked label is %q", got)
	}
	if LabelBase(got) != label {
		t.Errorf("stripping gives %q, want %q", LabelBase(got), label)
	}
}
