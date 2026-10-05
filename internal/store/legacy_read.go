package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ReadLegacyJobs reads a stopped, checkpointed upstream snapshot. It deliberately
// never calls Open: no DDL, backfill, chmod, WAL setup, or runtime state adoption.
func ReadLegacyJobs(ctx context.Context, path string) ([]Job, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(absolute); err != nil {
		return nil, err
	}
	if info, err := os.Stat(absolute + "-wal"); err == nil && info.Size() > 0 {
		return nil, errors.New("source has a WAL; supply a stopped, checkpointed SQLite backup")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	db, err := sql.Open("sqlite", legacySQLiteURI(filepath.ToSlash(absolute)))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(jobs)`)
	if err != nil {
		return nil, err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
			rows.Close()
			return nil, err
		}
		columns[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, required := range []string{"id", "prompt"} {
		if !columns[required] {
			return nil, fmt.Errorf("upstream jobs missing %s", required)
		}
	}
	var selectColumns []string
	numeric := map[string]bool{"interval_seconds": true, "timeout_ms": true, "enabled": true, "favorite": true, "persistent": true, "keep_context": true, "created_at": true, "updated_at": true, "skip_permissions": true}
	defaults := map[string]string{"kind": "'claude'", "model": "'sonnet'", "schedule_type": "'manual'", "catchup": "'latest'", "on_context_loss": "'fresh'", "run_at": "NULL"}
	for _, column := range strings.Split(jobColumns, ",") {
		column = strings.TrimSpace(column)
		if columns[column] {
			selectColumns = append(selectColumns, column)
			continue
		}
		fallback := "''"
		if numeric[column] {
			fallback = "0"
		}
		if value, ok := defaults[column]; ok {
			fallback = value
		}
		selectColumns = append(selectColumns, fallback)
	}
	rows, err = db.QueryContext(ctx, "SELECT "+strings.Join(selectColumns, ",")+" FROM jobs ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// legacySQLiteURI accepts an absolute slash-separated filesystem path. The
// leading slash keeps a Windows drive in Path rather than the URI authority.
func legacySQLiteURI(slash string) string {
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	uri := url.URL{Scheme: "file", Path: slash, RawQuery: "mode=ro&immutable=1"}
	return uri.String()
}
