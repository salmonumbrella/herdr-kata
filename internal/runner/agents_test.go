package runner

import "testing"

func TestAgentReferenceOwnership(t *testing.T) {
	for _, tc := range []struct {
		name   string
		active string
		steps  []StepState
		owner  int
	}{
		{"shared prompt", "make", []StepState{{Key: "make", Kind: "prompt", Submission: Accepted}}, -1},
		{"fresh prompt", "make", []StepState{{Key: "make", Kind: "prompt", Submission: Accepted, Herdr: &HerdrRef{PaneID: "fresh"}}}, 0},
		{"command after shared", "check", []StepState{{Key: "make", Kind: "prompt", Submission: Accepted}, {Key: "check", Kind: "command"}}, -1},
		{"command after fresh", "check", []StepState{{Key: "make", Kind: "prompt", Submission: Accepted, Herdr: &HerdrRef{PaneID: "fresh"}}, {Key: "check", Kind: "command"}}, 0},
		{"command after skipped", "check", []StepState{{Key: "make", Kind: "prompt", Submission: Accepted, Herdr: &HerdrRef{PaneID: "fresh"}}, {Key: "skip", Kind: "prompt", Submission: Accepted, Status: "skipped", Herdr: &HerdrRef{PaneID: "skipped"}}, {Key: "check", Kind: "command"}}, 0},
		{"command after unsubmitted", "check", []StepState{{Key: "make", Kind: "prompt", Submission: Accepted, Herdr: &HerdrRef{PaneID: "fresh"}}, {Key: "pending", Kind: "prompt", Submission: None, Herdr: &HerdrRef{PaneID: "pending"}}, {Key: "check", Kind: "command"}}, 0},
		{"command without prompt", "check", []StepState{{Key: "check", Kind: "command"}}, -1},
		{"legacy nil", "check", []StepState{{Key: "older", Kind: "prompt", Submission: Accepted, Herdr: &HerdrRef{PaneID: "older"}}, {Key: "make", Kind: "prompt", Submission: Accepted}, {Key: "check", Kind: "command"}}, -1},
		{"explicit command ref", "check", []StepState{{Key: "check", Kind: "command", Herdr: &HerdrRef{PaneID: "command"}}}, 0},
		{"unknown step", "missing", []StepState{{Key: "make", Kind: "prompt", Herdr: &HerdrRef{PaneID: "fresh"}}}, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Run{ActiveStep: tc.active, Steps: tc.steps, Herdr: HerdrRef{PaneID: "shared"}}
			if got := occupantOwner(r, r.ActiveStep); got != tc.owner {
				t.Fatalf("owner = %d, want %d", got, tc.owner)
			}
			want := &r.Herdr
			if tc.owner >= 0 {
				want = r.Steps[tc.owner].Herdr
			}
			if got := AgentReference(r); got != want {
				t.Fatalf("occupant pointer = %p (%+v), want %p (%+v)", got, got, want, want)
			}
		})
	}
}
