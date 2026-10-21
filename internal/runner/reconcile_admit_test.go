package runner

import (
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

func TestAdmitReconcilesHeldWhenFull(t *testing.T) {
	f := newAdmission(t)
	h := herdrtest.New(t)
	f.m.h = &herdr.Client{Path: h.Path}
	f.m.cfg.Limits.MaxActiveRuns = 1
	f.start(t)
	f.m.executions.Store(f.uid, struct{}{})
	if decision, err := f.m.Admit(t.Context(), f.d); err != nil || decision != sched.Admitted {
		t.Fatal("initial admission", decision, err)
	}
	// Model a held prompt occupant while a different job is waiting for room.
	if err := f.m.launch(t.Context(), f.uid); err != nil {
		t.Fatal(err)
	}
	if err := f.m.hold(f.uid, "launch_timeout"); err != nil {
		t.Fatal(err)
	}
	d := f.d
	d.Job.UID = "other-job"
	d.Occurrence = d.Occurrence.Add(time.Second)
	// Admission itself is under test; reserve its runner separately.
	d.Manual, d.ManualRunUID = true, "01K00000000000000000000001"
	f.m.executions.Store(d.ManualRunUID, struct{}{})
	decision, err := f.m.Admit(t.Context(), d)
	if err != nil || decision != sched.Admitted {
		t.Fatal("full admission did not reconcile stopped held occupant", decision, err)
	}
	if r := loaded(t, f.s, f.uid); !r.SlotReleased || r.Phase != Held || !f.m.GateHeld(f.d.Job.UID) {
		t.Fatal("held execution or gate changed", r)
	}
}
