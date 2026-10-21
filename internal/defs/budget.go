package defs

import "github.com/salmonumbrella/herdr-kata/internal/config"

// PromptBudget bounds submitted attempts, including retries and loop visits.
// Classification validates the definition before it can be executed.
func PromptBudget(j JobDef, w *WorkflowDef) int {
	if j.Action.Kind != "execute" {
		return 0
	}
	if j.Action.WorkflowUID == "" {
		return 1
	}
	if w == nil {
		return 0
	}
	total := 0
	prompts := map[string]bool{}
	for _, s := range w.Steps {
		if s.Kind == "prompt" {
			total += 1 + s.Retries
			prompts[s.Key] = true
		}
	}
	if edge, err := WorkflowLoop(w); err == nil && edge != nil {
		for _, key := range edge.Span {
			if prompts[key] {
				total += edge.MaxLoops
			}
		}
	}
	return total
}

// EffectiveCap applies the optional profile ceiling to the installation limit.
func EffectiveCap(c config.Config, profile string) int {
	cap := c.Limits.MaxRunPrompts
	if cap == 0 {
		cap = 12
	}
	if p := c.Profiles[profile].MaxRunPrompts; p != nil && *p < cap {
		cap = *p
	}
	return cap
}

// RunCap is the stricter of the run profile's cap and each prompt step profile's cap.
func RunCap(c config.Config, profile string, w *WorkflowDef) int {
	cap := EffectiveCap(c, profile)
	if w != nil {
		for _, s := range w.Steps {
			if s.Kind == "prompt" {
				cap = min(cap, EffectiveCap(c, StepProfile(s, profile)))
			}
		}
	}
	return cap
}
