package runner

import (
	"errors"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

func TestReservationCapacityAcrossAdmissionAndResume(t *testing.T) {
	for _, tc := range []struct {
		name           string
		global, lane   int
		legacy, forbid bool
		free           int
		accept         bool
	}{
		{name: "room", global: 4, lane: 2, free: 1, accept: true},
		{name: "global full", global: 2, lane: 2},
		{name: "lane full", global: 4, lane: 1},
		{name: "released forbid peer", global: 4, lane: 2, forbid: true, free: 1},
		{name: "unlimited lane", global: 4, free: 2, accept: true},
		{name: "legacy keys have no lane prefix", global: 4, lane: 1, legacy: true, free: 1, accept: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var f *admissionFixture
			var m *Manager
			if tc.legacy {
				f = heldResumeFixture(t)
				m = f.m
			} else {
				f, m = projectResumeFixture(t)
			}
			m.cfg.Limits.MaxActiveRuns, m.cfg.ProjectLimit = tc.global, tc.lane
			peerJob := "released-job"
			if tc.forbid {
				peerJob = "job"
			}
			for _, peer := range []struct {
				uid, project, job string
				released          bool
			}{
				{"active", "project", "active-job", false},
				{"busy", "other-project", "busy-job", false},
				{"released", "project", peerJob, true},
			} {
				if err := f.s.Create(&Run{Version: 1, UID: peer.uid, Phase: Held, Job: kata.Job{UID: peer.job}, Target: Target{Identity: kata.Identity{ProjectUID: peer.project}}, SlotReleased: peer.released}); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.RestoreGates(); err != nil {
				t.Fatal(err)
			}
			// Admission counts a busy journal without blocking. Resume uses its
			// blocking loader after the peer releases the journal lock.
			journal := f.s.lock("busy")
			journal.Lock()
			func() {
				defer journal.Unlock()
				if got := m.FreeSlots(); got != tc.free {
					t.Errorf("free slots = %d, want %d", got, tc.free)
				}
				// Compare a new admission with the resumed run's own gate removed.
				m.unreserve(f.uid)
				if got := m.reserve("candidate", "job", true); got != tc.accept {
					t.Errorf("reserved = %v, want %v", got, tc.accept)
				}
				m.unreserve("candidate")
			}()
			// A resume must exclude its own reservation even if it still occupies
			// a slot. The released peer must still enforce overlap=forbid.
			if _, err := f.s.Transition(f.uid, func(r *Run) error { r.SlotReleased = false; return nil }); err != nil {
				t.Fatal(err)
			}
			m.reservations[f.uid] = gateKey(m.projectUID, "job")
			err := m.acceptResume(t.Context(), Request{UID: "resume-request", RunUID: f.uid})
			r := loaded(t, f.s, f.uid)
			if tc.accept {
				if err != nil || r.Resume == nil || r.Steps[0].Attempt != 2 || r.SlotReleased {
					t.Fatalf("resume not accepted: err=%v resume=%+v step=%+v", err, r.Resume, r.Steps[0])
				}
			} else if !errors.Is(err, errResumePending) || r.Resume != nil || r.Steps[0].Attempt != 1 {
				t.Fatalf("capacity or overlap gate ignored: err=%v resume=%+v step=%+v", err, r.Resume, r.Steps[0])
			}
		})
	}
}
