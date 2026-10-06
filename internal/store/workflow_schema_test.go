package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRejectsRetiredWorkflowColumns(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "herdr-kata.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE jobs (id TEXT PRIMARY KEY, flow_id TEXT NOT NULL DEFAULT '', flow_input TEXT NOT NULL DEFAULT '')"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if s != nil {
		s.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "legacy flow columns") {
		t.Fatalf("legacy database must be refused before adding replacement columns: %v", err)
	}
	db, err = sql.Open("sqlite", filepath.Join(dir, "herdr-kata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err = db.QueryRow("SELECT count(*) FROM pragma_table_info('jobs') WHERE name = 'workflow_id'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("refused database gained replacement columns")
	}
}
