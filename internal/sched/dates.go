package sched

import "github.com/salmonumbrella/herdr-kata/internal/kata"

func DateOccurrence(issueUID string, v *kata.DateValue) *Occurrence {
	if v == nil {
		return nil
	}
	return &Occurrence{Key: "issue-scheduled:" + issueUID + ":" + v.Value, At: v.Instant.UTC()}
}
