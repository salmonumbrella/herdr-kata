package defs

import (
	"slices"
	"testing"
	"time"
)

// A deterministic refusal must identify the definition/configuration field
// that the operator can repair, rather than only a machine-readable reason.
func TestClassifyManualPolicyRefusalNamesRepair(t *testing.T) {
	for _, policy := range []string{"timeout", "commands"} {
		t.Run(policy, func(t *testing.T) {
			c := cfg()
			c.Kata.Project = "example-project"
			c.Kata.Env = map[string]string{"KATA_HOME": t.TempDir()}
			j := fixture(t, "job")
			j.Action = Action{Kind: "execute", WorkflowUID: "workflow"}
			w := &WorkflowDef{Version: 1, Steps: []Step{{Key: "check", Kind: "command", Command: "true"}}}
			var want string
			switch policy {
			case "timeout":
				j.TimeoutSeconds = 1900
				c.Limits.MaxTimeout = 30 * time.Minute
				want = "timeout_seconds 1900 exceeds limits.max_timeout 30m0s"
			case "commands":
				c.Commands = new(false)
				want = `commands are disabled for project "example-project" (commands=false)`
			}
			owner, support := Classify(j, w, c)
			if owner != Adapter || support.Supported || !slices.Contains(support.Reasons, want) {
				t.Fatalf("policy %s: owner=%v support=%+v, want %q", policy, owner, support, want)
			}
			// Repair exactly the field named by the refusal; the same definition then
			// passes without changing any other admission requirement.
			if policy == "timeout" {
				j.TimeoutSeconds = 1800
			} else {
				c.Commands = new(true)
			}
			owner, support = Classify(j, w, c)
			if owner != Adapter || !support.Supported {
				t.Fatalf("repair did not clear refusal: owner=%v support=%+v", owner, support)
			}
		})
	}
}
