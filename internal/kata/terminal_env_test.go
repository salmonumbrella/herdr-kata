package kata

import (
	"slices"
	"testing"
)

func TestKataTerminalEnvPassesDisplayVariablesOnly(t *testing.T) {
	base := []string{"HOME=/home/worker", "KATA_HOME=/srv/kata"}
	environ := []string{"TERM=xterm-256color", "COLORTERM=truecolor", "TERM_PROGRAM=herdr", "TERM_PROGRAM_VERSION=0.9.5", "COLORFGBG=15;0", "NO_COLOR=1", "FORCE_COLOR=1", "CLICOLOR=1", "CLICOLOR_FORCE=1", "KATA_COLOR_MODE=dark", "KATA_AUTH_TOKEN=secret", "TERMINFO=/usr/share/terminfo", "FOO=bar"}
	want := []string{"HOME=/home/worker", "KATA_HOME=/srv/kata", "TERM=xterm-256color", "COLORTERM=truecolor", "TERM_PROGRAM=herdr", "TERM_PROGRAM_VERSION=0.9.5", "COLORFGBG=15;0", "NO_COLOR=1", "FORCE_COLOR=1", "CLICOLOR=1", "CLICOLOR_FORCE=1", "KATA_COLOR_MODE=dark"}
	if got := TerminalEnv(base, environ); !slices.Equal(got, want) {
		t.Fatalf("env = %q, want %q", got, want)
	}
	// A configured value keeps precedence over the pane's.
	if got := TerminalEnv([]string{"TERM=dumb"}, []string{"TERM=xterm-256color", "COLORTERM=truecolor"}); !slices.Equal(got, []string{"TERM=dumb", "COLORTERM=truecolor"}) {
		t.Fatalf("env = %q", got)
	}
	// Unset variables are not invented.
	if got := TerminalEnv(base, nil); !slices.Equal(got, base) {
		t.Fatalf("env = %q", got)
	}
}
