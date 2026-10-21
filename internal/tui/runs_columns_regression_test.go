package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

func failedStepColumnsModel(t *testing.T, width int) *model {
	t.Helper()
	start := runNow.Add(-2 * time.Second)
	m := newModel(Options{Root: t.TempDir(), Now: func() time.Time { return runNow }})
	m.width, m.height = width, 24
	m.Update(dataMsg{data: Data{Runs: []runner.Run{{UID: "run", Job: kata.Job{Name: "Example job"}, Phase: runner.Settled, Outcome: "failed", StartedAt: &start,
		WFDef: &defs.WorkflowDef{Steps: []defs.Step{{Key: "smoke-test", Kind: "command"}}},
		Steps: []runner.StepState{{Key: "smoke-test", Kind: "command", Status: "failed", Reason: "command smoke-test exited 2", Receipt: &runner.Receipt{StartedAt: start, EndedAt: runNow, ExitCode: 2}, Note: "inspect output"}},
	}}}})
	m.selectTab(1)
	m.Update(press("space"))
	return m
}

func TestExpandedRunsOutcomeStaysInsideItsColumn(t *testing.T) {
	m := failedStepColumnsModel(t, 80)
	check := func(want string) {
		t.Helper()
		line := m.renderRuns()[2].Text
		if got := ansi.Strip(ansi.Cut(line, 31, 47)); got != want {
			t.Errorf("outcome cell = %q, want %q", got, want)
		}
		if got := ansi.Strip(ansi.Cut(line, 48, 56)); got != "2s      " {
			t.Errorf("outcome displaced duration: %q", got)
		}
		if got := strings.TrimSpace(ansi.Strip(ansi.Cut(line, 57, 79))); got != "inspect output" {
			t.Errorf("outcome displaced note: %q", got)
		}
		if ansi.StringWidth(line) > 79 {
			t.Errorf("step row exceeds pane: %q", line)
		}
	}
	check("failed (command ")
	if !m.marqueePending {
		t.Fatal("step-only overflow did not schedule the marquee")
	}
	for range 7 {
		m.Update(marqueeTickMsg{generation: m.marqueeGeneration})
	}
	check("ailed (command s")
}

func TestExpandedRunsStepCellsMarqueeIndependently(t *testing.T) {
	m := failedStepColumnsModel(t, 80)
	r := m.data.Runs[0]
	r.Steps[0].Key = "smoke-test-with-extra-qualifiers"
	r.Steps[0].Note = "inspect the command output before continuing"
	r.PastAttempts = []runner.StepState{{Key: r.Steps[0].Key, Kind: "command", Status: "failed", Reason: "command smoke-test exited 2", Note: "inspect the archived command output", Receipt: r.Steps[0].Receipt}}
	m.Update(dataMsg{data: Data{Runs: []runner.Run{r}}})
	for _, tc := range []struct {
		ticks                            int
		key, outcome, note, archivedNote string
	}{
		{0, "smoke-test-with-", "failed (command ", "inspect the command ou", "inspect the arch"},
		{7, "moke-test-with-e", "ailed (command s", "nspect the command out", "nspect the archi"},
	} {
		for range tc.ticks {
			m.Update(marqueeTickMsg{generation: m.marqueeGeneration})
		}
		lines := m.renderRuns()
		current := lines[2].Text
		for _, c := range []struct {
			start, end int
			want       string
		}{{8, 24, tc.key}, {25, 30, "cmd  "}, {31, 47, tc.outcome}, {48, 56, "2s      "}, {57, 79, tc.note}} {
			if got := ansi.Strip(ansi.Cut(current, c.start, c.end)); got != c.want {
				t.Errorf("current cell %d:%d = %q, want %q", c.start, c.end, got, c.want)
			}
		}
		archived := lines[3].Text
		if got := ansi.Strip(ansi.Cut(archived, 37, 53)); got != tc.outcome {
			t.Errorf("archived outcome = %q, want %q", got, tc.outcome)
		}
		if got := ansi.Strip(ansi.Cut(archived, 54, 62)); got != "2s      " {
			t.Errorf("archived outcome displaced duration: %q", got)
		}
		if got := ansi.Strip(ansi.Cut(archived, 63, 79)); got != tc.archivedNote {
			t.Errorf("archived note = %q, want %q", got, tc.archivedNote)
		}
		if ansi.StringWidth(archived) > 79 {
			t.Errorf("archived row exceeds pane: %q", archived)
		}
	}
}

func TestExpandedRunsPromptDurationStaysInsideItsColumn(t *testing.T) {
	m := failedStepColumnsModel(t, 80)
	r := m.data.Runs[0]
	sent := runNow.Add(-3*time.Hour - 20*time.Minute)
	r.Phase = runner.Running
	r.ActiveStep = "verify"
	r.WFDef = &defs.WorkflowDef{Steps: []defs.Step{{Key: "verify", Kind: "prompt"}}}
	r.Steps = []runner.StepState{{Key: "verify", Kind: "prompt", Submission: runner.Accepted, SubmittedAt: &sent, Note: "inspect output"}}
	m.Update(dataMsg{data: Data{Runs: []runner.Run{r}}})
	for _, tc := range []struct {
		ticks int
		want  string
	}{{0, "sent 3h "}, {7, "ent 3h 2"}} {
		for range tc.ticks {
			m.Update(marqueeTickMsg{generation: m.marqueeGeneration})
		}
		line := m.renderRuns()[2].Text
		if got := ansi.Strip(ansi.Cut(line, 48, 56)); got != tc.want {
			t.Errorf("duration = %q, want %q", got, tc.want)
		}
		if got := strings.TrimSpace(ansi.Strip(ansi.Cut(line, 57, 79))); got != "inspect output" {
			t.Errorf("duration displaced note: %q", got)
		}
	}
}

func TestExpandedRunsCellsGolden(t *testing.T) {
	for _, frame := range []int{0, 7} {
		assertFrames(t, "runs", fmt.Sprintf("step-cell-overflow-%d", frame), [][2]int{{80, 24}, {120, 40}}, func(w, h int) string {
			m := failedStepColumnsModel(t, w)
			m.height = h
			m.frame = frame
			m.frameValid = false
			return m.viewContent()
		})
	}
}
