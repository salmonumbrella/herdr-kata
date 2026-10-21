package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
)

type layoutMarker struct {
	Version int `json:"version"`
}

// InitializeLayout requires worker.lock. Existing state is validated before use.
func InitializeLayout(f FS, l Layout) error {
	err := checkLayout(f, l)
	if errors.Is(err, fs.ErrNotExist) {
		return WriteJSON(f, l.StateFile(), layoutMarker{Version: 1})
	}
	return err
}

// LayoutPresent reports whether the current layout marker exists and is supported.
func LayoutPresent(f FS, l Layout) (bool, error) {
	err := checkLayout(f, l)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func checkLayout(f FS, l Layout) error {
	// Inspect the version before applying the current marker's shape. Older
	// layouts carry pin or migration fields that a strict marker decode rejects.
	var fields map[string]json.RawMessage
	if err := ReadJSON(f, l.StateFile(), 64<<10, &fields); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return RepairError(fmt.Errorf("cannot read state layout %s: %w", l.StateFile(), err), l)
	}
	corrupt := func(err error) error {
		return RepairError(&CorruptError{Path: l.StateFile(), Err: err}, l)
	}
	var version int
	if err := json.Unmarshal(fields["version"], &version); err != nil {
		return corrupt(err)
	}
	if version < 1 {
		return corrupt(errors.New("missing or invalid state layout version"))
	}
	if version != 1 {
		return incompatibleLayout(l, fmt.Sprintf("unsupported state layout version %d", version))
	}
	// The old single-project root was an installation pin. Its creation field
	// identifies the format; its values are never used, even with an empty project UID.
	if _, legacy := fields["state_created_at"]; legacy {
		return incompatibleLayout(l, "legacy single-project state layout")
	}
	if len(fields) != 1 {
		return corrupt(errors.New("unexpected fields in current state layout"))
	}
	return nil
}

func incompatibleLayout(l Layout, detail string) error {
	return fmt.Errorf("incompatible state layout %s: %s; move the state directory aside and run herdr-kata up again", l.StateFile(), detail)
}

func SafeComponent(s string) bool { return s != "" && s != "." && s != ".." && filepath.Base(s) == s }

// ProjectLayouts reads only project-scoped pins from the current layout.
func ProjectLayouts(f FS, l Layout) ([]Layout, error) {
	if err := checkLayout(f, l); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	entries, err := f.ReadDir(filepath.Join(l.Root, "projects"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var layouts []Layout
	for _, entry := range entries {
		if entry.IsDir() && SafeComponent(entry.Name()) {
			project := l.Project(entry.Name())
			if _, err := f.Lstat(project.StateFile()); errors.Is(err, fs.ErrNotExist) {
				continue
			} else if err != nil {
				return nil, err
			}
			layouts = append(layouts, project)
		}
	}
	return layouts, nil
}

// RepairError adds preservation advice for damaged state whose occupants and
// artifact paths must be resolved before a fresh state directory is safe.
func RepairError(err error, l Layout) error {
	if _, corrupt := errors.AsType[*CorruptError](err); corrupt {
		return fmt.Errorf("%w; herdr-kata stop; preserve original artifact paths under %s; restore damaged state from trusted evidence or explicitly resolve existing occupants before initializing a separate state directory", err, l.Root)
	}
	return err
}
