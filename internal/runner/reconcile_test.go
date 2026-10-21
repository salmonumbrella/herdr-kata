package runner

import (
	"context"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"io/fs"
	"os"
	"os/exec"
	"reflect"
	"syscall"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/herdr/herdrtest"
)

func reconcileHeld(t *testing.T, m *Manager) {
	t.Helper()
	r, ok := any(m).(interface{ ReconcileHeld(context.Context) error })
	if !ok {
		t.Fatal("held runs have no reconciliation path")
	}
	if err := r.ReconcileHeld(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestHeldSlotReconciliation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*testing.T, *executionFixture, *Run, *herdr.Agent)
		release bool
	}{
		{"idle", func(_ *testing.T, _ *executionFixture, _ *Run, a *herdr.Agent) { a.Status = "idle" }, true},
		{"done", func(_ *testing.T, _ *executionFixture, _ *Run, a *herdr.Agent) { a.Status = "done" }, true},
		{"working", func(_ *testing.T, _ *executionFixture, _ *Run, a *herdr.Agent) { a.Status = "working" }, false},
		{"blocked", func(_ *testing.T, _ *executionFixture, _ *Run, a *herdr.Agent) { a.Status = "blocked" }, false},
		{"stale sequence", func(_ *testing.T, _ *executionFixture, r *Run, a *herdr.Agent) {
			a.Status = "idle"
			a.CompletionSeq = r.Herdr.PreSubmitSeq
		}, false},
		{"omitted sequence", func(_ *testing.T, _ *executionFixture, _ *Run, a *herdr.Agent) {
			a.Status = "idle"
			a.CompletionSeq = nil
		}, true},
		{"foreign name", func(_ *testing.T, _ *executionFixture, _ *Run, a *herdr.Agent) { a.Status = "idle"; a.Name = "foreign" }, false},
		{"foreign session", func(_ *testing.T, _ *executionFixture, _ *Run, a *herdr.Agent) {
			a.Status = "idle"
			s := *a.Session
			s.Value = "replacement"
			a.Session = &s
		}, false},
		{"late result", func(t *testing.T, f *executionFixture, _ *Run, a *herdr.Agent) {
			a.Status = "idle"
			f.result(t, "succeeded")
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecovery(t)
			f.m.cfg.Limits.MaxActiveRuns = 1
			f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Working}})
			f.accepted(t)
			if err := f.m.hold(f.uid, "timeout"); err != nil {
				t.Fatal(err)
			}
			if err := f.m.RestoreGates(); err != nil {
				t.Fatal(err)
			}
			r := loaded(t, f.s, f.uid)
			a, err := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
			if err != nil {
				t.Fatal(err)
			}
			seq := uint64(1)
			a.CompletionSeq = &seq
			tc.change(t, f, r, &a)
			f.h.SetAgent(a)
			before := loaded(t, f.s, f.uid)
			reconcileHeld(t, f.m)
			reconcileHeld(t, f.m)
			got := loaded(t, f.s, f.uid)
			if got.SlotReleased != tc.release {
				t.Fatalf("slot released = %v, want %v", got.SlotReleased, tc.release)
			}
			got.SlotReleased = before.SlotReleased
			if !reflect.DeepEqual(got, before) {
				t.Fatalf("reconciliation changed held outcome or attempt: %+v", got)
			}
			wantSlots := 0
			if tc.release {
				wantSlots = 1
			}
			if f.m.FreeSlots() != wantSlots || !f.m.GateHeld(r.Job.UID) {
				t.Fatal("capacity or overlap gate changed incorrectly")
			}
			if f.count("agent.prompt") != 1 || f.count("agent.start") != 1 || f.count("tab.close") != 0 {
				t.Fatal("held run resumed or cleaned up")
			}
		})
	}
}

func TestHeldGoneSlotReconciliation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		release bool
	}{
		{"shell", true}, {"exited pane", true}, {"foreign pane occupant", false},
		{"changed terminal", false}, {"changed server", false}, {"process read failure", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecovery(t)
			f.launch(t)
			if err := f.m.hold(f.uid, "launch_timeout"); err != nil {
				t.Fatal(err)
			}
			r := loaded(t, f.s, f.uid)
			a, err := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
			if err != nil {
				t.Fatal(err)
			}
			f.h.RemoveAgent(a.Name)
			switch tc.name {
			case "exited pane":
				if err := f.m.h.TabClose(t.Context(), r.Herdr.TabID); err != nil {
					t.Fatal(err)
				}
			case "foreign pane occupant":
				a.Name = "foreign"
				f.h.SetAgent(a)
			case "changed terminal":
				p, _ := f.m.h.PaneGet(t.Context(), r.Herdr.PaneID)
				p.TerminalID = "replacement"
				f.h.SetPane(p)
			case "changed server":
				_, err = f.s.Transition(f.uid, func(r *Run) error { r.Herdr.ServerID.Ino++; return nil })
				if err != nil {
					t.Fatal(err)
				}
			case "process read failure":
				f.h.Fail("pane.process_info", herdr.Error{Code: "internal", Message: "unavailable"})
			}
			reconcileHeld(t, f.m)
			if got := loaded(t, f.s, f.uid); got.SlotReleased != tc.release || got.Phase != Held || got.Reason != "launch_timeout" {
				t.Fatalf("reconciled gone occupant: %+v", got)
			}
		})
	}
}

