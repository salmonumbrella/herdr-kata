package main

import (
	"io"
	"os"
	"os/exec"
	"testing"
)

// shellQuote's only job is that `flow edit` opens the file it was asked to open
// and runs nothing else. It is handed a path built from a flow id and the state
// directory, both of which can hold anything a filesystem allows, and its result
// is concatenated into an `sh -c` string — so the test runs the real shell
// rather than asserting on the quoting.
//
// Asserting the returned string would only restate the implementation. What a
// caller depends on is what sh does with it: one argument, byte for byte the
// path, and no substitution.
func TestShellQuoteSurvivesTheRealShell(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{"plain", "/home/worker/.herdr-kata/flows/nightly.yaml"},
		{"spaces", "/home/worker/My Flows/nightly run.yaml"},
		{"single quote", "/home/worker/worker's flows/nightly.yaml"},
		{"double quote", `/home/worker/"quoted"/nightly.yaml`},
		{"command substitution", "/home/worker/$(touch pwned)/nightly.yaml"},
		{"backticks", "/home/worker/`touch pwned`/nightly.yaml"},
		{"semicolon", "/home/worker/x; touch pwned; :/nightly.yaml"},
		{"variable", "/home/worker/$HOME/nightly.yaml"},
		{"glob", "/home/worker/*/nightly.yaml"},
		{"newline", "/home/worker/two\nlines/nightly.yaml"},
		{"backslash", `/home/worker/back\slash/nightly.yaml`},
		{"quote then escape", `/home/worker/'\''/nightly.yaml`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The trailing | is what catches word splitting: printf repeats its
			// format once per argument, so a path that arrived as two words comes
			// back with two separators.
			out, err := exec.Command("sh", "-c", "printf '%s|' "+shellQuote(tc.path)).Output()
			if err != nil {
				t.Fatalf("sh rejected the quoted path: %v", err)
			}
			if got := string(out); got != tc.path+"|" {
				t.Errorf("sh saw %q, want %q as a single unexpanded argument", got, tc.path+"|")
			}
		})
	}
}

// The editor is chosen by the first variable that names one, and HERDR_KATA_EDITOR
// comes first so a flow can be opened with something other than whatever the
// surrounding shell exports.
func TestFirstNonEmptyEnvTakesTheFirstThatIsSet(t *testing.T) {
	t.Setenv("HERDR_KATA_EDITOR", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")

	if got := firstNonEmptyEnv("HERDR_KATA_EDITOR", "VISUAL", "EDITOR"); got != "" {
		t.Errorf("got %q with nothing set, want empty so the path is printed instead", got)
	}

	t.Setenv("EDITOR", "vi")
	if got := firstNonEmptyEnv("HERDR_KATA_EDITOR", "VISUAL", "EDITOR"); got != "vi" {
		t.Errorf("got %q, want the only one set", got)
	}

	t.Setenv("VISUAL", "code -w")
	if got := firstNonEmptyEnv("HERDR_KATA_EDITOR", "VISUAL", "EDITOR"); got != "code -w" {
		t.Errorf("got %q, want VISUAL to beat EDITOR", got)
	}

	t.Setenv("HERDR_KATA_EDITOR", "hx")
	if got := firstNonEmptyEnv("HERDR_KATA_EDITOR", "VISUAL", "EDITOR"); got != "hx" {
		t.Errorf("got %q, want HERDR_KATA_EDITOR to win", got)
	}
}

// A variable set to whitespace counts as unset, and the value that comes back
// is trimmed.
//
// Both matter at the same call site: `EDITOR=" "` exported by a login script
// would otherwise run `sh -c "   '/path'"`, and a trailing space in a real
// editor name is invisible in the error when it fails.
func TestFirstNonEmptyEnvIgnoresWhitespaceAndTrims(t *testing.T) {
	t.Setenv("HERDR_KATA_EDITOR", "   ")
	t.Setenv("VISUAL", "\t\n")
	t.Setenv("EDITOR", "  vi  ")

	if got := firstNonEmptyEnv("HERDR_KATA_EDITOR", "VISUAL", "EDITOR"); got != "vi" {
		t.Errorf("got %q, want blank variables skipped and the value trimmed", got)
	}
}

// captureStderr records diagnostics so tests can assert their expected output.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	previous := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = previous
	w.Close()
	out, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(out)
}
