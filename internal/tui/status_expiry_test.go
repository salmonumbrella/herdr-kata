package tui

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"
)

func TestStatusClearsOnNavigation(t *testing.T) {
	for _, navigate := range []string{"tab", "detail", "back", "mouse-tab"} {
		t.Run(navigate, func(t *testing.T) {
			m, _ := fixture(t)
			if navigate == "back" {
				m.pushDetail(viewJob, "", "j00")
			}
			m.Update(actionMsg{status: "parent binding closed"})
			if !strings.Contains(m.viewContent(), "parent binding closed") {
				t.Fatal("status not shown")
			}
			switch navigate {
			case "tab":
				m.Update(press("3"))
			case "detail":
				m.Update(press("l"))
			case "back":
				m.Update(press("h"))
			case "mouse-tab":
				for _, tab := range m.hits.Tabs {
					if tab.Index == 2 {
						m.Update(tea.MouseClickMsg{X: tab.X0, Y: m.hits.TabY, Button: tea.MouseLeft})
						break
					}
				}
			}
			if strings.Contains(m.viewContent(), "parent binding closed") {
				t.Fatal("status followed navigation", m.viewContent())
			}
		})
	}
}

func TestStatusExpiresWithoutClearingPersistentHealth(t *testing.T) {
	m, _ := fixture(t)
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.data.Route = map[string]any{"allowed": false, "error": "route unavailable"}
	m.Update(actionMsg{status: "parent binding closed"})
	now = now.Add(4 * time.Second)
	m.Update(tickMsg(now))
	if !strings.Contains(m.viewContent(), "parent binding closed") {
		t.Fatal("status expired too soon")
	}
	m.Update(actionMsg{status: "newer status"})
	now = now.Add(2 * time.Second)
	m.Update(tickMsg(now))
	if !strings.Contains(m.viewContent(), "newer status") {
		t.Fatal("old expiry cleared newer status")
	}
	now = now.Add(4 * time.Second)
	m.Update(tickMsg(now))
	if strings.Contains(m.viewContent(), "newer status") || !strings.Contains(m.viewContent(), "route unavailable") {
		t.Fatal("transient and persistent messages expired together", m.viewContent())
	}
}

func TestDeletedJobDoesNotInheritKataSuggestion(t *testing.T) {
	m := jobDetailFixture(t, `{"enabled":true,"action":{"kind":"execute","prompt":"work"}}`)
	if !strings.Contains(m.viewContent(), "no local runs · K opens Kata") {
		t.Fatal("missing initial hint")
	}
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	data := m.data
	data.Jobs = slices.Clone(data.Jobs)
	data.Jobs[0].DeletedAt = &now
	m.Update(dataMsg{data: data})
	if !strings.Contains(m.viewContent(), "deleted in Kata") || strings.Contains(m.viewContent(), "K opens Kata") {
		t.Fatal("deleted frame suggests unavailable action", m.viewContent())
	}
}

func TestStatusMouseNoticeReplacesErrorKind(t *testing.T) {
	m, _ := fixture(t)
	m.Update(actionMsg{err: errors.New("action failed")})
	m.mouseOff = true
	m.Update(press("M"))
	got := m.viewContent()
	if !strings.Contains(got, "mouse on") || strings.Contains(got, "error: mouse on") {
		t.Fatal("mouse notice inherited the error kind", got)
	}
}

func TestStatusRepeatedErrorRestartsLifetime(t *testing.T) {
	m, _ := fixture(t)
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.Update(actionMsg{err: errors.New("action failed")})
	now = now.Add(4 * time.Second)
	m.Update(actionMsg{err: errors.New("action failed")})
	now = now.Add(4 * time.Second)
	m.Update(tickMsg(now))
	if !strings.Contains(m.viewContent(), "error: action failed") {
		t.Fatal("repeated notice expired against the first write", m.viewContent())
	}
	now = now.Add(2 * time.Second)
	m.Update(tickMsg(now))
	if strings.Contains(m.viewContent(), "action failed") {
		t.Fatal("repeated notice never expired", m.viewContent())
	}
}

func TestStatusResizeFailureReplacesSuccessKind(t *testing.T) {
	m, _ := fixture(t)
	file, err := os.CreateTemp(t.TempDir(), "pty")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	screen := vt.NewEmulator(80, 24)
	t.Cleanup(func() { _ = screen.Close() })
	m.kata = &kataSession{terminal: &kataTerminal{file: file, screen: screen}}
	m.Update(actionMsg{status: "saved"})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if !strings.Contains(m.viewContent(), "error: ") || strings.Contains(m.viewContent(), "saved") {
		t.Fatal("resize error inherited the success kind", m.viewContent())
	}
}
