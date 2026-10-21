package tui

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/creack/pty"
)

func TestDisplayEnvPrecedenceAndIsolation(t *testing.T) {
	e := displayEnv([]string{"NO_COLOR=", "KATA_COLOR_MODE=dark", "SECRET=sentinel"}, []string{"TERM=xterm-256color", "NO_COLOR=1", "KATA_AUTH_TOKEN=sentinel"})
	want := displayEnvironment{"TERM": "xterm-256color", "NO_COLOR": "", "KATA_COLOR_MODE": "dark"}
	if !reflect.DeepEqual(e, want) {
		t.Fatalf("display env = %v", e)
	}
	if !reflect.DeepEqual(e.Environ(), []string{"KATA_COLOR_MODE=dark", "NO_COLOR=", "TERM=xterm-256color"}) {
		t.Fatal(e.Environ())
	}
}
func TestBoardDisplayEnvLanes(t *testing.T) {
	for _, c := range []struct {
		lanes  [][]string
		want   displayEnvironment
		notice bool
	}{
		{nil, displayEnvironment{"TERM": "dumb"}, false},
		{[][]string{{"TERM=xterm"}}, displayEnvironment{"TERM": "xterm"}, false},
		{[][]string{{"TERM=xterm", "NO_COLOR="}, {"NO_COLOR=", "TERM=xterm"}}, displayEnvironment{"TERM": "xterm", "NO_COLOR": ""}, false},
		{[][]string{{"TERM=xterm", "NO_COLOR="}, {"TERM=xterm"}}, displayEnvironment{"TERM": "dumb"}, true},
	} {
		e, msg := boardDisplayEnv(c.lanes, []string{"TERM=dumb"})
		if !reflect.DeepEqual(e, c.want) {
			t.Fatal(e)
		}
		want := ""
		if c.notice {
			want = "projects set different terminal display settings; using this pane's"
		}
		if msg != want {
			t.Fatal(msg)
		}
	}
}

// terminal is a real TTY, so profile detection reads the display env.
func terminal(t *testing.T) *os.File {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ptmx.Close(); _ = tty.Close() })
	return tty
}

func TestBoardThemeOutput(t *testing.T) {
	preserveTheme(t)
	for _, c := range []struct {
		mode    string
		want    string
		profile colorprofile.Profile
	}{{"auto", "46", colorprofile.ANSI256}, {"light", "28", colorprofile.ANSI256}, {"dark", "46", colorprofile.ANSI256}} {
		mode, profile := boardTheme(terminal(t), displayEnvironment{"TERM": "xterm-256color", "KATA_COLOR_MODE": c.mode})
		if mode != resolveColorMode(func(k string) string {
			if k == "KATA_COLOR_MODE" {
				return c.mode
			}
			return ""
		}) {
			t.Fatal(mode)
		}
		if profile != c.profile {
			t.Fatalf("%s profile = %v", c.mode, profile)
		}
		if openStyle.GetForeground() != lipgloss.Color(c.want) {
			t.Fatal(openStyle.GetForeground())
		}
	}
}
func TestNoneAttributes(t *testing.T) {
	preserveTheme(t)
	for _, e := range []displayEnvironment{{"KATA_COLOR_MODE": "none"}, {"NO_COLOR": "1", "KATA_COLOR_MODE": "dark"}} {
		if _, profile := boardTheme(terminal(t), e); profile != colorprofile.ANSI {
			t.Fatalf("None profile = %v", profile)
		}
		for _, s := range []lipgloss.Style{titleStyle, deletedStyle, tabActive, selectedStyle} {
			text := s.Render("sample")
			if !strings.Contains(text, "\x1b[") || strings.Contains(text, "38;") || strings.Contains(text, "48;") {
				t.Fatalf("None attributes = %q", text)
			}
		}
	}
}

// Colored modes keep the display env's own capability: a dumb terminal
// renders neither colors nor attributes.
func TestDumbTerminalCapability(t *testing.T) {
	preserveTheme(t)
	for mode, want := range map[string]colorprofile.Profile{"dark": colorprofile.NoTTY, "none": colorprofile.ANSI} {
		if _, profile := boardTheme(terminal(t), displayEnvironment{"TERM": "dumb", "KATA_COLOR_MODE": mode}); profile != want {
			t.Fatalf("mode %s profile = %v", mode, profile)
		}
	}
}

func TestCLICOLORZeroCapability(t *testing.T) {
	preserveTheme(t)
	for mode, want := range map[string]colorprofile.Profile{"dark": colorprofile.NoTTY, "none": colorprofile.ANSI} {
		if _, profile := boardTheme(terminal(t), displayEnvironment{"TERM": "xterm-256color", "CLICOLOR": "0", "KATA_COLOR_MODE": mode}); profile != want {
			t.Fatalf("mode %s profile = %v, want %v", mode, profile, want)
		}
	}
}
