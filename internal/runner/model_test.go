package runner

import "testing"

// Only a completed operation without retained inspection evidence counts as
// landed; retired diagnostics cannot count as delivered evidence.
func TestOpDeliveryStates(t *testing.T) {
	for _, tc := range []struct {
		name            string
		op              Op
		landed, blocked bool
	}{
		{name: "pending", op: Op{}, landed: false, blocked: false},
		{name: "blocked", op: Op{Inspect: "conflict"}, landed: false, blocked: true},
		{name: "landed", op: Op{Done: true}, landed: true, blocked: false},
		{name: "retired", op: Op{Done: true, Inspect: "cancelled by release"}, landed: false, blocked: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.op.Landed(); got != tc.landed {
				t.Errorf("Landed() = %t, want %t", got, tc.landed)
			}
			if got := tc.op.Blocked(); got != tc.blocked {
				t.Errorf("Blocked() = %t, want %t", got, tc.blocked)
			}
		})
	}
}

func TestOpRetireKeepsFrozenIntentAndSigningEvidence(t *testing.T) {
	op := operation("attention", "meta.set", Payload{Ref: "issue", Name: "work.attention", Value: "needs-human"})
	op.Inspect, op.SigningRefused = "conflict", true
	before := intent(op)
	op.retire("cancelled by release")
	if !op.Done || op.Inspect != "cancelled by release" || !op.SigningRefused || intent(op) != before {
		t.Fatalf("retirement lost intent or diagnostic evidence: %+v", op)
	}
}
