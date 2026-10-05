package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestForkStoreDoesNotAdoptLegacyDatabase(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "bermuda.db")
	seedOldSchema(t, legacy)
	before, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutJob(context.Background(), Job{ID: "example-job", Name: "Example job", Prompt: "Example work"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "herdr-kata.db")); err != nil {
		t.Fatalf("fork database: %v", err)
	}
	after, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("opening fork store changed legacy database")
	}
}
