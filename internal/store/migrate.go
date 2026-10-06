package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func addColumn(db *sql.DB, table, column, ddl string) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Most opens find a current schema. Read first so concurrent no-op
	// migrations do not repeatedly compete for the writer lock. A missing
	// column is checked again below after acquiring that lock.
	has, err := connHasColumn(ctx, conn, table, column)
	if err != nil {
		return fmt.Errorf("migrate %s: %w", table, err)
	}
	if has {
		return nil
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		// Another opener may have installed this column while we waited for
		// its writer lock. Recognize that completed migration using a read;
		// an absent column or an unrelated error must still fail.
		var sqliteErr interface{ Code() int }
		if errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == 5 { // SQLITE_BUSY
			has, readErr := connHasColumn(ctx, conn, table, column)
			if readErr != nil {
				err = errors.Join(err, readErr)
			} else if has {
				return nil
			}
		}
		return fmt.Errorf("migrate %s.%s begin: %w", table, column, err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	has, err = connHasColumn(ctx, conn, table, column)
	if err != nil {
		return fmt.Errorf("migrate %s: %w", table, err)
	}
	if has {
		return nil
	}
	if _, err := conn.ExecContext(ctx,
		"ALTER TABLE "+table+" ADD COLUMN "+column+" "+ddl); err != nil {
		return fmt.Errorf("migrate %s: %w", table, err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("migrate %s: %w", table, err)
	}
	committed = true
	return nil
}

// connHasColumn is hasColumn asked inside a transaction, so the answer cannot
// change between the question and the ALTER that depends on it.
func connHasColumn(ctx context.Context, conn *sql.Conn, table, column string) (bool, error) {
	rows, err := conn.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// rejectLegacyWorkflowColumns prevents a naming change from silently adding empty
// replacement columns beside existing execution history. Import old snapshots
// explicitly into a fresh installation instead.
func rejectLegacyWorkflowColumns(db *sql.DB) error {
	conn, err := db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	for _, table := range []string{"jobs", "runs"} {
		for _, column := range []string{"flow_id", "flow_input"} {
			found, err := connHasColumn(context.Background(), conn, table, column)
			if err != nil {
				return err
			}
			if found {
				return fmt.Errorf("legacy flow columns in %s: retain this database with its matching older binary; use a fresh installation for workflows and import a stopped snapshot explicitly", table)
			}
		}
	}
	return nil
}
