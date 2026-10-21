package tui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
)

type embeddedActions struct {
	*fakeActions
	issue, mode string
}

func (a *embeddedActions) KataCommand(issue string) (*exec.Cmd, error) {
	a.issue = issue
	cmd := exec.Command(os.Args[0], "-test.run=^TestKataPTYProcess$")
	mode := a.mode
	if mode == "" {
		mode = "normal"
	}
	cmd.Env = append(os.Environ(), "HERDR_KATA_PTY_TEST_MODE="+mode)
	return cmd, nil
}

// A real PTY peer exposes the host's input encoding and window dimensions.
func TestKataPTYProcess(t *testing.T) {
	mode := os.Getenv("HERDR_KATA_PTY_TEST_MODE")
	if mode == "" {
		return
	}
	if _, err := term.MakeRaw(os.Stdin.Fd()); err != nil {
		t.Fatal(err)
	}
	if mode == "colors" {
		fmt.Print("\x1b[2J\x1b[H\x1b[48;2;255;243;187m\x1b[38;2;73;56;0mKata child\x1b[0m\x1b[38;5;248m separator\x1b[0m\r\n")
	} else {
		fmt.Print("\x1b[2J\x1b[H\x1b[38;5;178mKata child\x1b[0m\r\n")
	}
	size := func() {
		h, w, err := pty.Getsize(os.Stdin)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("%d %d\r\n", h, w)
	}
	size()
	if mode == "mouse" {
		fmt.Print("\x1b[?1003h\x1b[?1006h")
	}
	if mode == "query" {
		fmt.Print("\x1b]11;?\a")
	}
	fmt.Print("ready\r\n")
	if mode == "cursor" {
		fmt.Print("\x1b[?25h\x1b[3;2H")
	}
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		switch string(buf[:n]) {
		case "q":
			return
		case "s":
			size()
		default:
			fmt.Printf("input:%x\r\n", buf[:n])
		}
	}
}

// The embedded child owns body input; the board keeps its tabs and full width.
func TestKataEmbeddedBodyInputResizeAndRestart(t *testing.T) {
	a := &embeddedActions{fakeActions: &fakeActions{}}
	m := newModel(Options{Root: t.TempDir(), Service: a})
	t.Cleanup(func() { _ = m.Close() })
	answerColors(t, m)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	_, start := m.Update(press("K"))
	if start == nil {
		t.Fatal("K did not start Kata")
	}
	_, read := m.Update(start())
	read = readUntil(t, m, read, "21 80")
	view := ansi.Strip(m.viewContent())
	if !strings.Contains(strings.Split(view, "\n")[1], "Kata") || !strings.Contains(view, "Kata child") {
		t.Fatal(view)
	}
	// The color from the child survives rendering inside the board.
	if !strings.Contains(m.viewContent(), "38;5;178") {
		t.Fatal("child color lost", m.viewContent())
	}
	m.Update(press("2")) // a number belongs to Kata while it is active
	read = readUntil(t, m, read, "input:32")
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(press("s"))
	read = readUntil(t, m, read, "27 100")
	// Ctrl+] belongs to the host, even inside a Kata text field.
	m.Update(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl})
	if strings.Contains(ansi.Strip(m.viewContent()), "Kata child") {
		t.Fatal("return key stayed in Kata")
	}
	_, reopen := m.Update(press("K"))
	if reopen != nil {
		m.Update(reopen())
	}
	if !strings.Contains(ansi.Strip(m.viewContent()), "Kata child") {
		t.Fatal("tab did not preserve child")
	}
	m.Update(press("q"))
	for read != nil {
		_, read = m.Update(nextMessage(t, read))
	}
	if strings.Contains(ansi.Strip(m.viewContent()), "Kata child") {
		t.Fatal("child exit did not return to JOBS")
	}
	_, start = m.Update(press("K"))
	_, read = m.Update(start())
	readUntil(t, m, read, "Kata child")
}

// answerColors replies to the board's terminal color queries as a dark
// terminal would, so Kata's output can reach the emulator.
func answerColors(t *testing.T, m *model) {
	t.Helper()
	preserveTheme(t)
	m.Update(tea.ForegroundColorMsg{Color: color.White})
	m.Update(tea.BackgroundColorMsg{Color: color.Black})
}

func nextMessage(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case msg := <-result:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for PTY output")
		return nil
	}
}
func readUntil(t *testing.T, m *model, read tea.Cmd, want string) tea.Cmd {
	t.Helper()
	for i := 0; !strings.Contains(ansi.Strip(m.viewContent()), want); i++ {
		if i > 100 || read == nil {
			t.Fatalf("missing %q:\n%s", want, m.viewContent())
		}
		_, read = m.Update(nextMessage(t, read))
	}
	return read
}

