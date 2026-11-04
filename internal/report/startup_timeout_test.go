package report

import (
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/journal"
)

func TestStopFailureReasonSurvivesFinalReport(t *testing.T) {
	for _, reason := range []string{"timeout", "agent_not_started"} {
		ctx, st, k, r := setup(t)
		if err := st.Update(r.Key, func(r *journal.Run) error {
			r.Phase = "settled"
			r.Outcome = "failed"
			r.Attempts = []journal.Attempt{{Status: "failed", StopReason: reason}}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := Final(ctx, k, st, r.Key); err != nil {
			t.Fatal(err)
		}
		got := body(t, k.bodies[0])
		if got.Status != "failed" || got.Summary.Message == nil || *got.Summary.Message != "reason: "+reason {
			t.Fatalf("lost stop reason: %s", k.bodies[0])
		}
	}
}
