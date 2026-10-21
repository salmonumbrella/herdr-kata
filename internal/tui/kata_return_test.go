package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// D13: a visit to Kata must preserve both the visible frame and the path back.
func TestKataRestoresBoardVisit(t *testing.T) {
	for _, entry := range []string{"1-list", "K-workflow", "K-linked-run", "i-linked-run"} {
		for _, exit := range []string{"reserved-key", "child-quit", "same-tab"} {
			t.Run(entry+"/"+exit, func(t *testing.T) {
				m, _ := fixture(t)
				m.opts.Service = &embeddedActions{fakeActions: &fakeActions{}}
				m.now = func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) }
				m.width, m.height = 120, 24
				t.Cleanup(func() { _ = m.Close() })
				answerColors(t, m)
				m.query = "Job"
				m.cursor = 3
				m.scroll = 1
				m.follow = false
				m.Update(dataMsg{data: m.data})
				var parent string
				switch entry {
				case "K-workflow":
					m.selectTab(2)
					m.data.Workflows[0].Definition = json.RawMessage(`{"version":1,"about":"` + strings.Repeat("Workflow context. ", 80) + `","steps":[{"key":"check","kind":"command","command":"true"}]}`)
					m.Update(dataMsg{data: m.data})
					parent = m.viewContent()
					m.pushDetail(viewWorkflow, "", "w1")
					m.scroll = 3
					m.follow = false
				case "K-linked-run", "i-linked-run":
					m.pushDetail(viewJob, "", "j00")
					m.frameValid = false
					parent = m.viewContent()
					m.pushDetail(viewRun, "", "r1")
					m.artifactLoaded = true
					m.artifactVisit = m.activeRunKey()
					m.scroll = 2
					m.follow = false
				}
				m.frameValid = false
				before := m.viewContent()
				entryKey := "K"
				if entry == "1-list" {
					entryKey = "1"
				}
				if entry == "i-linked-run" {
					entryKey = "i"
				}
				_, start := m.Update(press(entryKey))
				if start == nil {
					t.Fatal("Kata start missing", m.viewContent())
				}
				_, read := m.Update(start())
				read = readUntil(t, m, read, "ready")
				switch exit {
				case "reserved-key":
					m.Update(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl})
				case "child-quit":
					m.Update(press("q"))
					for i := 0; m.kataActive; i++ {
						if read == nil || i > 30 {
							t.Fatal("Kata did not exit", m.viewContent())
						}
						_, read = m.Update(nextMessage(t, read))
					}
				case "same-tab":
					for _, tab := range m.hits.Tabs {
						if tab.Index == m.tab+1 {
							m.Update(tea.MouseClickMsg{X: tab.X0, Y: m.hits.TabY, Button: tea.MouseLeft})
							break
						}
					}
				}
				if got := m.viewContent(); got != before {
					t.Fatalf("Kata discarded the board frame\nbefore:\n%s\nafter:\n%s", before, got)
				}
				if parent != "" {
					m.Update(press("h"))
					if got := m.viewContent(); got != parent {
						t.Fatalf("Kata discarded the back stack\nwant:\n%s\ngot:\n%s", parent, got)
					}
				}
			})
		}
	}
}

func TestKataDifferentTabClickSelectsRequestedList(t *testing.T) {
	m, _ := fixture(t)
	m.opts.Service = &embeddedActions{fakeActions: &fakeActions{}}
	t.Cleanup(func() { _ = m.Close() })
	answerColors(t, m)
	m.selectTab(2)
	m.pushDetail(viewWorkflow, "", "w1")
	_, start := m.Update(press("K"))
	_, read := m.Update(start())
	readUntil(t, m, read, "ready")
	for _, tab := range m.hits.Tabs {
		if tab.Index == 2 {
			m.Update(tea.MouseClickMsg{X: tab.X0, Y: m.hits.TabY, Button: tea.MouseLeft})
			break
		}
	}
	if m.currentView() != viewList || m.tab != 1 || !strings.Contains(m.viewContent(), "Job 00") {
		t.Fatal("tab click failed to select Runs", m.viewContent())
	}
}

