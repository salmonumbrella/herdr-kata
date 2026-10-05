package statefs

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

func ReadJSON(path string, limit int64, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > limit {
		return errors.New("local JSON file exceeds byte bound")
	}
	return json.Unmarshal(raw, out)
}

// WriteJSON syncs an owner-only replacement. Callers serialize competing writes
// using their existing local file lock; this is not distributed coordination.
func WriteJSON(path string, value any, limit int) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(raw) > limit {
		return errors.New("local JSON file exceeds byte bound")
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, Dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "state-*.json")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(File); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
