package daemon

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

type LogWriter struct {
	mu    sync.Mutex
	path  string
	limit int64
	keep  int
	file  *os.File
	size  int64
}

func NewLogWriter(path string) (*LogWriter, error) { return newLogWriter(path, 5<<20, 5) }
func newLogWriter(path string, limit int64, keep int) (*LogWriter, error) {
	w := &LogWriter{path: path, limit: limit, keep: keep}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	err := w.open()
	return w, err
}
func (w *LogWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	i, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.file = f
	w.size = i.Size()
	return nil
}
func (w *LogWriter) rotate() error {
	if err := w.file.Close(); err != nil {
		return err
	}
	w.file = nil
	for i := w.keep - 1; i >= 1; i-- {
		src := w.path
		if i > 1 {
			src += "." + strconv.Itoa(i-1)
		}
		if err := os.Rename(src, w.path+"."+strconv.Itoa(i)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return w.open()
}
func (w *LogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	total := 0
	for len(p) > 0 {
		if w.file == nil {
			if err := w.open(); err != nil {
				return total, err
			}
		}
		if w.size >= w.limit {
			if err := w.rotate(); err != nil {
				return total, err
			}
		}
		n := min(int64(len(p)), w.limit-w.size)
		written, err := w.file.Write(p[:n])
		total += written
		w.size += int64(written)
		p = p[written:]
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
func (w *LogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
