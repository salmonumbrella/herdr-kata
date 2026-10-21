package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestMarqueeScheduling(t *testing.T) {
	m, _ := fixture(t)
	m.data.Jobs[1].Name = "Job 01 " + strings.Repeat("overflow ", 20)
	m.rebuildJobIndex()
	m.rebuild()
	_, cmd := m.Update(struct{}{})
	if cmd == nil || !m.marqueePending {
		t.Fatal("unselected overflow did not schedule")
	}
	g := m.marqueeGeneration
	_, cmd = m.Update(struct{}{})
	if cmd != nil {
		t.Fatal("second timer")
	}
	m.Update(marqueeTickMsg{generation: g})
	if m.frame != 1 || !m.marqueePending {
		t.Fatal(m.frame, m.marqueePending)
	}
	before := m.frame
	m.Update(press("j"))
	if m.frame != before {
		t.Fatal("selection reset counter")
	}
}
func TestMarqueeStaleGeneration(t *testing.T) {
	m, _ := fixture(t)
	m.data.Jobs[1].Name = "Job 01 " + strings.Repeat("overflow ", 20)
	m.rebuildJobIndex()
	m.rebuild()
	m.Update(struct{}{})
	g := m.marqueeGeneration
	m.Update(press("l"))
	if m.marqueePending {
		t.Fatal("detail timer")
	}
	m.Update(marqueeTickMsg{generation: g})
	if m.frame != 0 {
		t.Fatal("stale tick advanced")
	}
	m.Update(press("h"))
	if !m.marqueePending {
		t.Fatal("did not restart")
	}
	if m.frame != 0 {
		t.Fatal("restart changed frame")
	}
}
func TestMarqueeStopsForHiddenCell(t *testing.T) {
	m, _ := fixture(t)
	m.data.Jobs = m.data.Jobs[:1]
	m.data.Runs = nil
	m.data.Jobs[0].Name = "short"
	m.rebuildJobIndex()
	m.rebuild()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 4})
	if m.marqueePending || m.rendered.Animates {
		t.Fatal("clipped rows animated")
	}
	m.Update(tea.WindowSizeMsg{Width: 180, Height: 40})
	m.frameValid = false
	m.opts.Config.Checkouts = map[string]string{"work": strings.Repeat("/example", 20)}
	m.data.Jobs[0].Definition = []byte(`{"action":{"kind":"execute","prompt":"work"},"checkout_key":"work"}`)
	m.rebuildJobIndex()
	m.rebuild()
	m.Update(struct{}{})
	if !m.marqueePending {
		t.Fatal("inspector-only overflow")
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 4})
	if m.marqueePending {
		t.Fatal("clipped inspector animated")
	}
}
func TestMarqueeDoesNotPollOrAct(t *testing.T) {
	m, f := fixture(t)
	m.data.Jobs[1].Name = "Job 01 " + strings.Repeat("overflow ", 20)
	m.rebuildJobIndex()
	m.rebuild()
	m.Update(struct{}{})
	for range 50 {
		m.Update(marqueeTickMsg{generation: m.marqueeGeneration})
	}
	if len(f.calls) != 0 || m.polling || m.frame != 50 {
		t.Fatal(f.calls, m.polling)
	}
}
