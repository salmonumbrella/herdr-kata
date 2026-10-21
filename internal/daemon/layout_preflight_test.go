package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestUpRefusesIncompatibleLayoutBeforeStagingOrUpgrade(t *testing.T) {
	for _, tc := range []struct {
		fixture, remedy string
		incompatible    bool
	}{
		{"version2", "move the state directory aside and run herdr-kata up again", true},
		{"version2-migration", "move the state directory aside and run herdr-kata up again", true},
		{"version3", "move the state directory aside and run herdr-kata up again", true},
		{"legacy-empty-project", "move the state directory aside and run herdr-kata up again", true},
		{"legacy-project", "move the state directory aside and run herdr-kata up again", true},
		{"damaged-legacy", "move the state directory aside and run herdr-kata up again", true},
		{"damaged-current", "preserve original artifact paths", false},
		{"undecodable", "preserve original artifact paths", false},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			marker, err := os.ReadFile(filepath.Join("..", "state", "testdata", "layouts", tc.fixture+".json"))
			if err != nil {
				t.Fatal(err)
			}
			l := state.Layout{Root: t.TempDir()}
			t.Setenv("HERDR_SOCKET_PATH", "/tmp/sessions/work/herdr.sock")
			if err := os.WriteFile(l.StateFile(), marker, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(l.UpgradeFile(), []byte("previous candidate"), 0600); err != nil {
				t.Fatal(err)
			}
			result, err := Up(t.Context(), config.Config{Session: "work"}, l, "/missing/executable")
			if err == nil || !strings.Contains(err.Error(), tc.remedy) || strings.Contains(err.Error(), "move the state directory aside") != tc.incompatible {
				t.Fatalf("result=%s err=%v", result, err)
			}
			saved, e := os.ReadFile(l.StateFile())
			if e != nil || string(saved) != string(marker) {
				t.Fatal("marker changed", e)
			}
			data, e := os.ReadFile(l.UpgradeFile())
			if e != nil || string(data) != "previous candidate" {
				t.Fatal("upgrade changed", string(data), e)
			}
			for _, path := range []string{l.BinDir(), l.StopFile(), l.ServeInfo()} {
				if _, e := os.Stat(path); !os.IsNotExist(e) {
					t.Fatal("refused startup mutated state", path, e)
				}
			}
		})
	}
}
