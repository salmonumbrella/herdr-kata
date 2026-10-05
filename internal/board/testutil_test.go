package board

import (
	tea "github.com/charmbracelet/bubbletea"
	"testing"
)

// Typed input ignores cursor-blink commands, which do not affect form data.
func (m *Model) sendKey(msg tea.KeyMsg) { m.Update(msg) }
func (m *Model) typeText(t *testing.T, s string) {
	t.Helper()
	for _, r := range s {
		m.sendKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}
