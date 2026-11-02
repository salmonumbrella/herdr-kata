package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestUnknownCommandExit2(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"unknown"}, &out); code != 2 || !strings.Contains(out.String(), "unknown command") {
		t.Fatalf("code/output: %d %s", code, &out)
	}
}

func TestSkeletonCommands(t *testing.T) {
	for _, cmd := range []string{"up", "serve", "stop", "run", "release", "logs", "doctor"} {
		var out bytes.Buffer
		if code := run([]string{cmd}, &out); code != 2 || !strings.Contains(out.String(), "not implemented") {
			t.Fatalf("%s: %d %s", cmd, code, &out)
		}
	}
}

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"version"}, &out); code != 0 || !strings.Contains(out.String(), "herdr-kata v2") {
		t.Fatalf("code/output: %d %s", code, &out)
	}
}
