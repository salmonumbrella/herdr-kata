package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// KataCommander prepares a pinned project command without starting it.
type kataCommander interface {
	KataCommand(issue string) (*exec.Cmd, error)
}

type kataTerminal struct {
	file      *os.File
	screen    *vt.Emulator // Only the Bubble Tea update loop accesses the screen.
	cmd       *exec.Cmd
	done      chan struct{}
	closeOnce sync.Once
	err       error
}
type kataStartedMsg struct {
	terminal *kataTerminal
	session  *kataSession
	err      error
}
type kataOutputMsg struct {
	terminal *kataTerminal
	data     []byte
	err      error
}

func startKataTerminal(ctx context.Context, cmd *exec.Cmd, width, height int) (*kataTerminal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(max(1, width)), Rows: uint16(max(1, height))})
	if err != nil {
		return nil, err
	}
	s := &kataTerminal{file: file, screen: vt.NewEmulator(max(1, width), max(1, height)), cmd: cmd, done: make(chan struct{})}
	// OSC queries, input and device reports use the emulator's input pipe. Copy
	// replies without polling; close the pipe itself to unblock this reader.
	go func() { _, _ = io.Copy(file, s.screen) }()
	go func() { s.err = cmd.Wait(); close(s.done) }()
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.done:
		}
	}()
	return s, nil
}
func (s *kataTerminal) Close() error {
	s.closeOnce.Do(func() {
		_ = s.screen.InputPipe().(io.Closer).Close()
		// Kata may have launched an editor. Its PTY session belongs to the board.
		_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
		_ = s.file.Close()
		<-s.done
	})
	return nil
}
func (s *kataTerminal) read() tea.Cmd {
	return func() tea.Msg {
		buf := make([]byte, 32<<10)
		n, err := s.file.Read(buf)
		return kataOutputMsg{terminal: s, data: buf[:n], err: err}
	}
}

// Bubble Tea owns the outer cursor. Paint the child's visible cursor as a
// steady inverse cell so text fields remain usable without an idle blink timer.
func (s *kataTerminal) render() string {
	if s.screen.CursorHidden() {
		return s.screen.Render()
	}
	pos := s.screen.CursorPosition()
	cell := s.screen.CellAt(pos.X, pos.Y)
	if cell == nil {
		return s.screen.Render()
	}
	saved := *cell
	cursor := saved
	if cursor.Content == "" {
		cursor = uv.EmptyCell
	}
	cursor.Style.Attrs ^= uv.AttrReverse
	s.screen.SetCell(pos.X, pos.Y, &cursor)
	text := s.screen.Render()
	s.screen.SetCell(pos.X, pos.Y, &saved)
	return text
}

func (s *kataTerminal) resize(w, h int) error {
	w, h = max(1, w), max(1, h)
	s.screen.Resize(w, h)
	return pty.Setsize(s.file, &pty.Winsize{Cols: uint16(w), Rows: uint16(h)})
}

// xterm's modified navigation keys. The VT encoder sends these keys
// without their modifiers.
var (
	modifiedKeyFinal  = map[rune]string{tea.KeyUp: "A", tea.KeyDown: "B", tea.KeyRight: "C", tea.KeyLeft: "D", tea.KeyHome: "H", tea.KeyEnd: "F"}
	modifiedKeyNumber = map[rune]int{tea.KeyPgUp: 5, tea.KeyPgDown: 6, tea.KeyInsert: 2, tea.KeyDelete: 3}
)

// Bubble Tea's keys are Ultraviolet's, so the VT encoder takes them as they
// are and handles the child's application-cursor and keypad modes.
func (s *kataTerminal) key(k tea.KeyPressMsg) {
	k.Mod &= tea.ModShift | tea.ModAlt | tea.ModCtrl
	k.IsRepeat = false
	if k.Mod != 0 && k.Code != tea.KeyTab {
		modifier := 1
		if k.Mod.Contains(tea.ModShift) {
			modifier++
		}
		if k.Mod.Contains(tea.ModAlt) {
			modifier += 2
		}
		if k.Mod.Contains(tea.ModCtrl) {
			modifier += 4
		}
		if final := modifiedKeyFinal[k.Code]; final != "" {
			s.screen.SendText(fmt.Sprintf("\x1b[1;%d%s", modifier, final))
			return
		}
		if number := modifiedKeyNumber[k.Code]; number != 0 {
			s.screen.SendText(fmt.Sprintf("\x1b[%d;%d~", number, modifier))
			return
		}
	}
	// Alt+Shift+letter arrives without text; the VT encoder sends only
	// its Alt prefix unless the shifted character is the text.
	if k.Text == "" && k.Mod == tea.ModAlt|tea.ModShift && k.ShiftedCode != 0 {
		k.Text = string(k.ShiftedCode)
	}
	s.screen.SendKey(uv.KeyPressEvent(k))
}

func (s *kataTerminal) mouse(v tea.MouseMsg, top int) {
	mouse := v.Mouse()
	if mouse.Y < top || mouse.Y >= top+s.screen.Height() || mouse.X < 0 || mouse.X >= s.screen.Width() {
		return
	}
	mouse.Y -= top
	mouse.Mod &= tea.ModShift | tea.ModAlt | tea.ModCtrl
	switch v.(type) {
	case tea.MouseMotionMsg:
		s.screen.SendMouse(vt.MouseMotion(mouse))
	case tea.MouseReleaseMsg:
		s.screen.SendMouse(vt.MouseRelease(mouse))
	case tea.MouseWheelMsg:
		s.screen.SendMouse(vt.MouseWheel(mouse))
	default:
		s.screen.SendMouse(vt.MouseClick(mouse))
	}
}
