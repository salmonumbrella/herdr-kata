// Package migration adopts the former plugin ID without cloning its identity.
package migration

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/salmonumbrella/herdr-kata/internal/crashpoint"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

const legacyID = "salmonumbrella.herdr-kata"

type move struct {
	Source  string `json:"source"`
	Target  string `json:"target"`
	Present bool   `json:"present"`
}
type journal struct {
	Version  int  `json:"version"`
	Config   move `json:"config"`
	State    move `json:"state"`
	Complete bool `json:"complete"`
}

func paths(configDir, stateDir string) (journal, string, bool) {
	configDir, stateDir = filepath.Clean(configDir), filepath.Clean(stateDir)
	if !filepath.IsAbs(configDir) || !filepath.IsAbs(stateDir) || filepath.Base(configDir) != "kata" || filepath.Base(stateDir) != "kata" || configDir == stateDir {
		return journal{}, "", false
	}
	j := journal{Version: 1,
		Config: move{Source: filepath.Join(filepath.Dir(configDir), legacyID), Target: configDir},
		State:  move{Source: filepath.Join(filepath.Dir(stateDir), legacyID), Target: stateDir},
	}
	return j, filepath.Join(filepath.Dir(stateDir), "kata-id-migration.json"), true
}

func load(want journal, marker string) (journal, bool, error) {
	var j journal
	err := state.ReadJSON(state.OSFS{}, marker, 16<<10, &j)
	if errors.Is(err, fs.ErrNotExist) {
		return want, false, nil
	}
	if err != nil {
		return j, true, err
	}
	if j.Version != 1 || j.Config.Source != want.Config.Source || j.Config.Target != want.Config.Target || j.State.Source != want.State.Source || j.State.Target != want.State.Target {
		return j, true, errors.New("migration marker differs from selected directories; restore the original directory overrides")
	}
	return j, true, nil
}

func directory(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("%s is not a directory; inspect migration paths before starting", path)
	}
	return true, nil
}
func empty(path string) error {
	entries, err := os.ReadDir(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("migration destination %s is not empty; keep both installations stopped and resolve it manually (no merge or overwrite)", path)
	}
	// A destination symlink is never replaced, even when its target is empty.
	_, err = directory(path)
	return err
}
func prepare(j journal) (journal, bool, error) {
	var err error
	j.Config.Present, err = directory(j.Config.Source)
	if err != nil {
		return j, false, err
	}
	j.State.Present, err = directory(j.State.Source)
	if err != nil {
		return j, false, err
	}
	if !j.Config.Present && !j.State.Present {
		return j, false, nil
	}
	for _, m := range []move{j.Config, j.State} {
		if err := empty(m.Target); err != nil {
			return j, true, err
		}
	}
	return j, true, nil
}
func alias(m move) bool {
	target, err := os.Readlink(m.Source)
	return err == nil && filepath.Clean(filepath.Join(filepath.Dir(m.Source), target)) == m.Target
}
func validateComplete(j journal) error {
	for _, m := range []move{j.Config, j.State} {
		if !m.Present {
			continue
		}
		if !alias(m) {
			return fmt.Errorf("legacy alias %s is missing or changed; restore its link to %s before starting", m.Source, m.Target)
		}
		if exists, err := directory(m.Target); err != nil || !exists {
			if err != nil {
				return err
			}
			return fmt.Errorf("migrated directory %s is missing; restore it before starting", m.Target)
		}
	}
	return nil
}

// Inspect explains migration health without creating files or taking locks.
func Inspect(configDir, stateDir string) (string, error) {
	want, marker, ok := paths(configDir, stateDir)
	if !ok {
		return "custom directories; automatic plugin ID migration does not apply", nil
	}
	j, found, err := load(want, marker)
	if err != nil {
		return "migration marker unreadable", err
	}
	if found {
		if !j.Complete {
			return "interrupted plugin ID migration; run the new herdr-kata up before starting either installation", errors.New("migration incomplete")
		}
		if err := validateComplete(j); err != nil {
			return "plugin ID migration needs repair", err
		}
		return "complete: kata owns config/state; legacy aliases preserve saved artifact paths; keep only one plugin enabled", nil
	}
	_, needed, err := prepare(j)
	if err != nil {
		return "plugin ID migration blocked", err
	}
	if needed {
		return "pending: disable the old plugin, stop its runtime, then run the new herdr-kata up", errors.New("migration pending")
	}
	return "kata installation; no legacy directories to migrate", nil
}

