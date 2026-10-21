package kata

import (
	"slices"
	"strings"
)

// terminalDisplayNames are the variables a terminal program reads to decide
// how to render, plus Kata's own forced color mode. The Kata view runs in the
// pane the user is looking at, so they pass through from that pane unchanged
// and `kata tui` renders exactly as it would from a shell there. Runtime
// machine calls keep CLI.Env's allowlist.
var terminalDisplayNames = []string{"TERM", "COLORTERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION", "COLORFGBG", "NO_COLOR", "FORCE_COLOR", "CLICOLOR", "CLICOLOR_FORCE", "KATA_COLOR_MODE"}

// TerminalEnv appends the pane's terminal display variables from environ
// to base. A name already in base, such as a configured kata.env value, keeps
// its value; nothing else from environ is added.
func TerminalEnv(base, environ []string) []string {
	present := map[string]bool{}
	for _, pair := range base {
		name, _, _ := strings.Cut(pair, "=")
		present[name] = true
	}
	env := slices.Clone(base)
	for _, pair := range environ {
		name, _, _ := strings.Cut(pair, "=")
		if slices.Contains(terminalDisplayNames, name) && !present[name] {
			env = append(env, pair)
		}
	}
	return env
}

// IsDisplayVar reports whether name is one of the terminal display variables.
func IsDisplayVar(name string) bool { return slices.Contains(terminalDisplayNames, name) }
