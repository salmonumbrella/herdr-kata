package defs

import (
	"strings"
	"testing"
)

// An empty project profile allowlist forbids prompts, not command or notify work.
func TestProjectWithoutProfilesAllowsNonPromptJobs(t *testing.T) {
	for _, kind := range []string{"command", "notify", "prompt", "mixed workflow"} {
		t.Run(kind, func(t *testing.T) {
			c := cfg()
			c.Profiles = nil
			j := fixture(t, "job")
			var w *WorkflowDef
			switch kind {
			case "command", "mixed workflow":
				j.Action = Action{Kind: "execute", WorkflowUID: "workflow"}
				w = &WorkflowDef{Version: 1, Steps: []Step{{Key: "check", Kind: "command", Command: "true"}}}
				if kind == "mixed workflow" {
					w.Steps = append(w.Steps, Step{Key: "review", Kind: "prompt", Prompt: "Review", After: []string{"check"}})
				}
			case "notify":
				j.CheckoutKey = ""
				j.Action = Action{Kind: "notify", Recipient: "worker", Message: "Review"}
				j.Issue = &IssuePolicy{Kind: "existing", UID: "issue"}
			}
			owner, support := Classify(j, w, c)
			if kind == "command" || kind == "notify" {
				if owner != Adapter || !support.Supported {
					t.Fatalf("non-prompt job refused: owner=%v support=%+v", owner, support)
				}
			} else if support.Supported || !strings.Contains(strings.Join(support.Reasons, " "), "unmapped profile") {
				t.Fatalf("prompt job bypassed profile allowlist: %+v", support)
			}
		})
	}
}

func TestUnmappedDefinitionsNameConfigKeys(t *testing.T) {
	c := cfg()
	j := fixture(t, "job")
	j.CheckoutKey = "missing"
	j.Options = []byte(`{"agent":"review"}`)
	_, support := Classify(j, nil, c)
	detail := strings.Join(support.Reasons, "; ")
	for _, want := range []string{`checkouts.missing`, `profiles.review`, `checkout_key`, `options.agent`} {
		if !strings.Contains(detail, want) {
			t.Errorf("missing %q: %s", want, detail)
		}
	}
}
