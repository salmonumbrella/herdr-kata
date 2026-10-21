package sched

import (
	"context"
	"testing"
	"time"
)

type decisionAdmitter struct {
	*fakeAdmitter
	reason string
	result AdmitResult
}

func (a decisionAdmitter) Admit(context.Context, Due) (AdmitResult, error) {
	return a.result, &AdmissionDecision{Reason: a.reason}
}
func TestAdmissionDecisionKeepsPendingOrDropsWithReason(t *testing.T) {
	for _, result := range []AdmitResult{NotAdmitted, Dropped} {
		t.Run(map[AdmitResult]string{NotAdmitted: "pending", Dropped: "skip"}[result], func(t *testing.T) {
			s, _, a, c := setup(t)
			s.admitter = decisionAdmitter{a, "parent_closed", result}
			calls := 0
			s.RecordDecision = func(job, reason string, dropped bool) error {
				calls++
				if job != jobID || reason != "parent_closed" || dropped != (result == Dropped) {
					t.Fatal(job, reason, dropped)
				}
				return nil
			}
			c.advance(time.Minute)
			tick(t, s)
			if (len(pending(t, s)) == 0) != (result == Dropped) || calls != 1 {
				t.Fatal(pending(t, s), calls)
			}
		})
	}
}
