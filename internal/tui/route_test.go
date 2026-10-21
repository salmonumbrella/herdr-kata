package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestKeys(t *testing.T) {
	cases := []struct {
		keys  []string
		check func(*testing.T, *model, *fakeActions)
	}{
		{[]string{"3", "4", "5", "2", "tab", "shift+tab"}, func(t *testing.T, m *model, f *fakeActions) {
			if m.tab != 0 {
				t.Fatal(m.tab)
			}
		}},
		{[]string{"j", "k"}, func(t *testing.T, m *model, f *fakeActions) {
			if m.cursor != 0 {
				t.Fatal(m.cursor)
			}
		}},
		{[]string{"l"}, func(t *testing.T, m *model, f *fakeActions) {
			if m.currentView() != viewJob || len(f.calls) != 0 {
				t.Fatal(m.currentView(), f.calls)
			}
		}},
		{[]string{"enter", "h"}, func(t *testing.T, m *model, f *fakeActions) {
			if m.currentView() != viewList {
				t.Fatal(m.currentView())
			}
		}},
		{[]string{"R", "p"}, func(t *testing.T, m *model, f *fakeActions) {
			if strings.Join(f.calls, ",") != "run:j00,pause:j00" {
				t.Fatal(f.calls)
			}
		}},
		{[]string{"f"}, func(t *testing.T, m *model, f *fakeActions) {
			if !m.favorites["j00"] {
				t.Fatal("favorite")
			}
		}},
		{[]string{"F"}, func(t *testing.T, m *model, f *fakeActions) {
			if !m.finished {
				t.Fatal("finished")
			}
		}},
		{[]string{"/", "4", "q", "esc"}, func(t *testing.T, m *model, f *fakeActions) {
			if m.query != "4q" || m.tab != 0 {
				t.Fatal("search swallowed keys")
			}
		}},
		{[]string{"]", "["}, func(t *testing.T, m *model, f *fakeActions) {
			if m.cursor != 0 {
				t.Fatal(m.cursor)
			}
		}},
		{[]string{"M", "M"}, func(t *testing.T, m *model, f *fakeActions) {
			if m.mouseOff {
				t.Fatal("mouse")
			}
		}},
		{[]string{"D", "y"}, func(t *testing.T, m *model, f *fakeActions) {
			if strings.Join(f.calls, ",") != "delete:j00" {
				t.Fatal(f.calls)
			}
		}},
		{[]string{"3", "a", "i", "enter", "K"}, func(t *testing.T, m *model, f *fakeActions) {
			if strings.Join(f.calls, ",") != "focus:r1,issue:issue,kata" {
				t.Fatal(f.calls)
			}
		}},
		{[]string{"3", " "}, func(t *testing.T, m *model, f *fakeActions) {
			if !m.expanded["r1"] || !strings.Contains(uncachedView(m), "build") {
				t.Fatal("steps")
			}
		}},
		{[]string{"n"}, func(t *testing.T, m *model, f *fakeActions) {
			if m.notice.Text != "no project accepts new jobs" {
				t.Fatal(m.notice.Text)
			}
		}},
		{[]string{"e"}, func(t *testing.T, m *model, f *fakeActions) {
			if !strings.Contains(m.notice.Text, "editor") {
				t.Fatal(m.notice.Text)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.keys, "_"), func(t *testing.T) {
			m, f := fixture(t)
			if len(tc.keys) == 1 && (tc.keys[0] == "e" || tc.keys[0] == "n") {
				m.opts.Service = nil
			}
			if strings.Join(tc.keys, " ") == "R p" || strings.Join(tc.keys, " ") == "D y" {
				pinLegacyActions(m)
			}
			for _, s := range tc.keys {
				send(m, s)
			}
			tc.check(t, m, f)
		})
	}
	m, _ := fixture(t)
	_, cmd := m.Update(press("q"))
	if cmd == nil {
		t.Fatal("quit missing")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("quit")
	}
}

func TestRealSpaceKeyInSearch(t *testing.T) {
	m, _ := fixture(t)
	send(m, "/")
	send(m, "Job")
	m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	send(m, "01")
	if m.query != "Job 01" || len(m.rows) != 1 {
		t.Fatal(m.query, len(m.rows))
	}
}

func TestSearchOwnsTabClickAndCtrlCQuits(t *testing.T) {
	m, _ := fixture(t)
	send(m, "/")
	send(m, "Job")
	m.Update(tea.MouseClickMsg{X: 10, Y: 1, Button: tea.MouseLeft})
	if !m.searching || m.tab != 0 || m.query != "Job" {
		t.Fatal("tab click left search focused", m.searching, m.tab, m.query)
	}
	send(m, "/")
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("search ignored Ctrl+C")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("did not quit")
	}
}

func TestBusyActionGivesFeedbackAndAcceptsNextAfterCompletion(t *testing.T) {
	m, f := fixture(t)
	pinLegacyActions(m)
	_, pending := m.Update(press("p"))
	_, blocked := m.Update(press("K"))
	if pending == nil || blocked != nil || !strings.Contains(uncachedView(m), "Working") {
		t.Fatal("busy input silently dropped", uncachedView(m))
	}
	m.Update(pending())
	send(m, "K")
	if strings.Join(f.calls, ",") != "pause:j00,kata" {
		t.Fatal(f.calls)
	}
}

func TestAppliedFilterShowsActionStatus(t *testing.T) {
	m, _ := fixture(t)
	send(m, "/")
	send(m, "J")
	send(m, "enter")
	m.notice.Text = "route refused: boom"
	if !strings.Contains(uncachedView(m), "route refused") {
		t.Fatal("status hidden by active filter")
	}
}
