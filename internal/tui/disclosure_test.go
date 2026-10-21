package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

func TestRunsUsesOneGutterGlyph(t *testing.T) {
	preserveTheme(t)
	applyColorMode(colorDark, false)
	m, _ := fixture(t)
	m.opts.Projects = map[string]*Lane{"example-project": {}, "other-project": {}}
	m.data.Projects = map[string]Data{"p": {Project: "example-project"}}
	m.data.Runs[0].Target.Identity = kata.Identity{ProjectUID: "p"}
	m.Update(press("3"))
	for _, tc := range []struct{ key, want string }{
		{"", "▸ — running Job 00"},
		{" ", "▾ — running Job 00"},
	} {
		if tc.key != "" {
			m.Update(press(tc.key))
		}
		text := strings.Join(strings.Fields(ansi.Strip(m.renderRuns()[1].Text)), " ")
		if !strings.HasPrefix(text, tc.want) {
			t.Fatalf("run row=%q, want prefix %q", text, tc.want)
		}
	}
	m.data.Runs[0].Job.Name = strings.Repeat("Long example name ", 10)
	other := m.data.Runs[0]
	other.UID = "another-run"
	m.data.Runs = append(m.data.Runs, other)
	m.Update(dataMsg{data: m.data})
	for _, frame := range []int{0, 9} {
		m.frame = frame
		rows := m.renderRuns()
		for _, tc := range []struct {
			index  int
			prefix string
		}{{1, "▾ — running"}, {3, "▸ — running"}} {
			// Expanded parent steps sit between the two parent rows.
			text := strings.Join(strings.Fields(ansi.Strip(rows[tc.index].Text)), " ")
			if !strings.HasPrefix(text, tc.prefix) {
				t.Fatalf("scrolling parent row=%q, want prefix %q", text, tc.prefix)
			}
		}
	}
}

func TestExpandableRowsShowDisclosure(t *testing.T) {
	preserveTheme(t)
	for _, mode := range []colorMode{colorDark, colorLight, colorNone} {
		applyColorMode(mode, false)
		collapsed, expanded := "▸", "▾"
		if mode == colorNone {
			collapsed, expanded = "+", "-"
		}
		for _, tc := range []struct {
			name     string
			steps    []runner.StepState
			workflow bool
			attempts bool
			parent   bool
		}{
			{"workflow", []runner.StepState{{Key: "first"}, {Key: "second"}}, true, false, true},
			{"attempts", []runner.StepState{{Key: "main"}}, false, true, true},
			{"plain", []runner.StepState{{Key: "main"}}, false, false, false},
			{"empty", nil, true, false, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				m, _ := fixture(t)
				r := &m.data.Runs[0]
				r.Steps, r.WFDef, r.PastAttempts = tc.steps, nil, nil
				if tc.workflow {
					r.WFDef = &defs.WorkflowDef{}
				}
				if tc.attempts {
					r.PastAttempts = []runner.StepState{{Key: "main", Attempt: 1}}
				}
				m.Update(press("3"))
				for _, state := range []struct{ key, marker string }{{"", collapsed}, {" ", expanded}, {" ", collapsed}} {
					if state.key != "" {
						m.Update(press(state.key))
					}
					want := "▶ "
					if tc.parent {
						want = state.marker + " "
					}
					rows := m.renderRuns()
					if got := ansi.Strip(rows[1].Text); !strings.HasPrefix(got, want) {
						t.Fatalf("row=%q, want prefix %q", got, want)
					}
				}
			})
		}
		m := openWorkflow(t, workflowSnapshot())
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 80})
		assertStep := func(want string) {
			t.Helper()
			for _, line := range m.buildDetail().Body() {
				if line.Hit != nil && line.Hit.Kind == "step" {
					if got := ansi.Strip(line.Text); !strings.HasPrefix(got, want) {
						t.Fatalf("step=%q want prefix %q", got, want)
					}
					return
				}
			}
			t.Fatal("missing step")
		}
		assertStep("▶" + collapsed)
		m.Update(press(" "))
		assertStep("▶" + expanded)
	}
}

func TestTabHealthAttentionColor(t *testing.T) {
	preserveTheme(t)
	for _, mode := range []colorMode{colorDark, colorLight, colorNone} {
		applyColorMode(mode, false)
		m, _ := fixture(t)
		m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		lines := strings.Split(m.viewContent(), "\n")
		if !strings.Contains(lines[1], attentionStyle.Render("runtime stopped")) {
			t.Fatalf("health role: %q", lines[1])
		}
	}
}
