package daemon

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// Unknown or unreadable journals veto pruning. Later run retention can narrow
// this conservative set once it owns the run schema.
func prune(l state.Layout, current string) error {
	keep := map[string]bool{current: true}
	var info ServeInfo
	err := state.ReadJSON(state.OSFS{}, l.ServeInfo(), 4096, &info)
	if err == nil {
		keep[info.Exe] = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	held, err := heldExisting(l.WorkerLock())
	if err != nil {
		return err
	}
	serving, err := heldExisting(l.ServeLock())
	if err != nil {
		return err
	}
	if held && (!serving || info.WorkerPID == 0) {
		return nil
	} // An orphan may use an older executable.
	var u Upgrade
	err = state.ReadJSON(state.OSFS{}, l.UpgradeFile(), 4096, &u)
	if err == nil {
		keep[u.Exe] = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	var collect func(any)
	collect = func(v any) {
		switch v := v.(type) {
		case string:
			dir := filepath.Dir(v)
			if dir == l.BinDir() {
				keep[v] = true
				return
			}
			// ID migration retains a legacy directory alias. Frozen reporters
			// through that alias still refer to executables in this bin tree.
			if !strings.HasPrefix(filepath.Base(v), "herdr-kata-") {
				return
			}
			savedDir, savedErr := os.Stat(dir)
			binDir, binErr := os.Stat(l.BinDir())
			if savedErr == nil && binErr == nil && os.SameFile(savedDir, binDir) {
				keep[filepath.Join(l.BinDir(), filepath.Base(v))] = true
			}
		case []any:
			for _, e := range v {
				collect(e)
			}
		case map[string]any:
			for _, e := range v {
				collect(e)
			}
		}
	}
	for _, dir := range []string{filepath.Join(l.Root, "runs"), filepath.Join(l.Root, "quarantine")} {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, e error) error {
			if errors.Is(e, fs.ErrNotExist) && p == dir {
				return nil
			}
			if e != nil {
				return e
			}
			if d.IsDir() {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				return errors.New("unproven run state")
			}
			if strings.HasSuffix(d.Name(), "run.json") {
				var v map[string]any
				if err := state.ReadJSON(state.OSFS{}, p, 4<<20, &v); err != nil {
					return err
				}
				collect(v)
			}
			return nil
		})
		if err != nil {
			return nil
		}
	}
	entries, err := os.ReadDir(l.BinDir())
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(l.BinDir(), e.Name())
		if !keep[p] && strings.HasPrefix(e.Name(), "herdr-kata-") && e.Type().IsRegular() {
			if err := os.Remove(p); err != nil {
				return err
			}
		}
	}
	return (state.OSFS{}).SyncDir(l.BinDir())
}
