package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// The state directory is the one thing every part of herdr-kata has to agree on.
// The daemon writes the store, the board pane reads it, and the sentinel takes
// its lock beside it; if any of them resolves a different directory the failure
// is silent — a board that shows no jobs, a sentinel that guards nothing, a
// second database nobody looks at. So the resolution rules are asserted rather
// than left to the doc comment.
func TestStateDirResolution(t *testing.T) {
	tests := []struct {
		name      string
		herdrKata string
		plugin    string
		home      string
		want      string
	}{
		{
			name:      "override wins",
			herdrKata: "/tmp/explicit",
			home:      "/home/someone",
			want:      "/tmp/explicit",
		},
		{
			name:   "home fallback",
			home:   "/home/someone",
			want:   "/home/someone/.herdr-kata",
			plugin: "",
		},
		{
			// HERDR_PLUGIN_STATE_DIR is set for anything herdr launches, so
			// honouring it would give the board opened as a plugin pane a
			// different database from the one the daemon writes.
			name:   "herdr plugin state dir is ignored",
			plugin: "/tmp/herdr-plugin",
			home:   "/home/someone",
			want:   "/home/someone/.herdr-kata",
		},
		{
			// Both set: the override still decides, so a plugin pane started
			// with an explicit state directory lands on that one and not on
			// herdr's.
			name:      "override wins over herdr plugin state dir",
			herdrKata: "/tmp/explicit",
			plugin:    "/tmp/herdr-plugin",
			home:      "/home/someone",
			want:      "/tmp/explicit",
		},
		{
			// An empty override is not an override. Exported-but-empty is what
			// a shell leaves behind after `export HERDR_KATA_HOME=`, and
			// treating it as a state directory would put the store at the
			// filesystem root.
			name:      "empty override falls back",
			herdrKata: "",
			home:      "/home/someone",
			want:      "/home/someone/.herdr-kata",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HERDR_KATA_HOME", tc.herdrKata)
			t.Setenv("HERDR_PLUGIN_STATE_DIR", tc.plugin)
			t.Setenv("HOME", tc.home)

			if got := stateDir(); got != tc.want {
				t.Errorf("stateDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Everything herdr-kata keeps lives inside the state directory and nowhere else.
// That is the whole promise of "one visible directory rather than the XDG
// split": a person can find the lot by looking in one place, and a test machine
// pointed at a temp directory leaves nothing behind in the real one. A helper
// that quietly resolved against $HOME instead would still work on the developer
// machine and scatter files on every other.
func TestStateDirContainsEverythingHerdrKataWrites(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_KATA_HOME", root)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", filepath.Join(t.TempDir(), "herdr"))
	// Set so an accidental fallback lands somewhere identifiable rather than on
	// the developer's real home directory.
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	// The memory directory has an override of its own, which must not be in
	// force while the default layout is under test.
	t.Setenv("HERDR_KATA_MEMORY_DIR", "")

	paths := map[string]string{
		"stop file":     stopFile(),
		"flow dir":      flowDir(),
		"run dir":       runDirFor("20260101T000000Z-somejob"),
		"sentinel lock": lockPath(roleSentinel),
	}

	for what, p := range paths {
		if !filepath.IsAbs(p) {
			t.Errorf("%s = %q, want an absolute path", what, p)
			continue
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Errorf("%s = %q, which is outside the state directory %q", what, p, root)
		}
	}
}

// The memory directory is the one part of the layout that is meant to be
// movable: a vault usually already has a home, so HERDR_KATA_MEMORY_DIR points at
// it. Nothing else follows that override.
