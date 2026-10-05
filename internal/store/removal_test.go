package store

import (
	"strings"
	"testing"
)

func TestFreshSchemaContainsOnlyExecutionLeaseAndDerivedNativeState(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	allowed := map[string]bool{"native_definitions": true, "jobs": true, "runs": true, "run_steps": true, "run_events": true, "run_event_deliveries": true, "job_sessions": true, "lease_events": true, "sqlite_sequence": true}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !allowed[name] {
			t.Errorf("unexpected collaboration table %s", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"runs", "run_steps"} {
		rs, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			t.Fatal(err)
		}
		for rs.Next() {
			var cid, notnull, pk int
			var name, kind string
			var def any
			if err := rs.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(name, "thread") || strings.Contains(name, "check") {
				t.Errorf("legacy execution reference %s.%s", table, name)
			}
		}
		rs.Close()
	}
}
