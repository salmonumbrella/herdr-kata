package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

type resumeFailure struct{ fakeActions }

func (*resumeFailure) Resume(string) error { return errors.New("resume refused") }

func TestResumeAcknowledgementShowsRunning(t *testing.T) {
	for _, surface := range []string{"workflows", "runs", "workflow detail", "run detail"} {
		t.Run(surface, func(t *testing.T) {
			d := workflowSnapshot()
			d.Runs = d.Runs[:1]
			d.Runs[0].Phase = runner.Held
			d.Runs[0].Job.Name = "Example job"
			d.Runs[0].Reason = "no_result"
			d.Runs[0].ActiveStep = "inspect"
			d.Runs[0].Steps = []runner.StepState{{Key: "inspect", Status: "failed"}, {Key: "check"}}
			m := workflowModel(t, d)
			m.opts.Service = &fakeActions{}
			m.width, m.height = 180, 50
			switch surface {
			case "runs":
				m.selectTab(1)
			case "workflow detail":
				send(m, "enter")
			case "run detail":
				m.selectTab(1)
				send(m, "enter")
			}
			_, cmd := m.Update(press("u"))
			if cmd == nil {
				t.Fatal("missing resume request", m.notice.Text)
			}
			if text := ansi.Strip(m.viewContent()); !strings.Contains(text, "parked") {
				t.Fatal("changed before acknowledgement", text)
			}
			applyTestCommand(m, cmd)
			assertRunning := func() {
				t.Helper()
				text := ansi.Strip(uncachedView(m))
				if !strings.Contains(text, "running") || strings.Contains(text, "waiting: no_result") {
					t.Fatal(text)
				}
				// At list widths with an inspector, both the workflow row and last run
				// must show the new state. The inspector must retain the current step.
				lastRun := strings.LastIndex(text, "Last run")
				if surface == "workflows" && (strings.Count(text, "running") < 2 || lastRun < 0 || !strings.Contains(text[lastRun:], "inspect")) {
					t.Fatal(text)
				}
			}
			assertRunning()
			m.Update(dataMsg{data: d}) // An already outstanding poll can still return the parked snapshot.
			assertRunning()
			done := d
			done.Runs = append([]runner.Run(nil), d.Runs...)
			done.Runs[0].Phase = runner.Settled
			done.Runs[0].Outcome = "succeeded"
			done.Runs[0].Reason = ""
			m.Update(dataMsg{data: done})
			text := ansi.Strip(uncachedView(m))
			if !strings.Contains(text, "succeeded") || strings.Contains(text, "waiting: no_result") {
				t.Fatal(text)
			}
		})
	}
}

func TestResumeFailureKeepsParkedState(t *testing.T) {
	d := workflowSnapshot()
	d.Runs = d.Runs[:1]
	d.Runs[0].Phase = runner.Held
	d.Runs[0].Job.Name = "Example job"
	m := workflowModel(t, d)
	m.opts.Service = &fakeActions{}
	m.opts.Service = &resumeFailure{}
	send(m, "u")
	text := ansi.Strip(uncachedView(m))
	if !strings.Contains(text, "resume refused") || !strings.Contains(text, "parked") {
		t.Fatal(text)
	}
}

func TestResumeUnchangedSnapshotReturnsToParked(t *testing.T) {
	d := workflowSnapshot()
	d.Runs = d.Runs[:1]
	d.Runs[0].Phase = runner.Held
	d.Runs[0].Job.Name = "Example job"
	m := workflowModel(t, d)
	m.opts.Service = &fakeActions{}
	now := runNow
	m.now = func() time.Time { return now }
	send(m, "u")
	now = now.Add(time.Minute)
	m.Update(dataMsg{data: d})
	text := ansi.Strip(uncachedView(m))
	if !strings.Contains(text, "parked") {
		t.Fatal("stale acknowledgement hid runtime state", text)
	}
}

func TestAcceptedResumeSnapshotShowsRunning(t *testing.T) {
	for _, phase := range []runner.Phase{runner.Held, runner.Admitted} {
		d := workflowSnapshot()
		d.Runs = d.Runs[:1]
		d.Runs[0].Phase = phase
		d.Runs[0].Resume = &runner.ResumeState{UID: "resume", Pending: phase == runner.Held}
		d.Runs[0].ActiveStep = "inspect"
		m := workflowModel(t, d)
		text := ansi.Strip(uncachedView(m))
		if strings.Count(text, "running") < 2 || strings.Contains(text, "waiting:") {
			t.Fatal(text)
		}
		if d.Runs[0].Phase != phase {
			t.Fatal("mutated worker snapshot")
		}
	}
}

