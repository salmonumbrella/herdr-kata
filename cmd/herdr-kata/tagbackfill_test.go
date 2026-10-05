package main

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/store"
	_ "modernc.org/sqlite"
)

// Opening the store normalises tags left over from before they were sanitised
// on write, and says how many rows it touched.
//
// The row is dirtied with raw SQL on purpose: PutJob sanitises, so there is no
// way through the store's own API to produce the state an older binary left
// behind, and a test that cannot produce that state proves nothing about the
// backfill.
func TestOpenStoreReportsTagBackfill(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_KATA_HOME", dir)
	ctx := context.Background()

	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutJob(ctx, store.Job{ID: "t1", Name: "tagged", Model: store.DefaultModel}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	db, err := sql.Open("sqlite", filepath.Join(dir, "herdr-kata.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE jobs SET tags=? WHERE id=?`,
		"  Marketing , marketing,, daily  ,DAILY", "t1"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s2, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.TagsNormalized != 1 {
		t.Fatalf("TagsNormalized = %d, want 1", s2.TagsNormalized)
	}
	// Legacy normalization remains an adoption concern; product reads stay native.
	if jobs, e := s2.Jobs(ctx); e != nil || len(jobs) != 0 {
		t.Fatalf("legacy rows exposed through product: %+v %v", jobs, e)
	}
	s2.Close()
	legacy, e := store.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer legacy.Close()
	got, err := legacy.Job(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"marketing", "daily"}; !reflect.DeepEqual(got.Tags, want) {
		t.Fatalf("tags after backfill = %#v, want %#v", got.Tags, want)
	}
}

func TestReportTagBackfill(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, ""},
		{1, "herdr-kata: normalised tags on 1 job\n"},
		{4, "herdr-kata: normalised tags on 4 jobs\n"},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		reportTagBackfill(&buf, c.n)
		if buf.String() != c.want {
			t.Errorf("reportTagBackfill(%d) = %q, want %q", c.n, buf.String(), c.want)
		}
	}
}
