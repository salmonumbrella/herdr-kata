package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestRunUnknownCommandExits2(t *testing.T) {
	var out, err bytes.Buffer
	if got := run([]string{"nope"}, &out, &err); got != 2 {
		t.Fatalf("exit = %d", got)
	}
	if !strings.Contains(err.String(), `unknown command "nope"`) {
		t.Fatalf("stderr = %q", err.String())
	}
}
func TestVersionCommand(t *testing.T) {
	var out, err bytes.Buffer
	if got := run([]string{"version"}, &out, &err); got != 0 {
		t.Fatalf("exit = %d, stderr = %q", got, err.String())
	}
	if !regexp.MustCompile(`^herdr-kata .+\n$`).MatchString(out.String()) {
		t.Fatalf("stdout = %q", out.String())
	}
}
