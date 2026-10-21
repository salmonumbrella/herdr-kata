package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestMouseSelectOpenAndInspector(t *testing.T) {
	m, f := fixture(t)
	m.width, m.height = 150, 40
	uncachedView(m)
	click := func(x, y int) {
		_, cmd := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
		if cmd != nil {
			m.Update(cmd())
		}
	}
	click(2, 6)
	if m.cursor != 1 || m.currentView() != viewList {
		t.Fatal(m.cursor, m.currentView())
	}
	uncachedView(m)
	click(2, 6)
	if m.currentView() != viewJob || m.nav[len(m.nav)-1].uid != "j01" || len(f.calls) != 0 {
		t.Fatal(m.nav, f.calls)
	}
	send(m, "esc")
	uncachedView(m)
	click(145, 6)
	if m.currentView() != viewList {
		t.Fatal("inspector clicked")
	}
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.cursor != 4 {
		t.Fatal(m.cursor)
	}
	send(m, "M")
	click(2, 6)
	if m.cursor != 4 {
		t.Fatal("released mouse moved")
	}
}

func TestFooterMouseDoesNotSelectHiddenRow(t *testing.T) {
	m, _ := fixture(t)
	uncachedView(m)
	cursor := m.cursor
	m.Update(tea.MouseClickMsg{X: 2, Y: m.height - 2, Button: tea.MouseLeft})
	if m.cursor != cursor || m.currentView() != viewList {
		t.Fatal("footer selected a record")
	}
}
