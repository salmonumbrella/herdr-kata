package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The four tabs in the header and the 1–4 keys define the cycle order.
func TestTabCycleIncludesKata(t *testing.T) {
	for _, tc := range []struct {
		key   string
		start int
		want  []int
	}{
		{"tab", 0, []int{2, 3, 0, 1}},
		{"shift+tab", 2, []int{2, 1, 0, 3}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			m, _ := fixture(t)
			m.selectTab(tc.start)
			for _, want := range tc.want {
				// Do not launch the embedded process; opening it is observable
				// before the asynchronous command runs.
				m.Update(press(tc.key))
				got := m.tab + 1
				if m.kataActive {
					got = 0
				}
				if got != want {
					t.Fatalf("%s selected tab %d, want %d", tc.key, got, want)
				}
			}
			m.stopKata()
		})
	}
}

func TestListLayoutToggleSurvivesTabsAndResize(t *testing.T) {
	m, _ := fixture(t)
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	for _, key := range []string{"2", "3", "4"} {
		m.Update(press(key))
		if text := ansi.Strip(m.viewContent()); !strings.Contains(text, "┐┌") {
			t.Fatalf("tab %s has no inspector before toggle", key)
		}
		m.Update(press("L"))
		if text := ansi.Strip(m.viewContent()); strings.Contains(text, "┐┌") {
			t.Fatalf("tab %s kept inspector after L", key)
		}
		m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
		if text := ansi.Strip(m.viewContent()); strings.Contains(text, "┐┌") {
			t.Fatal("resize forgot full-width preference")
		}
		m.Update(press("L"))
	}
	m.Update(press("L"))
	for _, key := range []string{"2", "3", "4"} {
		m.Update(press(key))
		if strings.Contains(ansi.Strip(m.viewContent()), "┐┌") {
			t.Fatal("tab switch forgot full-width preference")
		}
	}
	m.Update(press("L"))
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if strings.Contains(ansi.Strip(m.viewContent()), "┐┌") {
		t.Fatal("split layout did not degrade")
	}
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	if !strings.Contains(ansi.Strip(m.viewContent()), "┐┌") {
		t.Fatal("resize forgot split preference")
	}
}

func TestRunsInspectorShowsSelectedRun(t *testing.T) {
	m := goldenFixture("chrome", 160, 40)
	m.selectTab(1)
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	text := ansi.Strip(bodyTextForTest(m.renderInspector(48)))
	if !strings.Contains(text, "model") || !strings.Contains(text, "status") || !strings.Contains(text, "trigger") {
		t.Fatalf("selected run has no inspector facts: %s", text)
	}
	for line := range strings.SplitSeq(ansi.Strip(m.viewContent()), "\n") {
		if strings.Contains(line, "┐┌") && !strings.HasSuffix(line, "┐") {
			t.Fatalf("run list pushed inspector outside frame: %s", line)
		}
		if strings.Contains(line, "detail") && !strings.Contains(line, "started") {
			t.Fatalf("run list header wrapped outside its pane: %s", line)
		}
	}
}

func TestGoldenListLayouts(t *testing.T) {
	for _, tc := range []struct {
		name string
		tab  int
	}{{"jobs", 0}, {"runs", 1}, {"workflows", 2}} {
		for _, layout := range []string{"inspector", "full-width"} {
			t.Run(tc.name+"/"+layout, func(t *testing.T) {
				assertFrames(t, "list-layout", tc.name+"-"+layout, [][2]int{{160, 40}}, func(w, h int) string {
					m := goldenFixture("chrome", w, h)
					m.opts.Service = &fakeActions{}
					m.reproject()
					m.rebuild()
					m.selectTab(tc.tab)
					if layout == "full-width" {
						m.Update(press("L"))
					}
					return m.viewContent()
				})
			})
		}
	}
}
