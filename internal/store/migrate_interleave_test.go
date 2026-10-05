package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

var migrationDriverSequence atomic.Uint64

// The driver wrapper schedules another real SQLite connection between the
// migration's first schema read and its writer-lock request. It never invents
// rows or a SQLITE_BUSY result: those come from the actual temporary database.
func TestColumnMigrationRecognizesCompletedChangeAfterBusy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		completed bool
		beginErr  error
		readErr   error
	}{
		{name: "completed", completed: true},
		{name: "still absent"},
		{name: "unrelated begin error", completed: true, beginErr: errors.New("begin failed for another reason")},
		{name: "fallback read error", completed: true, readErr: errors.New("schema read failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "example.db")
			ownerDB, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)")
			if err != nil {
				t.Fatal(err)
			}
			defer ownerDB.Close()
			if _, err := ownerDB.Exec("CREATE TABLE jobs (id TEXT PRIMARY KEY)"); err != nil {
				t.Fatal(err)
			}
			owner, err := ownerDB.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			defer owner.ExecContext(ctx, "ROLLBACK")

			name := fmt.Sprintf("migration-interleave-%d", migrationDriverSequence.Add(1))
			sql.Register(name, &migrationInterleaveDriver{
				base: ownerDB.Driver(), beginErr: tc.beginErr, readErr: tc.readErr,
				afterRead: func() {
					if tc.completed {
						if _, err := owner.ExecContext(ctx, "ALTER TABLE jobs ADD COLUMN name TEXT NOT NULL DEFAULT ''"); err != nil {
							t.Fatal(err)
						}
					}
					// A later migration or ordinary write retains the writer lock.
					if _, err := owner.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
						t.Fatal(err)
					}
				},
			})
			db, err := sql.Open(name, path+"?_pragma=busy_timeout(1)")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			err = addColumn(db, "jobs", "name", "TEXT NOT NULL DEFAULT ''")
			switch {
			case tc.beginErr != nil:
				if !errors.Is(err, tc.beginErr) {
					t.Fatalf("unrelated BEGIN failure masked: %v", err)
				}
			case tc.readErr != nil:
				if !errors.Is(err, tc.readErr) {
					t.Fatalf("fallback schema-read failure lost: %v", err)
				}
			case tc.completed:
				if err != nil {
					t.Fatalf("completed column refused after writer contention: %v", err)
				}
			default:
				var sqliteErr interface{ Code() int }
				if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != 5 {
					t.Fatalf("absent column must retain SQLITE_BUSY: %v", err)
				}
			}
		})
	}
}

type migrationInterleaveDriver struct {
	base      driver.Driver
	afterRead func()
	beginErr  error
	readErr   error
}

func (d *migrationInterleaveDriver) Open(name string) (driver.Conn, error) {
	c, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &migrationInterleaveConn{Conn: c, settings: d}, nil
}

type migrationInterleaveConn struct {
	driver.Conn
	settings *migrationInterleaveDriver
	once     sync.Once
	reads    int
}

func (c *migrationInterleaveConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if query == "BEGIN IMMEDIATE" && c.settings.beginErr != nil {
		return nil, c.settings.beginErr
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *migrationInterleaveConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.reads++
	if c.reads > 1 && c.settings.readErr != nil {
		return nil, c.settings.readErr
	}
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return &migrationInterleaveRows{Rows: rows, afterClose: func() { c.once.Do(c.settings.afterRead) }}, nil
}

type migrationInterleaveRows struct {
	driver.Rows
	afterClose func()
}

func (r *migrationInterleaveRows) Close() error {
	err := r.Rows.Close()
	r.afterClose()
	return err
}
