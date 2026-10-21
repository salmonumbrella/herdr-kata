package herdr_test

import (
	"path/filepath"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
)

// Herdr 0.9.5 reserves default for the config root, outside sessions/.
func TestSocketPathSessionLayouts(t *testing.T) {
	for _, tc := range []struct {
		name, xdg, session, relative string
	}{
		{"home default", "", "default", ".config/herdr/herdr.sock"},
		{"home named", "", "work", ".config/herdr/sessions/work/herdr.sock"},
		{"xdg default", "config", "default", "config/herdr/herdr.sock"},
		{"xdg named", "config", "work", "config/herdr/sessions/work/herdr.sock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", root)
			t.Setenv("XDG_CONFIG_HOME", "")
			if tc.xdg != "" {
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, tc.xdg))
			}
			t.Setenv("HERDR_SOCKET_PATH", "")
			got := herdr.SocketPath(tc.session)
			if want := filepath.Join(root, tc.relative); got != want {
				t.Fatalf("socket = %q, want %q", got, want)
			}
			if session := herdr.SessionFromSocket(got); session != tc.session {
				t.Fatalf("session gate saw %q, want %q", session, tc.session)
			}
			t.Setenv("HERDR_SOCKET_PATH", "/tmp/explicit.sock")
			if got := herdr.SocketPath(tc.session); got != "/tmp/explicit.sock" {
				t.Fatalf("override ignored: %q", got)
			}
		})
	}
}

// HERDR_SOCKET_PATH wins over the default session's config-root socket.
func TestSocketEnvOverridesDefaultSession(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	explicit := filepath.Join(root, "elsewhere", "herdr.sock")
	t.Setenv("HERDR_SOCKET_PATH", explicit)
	if got := herdr.SocketPath("default"); got != explicit {
		t.Fatalf("default session socket = %q, want %q", got, explicit)
	}
}
