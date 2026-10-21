package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/runstatus"
)

func runExpandable(r runner.Run) bool {
	return len(r.Steps) > 0 && (r.WFDef != nil || len(r.PastAttempts) > 0)
}

type runViewData struct {
	Run                  runner.Run
	Job, Trigger, Search string
	Issue                string // The issue column's ref, resolved by rebuild.
	When                 time.Time
}

func runView(r runner.Run) runViewData {
	v := runViewData{Run: r, Job: r.Job.Name, Trigger: "scheduled"}
	if v.Job == "" {
		if r.Workflow != nil && r.Workflow.Name != "" {
			v.Job = r.Workflow.Name
		} else if ref := r.IssueRefs[r.IssueUID]; ref != "" {
			v.Job = ref
		} else if ref := qualifiedIssueRef(r.ProjectName, r.IssueRef); ref != "" {
			v.Job = ref
		} else {
			v.Job = "run " + r.UID[:min(8, len(r.UID))]
		}
	}
	if r.Manual {
		v.Trigger = "manual"
	}
	if r.Child != nil {
		v.Trigger = "recurring child"
	}
	if r.StartedAt != nil {
		v.When = *r.StartedAt
	}
	v.Job = clean(v.Job)
	v.Search = clean(fmt.Sprint(r.UID, " ", r.Job.Name, " ", r.Job.UID, " ", r.Phase, " ", r.Outcome, " ", r.Reason, " ", r.Occurrence, " ", r.Inspect))
	for _, s := range r.Steps {
		v.Search += " " + clean(s.Note)
		if s.Result != nil {
			v.Search += " " + clean(s.Result.Summary)
		}
	}
	for _, s := range r.PastAttempts {
		v.Search += " " + clean(s.Note+" "+s.Reason)
	}
	return v
}
func activeStep(r runner.Run) *runner.StepState {
	for i := range r.Steps {
		if r.Steps[i].Key == r.ActiveStep {
			return &r.Steps[i]
		}
	}
	return nil
}
func stepProgress(steps []runner.StepState) string {
	done := 0
	next := ""
	for _, s := range steps {
		if s.Status == "succeeded" || s.Status == "skipped" {
			done++
		} else if next == "" {
			next = s.Key
		}
	}
	text := fmt.Sprintf("%d/%d", done, len(steps))
	if next != "" {
		text += " · " + clean(next)
	}
	return text
}

// RunSearch matches the issue as the issue column resolves it.
func runSearch(v runViewData, project string, now time.Time) string {
	label, _ := runLabel(v.Run)
	detail, _ := runDetailCell(v.Run, now)
	return strings.Join([]string{v.Search, clean(project), clean(v.Issue), label, v.Trigger, detail}, " ")
}

type stepDisplayData struct {
	Mark, Outcome string
	Role          lipgloss.Style
}

// stepWord colors a step word by its row in the shared status table.
func stepWord(mark, word, text string) stepDisplayData {
	return stepDisplayData{mark, text, statusRole(runstatus.Of(word).Role)}
}

func terminalStep(s runner.StepState) (stepDisplayData, bool) {
	withReason := func(word string) string {
		if s.Reason != "" {
			return word + " (" + clean(s.Reason) + ")"
		}
		return word
	}
	switch s.Status {
	case runstatus.Succeeded:
		return stepWord("✓", runstatus.Succeeded, runstatus.Succeeded), true
	case runstatus.Failed:
		return stepWord("✗", runstatus.Failed, withReason(runstatus.Failed)), true
	case runstatus.NeedsHuman:
		return stepWord("⏸", runstatus.NeedsHuman, runstatus.NeedsHuman), true
	case runstatus.Unknown:
		return stepWord("?", runstatus.Unknown, withReason(runstatus.Unknown)), true
	case runstatus.Skipped:
		return stepWord("·", runstatus.Skipped, runstatus.Skipped), true
	}
	return stepDisplayData{}, false
}
func stepDisplay(r runner.Run, s runner.StepState) stepDisplayData {
	if d, ok := terminalStep(s); ok {
		return d
	}
	if s.Key == r.ActiveStep {
		switch r.Phase {
		case runner.Held:
			text := runstatus.Parked
			if r.Reason != "" {
				text += " (" + clean(r.Reason) + ")"
			}
			if s.Submission == "" || s.Submission == runner.None {
				text += ", not sent"
			}
			return stepWord("⏸", runstatus.Parked, text)
		case runner.Blocked:
			return stepWord("⏸", runstatus.Blocked, runstatus.Blocked)
		case runner.HandedOff:
			return stepWord("⏸", runstatus.NeedsHuman, runstatus.NeedsHuman)
		}
		if s.Limit != nil {
			return stepWord("⏸", runstatus.Quota, runstatus.Quota+" ("+clean(s.Limit.Kind)+")")
		}
		if s.Submission == runner.Rejected {
			return stepWord("⏸", runstatus.Rejected, runstatus.Rejected)
		}
		if (r.Phase == runner.Running || r.Phase == runner.Admitted) && (s.Submission == runner.Sending || s.Submission == runner.Accepted) {
			return stepWord("▶", runstatus.Running, runstatus.Running)
		}
	}
	return stepWord("·", runstatus.NotStarted, runstatus.NotStarted)
}
func attemptDisplay(s runner.StepState) stepDisplayData {
	if d, ok := terminalStep(s); ok {
		return d
	}
	if s.Limit != nil {
		return stepWord("⏸", runstatus.Quota, runstatus.Quota+" ("+clean(s.Limit.Kind)+")")
	}
	if s.Submission == runner.Rejected {
		return stepWord("⏸", runstatus.Rejected, runstatus.Rejected)
	}
	if s.Reason != "" {
		return stepWord("⏸", runstatus.Stopped, runstatus.Stopped+" ("+clean(s.Reason)+")")
	}
	if s.SubmittedAt != nil || s.Submission == runner.Sending || s.Submission == runner.Accepted {
		return stepDisplayData{"?", "sent, outcome unproven", attentionStyle}
	}
	return stepDisplayData{"·", "not sent", subtleStyle}
}
func stepDuration(r runner.Run, s runner.StepState, archived bool, now time.Time) string {
	if s.Kind == "command" {
		if p := s.Receipt; p != nil && !p.StartedAt.IsZero() && !p.EndedAt.IsZero() && !p.EndedAt.Before(p.StartedAt) {
			return dur(p.EndedAt.Sub(p.StartedAt))
		}
		return ""
	}
	if _, ok := terminalStep(s); ok {
		return "—"
	}
	if !archived && s.Status == "" && s.Key == r.ActiveStep && (r.Phase == runner.Running || r.Phase == runner.Blocked) && s.SubmittedAt != nil {
		return "sent " + in(max(time.Duration(0), now.Sub(*s.SubmittedAt)))
	}
	if s.SubmittedAt != nil {
		return "—"
	}
	return ""
}

func admissionHold(r runner.Run) bool {
	return r.Phase == runner.Held && (r.Reason == "admission_rejected" || r.Admitted != nil && !*r.Admitted)
}
func runNextAction(r runner.Run) string {
	if r.Phase != runner.Held {
		return ""
	}
	if admissionHold(r) {
		return "run herdr-kata stop, then herdr-kata status --json to identify and abandon this run --local."
	}
	return "inspect the diagnosis, resolve it, then u resume."
}