func TestKataCloseCancelsPendingStart(t *testing.T) {
	// A command that finishes starting after the board closes must be reaped.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := exec.Command("sh", "-c", "sleep 30")
	s, err := startKataTerminal(ctx, cmd, 80, 20)
	if err == nil {
		_ = s.Close()
		t.Fatal("started a child after close")
	}
}

func TestKataMouseCoordinatesBelongToBody(t *testing.T) {
	a := &embeddedActions{fakeActions: &fakeActions{}, mode: "mouse"}
	m := newModel(Options{Root: t.TempDir(), Service: a})
	t.Cleanup(func() { _ = m.Close() })
	answerColors(t, m)
	_, cmd := m.Update(press("K"))
	_, read := m.Update(cmd())
	read = readUntil(t, m, read, "ready")
	m.Update(tea.MouseClickMsg{X: 7, Y: 3, Button: tea.MouseLeft})
	read = readUntil(t, m, read, "input:1b5b3c303b383b314d")
	m.Update(tea.MouseReleaseMsg{X: 7, Y: 3, Button: tea.MouseLeft})
	readUntil(t, m, read, "input:1b5b3c303b383b316d")
	m.Update(tea.MouseClickMsg{X: 15, Y: 1, Button: tea.MouseLeft})
	if strings.Contains(ansi.Strip(m.viewContent()), "Kata child") {
		t.Fatal(m.viewContent())
	}
}

func TestKataNavigationAndPasteReachChild(t *testing.T) {
	a := &embeddedActions{fakeActions: &fakeActions{}}
	m := newModel(Options{Root: t.TempDir(), Service: a})
	t.Cleanup(func() { _ = m.Close() })
	answerColors(t, m)
	_, cmd := m.Update(press("K"))
	_, read := m.Update(cmd())
	read = readUntil(t, m, read, "ready")
	for _, tc := range []struct {
		key  tea.Msg
		wire string
	}{
		{tea.KeyPressMsg{Code: tea.KeyUp}, "input:1b5b41"},
		{tea.KeyPressMsg{Code: tea.KeyHome, Mod: tea.ModCtrl}, "input:1b5b313b3548"},
		{tea.KeyPressMsg{Code: tea.KeyF1}, "input:1b4f50"},
		{tea.PasteMsg{Content: "q2"}, "input:7132"},
	} {
		m.Update(tc.key)
		read = readUntil(t, m, read, tc.wire)
	}
}

func TestKataBackgroundQueryMatchesOuterTerminal(t *testing.T) {
	a := &embeddedActions{fakeActions: &fakeActions{}, mode: "query"}
	m := newModel(Options{Root: t.TempDir(), Service: a})
	t.Cleanup(func() { _ = m.Close() })
	preserveTheme(t)
	m.Update(tea.ForegroundColorMsg{Color: color.Black})
	m.Update(tea.BackgroundColorMsg{Color: color.White})
	_, cmd := m.Update(press("K"))
	_, read := m.Update(cmd())
	// Literal OSC 11 reply for a white outer terminal; a default-black emulator
	// would make Kata select its dark palette even in the same light terminal.
	readUntil(t, m, read, "input:1b5d31313b7267623a666666662f666666662f6666666607")
}

func TestKataTextCursorIsVisible(t *testing.T) {
	a := &embeddedActions{fakeActions: &fakeActions{}, mode: "cursor"}
	m := newModel(Options{Root: t.TempDir(), Service: a})
	t.Cleanup(func() { _ = m.Close() })
	answerColors(t, m)
	_, cmd := m.Update(press("K"))
	_, read := m.Update(cmd())
	for !strings.Contains(m.viewContent(), "\x1b[7m") {
		if read == nil {
			t.Fatal(m.viewContent())
		}
		_, read = m.Update(nextMessage(t, read))
	}
}

// This peer runs the actual outer renderer, where the board previously
// downsampled child cells despite the VT buffer retaining their colors.
func TestBoardPTYProcess(t *testing.T) {
	if os.Getenv("HERDR_KATA_BOARD_PTY_TEST") != "1" {
		return
	}
	a := &embeddedActions{fakeActions: &fakeActions{}, mode: "colors"}
	if err := Run(context.Background(), Options{Root: os.Getenv("HERDR_PLUGIN_STATE_DIR"), Service: a, OpenKata: true, Stdout: os.Stdout}); err != nil {
		t.Fatal(err)
	}
}

