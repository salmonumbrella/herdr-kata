package board

import (
	"context"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"strings"
	"testing"
	"time"
)

func TestBoardRetainsExecutionTabsAndShowsLeases(t *testing.T) {
	s, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	m := New(s, nil, Deps{})
	_, e = s.AcquireLease(context.Background(), store.LeaseRequest{Scope: "local:example", Resource: "browser", By: store.Identity{Name: "worker", RunID: "run-a"}, TTL: time.Hour})
	if e != nil {
		t.Fatal(e)
	}
	m.Update(m.load()())
	m.width = 100
	m.height = 30
	for i, label := range []string{"JOBS", "RUNS", "WORKFLOWS", "LEASES"} {
		m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{rune('1' + i)}})
		view := m.View()
		if !strings.Contains(view, label) {
			t.Errorf("missing tab %s", label)
		}
		for _, removed := range []string{"THREADS", "FORUM", "MEMORY"} {
			if strings.Contains(view, removed) {
				t.Errorf("legacy tab %s", removed)
			}
		}
	}
	view := m.View()
	for _, want := range []string{"local:example", "browser", "worker", "run-a"} {
		if !strings.Contains(view, want) {
			t.Errorf("lease view missing %s: %s", want, view)
		}
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if m.runDetail != nil {
		t.Fatal("lease navigation opened unrelated run")
	}
}

func TestLeasesKeyboardScrollsWithoutSelection(t *testing.T) {
	m := newTestModel(t)
	m.selectTab(focusLeases)
	m.height = 20
	seedLongLeases(t, m, 60)
	m.View()
	m.press(t, "j")
	m.View()
	if m.scroll != 1 || m.cursor != 0 {
		t.Fatalf("scroll=%d cursor=%d, want scroll 1 without selection", m.scroll, m.cursor)
	}
	m.press(t, "k")
	m.View()
	if m.scroll != 0 {
		t.Fatalf("scroll=%d after up", m.scroll)
	}
	m.query = "resource 59"
	out := m.View()
	if !strings.Contains(out, "resource 59") || strings.Contains(out, "resource 58") {
		t.Fatalf("lease filter did not narrow visible resources: %s", out)
	}
}
