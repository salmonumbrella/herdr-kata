// Package tui is herdr-kata's terminal UI. It reads the worker's local
// snapshots and never polls Kata; it reaches Kata only through the service
// cmd injects, on an explicit user action.
package tui

import (
	"context"
	"io"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type displayEnvironment map[string]string

func (e displayEnvironment) Getenv(name string) string { return e[name] }
func (e displayEnvironment) Environ() []string {
	result := make([]string, 0, len(e))
	for k, v := range e {
		result = append(result, k+"="+v)
	}
	slices.Sort(result)
	return result
}
func displayEnv(base, pane []string) displayEnvironment {
	e := displayEnvironment{}
	for _, entry := range kata.TerminalEnv(base, pane) {
		name, value, ok := strings.Cut(entry, "=")
		if ok && kata.IsDisplayVar(name) {
			e[name] = value
		}
	}
	return e
}
func boardDisplayEnv(lanes [][]string, pane []string) (displayEnvironment, string) {
	if len(lanes) == 0 {
		return displayEnv(nil, pane), ""
	}
	common := displayEnv(lanes[0], pane)
	for _, lane := range lanes[1:] {
		if !maps.Equal(common, displayEnv(lane, pane)) {
			return displayEnv(nil, pane), "projects set different terminal display settings; using this pane's"
		}
	}
	return common, ""
}

// boardTheme applies the display env's color mode and returns the color
// profile that renders it. Colored modes keep the display env's own
// capability; None keeps bold, reverse and faint. The palette assumes a dark
// background until the terminal answers the background query.
func boardTheme(out io.Writer, env displayEnvironment) (colorMode, colorprofile.Profile) {
	mode := resolveColorMode(env.Getenv)
	profile := colorprofile.Detect(out, env.Environ())
	if mode == colorNone {
		profile = colorprofile.ANSI
	} else if env.Getenv("CLICOLOR") == "0" && env.Getenv("CLICOLOR_FORCE") == "" {
		profile = colorprofile.NoTTY
	}
	applyColorMode(mode, true)
	return mode, profile
}

// Run shows the TUI on opts.Stdout until the user quits.
func Run(ctx context.Context, opts Options) error {
	// A terminal hangup must cancel an external editor too, so Run can reach
	// the model's owned-file cleanup while Bubble Tea is waiting for it.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGHUP)
	defer stop()
	display, notice := boardDisplayEnv(opts.DisplayLanes, os.Environ())
	_, profile := boardTheme(opts.Stdout, display)
	opts.InitialStatus = notice
	m := newModel(opts)
	m.boardProfile = &profile
	m.ctx = ctx
	defer m.Close()
	_, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithFPS(25), tea.WithEnvironment(display.Environ()), tea.WithColorProfile(colorprofile.TrueColor), tea.WithInput(os.Stdin), tea.WithOutput(opts.Stdout)).Run()
	return err
}

// Convert only host-owned chrome. The child already selected its terminal
// profile and its VT cells must reach the outer renderer unchanged.
func (m *model) boardText(text string) string {
	if m.boardProfile == nil {
		return text
	}
	var out strings.Builder
	w := colorprofile.Writer{Forward: &out, Profile: *m.boardProfile}
	_, _ = w.WriteString(text)
	return out.String()
}
