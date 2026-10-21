package runner

import (
	"errors"
	"io/fs"
	"path/filepath"
	"time"
)

// Retain removes only terminal, fully delivered journals after fourteen days.
// Held, handed-off, unknown and inspectable runs retain all attempt artifacts
// and report-binary references. Binary pruning remains the supervisor's job.
func (m *Manager) Retain(now time.Time) error {
	// Forget terminal reservations while their journals still prove completion.
	// After deletion, a missing journal alone cannot authorize freeing a slot.
	m.mu.Lock()
	m.prune()
	m.mu.Unlock()
	ids, e := m.store.candidates()
	if e != nil {
		return e
	}
	for _, uid := range ids {
		lock := m.store.lock(uid)
		lock.Lock()
		e = func() error {
			r, e := m.store.load(uid)
			if missing(e) || m.store.quarantinedError(uid, e) {
				return nil
			}
			if e != nil {
				return e
			}
			if r.Phase != Settled || r.Outcome == "unknown" || r.Inspect != "" || r.EndedAt == nil || !r.EndedAt.Before(now.Add(-14*24*time.Hour)) {
				return nil
			}
			for _, op := range r.Ops {
				if !op.Landed() || op.SigningRefused {
					return nil
				}
			}
			return removeTree(m.store.fsys, m.store.layout.RunDir(uid))
		}()
		lock.Unlock()
		if e != nil {
			return e
		}
	}
	return nil
}

// Removal uses the same filesystem abstraction as transitions and never follows
// symlinks. Each directory removal is synced before continuing upward.
func removeTree(fsys interface {
	ReadDir(string) ([]fs.DirEntry, error)
	Remove(string) error
	SyncDir(string) error
}, path string) error {
	entries, e := fsys.ReadDir(path)
	if e != nil {
		return e
	}
	for _, entry := range entries {
		p := filepath.Join(path, entry.Name())
		if entry.IsDir() {
			e = removeTree(fsys, p)
		} else {
			e = fsys.Remove(p)
		}
		if e != nil && !errors.Is(e, fs.ErrNotExist) {
			return e
		}
	}
	if e = fsys.Remove(path); e != nil {
		return e
	}
	return fsys.SyncDir(filepath.Dir(path))
}
