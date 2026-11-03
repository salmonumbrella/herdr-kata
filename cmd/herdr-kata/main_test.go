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

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"version"}, &out); code != 0 || out.String() != "herdr-kata 0.3.0\n" {
		t.Fatalf("code/output: %d %s", code, &out)
	}
}
