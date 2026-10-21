package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

func TestTabRowReplacesBrand(t *testing.T) {
	m, _ := fixture(t)
	m.data.Running, m.data.WorkerRunning = true, true
	m.data.Runs = []runner.Run{{UID: "running", Phase: runner.Running}, {UID: "parked-a", Phase: runner.Held}, {UID: "parked-b", Phase: runner.Blocked}}
	m.Update(dataMsg{data: m.data})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	lines := strings.Split(ansi.Strip(m.viewContent()), "\n")
	if !strings.HasPrefix(lines[0], "╭") {
		t.Fatalf("tabs must start on first row: %q", lines[0])
	}
	if !strings.HasSuffix(lines[1], "1 running · 2 parked") || ansi.StringWidth(lines[1]) != 80 {
		t.Fatalf("counts not right aligned: %q", lines[1])
	}
	m.data.Runs = nil
	m.Update(dataMsg{data: m.data})
	lines = strings.Split(ansi.Strip(m.viewContent()), "\n")
	if lines[1] != "│ kata カタ │ jobs │ runs │ workflows │" {
		t.Fatalf("healthy idle tabs: %q", lines[1])
	}
}

func TestTabHealthSurvivesNarrowWidths(t *testing.T) {
	m, _ := fixture(t)
	m.data.Running = false
	for _, width := range []int{80, 42, 30, 24} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		lines := strings.Split(ansi.Strip(m.viewContent()), "\n")
		if !strings.HasSuffix(lines[1], "runtime stopped") || !strings.Contains(lines[1], "jobs") || ansi.StringWidth(lines[1]) != width {
			t.Fatalf("width %d: %q", width, lines[1])
		}
	}
}

func TestTabCountsSurviveNarrowWidths(t *testing.T) {
	m, _ := fixture(t)
	m.data.Running, m.data.WorkerRunning = true, true
	m.data.Runs = []runner.Run{{UID: "running", Phase: runner.Running}, {UID: "parked-a", Phase: runner.Held}, {UID: "parked-b", Phase: runner.Blocked}}
	m.Update(dataMsg{data: m.data})
	m.Update(tea.WindowSizeMsg{Width: 24, Height: 24})
	row := strings.Split(ansi.Strip(m.viewContent()), "\n")[1]
	if !strings.Contains(row, "jobs") || !strings.HasSuffix(row, "1r/2p") || ansi.StringWidth(row) != 24 {
		t.Fatalf("narrow counts: %q", row)
	}
}

// The embedded child keeps the same local health facts as the board. Its leave
// hint may use spare space, but cannot evict tabs or hide a stopped worker.
func TestKataTabHealthAndNavigation(t *testing.T) {
	for _, width := range []int{120, 80, 56, 30} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m, _ := fixture(t)
			m.kataActive = true
			m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
			m.Update(dataMsg{data: Data{Running: false}})
			row := strings.Split(ansi.Strip(m.viewContent()), "\n")[1]
			if !strings.Contains(row, "kata カタ") || !strings.HasSuffix(row, "runtime stopped") {
				t.Fatalf("stopped health missing: %q", row)
			}
			if width >= 56 && !strings.Contains(row, "workflows") {
				t.Fatalf("health displaced tabs: %q", row)
			}
			m.Update(dataMsg{data: Data{Running: true, WorkerRunning: true}})
			row = strings.Split(ansi.Strip(m.viewContent()), "\n")[1]
			if width >= 56 && (!strings.Contains(row, "workflows") || !strings.Contains(row, "ctrl+]")) {
				t.Fatalf("leave hint displaced tabs: %q", row)
			}
			if ansi.StringWidth(row) > width {
				t.Fatalf("row exceeds pane: %q", row)
			}
		})
	}
}

func TestKataTabTruncatedHintEndsWithEllipsis(t *testing.T) {
	m, _ := fixture(t)
	m.kataActive = true
	m.Update(tea.WindowSizeMsg{Width: 50, Height: 24})
	m.Update(dataMsg{data: Data{Running: true, WorkerRunning: true}})
	row := strings.Split(ansi.Strip(m.viewContent()), "\n")[1]
	if row != "│ kata カタ │ jobs │ runs │ workflows │ ctrl+] le…" {
		t.Fatalf("truncated hint: %q", row)
	}
}