func TestHeldMixedCommandKeepsSlot(t *testing.T) {
	bg := exec.Command("sleep", "30")
	bg.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := bg.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(-bg.Process.Pid, syscall.SIGKILL); _ = bg.Wait() }()
	f := newRecovery(t)
	f.m.cfg.Limits.MaxActiveRuns = 1
	f.script(herdrtest.Script{Prompt: []herdrtest.Outcome{herdrtest.Working}})
	f.accepted(t)
	r := loaded(t, f.s, f.uid)
	if _, err := f.s.Transition(f.uid, func(r *Run) error {
		r.Steps[0].Status = "succeeded"
		r.Steps = append(r.Steps, StepState{Key: "deploy", Kind: "command", Attempt: 1, Submission: Sending, Status: "unknown", Reason: "command_outcome_unknown",
			Receipt: &Receipt{Version: 1, Run: r.UID, Step: "deploy", Attempt: 1, StartedAt: settleTime, EndedAt: settleTime, PGID: bg.Process.Pid, GroupAlive: true}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.hold(f.uid, "command_outcome_unknown"); err != nil {
		t.Fatal(err)
	}
	if err := f.m.RestoreGates(); err != nil {
		t.Fatal(err)
	}
	a, err := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
	if err != nil {
		t.Fatal(err)
	}
	seq := uint64(1)
	a.CompletionSeq = &seq
	a.Status = "idle"
	f.h.SetAgent(a)
	reconcileHeld(t, f.m)
	if loaded(t, f.s, f.uid).SlotReleased {
		t.Fatal("slot released while the command's process group is alive")
	}
	if err := syscall.Kill(-bg.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = bg.Wait()
	reconcileHeld(t, f.m)
	if !loaded(t, f.s, f.uid).SlotReleased {
		t.Fatal("stopped command kept slot")
	}
}

type changedHeldRunFS struct {
	state.OSFS
	path            string
	reads, changeAt int
	corrupt         bool
}

func (f *changedHeldRunFS) Lstat(path string) (fs.FileInfo, error) {
	if path == f.path {
		f.reads++
		if f.reads == f.changeAt {
			if f.corrupt {
				if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
					return nil, err
				}
			} else if err := os.Remove(path); err != nil {
				return nil, err
			}
		}
	}
	return f.OSFS.Lstat(path)
}
func TestReconcileHeldSkipsUnavailableJournal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		corrupt  bool
		changeAt int
	}{{"removed after scan", false, 2}, {"quarantined after scan", true, 2}, {"removed before transition", false, 3}, {"quarantined before transition", true, 3}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecovery(t)
			if err := f.m.hold(f.uid, "timeout"); err != nil {
				t.Fatal(err)
			}
			f.s.fsys = &changedHeldRunFS{path: f.s.layout.RunFile(f.uid), corrupt: tc.corrupt, changeAt: tc.changeAt}
			if err := f.m.ReconcileHeld(t.Context()); err != nil {
				t.Fatal("unavailable journal restarted worker", err)
			}
		})
	}
}

func TestHeldProofLeavesRunUnchanged(t *testing.T) {
	for _, status := range []string{"working", "blocked"} {
		t.Run(status, func(t *testing.T) {
			f := newRecovery(t)
			f.accepted(t)
			if _, err := f.s.Transition(f.uid, func(r *Run) error {
				r.Steps[0].SawActivity = false
				// Keep the unresolved prompt at index zero despite a later dependency.
				r.Steps = append(r.Steps, StepState{Key: "dependency", Kind: "prompt", Status: "succeeded", SawActivity: true})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := f.m.hold(f.uid, "no_activity"); err != nil {
				t.Fatal(err)
			}
			r := loaded(t, f.s, f.uid)
			before := loaded(t, f.s, f.uid)
			a, err := f.m.h.AgentGet(t.Context(), r.Herdr.AgentName)
			if err != nil {
				t.Fatal(err)
			}
			a.Status = status
			f.h.SetAgent(a)
			stopped, activity := f.m.heldStopped(t.Context(), r)
			if stopped {
				t.Fatal("active occupant proved stopped")
			}
			if !reflect.DeepEqual(r, before) {
				t.Fatal("held proof mutated its input")
			}
			if !reflect.DeepEqual(activity, []int{0}) {
				t.Fatalf("activity indexes = %v, want [0]", activity)
			}
			reconcileHeld(t, f.m)
			got := loaded(t, f.s, f.uid)
			if got.SlotReleased || !got.Steps[0].SawActivity || !got.Steps[1].SawActivity {
				t.Fatalf("late activity not retained independently of capacity: %+v", got)
			}
		})
	}
}

// Capacity reconciliation checks Sending current commands when an agent exists;
// resume and local quarantine separately check Accepted and archived commands.
func TestHeldProofCommandCoverage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		past       bool
		submission Submission
	}{
		{"current accepted", false, Accepted},
		{"past sending", true, Sending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bg := exec.Command("sleep", "30")
			bg.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := bg.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = syscall.Kill(-bg.Process.Pid, syscall.SIGKILL); _ = bg.Wait() })
			f := newRecovery(t)
			f.accepted(t)
			if _, err := f.s.Transition(f.uid, func(r *Run) error {
				r.Steps[0].Status = "succeeded"
				s := StepState{Key: "command", Kind: "command", Attempt: 1, Submission: tc.submission,
					Receipt: &Receipt{Version: 1, Run: r.UID, Step: "command", Attempt: 1, StartedAt: settleTime, EndedAt: settleTime, PGID: bg.Process.Pid}}
				if tc.past {
					r.PastAttempts = append(r.PastAttempts, s)
				} else {
					r.Steps = append(r.Steps, s)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := f.m.hold(f.uid, "no_activity"); err != nil {
				t.Fatal(err)
			}
			r := loaded(t, f.s, f.uid)
			stopped, activity := f.m.heldStopped(t.Context(), r)
			if !stopped || len(activity) != 0 {
				t.Fatal("capacity proof widened command coverage")
			}
			if f.m.resumeStopped(t.Context(), r) || f.m.localStopped(t.Context(), r) {
				t.Fatal("operator proof ignored a live command")
			}
		})
	}
}
