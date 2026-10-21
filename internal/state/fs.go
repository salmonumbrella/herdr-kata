// Package state provides durable JSON files and process locks for runtime state.
package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

type File interface {
	io.Reader
	io.Writer
	io.Closer
	Name() string
	Stat() (fs.FileInfo, error)
	Sync() error
}
type FS interface {
	CreateTemp(dir, pattern string) (File, error)
	Rename(a, b string) error
	SyncDir(dir string) error
	Lstat(p string) (fs.FileInfo, error)
	Open(p string) (File, error)
	Remove(p string) error
	MkdirAll(p string, m fs.FileMode) error
	ReadDir(p string) ([]fs.DirEntry, error)
}

// OSFS uses the local filesystem. Open refuses symlinks and does not block on FIFOs.
type OSFS struct{}

func (OSFS) CreateTemp(d, p string) (File, error) { return os.CreateTemp(d, p) }
func (OSFS) Rename(a, b string) error             { return os.Rename(a, b) }
func (OSFS) SyncDir(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func (OSFS) Lstat(p string) (fs.FileInfo, error) { return os.Lstat(p) }
func (OSFS) Open(p string) (File, error) {
	return os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
func (OSFS) Remove(p string) error                   { return os.Remove(p) }
func (OSFS) MkdirAll(p string, m fs.FileMode) error  { return os.MkdirAll(p, m) }
func (OSFS) ReadDir(p string) ([]fs.DirEntry, error) { return os.ReadDir(p) }

// CorruptError means a file exists but cannot safely be used as state.
// I/O errors are returned unchanged; missing files remain fs.ErrNotExist.
type CorruptError struct {
	Path string
	Err  error
}

func (e *CorruptError) Error() string { return fmt.Sprintf("corrupt state %s: %v", e.Path, e.Err) }
func (e *CorruptError) Unwrap() error { return e.Err }

// WriteJSON atomically replaces path after syncing the new contents. A directory
// sync error is returned even though rename has already made the new file visible.
func WriteJSON(fsys FS, path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("null state is not allowed")
	}
	dir := filepath.Dir(path)
	if err := fsys.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := fsys.CreateTemp(dir, ".json-*")
	if err != nil {
		return err
	}
	defer fsys.Remove(f.Name()) //nolint:errcheck // Temporary-file removal remains best-effort.
	if _, err := io.Copy(f, bytes.NewReader(append(data, '\n'))); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := fsys.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncParents(fsys, dir)
}

var errFileChanged = errors.New("file changed while opening")

// ReadJSON accepts one bounded JSON value and rejects unknown struct fields.
// Atomic replacement between Lstat and Open makes up to three attempts. If the
// path keeps changing, return a local read error, not evidence of corrupt state.
func ReadJSON(fsys FS, path string, max int64, v any) error {
	if max <= 0 || max == int64(^uint64(0)>>1) {
		return fmt.Errorf("invalid JSON size limit %d", max)
	}
	for range 3 {
		if err := readJSON(fsys, path, max, v); !errors.Is(err, errFileChanged) {
			return err
		}
	}
	return &os.PathError{Op: "read", Path: path, Err: errFileChanged}
}

func readJSON(fsys FS, path string, max int64, v any) error {
	corrupt := func(err error) error { return &CorruptError{Path: path, Err: err} }
	info, err := fsys.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return corrupt(errors.New("not a regular file"))
	}
	if info.Size() > max {
		return corrupt(errors.New("size limit exceeded"))
	}
	f, err := fsys.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() {
		return corrupt(errors.New("not a regular file"))
	}
	if !os.SameFile(info, opened) {
		return errFileChanged
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > max {
		return corrupt(errors.New("size limit exceeded"))
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return corrupt(errors.New("null state is not allowed"))
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return corrupt(err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return corrupt(errors.New("trailing JSON data"))
	}
	return nil
}

func QuarantineRun(fsys FS, l Layout, runUID, path string) error {
	dir := l.QuarantineRunDir(runUID)
	if err := fsys.MkdirAll(dir, 0700); err != nil {
		return err
	}
	dst := filepath.Join(dir, strconv.FormatInt(time.Now().UnixNano(), 10)+"-run.json")
	if err := fsys.Rename(path, dst); err != nil {
		return err
	}
	// Persist both sides of a cross-directory rename.
	if err := syncParents(fsys, dir); err != nil {
		return err
	}
	return syncParents(fsys, filepath.Dir(path))
}

// Quarantined fails closed on unreadable quarantine state. Only an absent or
// confirmed empty directory permits a fresh preparation.
func Quarantined(fsys FS, l Layout, runUID string) bool {
	entries, err := fsys.ReadDir(l.QuarantineRunDir(runUID))
	return len(entries) > 0 || (err != nil && !errors.Is(err, fs.ErrNotExist))
}

// Sync the ancestry as well as the leaf, including on retries after a failed
// sync: an existing directory may have been created by an earlier failed write.
func syncParents(fsys FS, dir string) error {
	dir = filepath.Clean(dir)
	for {
		if err := fsys.SyncDir(dir); err != nil {
			return err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

// QuarantineRunDirectory preserves every artifact and uncertain operation in one
// rename. Retrying after the rename repeats both directory syncs.
func QuarantineRunDirectory(fsys FS, l Layout, uid string) error {
	src, dst := l.RunDir(uid), l.QuarantineRunDir(uid)
	source, err := fsys.Lstat(src)
	sourceMissing := errors.Is(err, fs.ErrNotExist)
	if err != nil && !sourceMissing {
		return err
	}
	dest, de := fsys.Lstat(dst)
	destMissing := errors.Is(de, fs.ErrNotExist)
	if de != nil && !destMissing {
		return de
	}
	if !sourceMissing && !destMissing {
		return errors.New("both run and quarantine directories exist")
	}
	if sourceMissing {
		if destMissing || !dest.IsDir() || !Quarantined(fsys, l, uid) {
			return errors.New("no quarantined run")
		}
	} else {
		if !source.IsDir() {
			return errors.New("run is not a directory")
		}
		if err = fsys.MkdirAll(filepath.Dir(dst), 0700); err != nil {
			return err
		}
		if err = fsys.Rename(src, dst); err != nil {
			return err
		}
	}
	if err = syncParents(fsys, filepath.Dir(dst)); err != nil {
		return err
	}
	return syncParents(fsys, filepath.Dir(src))
}

// LocalError distinguishes local files and durable state failures from a
// refused Kata lane. A local failure must stop the shared worker.
func LocalError(err error) bool {
	var path *os.PathError
	var link *os.LinkError
	var corrupt *CorruptError
	return errors.As(err, &path) || errors.As(err, &link) || errors.As(err, &corrupt)
}
