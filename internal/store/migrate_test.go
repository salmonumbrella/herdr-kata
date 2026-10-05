package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
)

// Two processes opening the same old database at once must both succeed.
//
// The daemon, the board and lease commands open this store, and Herdr
// starts several of them together. The jobs and runs migration used to check
// for a column and then ALTER it on a bare connection: both openers saw the
// column missing, both altered, and the loser died on "duplicate column name" —
// a store refusing to open over a schema change that had already worked.
func TestOpenMigratesConcurrently(t *testing.T) {
	dir := t.TempDir()
	seedOldSchema(t, filepath.Join(dir, "herdr-kata.db"))

	const openers = 8
	errs := make(chan error, openers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range openers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // together, or the race never happens
			s, err := Open(dir)
			if err != nil {
				errs <- err
				return
			}
			errs <- s.Close()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent open: %v", err)
		}
	}

	// And the columns really are there afterwards.
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	if err := s.PutJob(context.Background(), Job{
		ID: "j", Name: "j", Prompt: "p", Schedule: ScheduleManual, Tags: []string{"a"},
	}); err != nil {
		t.Fatalf("put job into the migrated schema: %v", err)
	}
}

// seedOldSchema writes a database with the original tables and none of the
// columns added since, which is what an upgrade actually finds on disk.
func seedOldSchema(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("seed schema: %v", err)
	}
}

// An already migrated store must not contend for a writer lock merely to
// discover that a column exists. Many simultaneous no-op migrations can
// otherwise starve an opener beyond SQLite's busy timeout.
func TestExistingColumnMigrationDoesNotAcquireWriterLock(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	db, err := sql.Open("sqlite", filepath.Join(dir, "herdr-kata.db")+"?_pragma=busy_timeout(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := addColumn(db, "jobs", "name", "TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatalf("existing column must require only a read: %v", err)
	}
}
