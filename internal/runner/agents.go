package runner

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
)

// AgentReference returns the current attempt's occupant. Legacy and shared
// attempts retain the run-level reference.
func AgentReference(r *Run) *HerdrRef {
	if owner := occupantOwner(r, r.ActiveStep); owner >= 0 {
		return r.Steps[owner].Herdr
	}
	return &r.Herdr
}

// occupantOwner returns -1 when a shared or legacy attempt uses r.Herdr.
func occupantOwner(r *Run, key string) int {
	for i, s := range r.Steps {
		if s.Key != key {
			continue
		}
		if s.Herdr != nil {
			return i
		}
		if s.Kind == "command" {
			if j := priorPromptIndex(r, i); j >= 0 && r.Steps[j].Herdr != nil {
				return j
			}
		}
		break
	}
	return -1
}

func priorPromptIndex(r *Run, index int) int {
	for i := index - 1; i >= 0; i-- {
		if submittedPrompt(r.Steps[i]) {
			return i
		}
	}
	return -1
}

func submittedPrompt(s StepState) bool {
	return s.Kind == "prompt" && s.Submission != None && s.Submission != "" && s.Status != "skipped"
}

func freshStep(r *Run, index int) bool {
	if len(r.JobDef.SecretRefs) > 0 || r.Steps[index].LoopRerun {
		return true
	}
	if s, ok := workflowStep(r, index); ok {
		return defs.FreshStep(s, r.ProfileName)
	}
	return false
}

func workflowStep(r *Run, index int) (defs.Step, bool) {
	if r.WFDef != nil {
		for _, s := range r.WFDef.Steps {
			if s.Key == r.Steps[index].Key {
				return s, true
			}
		}
	}
	return defs.Step{}, false
}