func TestResumeGolden(t *testing.T) {
	for _, surface := range []string{"workflows", "runs", "workflow-detail", "run-detail", "workflows-settled", "run-detail-settled"} {
		screen, name := strings.TrimSuffix(surface, "-settled"), "resume"
		if strings.HasSuffix(surface, "-settled") {
			name = "resume-settled"
		}
		t.Run(surface, func(t *testing.T) {
			assertFrames(t, screen, name, [][2]int{{80, 24}, {120, 40}, {200, 50}}, func(w, h int) string {
				d := workflowSnapshot()
				d.Runs = d.Runs[:1]
				d.Runs[0].Phase = runner.Held
				d.Runs[0].Job.Name = "Example job"
				d.Runs[0].ActiveStep = "inspect"
				d.Runs[0].Steps = []runner.StepState{{Key: "inspect", Status: "failed"}, {Key: "check"}}
				m := workflowModel(t, d)
				m.opts.Service = &fakeActions{}
				m.width, m.height = w, h
				switch strings.TrimSuffix(surface, "-settled") {
				case "runs":
					m.selectTab(1)
				case "workflow-detail":
					send(m, "enter")
				case "run-detail":
					m.selectTab(1)
					send(m, "enter")
				}
				if screen == "runs" || screen == "run-detail" {
					// Keep the legacy run notice's project display name explicit.
					m.opts.Projects = map[string]*Lane{"project-a": &m.opts.Lane}
				}
				send(m, "u")
				if strings.HasSuffix(surface, "-settled") {
					d.Runs[0].Phase = runner.Settled
					d.Runs[0].Outcome = "succeeded"
					d.Runs[0].Resume = &runner.ResumeState{UID: "resume"}
					d.Runs[0].Ops = []runner.Op{{Kind: "observe"}}
					m.Update(dataMsg{data: d})
				}
				return uncachedView(m)
			})
		})
	}
}

func TestResumedRunStopsReportingImmediately(t *testing.T) {
	for _, surface := range []string{"workflows", "run detail"} {
		for _, phase := range []runner.Phase{runner.Settled, runner.Held, runner.HandedOff} {
			t.Run(fmt.Sprintf("%s/%s", surface, phase), func(t *testing.T) {
				d := workflowSnapshot()
				d.Runs = d.Runs[:1]
				d.Runs[0].Phase = runner.Running
				d.Runs[0].Resume = &runner.ResumeState{UID: "resume"}
				d.Runs[0].ConsumedResumes = []string{"resume"}
				d.Runs[0].Ops = []runner.Op{{Kind: "observe"}}
				m := workflowModel(t, d)
				m.width, m.height = 200, 50
				if surface == "run detail" {
					m.selectTab(1)
					send(m, "enter")
				}
				if text := ansi.Strip(uncachedView(m)); !strings.Contains(text, "reporting…") {
					t.Fatal("missing active reporting state", text)
				}
				stopped := d
				stopped.Runs = append([]runner.Run(nil), d.Runs...)
				stopped.Runs[0].Phase = phase
				stopped.Runs[0].Outcome = "succeeded"
				stopped.Runs[0].Reason = "no_result"
				m.Update(dataMsg{data: stopped})
				if text := ansi.Strip(uncachedView(m)); strings.Contains(text, "reporting…") {
					t.Fatal("stopped snapshot kept the running reporting line", text)
				}
				if m.data.Runs[0].Ops[0].Done {
					t.Fatal("presentation marked an unfinished write complete")
				}
				stopped.Runs[0].Ops = []runner.Op{{Kind: "observe", Inspect: "revision changed"}}
				m.Update(dataMsg{data: stopped})
				if text := ansi.Strip(uncachedView(m)); !strings.Contains(text, "inspect: revision changed") {
					t.Fatal("reporting diagnosis hidden", text)
				}
			})
		}
	}
}

func TestFlowProgressInspectorAndResume(t *testing.T) {
	actions := &fakeActions{}
	m := newModel(Options{Root: t.TempDir(), Config: workflowTestConfig(), Service: actions})
	wf := defs.WorkflowDef{Version: 1, Options: json.RawMessage(`{"on_failure":"park"}`), Steps: []defs.Step{{Key: "make", Kind: "prompt", Prompt: "Make the example", Options: json.RawMessage(`{"agent_mode":"fresh"}`)}, {Key: "verify", Kind: "prompt", Prompt: "Verify the example", After: []string{"make"}, Options: json.RawMessage(`{"on_fail":{"goto":"make","max_loops":2}}`)}, {Key: "publish", Kind: "command", After: []string{"verify"}, Command: "true"}, {Key: "done", Kind: "command", After: []string{"publish"}, Command: "true"}}}
	raw, _ := json.Marshal(wf)
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	m.data = Data{Health: map[string]any{"version": float64(1), "project_uid": "project"}, Runs: []runner.Run{{UID: "run", Target: runner.Target{Identity: kata.Identity{ProjectUID: "project"}}, Job: kata.Job{Name: "flow"}, Phase: runner.Held, PromptCap: 12, PromptsSent: 5, ActiveStep: "verify", WFDef: &wf, Loop: runner.LoopState{Used: 1}, Steps: []runner.StepState{{Key: "make", Status: "succeeded"}, {Key: "verify", Status: "failed"}, {Key: "publish"}, {Key: "done"}}}}, Workflows: []kata.Workflow{{UID: "wf", ProjectUID: "project", Name: "flow", Definition: raw}}}
	m.Update(dataMsg{data: m.data})
	send(m, "3")
	if !strings.Contains(uncachedView(m), "1/4 · verify · loop 1/2") {
		t.Fatal(uncachedView(m))
	}
	send(m, "u")
	if len(actions.calls) != 1 || actions.calls[0] != "resume:run" {
		t.Fatal(actions.calls)
	}
	send(m, "4")
	view := ansi.Strip(uncachedView(m))
	for _, want := range []string{"↺make", "failure park", "1. make", "2. verify", "prompt fresh", "prompt shared"} {
		if !strings.Contains(view, want) {
			t.Fatal(want, view)
		}
	}
}
