package discovery

import (
	"os"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// Discovery without KATA_HOME reads the test home's Kata, never the owner's.
func TestKataHomeStaysInTestHome(t *testing.T) {
	home, path := os.Getenv(testenv.HomeVar), kataHome()
	if home == "" || testenv.Reaches(path) || !strings.HasPrefix(path, home+string(os.PathSeparator)) {
		t.Fatalf("default Kata home %s is outside the test home %q", path, home)
	}
}
