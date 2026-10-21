package runner

import (
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"testing"
	"time"
)

func TestRetireHeldRunPreservesSentObservationRevision(t *testing.T) {
	f := newAdmission(t)
	op := operation("old:settle:observe", "observe", Payload{Observation: &kata.Observation{Status: "unknown", ExpectedRevision: 1}})
	op.FirstSent = new(time.Now())
	r := &Run{Version: 1, UID: f.uid, Phase: Held, Outcome: "unknown", EndedAt: new(time.Now()), Target: Target{Identity: kata.Identity{ProjectUID: "project"}}, Ops: []Op{op}}
	if err := f.s.Create(r); err != nil {
		t.Fatal(err)
	}
	if err := f.m.RetireProject(t.Context(), "project"); err != nil {
		t.Fatal(err)
	}
	got := loaded(t, f.s, f.uid)
	if got.Phase != HandedOff || !got.SlotReleased {
		t.Fatal("retired held run kept its gate", got.Phase, got.SlotReleased)
	}
	final := terminalObservation(got.Ops[len(got.Ops)-1])
	if final == nil || final.ExpectedRevision != 2 || got.Ops[0].FirstSent == nil || got.Ops[0].Done {
		t.Fatal("sent observation was lost or revision reused", got.Ops)
	}
}

func TestRetireProjectHandsOffWithoutLaunching(t *testing.T) {
	for _, phase := range []Phase{Prepared, Running, Settled} {
		t.Run(string(phase), func(t *testing.T) {
			f := newAdmission(t)
			r := &Run{Version: 1, UID: f.uid, Target: Target{Identity: kata.Identity{ProjectUID: "project"}}, Phase: phase, Ops: []Op{operation("create", "create", Payload{})}}
			if phase == Settled {
				r.Outcome = "succeeded"
				r.EndedAt = new(f.m.clock.Now())
			}
			if err := f.s.Create(r); err != nil {
				t.Fatal(err)
			}
			if err := f.m.RetireProject(t.Context(), "project"); err != nil {
				t.Fatal(err)
			}
			got := loaded(t, f.s, f.uid)
			if phase == Settled {
				if got.Phase != Settled || got.Outcome != "succeeded" {
					t.Fatal("changed saved result", got.Phase, got.Outcome)
				}
			} else if got.Phase != HandedOff || got.Outcome != "unknown" || !got.SlotReleased || got.EndedAt == nil {
				t.Fatalf("unfinished run not handed off: %+v", got)
			}
			if phase == Prepared && (admitted(got) || !got.Ops[0].Done) {
				t.Fatal("removed lane can still admit", got)
			}
		})
	}
}

func TestRetirePendingResumePreservesSentObservationRevision(t *testing.T) {
	f := newAdmission(t)
	op := operation("herdr-kata:run:resume:request:observe", "observe", Payload{Observation: &kata.Observation{Status: "running", ExpectedRevision: 2}})
	op.FirstSent = new(time.Now())
	r := &Run{Version: 1, UID: f.uid, Phase: Held, Outcome: "unknown", EndedAt: new(time.Now()), Target: Target{Identity: kata.Identity{ProjectUID: "project"}}, Ops: []Op{op}}
	if err := f.s.Create(r); err != nil {
		t.Fatal(err)
	}
	if err := f.m.RetireProject(t.Context(), "project"); err != nil {
		t.Fatal(err)
	}
	got := loaded(t, f.s, f.uid)
	if got.Phase != HandedOff || !got.SlotReleased {
		t.Fatal("retired held run kept its gate", got.Phase, got.SlotReleased)
	}
	final := terminalObservation(got.Ops[len(got.Ops)-1])
	if final == nil || final.ExpectedRevision != 3 || got.Ops[0].FirstSent == nil || got.Ops[0].Done {
		t.Fatal("sent observation was lost or revision reused", got.Ops)
	}
}
