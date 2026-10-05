package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReadLegacyJobsDoesNotMigrateMinimalSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE jobs (id TEXT PRIMARY KEY,prompt TEXT,cwd TEXT,kind TEXT,created_at INTEGER); INSERT INTO jobs VALUES ('inspect','Inspect workspace','/old/checkout','claude',1); CREATE TABLE memory (body TEXT); INSERT INTO memory VALUES ('ignore');`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := ReadLegacyJobs(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != "inspect" || jobs[0].Prompt != "Inspect workspace" {
		t.Fatalf("legacy mapping %+v", jobs)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("source bytes changed")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatal("source sidecars created")
	}
}
func TestReadLegacyJobsRefusesLiveWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.db")
	os.WriteFile(path, []byte("not needed"), 0600)
	os.WriteFile(path+"-wal", []byte("uncheckpointed"), 0600)
	if _, err := ReadLegacyJobs(t.Context(), path); err == nil {
		t.Fatal("live WAL ignored")
	}
}

func TestReadLegacyJobsEscapedSnapshotFilename(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "snapshot.db")
	name := "snapshot #%.db"
	if runtime.GOOS != "windows" {
		name = "snapshot #?%.db"
	}
	path := filepath.Join(dir, name)
	// Create through a plain filename, then rename the stopped fixture. The
	// driver interprets '%' in a raw setup DSN before the reader is exercised.
	db, err := sql.Open("sqlite", plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE jobs (id TEXT PRIMARY KEY,prompt TEXT); INSERT INTO jobs VALUES ('inspect','Inspect workspace');`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(plain, path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := ReadLegacyJobs(t.Context(), path)
	if err != nil || len(jobs) != 1 || jobs[0].ID != "inspect" || jobs[0].Prompt != "Inspect workspace" {
		t.Fatalf("escaped filename read: %+v %v", jobs, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("escaped source changed: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("escaped source created sidecars: %v %v", entries, err)
	}
}
