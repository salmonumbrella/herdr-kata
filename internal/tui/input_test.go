package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

func TestNumberKeysAndDetailIsolation(t *testing.T) {
	m, f := fixture(t)
	for _, s := range []string{"2", "3", "4"} {
		send(m, s)
		if s == "1" {
			if m.tab != 0 {
				t.Fatal(m.tab)
			}
			continue
		}
		if m.tab != int(s[0]-'2') {
			t.Fatal(s, m.tab)
		}
	}
	m.selectTab(0)
	cmd := m.pushDetail(viewJob, "", "j00")
	if cmd != nil {
		m.Update(cmd())
	}
	for _, s := range []string{"1", "2", "3", "4", "5", "tab", "shift+tab"} {
		send(m, s)
		if m.currentView() != viewJob || m.tab != 0 {
			t.Fatal(s, m.nav)
		}
	}
	if len(f.calls) != 0 {
		t.Fatal(f.calls)
	}
}
func TestPageVersusViewport(t *testing.T) {
	m, _ := fixture(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	rows := m.pageRows()
	send(m, "]")
	if m.cursor != rows || m.scroll != 0 || !m.follow {
		t.Fatal(m.cursor, m.scroll, m.follow)
	}
	cursor := m.cursor
	send(m, "ctrl+d")
	if m.cursor != cursor || m.follow {
		t.Fatal("viewport retargeted")
	}
	_ = m.pushDetail(viewRun, "", "r1")
	m.scroll = 0
	for _, s := range []string{"]", "[", "ctrl+b", "ctrl+f"} {
		_, _ = m.Update(press(s))
		if m.scroll != 0 {
			t.Fatal(s, m.scroll)
		}
	}
}
func TestSearchContract(t *testing.T) {
	m, _ := fixture(t)
	m.cursor = 3
	send(m, "/")
	send(m, "Job")
	send(m, "esc")
	if m.query != "Job" || m.searching {
		t.Fatal(m.query)
	}
	send(m, "esc")
	if m.query != "" {
		t.Fatal(m.query)
	}
	send(m, "/")
	send(m, "M")
	if m.query != "M" || m.mouseOff {
		t.Fatal(m.query)
	}
	send(m, "ctrl+u")
	if m.query != "" || !m.searching {
		t.Fatal(m.query)
	}
	send(m, "backspace")
	if m.searching {
		t.Fatal("empty search stays editing")
	}
}
func TestNavigationNeverActs(t *testing.T) {
	m, f := fixture(t)
	send(m, "3")
	send(m, "enter")
	if m.currentView() != viewRun || len(f.calls) != 0 {
		t.Fatal(m.nav, f.calls)
	}
	send(m, "l")
	if m.currentView() != viewJob || len(f.calls) != 0 {
		t.Fatal(m.nav, f.calls)
	}
}
func TestMouseUsesVisibleHits(t *testing.T) {
	m, f := fixture(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	var y int
	var hit rowHit
	for row, h := range m.hits.Rows {
		if h.Index == 0 {
			y, hit = row, h
			break
		}
	}
	m.searching = true
	m.Update(tea.MouseClickMsg{X: hit.X0, Y: y, Button: tea.MouseLeft})
	if m.currentView() != viewList || len(f.calls) > 0 {
		t.Fatal(f.calls)
	}
	m.searching = false
	m.frameValid = false
	m.viewContent()
	m.Update(tea.MouseClickMsg{X: hit.X0, Y: y, Button: tea.MouseLeft})
	if m.currentView() != viewJob {
		t.Fatal(m.nav)
	}
}
func TestMouseModalOwnership(t *testing.T) {
	m, f := fixture(t)
	m.searching = true
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(tea.MouseClickMsg{X: 2, Y: 1, Button: tea.MouseLeft})
	if len(f.calls) != 0 || !m.searching {
		t.Fatal(f.calls)
	}
}

func TestFullPageDetailViewportKeepsTarget(t *testing.T) {
	m, _ := fixture(t)
	m.height = 24
	m.width = 80
	m.data.Runs[0].Phase = "held"
	m.data.Runs[0].ActiveStep = "build"
	m.data.Runs[0].Steps[0].Status = "failed"
	m.data.Runs[0].Steps[0].Note = strings.Repeat("long diagnosis with useful context ", 100)
	_ = m.pushDetail(viewRun, "", "r1")
	m.Update(struct{}{})
	send(m, "ctrl+d")
	if m.scroll != 21 || m.follow {
		t.Fatal(m.scroll, m.follow)
	}
	for _, s := range []string{"]", "[", "ctrl+b", "ctrl+f"} {
		send(m, s)
		if m.scroll != 21 {
			t.Fatal(s, m.scroll)
		}
	}
	send(m, "ctrl+u")
	if m.scroll != 0 {
		t.Fatal(m.scroll)
	}
}

func TestModalWheelUsesItsOwnViewport(t *testing.T) {
	for _, kind := range []string{"confirm", "conflict"} {
		t.Run(kind, func(t *testing.T) {
			m, _ := fixture(t)
			m.width, m.height = 80, 24
			m.scroll = 7
			m.follow = false
			jobs := []kata.Job{}
			for i := range 20 {
				jobs = append(jobs, m.job(row{job: i}))
			}
			if kind == "confirm" {
				m.confirm = jobs
			} else {
				m.conflict = &ConflictError{Pending: kata.Job{Name: "original"}, Newer: kata.Job{Name: "newer"}, Remaining: jobs, Operation: "prune"}
			}
			m.frameValid = false
			before := m.viewContent()
			m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
			if text := m.viewContent(); text == before || m.modalScroll == 0 {
				t.Fatal("modal did not scroll", text)
			}
			if m.scroll != 7 {
				t.Fatal("modal wheel moved the saved view", m.scroll)
			}
		})
	}
}

func TestUnavailableKataTabMatchesNumberShortcut(t *testing.T) {
	m, _ := fixture(t)
	m.opts.Service = nil
	m.frameValid = false
	m.viewContent()
	_, cmd := m.Update(tea.MouseClickMsg{X: 2, Y: 1, Button: tea.MouseLeft})
	if cmd != nil || m.notice.Text != "Kata unavailable" || m.currentView() != viewList {
		t.Fatal("unavailable click started Kata", m.notice.Text)
	}
}
func TestPruneResetsModalViewportAndPreservesList(t *testing.T) {
	m, _ := fixture(t)
	m.width, m.height = 180, 10
	m.finished = true
	m.data.Runs = nil
	for i := range m.data.Jobs {
		m.data.Jobs[i].Definition = []byte(`{"enabled":true,"trigger":{"kind":"once","at":"2020-01-01T00:00:00Z"},"action":{"kind":"execute","prompt":"` + strings.Repeat("long prose ", 100) + `"}}`)
		m.data.Runs = append(m.data.Runs, runner.Run{UID: m.data.Jobs[i].UID, Job: kata.Job{UID: m.data.Jobs[i].UID}, Phase: runner.Settled, Outcome: "succeeded", Occurrence: "2020-01-01T00:00:00Z"})
	}
	m.rebuildJobIndex()
	m.rebuild()
	m.scroll = 0
	m.follow = false
	m.modalScroll = 9
	m.frameValid = false
	m.viewContent()
	m.Update(press("P"))
	if !strings.Contains(m.viewContent(), "run history is kept") || m.scroll != 0 || m.modalScroll != 0 {
		t.Fatal("prune moved the saved list or retained modal offset", m.scroll, m.viewContent())
	}
}

func TestHeaderTabCycle(t *testing.T) {
	m, _ := fixture(t)
	t.Cleanup(m.stopKata)
	for _, tc := range []struct{ key, heading string }{
		{"4", "workflows"}, {"tab", "kata カタ"}, {"tab", "jobs"}, {"shift+tab", "kata カタ"}, {"shift+tab", "workflows"},
		{"2", "jobs"}, {"3", "runs"}, {"4", "workflows"}, {"5", "workflows"},
	} {
		m.Update(press(tc.key))
		tabs, selected := m.folderTabs()
		if m.kataActive {
			selected = 0
		}
		if tabs[selected] != tc.heading {
			t.Fatalf("after %s: selected %s, want %s", tc.key, tabs[selected], tc.heading)
		}
	}
}