func TestKataOtherTabClickPreservesOriginatingList(t *testing.T) {
	m, _ := fixture(t)
	m.opts.Service = &embeddedActions{fakeActions: &fakeActions{}}
	t.Cleanup(func() { _ = m.Close() })
	answerColors(t, m)
	pinLegacyActions(m)
	m.query = "Job"
	m.cursor, m.scroll, m.follow = 3, 0, false
	m.expanded["kept"] = true
	m.pushDetail(viewJob, "p", "j03")
	m.scroll, m.follow = 7, true
	m.expanded = map[string]bool{}
	_, start := m.Update(press("K"))
	if start == nil {
		t.Fatal("Kata start refused", m.notice.Text)
	}
	_, read := m.Update(start())
	readUntil(t, m, read, "ready")
	for _, tab := range m.hits.Tabs {
		if tab.Index == 2 {
			m.Update(tea.MouseClickMsg{X: tab.X0, Y: m.hits.TabY, Button: tea.MouseLeft})
			break
		}
	}
	m.Update(press("2"))
	selected, ok := m.selected()
	if !ok || selected.uid != "j03" || m.query != "Job" || m.scroll != 0 || m.follow || !m.expanded["kept"] {
		t.Fatalf("Kata tab click lost originating list: selection=%#v query=%q scroll=%d follow=%v expanded=%v", selected, m.query, m.scroll, m.follow, m.expanded)
	}
}

// Reusing the running child must save the new visit, rather than restoring
// the list from which that child was first started.
func TestKataReusedChildRestoresNewVisit(t *testing.T) {
	m, _ := fixture(t)
	m.opts.Service = &embeddedActions{fakeActions: &fakeActions{}}
	t.Cleanup(func() { _ = m.Close() })
	answerColors(t, m)
	pinLegacyActions(m)
	_, start := m.Update(press("K"))
	if start == nil {
		t.Fatal("Kata start refused", m.viewContent())
	}
	_, read := m.Update(start())
	readUntil(t, m, read, "ready")
	m.Update(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl})
	m.Update(press("3"))
	parent := m.viewContent()
	m.Update(press("l"))
	before := m.viewContent()
	_, restart := m.Update(press("K"))
	if restart != nil {
		t.Fatal("returning to Kata restarted its child")
	}
	if !strings.Contains(m.viewContent(), "Kata child") {
		t.Fatal("running child was not restored", m.viewContent())
	}
	m.Update(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl})
	if got := m.viewContent(); got != before {
		t.Fatalf("reused child lost the new detail frame\nwant:\n%s\ngot:\n%s", before, got)
	}
	m.Update(press("h"))
	if got := m.viewContent(); got != parent {
		t.Fatalf("reused child lost the new back stack\nwant:\n%s\ngot:\n%s", parent, got)
	}
}

func TestKataTabCyclePreservesOriginatingList(t *testing.T) {
	for _, key := range []string{"tab", "shift+tab"} {
		t.Run(key, func(t *testing.T) {
			m, _ := fixture(t)
			m.opts.Service = &embeddedActions{fakeActions: &fakeActions{}}
			t.Cleanup(func() { _ = m.Close() })
			answerColors(t, m)
			pinLegacyActions(m)
			m.Update(press("/"))
			m.Update(press("Job"))
			m.Update(press("enter"))
			m.Update(press("j"))
			m.Update(press("j"))
			before := m.viewContent()
			m.Update(press("l"))
			_, start := m.Update(press("K"))
			if start == nil {
				t.Fatal("Kata start refused", m.viewContent())
			}
			_, read := m.Update(start())
			readUntil(t, m, read, "ready")
			m.Update(press(key))
			if m.kataActive || m.currentView() != viewList {
				t.Fatal("tab cycle did not select a local list", m.viewContent())
			}
			m.Update(press("2"))
			if got := m.viewContent(); got != before {
				t.Fatalf("tab cycle lost the originating list\nwant:\n%s\ngot:\n%s", before, got)
			}
		})
	}
}
