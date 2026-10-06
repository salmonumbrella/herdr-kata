package adoption

import (
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"hegel.dev/go/hegel"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Stable import identity is independent of paths/content and separates all
// namespace/resource/item components, including embedded separators.
func TestStableIdentityAcrossFullText(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		source := hegel.Draw(ht, hegel.Text())
		item := hegel.Draw(ht, hegel.Text())
		a := StableUID(source, "job", item)
		if uid, err := katacli.NormalizeUID(a); err != nil || uid != a {
			ht.Fatalf("invalid identity %q %v", a, err)
		}
		if a != StableUID(source, "job", item) || a == StableUID(source, "flow", item) || a == StableUID(source+"x", "job", item) {
			ht.Fatal("unstable or colliding import identity")
		}
	})
}
func TestBuildWorkflowsWithoutSourcePointers(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "flows"), 0700)
	os.WriteFile(filepath.Join(dir, "flows", "inspect.yml"), []byte("steps:\n - id: inspect\n   run: git status\n"), 0600)
	repo := &store.NativeRepository{Client: &katacli.Client{}}
	plan, err := Build(t.Context(), dir, "example-installation", "primary", "", repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Drafts) != 1 || plan.Drafts[0].Resource != "workflow" || strings.Contains(string(plan.Drafts[0].Definition), dir) {
		t.Fatalf("bad workflow plan %+v", plan)
	}
}

func TestBuildRefusesAmbiguousRecognizedDatabases(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"bermuda.db", "herdr-kata.db"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Build(t.Context(), dir, "example-installation", "primary", "", &store.NativeRepository{Client: &katacli.Client{}})
	if err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("ambiguous recognized source silently selected: %v", err)
	}
}
func TestBuildRefusesEmptyUnrecognizedSource(t *testing.T) {
	_, err := Build(t.Context(), t.TempDir(), "example-installation", "primary", "", &store.NativeRepository{Client: &katacli.Client{}})
	if err == nil || !strings.Contains(err.Error(), "no recognized") {
		t.Fatalf("empty source reported successful adoption: %v", err)
	}
}
