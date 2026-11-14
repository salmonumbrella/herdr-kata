// Package flow decides one serial transition without performing side effects.
package flow

import "github.com/salmonumbrella/herdr-kata/internal/defs"

type StepState struct {
	Status   string
	Attempts int
}
type Decision interface{ decision() }
type Start struct {
	Step string
	N    int
}
type Wait struct{}
type Finish struct{ Outcome string }
type Hold struct{}
type Invalid struct{}

func (Start) decision()   {}
func (Wait) decision()    {}
func (Finish) decision()  {}
func (Hold) decision()    {}
func (Invalid) decision() {}

func Next(p defs.Plan, states map[string]StepState) Decision {
	running, handoff, failed := false, false, false
	var queue []string
	for _, s := range p.Steps {
		st := states[s.Key]
		switch st.Status {
		case "held", "unknown":
			return Hold{}
		case "needs-human":
			handoff = true
		case "running", "launching", "settling", "expiring":
			running = true
		case "failed":
			if st.Attempts > s.Retries {
				failed = true
				queue = append(queue, s.Key)
			}
		}
	}
	if handoff {
		return Finish{"unknown"}
	}
	if running {
		return Wait{}
	}
	for key, st := range states {
		if st.Status == "skipped" {
			queue = append(queue, key)
		}
	}
	// Visit each blocked edge once, without modifying the caller's run state.
	var blocked map[string]bool
	if len(queue) > 0 {
		blocked = make(map[string]bool, len(p.Steps))
		dependents := make(map[string][]string, len(p.Steps))
		for _, s := range p.Steps {
			if states[s.Key].Status != "" {
				continue
			}
			for _, dep := range s.After {
				dependents[dep] = append(dependents[dep], s.Key)
			}
		}
		for head := 0; head < len(queue); head++ {
			key := queue[head]
			if !blocked[key] {
				blocked[key] = true
				queue = append(queue, dependents[key]...)
			}
		}
	}
	unresolved := false
	for _, s := range p.Steps {
		st := states[s.Key]
		if st.Status == "failed" && st.Attempts <= s.Retries {
			return Start{s.Key, st.Attempts + 1}
		}
		if st.Status != "" || blocked[s.Key] {
			continue
		}
		unresolved = true
		ready := true
		for _, dep := range s.After {
			if states[dep].Status != "succeeded" {
				ready = false
				break
			}
		}
		if ready {
			return Start{s.Key, 1}
		}
	}
	if unresolved {
		return Invalid{}
	}
	if failed {
		return Finish{"failed"}
	}
	return Finish{"succeeded"}
}
