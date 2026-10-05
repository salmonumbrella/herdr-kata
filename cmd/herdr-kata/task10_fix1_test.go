package main

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

// This fixture uses the authentic upstream filename and test-local legacy DDL,
// not the current renamed writer, so it can detect skipping every source job.
func TestRealTask10Fix1AuthenticBermudaSnapshot(t *testing.T) {
	c, s := realNativeProduct(t)
	source := t.TempDir()
	path := filepath.Join(source, "bermuda.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE jobs (id TEXT PRIMARY KEY,prompt TEXT NOT NULL,cwd TEXT NOT NULL DEFAULT '',kind TEXT NOT NULL DEFAULT 'claude',created_at INTEGER NOT NULL,name TEXT NOT NULL DEFAULT '',enabled INTEGER NOT NULL DEFAULT 1); INSERT INTO jobs VALUES ('inspect','Inspect workspace','/old/local/checkout','claude',1,'Inspect',1); CREATE TABLE runs (id TEXT,job_id TEXT,tab_id TEXT); INSERT INTO runs VALUES ('old-run','inspect','old-tab'); CREATE TABLE memory (body TEXT); INSERT INTO memory VALUES ('legacy memory');`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := sourceBytes(t, source)
	argv := []string{"import", "--source", source, "--source-id", "authentic-installation", "--checkout-key", "primary"}
	if _, err := captureStdout(t, func() error { return nativeCmd(argv) }); err != nil {
		realNativeFailure(t, err)
	}
	jobs, err := s.Jobs(t.Context())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("authentic upstream jobs omitted: %+v %v", jobs, err)
	}
	var document struct{ Enabled bool }
	if err := json.Unmarshal(jobs[0].NativeDefinition, &document); err != nil {
		t.Fatal(err)
	}
	if document.Enabled || jobs[0].Enabled || jobs[0].CWD != c.Target.Workspace || jobs[0].Prompt != "Inspect workspace" {
		t.Fatalf("unsafe authentic job adoption: %+v", jobs[0])
	}
	uid, winner := jobs[0].ID, jobs[0].NativeEventUID
	if _, err := captureStdout(t, func() error { return nativeCmd(argv) }); err != nil {
		realNativeFailure(t, err)
	}
	jobs, err = s.Jobs(t.Context())
	if err != nil || len(jobs) != 1 || jobs[0].ID != uid || jobs[0].NativeEventUID != winner {
		t.Fatalf("authentic retry changed identity/winner: %+v %v", jobs, err)
	}
	if after := sourceBytes(t, source); !reflect.DeepEqual(before, after) {
		t.Fatal("authentic source bytes or entries changed")
	}
	runs, err := s.Runs(t.Context(), "", 100)
	if err != nil || len(runs) != 0 {
		t.Fatalf("source runtime history adopted: %+v %v", runs, err)
	}
	var history struct{ Runs []any }
	if err := c.Runs(t.Context(), "", "", &history); err != nil || len(history.Runs) != 0 {
		t.Fatalf("source history published: %+v %v", history, err)
	}
}
