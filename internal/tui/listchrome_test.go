package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

// The list contract follows Kata: row zero is normal, odd rows use
// 254/236, and selection overrides striping with 153/24 (reverse in None).
func TestListRowBands(t *testing.T) {
	preserveTheme(t)
	for _, mode := range []colorMode{colorLight, colorDark, colorNone} {
		applyColorMode(mode, false)
		for tab := range 3 {
			for _, broken := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/tab%d/broken%v", mode, tab, broken), func(t *testing.T) {
					m := goldenFixture("chrome", 120, 40)
					if tab == 2 {
						m.data.Workflows = nil
						for i := range 3 {
							def := json.RawMessage(`{"version":1,"steps":[{"key":"check","kind":"command","command":"true"}]}`)
							if broken && i == 1 {
								def = json.RawMessage(`{`)
							}
							m.data.Workflows = append(m.data.Workflows, kata.Workflow{UID: fmt.Sprintf("flow-%d", i), Name: fmt.Sprintf("Example flow %d", i), Definition: def})
						}
					}
					m.tab = tab
					m.Update(dataMsg{data: m.data})
					// Move across both parity classes so the old selected row regains its band.
					for _, cursor := range []int{0, 1, 2} {
						m.cursor = cursor
						m.frameValid = false
						frame := m.renderFrame()
						lines := strings.Split(frame.Text, "\n")
						seen := 0
						for y, hit := range frame.Hits.Rows {
							if hit.Kind != []string{"job", "run", "workflow"}[tab] {
								continue
							}
							line := lines[y]
							want := ""
							if hit.Index == cursor {
								want = "48;5;153"
								if mode == colorDark {
									want = "48;5;24"
								}
								if mode == colorNone {
									want = "7"
								}
							} else if hit.Index%2 == 1 && mode != colorNone {
								want = "48;5;254"
								if mode == colorDark {
									want = "48;5;236"
								}
							}
							if want != "" && !strings.Contains(line, want+"m") {
								t.Errorf("row %d cursor %d: missing %s in %q", hit.Index, cursor, want, line)
							}
							if mode == colorNone && strings.Contains(line, "48;") {
								t.Errorf("colorless row %q", line)
							}
							seen++
						}
						if seen != 3 {
							t.Fatalf("got %d rows", seen)
						}
					}
				})
			}
		}
	}
}

func TestJobProblemSectionHeadings(t *testing.T) {
	preserveTheme(t)
	applyColorMode(colorDark, false)
	for _, heading := range []struct{ name, title string }{{"warnings", "Warnings"}, {"unsupported", "Unsupported"}} {
		m := jobDetailFixture(t, `{"enabled":true,"action":{"kind":"execute","prompt":"work"}}`)
		if heading.name == "warnings" {
			m.data.Jobs[0].Warnings = []string{"ignored options key: example"}
		} else {
			m.data.Jobs[0].Supported = false
			m.data.Jobs[0].Reasons = []string{"unsupported example option"}
		}
		m.rebuildJobIndex()
		text := bodyTextForTest(m.buildJobDetail().Body())
		if !strings.Contains(text, detailSectionHeaderStyle.Render(heading.title)) || detailSectionHeaderStyle.Render(heading.title) != "\x1b[1m"+heading.title+"\x1b[m" {
			t.Errorf("section %s does not use Kata’s capitalized default-color heading role", heading.title)
		}
	}
}

// Lists fill their pane, with a passive inspector on wide terminals.
func TestListsFillPaneWidth(t *testing.T) {
	preserveTheme(t)
	for _, mode := range []colorMode{colorLight, colorDark, colorNone} {
		applyColorMode(mode, false)
		for _, width := range []int{80, 120, 320} {
			for tab := range 3 {
				for _, multi := range []bool{false, true} {
					t.Run(fmt.Sprintf("%d/%d/tab%d/multi%v", mode, width, tab, multi), func(t *testing.T) {
						m := goldenFixture("chrome", width, 40)
						if multi {
							m.opts.Projects = map[string]*Lane{"example-project": {}, "other-project": {}}
						}
						if tab == 2 {
							original := m.data.Workflows[0]
							m.data.Workflows = nil
							for i := range 3 {
								w := original
								w.UID = fmt.Sprintf("flow-%d", i)
								w.Name = fmt.Sprintf("Example flow %d", i)
								m.data.Workflows = append(m.data.Workflows, w)
							}
						}
						m.tab = tab
						m.Update(dataMsg{data: m.data})
						m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
						frame := m.renderFrame()
						lines := strings.Split(frame.Text, "\n")
						if got := ansi.StringWidth(lines[2]); got != width {
							t.Errorf("tab rule width %d, want %d", got, width)
						}
						if tab == 0 && width == 120 && !multi {
							header := ansi.Strip(lines[3])
							if job, model := strings.Index(header, "job"), strings.Index(header, "model"); model-job < 60 {
								t.Errorf("job must take the extra width; job %d, model %d:\n%s", job, model, header)
							}
						}
						tableEnd := width
						if width == 320 {
							tableEnd = 109
						}
						seen, secondRow := 0, -1
						for y, hit := range frame.Hits.Rows {
							if hit.Kind != []string{"job", "run", "workflow"}[tab] {
								continue
							}
							seen++
							if hit.Index == 1 {
								secondRow = y
							}
							if hit.X1 != tableEnd {
								t.Errorf("row %d clickable edge %d, want %d", hit.Index, hit.X1, tableEnd)
							}
							if tableEnd == width && ansi.StringWidth(lines[y]) != width {
								t.Errorf("row %d ends before pane edge: %q", hit.Index, lines[y])
							}
						}
						if seen != 3 {
							t.Fatalf("got %d rows", seen)
						}
						if tableEnd != width {
							title := ansi.Strip(ansi.Cut(lines[4], 111, 320))
							if !strings.HasPrefix(title, "Example") {
								t.Errorf("inspector is not pinned inside the right pane: %q", title)
							}
						}
						m.Update(tea.MouseClickMsg{X: tableEnd - 1, Y: secondRow, Button: tea.MouseLeft})
						if m.cursor != 1 {
							t.Errorf("click at the table edge left cursor at %d", m.cursor)
						}
						m.Update(dataMsg{data: Data{}})
						empty := strings.Split(m.viewContent(), "\n")
						if ansi.StringWidth(empty[3]) != width {
							t.Errorf("empty table header does not fill the pane: %q", empty[3])
						}
					})
				}
			}
		}
	}
}
