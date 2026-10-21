// Package runstatus names run and step states for operators. One table drives
// the board and the CLI, so a status reads and colors the same everywhere.
//
// Words follow Kata's cron run statuses (running, succeeded, failed,
// cancelled, unknown) wherever Kata has one, and Kata's needs-human attention
// value for a run handed to a person. Colors follow Kata's issue list: open is
// green, closed is gray, errors are red.
package runstatus

import "github.com/salmonumbrella/herdr-kata/internal/runner"

// Role is the color role a status renders in.
type Role string

const (
	// Active work is moving on its own, like a Kata open issue (green).
	Active Role = "active"
	// Attention means the operator should look (pink).
	Attention Role = "attention"
	// Failure uses Kata's error color (red).
	Failure Role = "failure"
	// Finished needs nothing more, like a Kata closed issue (gray).
	Finished Role = "finished"
	// Idle means nothing has happened yet (dim).
	Idle Role = "idle"
)

// Status words.
const (
	Queued     = "queued"
	Running    = "running"
	Blocked    = "blocked"
	Parked     = "parked"
	NeedsHuman = "needs-human"
	Succeeded  = "succeeded"
	Failed     = "failed"
	Cancelled  = "cancelled"
	Unknown    = "unknown"
	Skipped    = "skipped"
	NotStarted = "not started"
	Never      = "never"
	Quota      = "quota"
	Rejected   = "rejected"
	Stopped    = "stopped"
)

// Status is one row of the shared table.
type Status struct {
	Word, Meaning string
	Role          Role
	// Kata reports whether Kata itself uses this word for a cron run.
	Kata bool
	// Step reports whether the word only describes a step, not a whole run.
	Step bool
}

// Table lists every status word the board and the CLI print.
var Table = []Status{
	{Word: Queued, Meaning: "admitted and waiting to start", Role: Active},
	{Word: Running, Meaning: "an agent or command is working", Role: Active, Kata: true},
	{Word: Blocked, Meaning: "the agent is waiting for input in its pane", Role: Attention},
	{Word: Parked, Meaning: "stopped before finishing; resume it with u or herdr-kata resume", Role: Attention},
	{Word: NeedsHuman, Meaning: "handed to a person, by release or by the agent's report; Kata's issue carries needs-human attention", Role: Attention},
	{Word: Unknown, Meaning: "ended, but the result could not be confirmed; check the run", Role: Attention, Kata: true},
	{Word: Failed, Meaning: "ended with a failure", Role: Failure, Kata: true},
	{Word: Succeeded, Meaning: "finished; nothing more to do", Role: Finished, Kata: true},
	{Word: Cancelled, Meaning: "stopped on purpose; nothing more to do", Role: Finished, Kata: true},
	{Word: Never, Meaning: "the job or workflow has no retained run", Role: Idle},
	{Word: Skipped, Meaning: "the workflow did not need this step", Role: Finished, Step: true},
	{Word: NotStarted, Meaning: "the step has not started", Role: Idle, Step: true},
	{Word: Quota, Meaning: "the step waits for the agent's usage limit to reset", Role: Attention, Step: true},
	{Word: Rejected, Meaning: "Herdr refused the step's prompt", Role: Attention, Step: true},
	{Word: Stopped, Meaning: "an earlier attempt of the step stopped without a result", Role: Attention, Step: true},
}

var byWord = func() map[string]Status {
	m := make(map[string]Status, len(Table))
	for _, s := range Table {
		m[s.Word] = s
	}
	return m
}()

// Lookup returns the table row for word.
func Lookup(word string) (Status, bool) {
	s, ok := byWord[word]
	return s, ok
}

// Of returns the table row for word; an unlisted word keeps its text and asks
// for attention, as unknown does.
func Of(word string) Status {
	if s, ok := byWord[word]; ok {
		return s
	}
	return Status{Word: word, Meaning: byWord[Unknown].Meaning, Role: Attention}
}

// Run names a run's state from its local journal.
func Run(r runner.Run) Status {
	switch r.Phase {
	case runner.Prepared, runner.Admitted:
		return byWord[Queued]
	case runner.Running:
		return byWord[Running]
	case runner.Blocked:
		return byWord[Blocked]
	case runner.Held:
		return byWord[Parked]
	case runner.HandedOff:
		return byWord[NeedsHuman]
	case runner.Settled:
		switch r.Outcome {
		case Succeeded, Failed, Cancelled:
			return byWord[r.Outcome]
		}
	case "":
		if r.Outcome == "" {
			return byWord[Never]
		}
	}
	return byWord[Unknown]
}

// Remote names a run listed from Kata. Kata's own status is kept, except where
// a matching local journal knows the run is parked or handed to a person,
// which Kata records only as running or unknown.
func Remote(kataStatus string, local *runner.Run) Status {
	if local != nil {
		switch local.Phase {
		case runner.Held, runner.HandedOff:
			return Run(*local)
		}
	}
	return Of(kataStatus)
}

// Step names a step's state. A recorded outcome wins; otherwise the active
// step follows its run's phase.
func Step(r runner.Run, s runner.StepState) Status {
	switch s.Status {
	case "":
	case "needs-human":
		return byWord[NeedsHuman]
	default:
		return Of(s.Status)
	}
	if s.Submission != runner.Sending && s.Submission != runner.Accepted && s.SubmittedAt == nil {
		return byWord[NotStarted]
	}
	if s.Key == r.ActiveStep {
		switch r.Phase {
		case runner.Running, runner.Admitted:
			return byWord[Running]
		case runner.Blocked, runner.Held, runner.HandedOff:
			return Run(r)
		}
	}
	return byWord[Unknown]
}
