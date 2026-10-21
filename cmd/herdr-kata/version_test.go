package main

import (
	"bytes"
	"os"
	"regexp"
	"testing"

	"github.com/BurntSushi/toml"
)

// The version advertised to Herdr, CLI users and changelog readers must agree.
func TestReleaseVersionConsistency(t *testing.T) {
	var manifest struct{ Version string }
	if _, err := toml.DecodeFile("../../herdr-plugin.toml", &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version == "" {
		t.Fatal("manifest has no version")
	}
	t.Run("changelog", func(t *testing.T) {
		data, err := os.ReadFile("../../docs/changelog.md")
		if err != nil {
			t.Fatal(err)
		}
		latest := regexp.MustCompile(`(?m)^## v?([0-9]+\.[0-9]+\.[0-9]+)(?:[ \t].*)?$`).FindSubmatch(data)
		if latest == nil {
			t.Fatal("changelog has no release section")
		}
		if string(latest[1]) != manifest.Version {
			t.Fatalf("latest changelog release = %q, manifest = %q", latest[1], manifest.Version)
		}
	})
	t.Run("cli", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"version"}, &stdout, &stderr); code != 0 {
			t.Fatalf("version exit = %d: %s", code, stderr.String())
		}
		if want := "herdr-kata " + manifest.Version + "\n"; stdout.String() != want {
			t.Fatalf("version output = %q, want %q", stdout.String(), want)
		}
	})
}