// Ensure moves only into empty new-ID directories. The old plugin must be
// disabled and stopped first. After a crash, restart the NEW binary to finish
// the transaction before allowing the old binary to use its legacy paths.
func Ensure(configDir, stateDir string) error {
	want, marker, ok := paths(configDir, stateDir)
	if !ok {
		return nil
	}
	j, found, err := load(want, marker)
	if err != nil {
		return err
	}
	if found && j.Complete {
		return validateComplete(j)
	}
	if !found {
		var needed bool
		j, needed, err = prepare(j)
		if err != nil || !needed {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0700); err != nil {
		return err
	}
	gate, ok, err := state.TryLock(filepath.Join(filepath.Dir(marker), "kata-id-migration.lock"))
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("plugin ID migration is already in progress; retry up after it finishes")
	}
	defer gate.Unlock() //nolint:errcheck // Lock cleanup remains best-effort.
	j, found, err = load(want, marker)
	if err != nil {
		return err
	}
	if found && j.Complete {
		return validateComplete(j)
	}
	if !found {
		var needed bool
		j, needed, err = prepare(j)
		if err != nil || !needed {
			return err
		}
	}
	// These inodes move with the tree. Retaining all three excludes old starts,
	// supervisors and workers through publication of the compatibility aliases.
	var locks []*state.Lock
	defer func() {
		for _, l := range locks {
			_ = l.Unlock()
		}
	}()
	if j.State.Present {
		root := j.State.Source
		if alias(j.State) {
			root = j.State.Target
		} else if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
			root = j.State.Target
		}
		for _, name := range []string{"up.lock", "serve.lock", "worker.lock"} {
			lock, held, err := state.TryLock(filepath.Join(root, name))
			if err != nil {
				return err
			}
			if !held {
				return errors.New("legacy runtime is active; disable the old plugin and stop it before migration")
			}
			locks = append(locks, lock)
		}
	}
	if !found {
		if err := state.WriteJSON(state.OSFS{}, marker, j); err != nil {
			return err
		}
		crashpoint.Hit("migration-intent")
	}
	for i, m := range []move{j.Config, j.State} {
		kind := "config"
		if i == 1 {
			kind = "state"
		}
		if err := adopt(m, kind); err != nil {
			return err
		}
	}
	j.Complete = true
	if err := state.WriteJSON(state.OSFS{}, marker, j); err != nil {
		return err
	}
	crashpoint.Hit("migration-complete")
	return validateComplete(j)
}

func adopt(m move, kind string) error {
	if !m.Present {
		return nil
	}
	if alias(m) {
		if exists, err := directory(m.Target); err != nil {
			return err
		} else if !exists {
			return fmt.Errorf("missing migration destination %s", m.Target)
		}
		return nil
	}
	exists, err := directory(m.Source)
	if err != nil {
		return err
	}
	if exists {
		if err := empty(m.Target); err != nil {
			return err
		}
		// Herdr creates empty destination directories at install time. Remove
		// only an empty directory; Remove refuses data that appeared meanwhile.
		if err := os.Remove(m.Target); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		crashpoint.Hit("migration-" + kind + "-empty-removed")
		if err := os.Rename(m.Source, m.Target); err != nil {
			return err
		}
		if err := (state.OSFS{}).SyncDir(filepath.Dir(m.Target)); err != nil {
			return err
		}
		crashpoint.Hit("migration-" + kind + "-moved")
	} else if exists, err := directory(m.Target); err != nil {
		return err
	} else if !exists {
		return fmt.Errorf("both migration paths are missing: %s and %s; restore from backup", m.Source, m.Target)
	}
	if err := os.Symlink(filepath.Base(m.Target), m.Source); err != nil {
		return err
	}
	if err := (state.OSFS{}).SyncDir(filepath.Dir(m.Source)); err != nil {
		return err
	}
	crashpoint.Hit("migration-" + kind + "-linked")
	return nil
}
