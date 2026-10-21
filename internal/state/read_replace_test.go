package state

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Replace the real file at the exact boundary used by the supervisor's writer:
// after the reader's Lstat, before its Open. No scheduling window is required.
type replaceOnOpenFS struct {
	OSFS
	replacements, opens int
	value               any
}

func (f *replaceOnOpenFS) Open(path string) (File, error) {
	f.opens++
	if f.replacements > 0 {
		f.replacements--
		if err := WriteJSON(OSFS{}, path, f.value); err != nil {
			return nil, err
		}
	}
	return f.OSFS.Open(path)
}

func TestReadJSONRetriesAtomicReplacement(t *testing.T) {
	for _, replacements := range []int{1, 2} {
		t.Run(strconv.Itoa(replacements), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "serve.json")
			if err := WriteJSON(OSFS{}, path, record{1, "old"}); err != nil {
				t.Fatal(err)
			}
			fsys := &replaceOnOpenFS{replacements: replacements, value: record{1, "replacement"}}
			var got record
			if err := ReadJSON(fsys, path, 1024, &got); err != nil || got != (record{1, "replacement"}) {
				t.Fatalf("read = %+v, %v", got, err)
			}
		})
	}
}

func TestReadJSONValidatesAtomicReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve.json")
	if err := WriteJSON(OSFS{}, path, record{1, "old"}); err != nil {
		t.Fatal(err)
	}
	fsys := &replaceOnOpenFS{replacements: 1, value: map[string]int{"extra": 1}}
	var got record
	err := ReadJSON(fsys, path, 1024, &got)
	var corrupt *CorruptError
	if !errors.As(err, &corrupt) || corrupt.Err.Error() != `json: unknown field "extra"` {
		t.Fatalf("expected replacement's invalid JSON to be rejected: %v", err)
	}
}

func TestReadJSONReplacementRetryIsBoundedAndNotCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve.json")
	if err := WriteJSON(OSFS{}, path, record{1, "old"}); err != nil {
		t.Fatal(err)
	}
	fsys := &replaceOnOpenFS{replacements: 10, value: record{1, "replacement"}}
	got := record{1, "untouched"}
	err := ReadJSON(fsys, path, 1024, &got)
	var corrupt *CorruptError
	var read *os.PathError
	if err == nil || errors.As(err, &corrupt) || !errors.As(err, &read) || read.Op != "read" || read.Path != path || !LocalError(err) {
		t.Fatalf("expected local read error, not corruption: %v", err)
	}
	if fsys.opens != 3 || got != (record{1, "untouched"}) {
		t.Fatalf("opens = %d, value = %+v", fsys.opens, got)
	}
}
