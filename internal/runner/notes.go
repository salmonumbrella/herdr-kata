package runner

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
)

func previousNote(r *Run, key string) string {
	if edge := loopEdge(r); edge != nil && key == edge.Goto && r.Loop.Used > 0 {
		return r.Loop.Rejection
	}
	for i, s := range r.Steps {
		if s.Key == key {
			for j := i - 1; j >= 0; j-- {
				if r.Steps[j].Status != "" && r.Steps[j].Status != "skipped" {
					return r.Steps[j].Note
				}
			}
			break
		}
	}
	return ""
}
func (m *Manager) commandNote(r *Run, index int) (string, error) {
	s := r.Steps[index]
	if !certainReceipt(r.UID, s) {
		return "", errors.New("note requires a certain receipt")
	}
	path := filepath.Join(m.store.layout.AttemptDir(r.UID, s.Key, s.Attempt), "note.txt")
	info, e := m.store.fsys.Lstat(path)
	if missing(e) {
		return "", nil
	}
	if e != nil {
		return "", e
	}
	if !info.Mode().IsRegular() || info.Size() > defs.MaxNoteBytes+2 {
		return "", errors.New("invalid note file")
	}
	f, e := m.store.fsys.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil {
		return "", e
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return "", errors.New("note file changed")
	}
	b, e := io.ReadAll(io.LimitReader(f, defs.MaxNoteBytes+3))
	if e != nil {
		return "", e
	}
	note := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	return note, defs.ValidateNote(note)
}
