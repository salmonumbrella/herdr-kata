package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// ID migration retains old paths so live agents and held runs can still use
// their frozen reporter. Startup pruning must preserve that executable too.
func TestPruneKeepsReporterThroughLegacyAlias(t *testing.T) {
	root := t.TempDir()
	l := state.Layout{Root: filepath.Join(root, "kata")}
	old := filepath.Join(root, "salmonumbrella.herdr-kata")
	if err := os.MkdirAll(l.BinDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("kata", old); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(l.BinDir(), "herdr-kata-current")
	saved := filepath.Join(old, "bin", "herdr-kata-saved")
	unused := filepath.Join(l.BinDir(), "herdr-kata-unused")
	for _, p := range []string{current, saved, unused} {
		if err := os.WriteFile(p, []byte("executable"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.WriteJSON(state.OSFS{}, l.RunFile("held-run"), map[string]any{"Herdr": map[string]any{"ReportBin": saved}}); err != nil {
		t.Fatal(err)
	}
	if err := prune(l, current); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(saved)
	if err != nil || string(got) != "executable" {
		t.Fatalf("frozen reporter lost after migration: %q %v", got, err)
	}
	if _, err := os.Stat(unused); !os.IsNotExist(err) {
		t.Fatalf("unused executable retained: %v", err)
	}
}
