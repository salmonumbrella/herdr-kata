package store

import (
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

var base = time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC)
