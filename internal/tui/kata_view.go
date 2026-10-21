package tui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"maps"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
)

func (m *model) kataTop() (bodyLines, []tabHit, int) {
	labels, _ := m.folderTabs()
	info := m.tabInfo()
	info.Hint = plain(helpText(kataHelp().Full))
	tabs, hits := renderTabs(labels, 0, m.width, m.width, info)
	return block(m.boardText(tabs)), hits, 1
}

func (m *model) kataSize() (int, int) {
	top, _, _ := m.kataTop()
	return max(1, m.width), max(1, m.height-len(top))
}

func (m *model) renderKata() renderedFrame {
	top, tabs, tabY := m.kataTop()
	text := "Opening Kata…"
	if m.kata != nil && m.kata.terminal != nil {
		text = m.kata.terminal.render()
	}
	body := block(text)
	f := (pane{Top: top, Body: body, Width: m.width}).Render(m.height)
	f.Hits.SetTabs(tabY, tabs, m.height)
	return f
}

func (m *model) openKataIssue(opts Lane, issue string) tea.Cmd {
	if m.busy {
		m.notify(statusInfo, "Working · wait for the current action to finish")
		return nil
	}
	opts = m.freshProjectOptions(opts)
	commander, ok := opts.Service.(kataCommander)
	if !ok {
		m.notify(statusInfo, "Kata unavailable for this project")
		return nil
	}
	m.clearStatus()
	m.refreshGeneration = 0
	m.saveFrame()
	m.kataNav = append([]navFrame(nil), m.nav...)
	for i := range m.kataNav {
		m.kataNav[i].expanded = maps.Clone(m.kataNav[i].expanded)
	}
	project := opts.ProjectUID + "\x00" + opts.Project
	if m.kata != nil && m.kata.terminal != nil && m.kata.project == project && issue == "" {
		m.kataActive = true
		return nil
	}
	m.stopKata()
	ctx, cancel := context.WithCancel(context.Background())
	session := &kataSession{project: project, cancel: cancel}
	m.kata = session
	m.kataActive = true
	w, h := m.kataSize()
	return func() tea.Msg {
		cmd, err := commander.KataCommand(issue)
		var terminal *kataTerminal
		if err == nil {
			terminal, err = startKataTerminal(ctx, cmd, w, h)
		}
		return kataStartedMsg{terminal: terminal, session: session, err: err}
	}
}

func (m *model) acceptKataStart(v kataStartedMsg) tea.Cmd {
	if m.kata == nil || v.session != m.kata {
		if v.terminal != nil {
			_ = v.terminal.Close()
		}
		return nil
	}
	if v.err != nil {
		m.stopKata()
		m.leaveKata()
		m.completeAction(actionMsg{err: v.err})
		if v.session.wrapTo != nil {
			m.wrapPastKata(*v.session.wrapTo)
			return nil
		}
		if !m.kataRetryUntil.IsZero() && time.Now().Before(m.kataRetryUntil) {
			m.notify(statusError, "Opening Kata once the runtime's route check completes: "+m.notice.Text)
			return tea.Tick(m.kataRetryEvery, func(time.Time) tea.Msg { return openKataRetryMsg{} })
		}
		m.kataRetryUntil = time.Time{}
		return nil
	}
	m.kata.terminal = v.terminal
	w, h := m.kataSize()
	if err := m.kata.terminal.resize(w, h); err != nil {
		m.notify(statusError, err.Error())
	}
	m.kataRetryUntil = time.Time{}
	return m.readKata()
}

// readKata starts copying Kata's output into the emulator once the outer
// terminal's colors are known, so the emulator answers Kata's OSC 10/11
// queries with them and Kata picks the same palette as in a plain terminal.
func (m *model) readKata() tea.Cmd {
	if m.kata == nil || m.kata.terminal == nil || m.kata.reading {
		return nil
	}
	if (m.terminalBackground == nil || m.terminalForeground == nil) && !m.terminalColorsLate {
		return nil
	}
	m.kata.reading = true
	if c := m.terminalBackground; c != nil {
		m.kata.terminal.screen.SetDefaultBackgroundColor(c)
	}
	if c := m.terminalForeground; c != nil {
		m.kata.terminal.screen.SetDefaultForegroundColor(c)
	}
	return m.kata.terminal.read()
}

func (m *model) acceptKataOutput(v kataOutputMsg) tea.Cmd {
	if m.kata == nil || v.terminal != m.kata.terminal {
		return nil
	}
	_, _ = v.terminal.screen.Write(v.data)
	if v.err == nil {
		return v.terminal.read()
	}
	m.stopKata()
	if m.kataActive {
		m.leaveKata()
	}
	if v.terminal.err != nil && !errors.Is(v.terminal.err, os.ErrClosed) {
		m.notify(statusError, fmt.Sprintf("Kata exited: %v · 1 or K restarts it", v.terminal.err))
	}
	return nil
}

func (m *model) leaveKata() {
	m.kataActive = false
	if len(m.kataNav) > 0 {
		m.nav = m.kataNav
		m.kataNav = nil
		m.restoreFrame(m.nav[len(m.nav)-1])
	}
}

type kataSession struct {
	project  string
	cancel   context.CancelFunc
	terminal *kataTerminal // nil while starting
	reading  bool
	wrapTo   *int // the tab Tab cycling lands on if this start is refused
}

// cycleToKata opens the Kata tab from Tab or Shift+Tab. A refused start
// skips the Kata tab and continues the cycle to wrap.
func (m *model) cycleToKata(wrap int) tea.Cmd {
	return m.wrapRefusedKata(m.requestKata(), &wrap)
}

// wrapRefusedKata attaches a Tab cycle's wrap target to the Kata start that
// cmd begins, wraps at once if the start was refused, or keeps the target
// while the project choice is open.
func (m *model) wrapRefusedKata(cmd tea.Cmd, wrap *int) tea.Cmd {
	switch {
	case wrap == nil:
	case len(m.projectChoices) > 0:
		m.projectWrapTo = wrap
	case m.kataActive && m.kata != nil && m.kata.terminal == nil:
		m.kata.wrapTo = wrap
	case !m.kataActive:
		m.wrapPastKata(*wrap)
	}
	return cmd
}

func (m *model) wrapPastKata(tab int) {
	notice := m.notice
	m.selectTab(tab)
	if notice.Text != "" {
		m.notice = notice
	}
}

func (m *model) stopKata() {
	if m.kata == nil {
		return
	}
	if m.kata.cancel != nil {
		m.kata.cancel()
	}
	if m.kata.terminal != nil {
		_ = m.kata.terminal.Close()
	}
	m.kata = nil
}

// kataTab is owned by this view.
type kataTab struct {
	kataActive         bool
	kataNav            []navFrame
	kata               *kataSession
	terminalForeground color.Color
	terminalBackground color.Color
	terminalColorsLate bool
	kataRetryEvery     time.Duration
	kataRetryUntil     time.Time
}