func TestEmbeddedChildColorsSurviveOuterRenderer(t *testing.T) {
	for _, display := range [][]string{{"KATA_COLOR_MODE=none", "CLICOLOR="}, {"KATA_COLOR_MODE=dark", "CLICOLOR=0"}} {
		t.Run(strings.Join(display, "/"), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			cmd := exec.Command(os.Args[0], "-test.run=^TestBoardPTYProcess$")
			cmd.Env = append(os.Environ(), "HERDR_KATA_BOARD_PTY_TEST=1", "HERDR_PLUGIN_STATE_DIR="+t.TempDir(), "TERM=xterm-256color", "NO_COLOR=", display[0], display[1])
			s, err := startKataTerminal(ctx, cmd, 100, 30)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				// Leave Kata, then quit the board so its deferred close reaps the child.
				s.screen.SendText("\x1dq")
				select {
				case <-s.done:
				case <-time.After(5 * time.Second):
					t.Error("outer board did not exit")
				}
				_ = s.Close()
			})
			for i := 0; !strings.Contains(ansi.Strip(s.render()), "ready"); i++ {
				if i > 100 {
					t.Fatal("outer renderer did not paint child", s.render())
				}
				msg := nextMessage(t, s.read()).(kataOutputMsg)
				_, _ = s.screen.Write(msg.data)
				if msg.err != nil {
					t.Fatal(msg.err, s.render())
				}
			}
			text := s.render()
			for _, color := range []string{"38;2;73;56;0", "48;2;255;243;187", "38;5;248"} {
				if !strings.Contains(text, color) {
					t.Errorf("child color %q changed by outer renderer:\n%q", color, text)
				}
			}
		})
	}
}

func TestKataStaleStartClosesOnlyItsTerminal(t *testing.T) {
	a := &embeddedActions{fakeActions: &fakeActions{}}
	m := newModel(Options{Root: t.TempDir(), Service: a})
	t.Cleanup(func() { _ = m.Close() })
	answerColors(t, m)
	_, start := m.Update(press("K"))
	_, read := m.Update(start())
	readUntil(t, m, read, "ready")
	current := m.kata.terminal
	orphan, err := startKataTerminal(context.Background(), exec.Command("sh", "-c", "sleep 30"), 80, 20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = orphan.Close() })
	_, cmd := m.Update(kataStartedMsg{session: &kataSession{project: m.kata.project}, terminal: orphan})
	if cmd != nil || !strings.Contains(ansi.Strip(m.viewContent()), "Kata child") {
		t.Fatal("stale start replaced current child", m.viewContent())
	}
	if _, err := orphan.file.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
		t.Fatal("stale child was not closed", err)
	}
	if err := current.resize(80, 20); err != nil {
		t.Fatal("current child was closed", err)
	}
}

func TestKataRestartWaitsForColorsAfterExit(t *testing.T) {
	a := &embeddedActions{fakeActions: &fakeActions{}}
	m := newModel(Options{Root: t.TempDir(), Service: a})
	t.Cleanup(func() { _ = m.Close() })
	answerColors(t, m)
	_, start := m.Update(press("K"))
	_, read := m.Update(start())
	read = readUntil(t, m, read, "ready")
	m.Update(press("q"))
	for read != nil {
		_, read = m.Update(nextMessage(t, read))
	}
	m.terminalForeground, m.terminalBackground = nil, nil
	m.terminalColorsLate = false
	_, start = m.Update(press("K"))
	_, read = m.Update(start())
	if read != nil {
		t.Fatal("restart read output before terminal colors")
	}
	_, read = m.Update(tea.ForegroundColorMsg{Color: color.White})
	if read != nil {
		t.Fatal("restart read output before background color")
	}
	_, read = m.Update(tea.BackgroundColorMsg{Color: color.Black})
	if read == nil {
		t.Fatal("restart retained the previous read latch")
	}
	readUntil(t, m, read, "ready")
}

func TestKataModelCloseCancelsPendingStart(t *testing.T) {
	a := &embeddedActions{fakeActions: &fakeActions{}}
	m := newModel(Options{Root: t.TempDir(), Service: a})
	start := m.openKata(m.opts.Lane)
	if start == nil {
		t.Fatal("Kata start not prepared")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	msg := start().(kataStartedMsg)
	if msg.terminal != nil {
		t.Cleanup(func() { _ = msg.terminal.Close() })
	}
	if msg.terminal != nil || !errors.Is(msg.err, context.Canceled) {
		t.Fatal("closed board started its pending child", msg.err)
	}
	if cmd := m.acceptKataStart(msg); cmd != nil {
		t.Fatal("closed session accepted a late start")
	}
}
