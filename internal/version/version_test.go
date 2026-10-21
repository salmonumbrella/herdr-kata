package version

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildIDStable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "exe")
	if err := os.WriteFile(p, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got, err := BuildID(p)
		if err != nil || got != "ba7816bf8f01cfea" {
			t.Fatalf("BuildID = %q, %v", got, err)
		}
	}
	if _, err := BuildID(p + "missing"); err == nil {
		t.Fatal("missing file accepted")
	}
}
