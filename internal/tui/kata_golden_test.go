package tui

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestKataGoldenFrame(t *testing.T) {
	assertModeFrames(t, "kata", "embedded", [][2]int{{80, 24}, {120, 40}}, func(mode colorMode, w, h int) string {
		m := goldenFixture("chrome", w, h)
		m.kataActive = true
		kw, kh := m.kataSize()
		screen := vt.NewEmulator(kw, kh)
		child := "\x1b[?25l\x1b[48;2;255;243;187m\x1b[38;2;73;56;0mConnected · Project: example-project\x1b[0m\r\n open: 1\r\n\x1b[48;5;24m▶ Example task\x1b[0m"
		if mode == colorNone {
			child = "\x1b[?25l" + ansi.Strip(child)
		}
		_, _ = screen.Write([]byte(child))
		m.kata = &kataSession{terminal: &kataTerminal{screen: screen}}
		return m.viewContent()
	})
}