// StepProfile returns the frozen profile an attempt launches with. Admission
// freezes every differing profile that classification accepted.
func StepProfile(r *Run, index int) (string, config.Profile) {
	if s, ok := workflowStep(r, index); ok {
		if name := defs.StepProfile(s, r.ProfileName); name != r.ProfileName {
			if p, ok := r.StepProfiles[name]; ok {
				return name, p
			}
		}
	}
	return r.ProfileName, r.Profile
}
func agentName(r *Run, index int) string {
	if !freshStep(r, index) {
		return "hk-" + strings.ToLower(r.UID)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", r.UID, r.Steps[index].Key, r.Steps[index].Attempt)))
	return fmt.Sprintf("hk-%x", sum[:14])
}
func (m *Manager) selectAgent(ctx context.Context, uid string, index int) (*Run, error) {
	r, e := m.store.Load(uid)
	if e != nil {
		return nil, e
	}
	if r.Phase == HandedOff || r.Phase == Settled || r.Phase == Held {
		return nil, fmt.Errorf("run cannot launch in phase %s", r.Phase)
	}
	key := r.Steps[index].Key
	exitedShared := -1
	archivedShared := false
	priorIndex := -1
	for i, s := range r.Steps {
		if s.Key == r.ActiveStep {
			priorIndex = i
			if s.Kind == "command" {
				priorIndex = priorPromptIndex(r, i)
			}
			break
		}
	}
	owner := -1
	priorRef := r.Herdr
	if priorIndex >= 0 {
		if r.Steps[priorIndex].Herdr != nil {
			owner = priorIndex
			priorRef = *r.Steps[owner].Herdr
		}
	}
	if priorIndex >= 0 && r.Steps[priorIndex].Key != key {
		// Prove the last prompt independently of command progress.
		s := r.Steps[priorIndex]
		if s.Kind == "prompt" && priorRef.AgentName != "" {
			a, e := m.proof(ctx, priorRef)
			if e != nil || !settledAgent(a) || !sequenceAdvanced(priorRef, a) || s.Result == nil || s.Status == "" {
				if m.paneExited(ctx, priorRef) && s.Result != nil && s.Status != "" {
					if s.Herdr == nil {
						exitedShared = priorIndex
					}
				} else if errors.Is(e, errNoSession) {
					return nil, e
				} else {
					return nil, errUnproven
				}
			}
		}
	}
	// A retained shared pane may exit while a fresh attempt owns the slot.
	if !freshStep(r, index) && owner >= 0 && r.Herdr.PaneID != "" {
		if m.paneExited(ctx, r.Herdr) {
			for i := index - 1; i >= 0; i-- {
				s := r.Steps[i]
				if s.Kind == "prompt" && s.Herdr == nil && (s.Status == "succeeded" || s.Status == "failed") {
					exitedShared = i
					break
				}
			}
			if exitedShared < 0 {
				// Loop rewinds and resume move the original shared attempt out of Steps.
				// Its provenance must already be retained before replacing it.
				for _, old := range r.PastAttempts {
					ref := old.Herdr
					resumedUncertainty := r.Resume != nil && !r.Resume.Pending && slices.Contains(r.ConsumedResumes, r.Resume.UID) && (old.Status == "" || old.Status == "unknown") && (old.Submission == Sending || old.Submission == Accepted)
					if old.Kind == "prompt" && (old.Status == "succeeded" || old.Status == "failed" || resumedUncertainty) && ref != nil && ref.ServerID == r.Herdr.ServerID && ref.TabID == r.Herdr.TabID && ref.PaneID == r.Herdr.PaneID && ref.TerminalID == r.Herdr.TerminalID && ref.AgentName == r.Herdr.AgentName {
						archivedShared = true
						break
					}
				}
				if !archivedShared {
					return nil, errUnproven
				}
			}
		}
	}
	report := r.Herdr.ReportBin
	if report == "" {
		report = AgentReference(r).ReportBin
	}
	if report == "" {
		report, e = os.Executable()
		if e != nil {
			return nil, e
		}
	}
	if r.ActiveStep == key && r.Herdr.ReportBin == report && (!freshStep(r, index) || r.Steps[index].Herdr != nil) {
		return r, nil
	}
	return m.store.Transition(uid, func(r *Run) error {
		if exitedShared >= 0 {
			retainAgentRef(r, exitedShared, r.Herdr)
		}
		if exitedShared >= 0 || archivedShared {
			retainSharedAgentRefs(r)
			report := r.Herdr.ReportBin
			r.Herdr = HerdrRef{ReportBin: report}
		}
		r.Herdr.ReportBin = report
		r.ActiveStep = key
		if freshStep(r, index) && r.Steps[index].Herdr == nil {
			r.Steps[index].Herdr = &HerdrRef{ReportBin: r.Herdr.ReportBin}
		}
		return nil
	})
}

func agentReferences(r *Run) []HerdrRef {
	refs := []HerdrRef{r.Herdr}
	for _, s := range append(append([]StepState{}, r.PastAttempts...), r.Steps...) {
		if s.Herdr != nil {
			refs = append(refs, *s.Herdr)
		}
	}
	return refs
}

func retainAgent(r *Run, index int) { retainAgentRef(r, index, *AgentReference(r)) }
func retainedPromptAttempt(r *Run, s StepState) bool {
	for _, old := range r.PastAttempts {
		if old.Key == s.Key && old.Kind == s.Kind && old.Attempt == s.Attempt && submittedPrompt(old) {
			return true
		}
	}
	return false
}

// Retain every submitted current step associated with the shared occupant
// before clearing it. Previously retained copies already identify older groups.
func retainSharedAgentRefs(r *Run) {
	if r.Herdr.HasSubmitted != nil && !*r.Herdr.HasSubmitted {
		return
	}
	for i, step := range r.Steps {
		if step.Herdr == nil && submittedPrompt(step) && !retainedPromptAttempt(r, step) {
			retainAgentRef(r, i, r.Herdr)
		}
	}
}

func retainAgentRef(r *Run, index int, ref HerdrRef) {
	old := r.Steps[index]
	// The bounded journal retains ownership and submission evidence. Full
	// reports stay in the attempt artifacts rather than accumulating per retry.
	old.FindingContext = nil
	old.Result = nil
	old.Receipt = nil
	old.Herdr = &ref
	r.PastAttempts = append(r.PastAttempts, old)
}

// A consumed resume can resolve an idle no-activity occupant without a turn
// completion. Preserve its original sequence and require fresh identity/idle
// proof; an unfinished later submission to the same occupant cannot use it.
func retainedNoActivity(r *Run, ref HerdrRef) bool {
	for _, s := range r.Steps {
		if s.Kind != "prompt" || s.Submission != Sending && s.Submission != Accepted || s.Status != "" && s.Status != "unknown" {
			continue
		}
		current := r.Herdr
		if s.Herdr != nil {
			current = *s.Herdr
		}
		if reflect.DeepEqual(current, ref) {
			return false
		}
	}
	for _, s := range r.PastAttempts {
		if s.Kind == "prompt" && s.Reason == "no_activity" && !s.SawActivity && s.Herdr != nil && reflect.DeepEqual(*s.Herdr, ref) {
			return true
		}
	}
	return false
}
