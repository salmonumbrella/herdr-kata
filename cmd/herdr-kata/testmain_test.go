package main

import (
	"os"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"github.com/salmonumbrella/herdr-kata/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// A test that sets no directory of its own resolves the test home, never the
// owner's plugin config, state or Herdr socket.
func TestDefaultsStayInTestHome(t *testing.T) {
	home := os.Getenv(testenv.HomeVar)
	for name, path := range map[string]string{"config": config.DefaultPath(), "state": state.DefaultRoot(), "socket": herdr.SocketPath("default")} {
		if home == "" || testenv.Reaches(path) || !strings.HasPrefix(path, home+string(os.PathSeparator)) {
			t.Errorf("default %s path %s is outside the test home %q", name, path, home)
		}
	}
}
