package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestWideListsUseBorderedPassiveInspector(t *testing.T) {
	for _, tab := range []int{0, 2} {
		m, _ := fixture(t)
		m.selectTab(tab)
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 36})
		frame := m.renderFrame()
		text := ansi.Strip(frame.Text)
		if !strings.Contains(text, "┌") || !strings.Contains(text, "┐┌") {
			t.Fatal("wide list did not split", text)
		}
		for _, hit := range frame.Hits.Rows {
			if hit.X0 < 1 || hit.X1 >= 140 {
				t.Fatal("row hit includes border or inspector", hit)
			}
		}
		m.height = 35
		m.frameValid = false
		if text := ansi.Strip(m.viewContent()); strings.Contains(text, "┌") {
			t.Fatal("short list kept split panes", text)
		}
	}
}

func TestPromptMarkdownUsesKataRendering(t *testing.T) {
	preserveTheme(t)
	applyColorMode(colorDark, true)
	text := strings.Join(renderMarkdownLines("**Review** the `example`", 40), "\n")
	if strings.Contains(ansi.Strip(text), "**") || !strings.Contains(ansi.Strip(text), "Review") || !strings.Contains(text, "\x1b[") {
		t.Fatal(text)
	}
	applyColorMode(colorNone, false)
	if text := strings.Join(renderMarkdownLines("**Review**", 40), "\n"); text != "Review" {
		t.Fatal("colorless markdown", text)
	}
}
