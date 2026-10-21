package sched

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type inputRefusalAdmitter struct{ *fakeAdmitter }

func (a inputRefusalAdmitter) Admit(context.Context, Due) (AdmitResult, error) {
	return NotAdmitted, fmt.Errorf("%w: workflow requires input", defs.ErrInput)
}

type absentManualJob struct{ *fakeKata }

func (k absentManualJob) ListJobs(context.Context) ([]kata.Job, error) { return nil, nil }

func TestManualCompletionPublishesBeforeWake(t *testing.T) {
	const uid = "01K00000000000000000000000"
	for _, reason := range []string{"disabled", "unmapped profile", "deleted", "input", "parent_closed", "quarantined", "malformed", "native"} {
		t.Run(reason, func(t *testing.T) {
			s, k, a, _ := setup(t)
			s.cfg.Kata.Project = "example-project"
			want := reason
			switch reason {
			case "disabled":
				edit(k, func(d *defs.JobDef) { d.Enabled = false })
				want = "job is disabled"
			case "unmapped profile":
				s.cfg.Profiles = nil
			case "deleted":
				k.showErr = &kata.Error{Code: "not_found"}
				s.k = absentManualJob{k}
			case "input":
				s.admitter = inputRefusalAdmitter{a}
				want = "workflow requires input"
			case "parent_closed":
				s.admitter = decisionAdmitter{a, "parent_closed", Dropped}
			case "quarantined":
				a.result = Dropped
			case "malformed":
				k.job.Definition = []byte(`{"version":"invalid"}`)
				want = "decode"
			case "native":
				edit(k, func(d *defs.JobDef) { d.Action = defs.Action{Kind: "notify", Recipient: "worker", Message: "Review"} })
				want = "native"
			}
			wakes := 0
			s.OnManualComplete = func() {
				wakes++
				done, err := s.ManualResult(uid)
				if !done || err == nil {
					t.Fatalf("wake before terminal result: %v, %v", done, err)
				}
				if reason == "malformed" && strings.Contains(err.Error(), "job is disabled") {
					t.Fatalf("decode failure misreported as disabled: %v", err)
				}
				for _, part := range []string{"example-project", jobID, want, "herdr-kata status --json"} {
					if !strings.Contains(err.Error(), part) {
						t.Errorf("refusal %q missing %q", err, part)
					}
				}
			}
			if err := s.RunNow(jobID, uid); err != nil {
				t.Fatal(err)
			}
			s.drainManual()
			tick(t, s)
			if wakes != 1 {
				t.Fatalf("terminal refusal woke request handler %d times, want 1", wakes)
			}
			tick(t, s)
			if wakes != 1 {
				t.Fatal("completed request woke twice", wakes)
			}
			s.ForgetManual(uid)
			if done, err := s.ManualResult(uid); done || err != nil {
				t.Fatal(done, err)
			}
		})
	}
}

func TestManualWaitingDoesNotComplete(t *testing.T) {
	for _, waiting := range []string{"capacity", "admission", "retry"} {
		t.Run(waiting, func(t *testing.T) {
			s, k, a, _ := setup(t)
			switch waiting {
			case "capacity":
				a.slots = 0
			case "admission":
				s.admitter = decisionAdmitter{a, "parent_waiting", NotAdmitted}
			case "retry":
				k.showErr = fmt.Errorf("temporarily unavailable")
			}
			wakes := 0
			s.OnManualComplete = func() { wakes++ }
			const uid = "01K00000000000000000000000"
			if err := s.RunNow(jobID, uid); err != nil {
				t.Fatal(err)
			}
			s.drainManual()
			tick(t, s)
			if done, err := s.ManualResult(uid); done || err != nil || wakes != 0 {
				t.Fatal("waiting request completed", done, err, wakes)
			}
			// Once the capacity/route/admission obstacle clears, the same request
			// remains queued and can be admitted; it was not consumed as a refusal.
			a.slots = 1
			k.showErr = nil
			s.admitter = a
			if err := s.load(context.Background()); err != nil {
				t.Fatal(err)
			}
			tick(t, s)
			if len(a.got) != 1 || a.got[0].ManualRunUID != uid || wakes != 0 {
				t.Fatal(a.got, wakes)
			}
		})
	}
}
