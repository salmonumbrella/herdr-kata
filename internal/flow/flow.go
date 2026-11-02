// Package flow decides one serial transition without performing side effects.
package flow

import (
	"maps"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
)

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

func (Start) decision()  {}
func (Wait) decision()   {}
func (Finish) decision() {}
func (Hold) decision()   {}

func Next(p defs.Plan, states map[string]StepState) Decision {
	states = maps.Clone(states)
	if states == nil {
		states = make(map[string]StepState)
	}
	running, handoff, failed := false, false, false
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
			}
		}
	}
	if handoff {
		return Finish{"unknown"}
	}
	if running {
		return Wait{}
	}
	// Propagate skipped dependency chains without inventing executed attempts.
	for range p.Steps {
		changed := false
		for _, s := range p.Steps {
			if states[s.Key].Status != "" {
				continue
			}
			for _, dep := range s.After {
				d := states[dep]
				spent := d.Status == "skipped"
				if d.Status == "failed" {
					for _, parent := range p.Steps {
						if parent.Key == dep && d.Attempts > parent.Retries {
							spent = true
						}
					}
				}
				if spent {
					states[s.Key] = StepState{Status: "skipped"}
					changed = true
					break
				}
			}
		}
		if !changed {
			break
		}
	}
	unresolved := false
	for _, s := range p.Steps {
		st := states[s.Key]
		if st.Status == "failed" && st.Attempts <= s.Retries {
			return Start{s.Key, st.Attempts + 1}
		}
		if st.Status != "" {
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
		return Hold{}
	}
	if failed {
		return Finish{"failed"}
	}
	return Finish{"succeeded"}
}
