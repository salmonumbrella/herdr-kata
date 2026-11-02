package journal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type Request struct{ ID, Kind, Project, JobUID, Run, Input string }
type Requests struct{ Dir string }

func safeID(s string) bool {
	return s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") == ""
}
func (q Requests) Write(r Request) error {
	if !safeID(r.ID) {
		return fmt.Errorf("invalid request id")
	}
	if err := state.EnsureDir(q.Dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(q.Dir, ".request-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(tmp) }()
	if err = state.WriteJSON(tmp, r); err != nil {
		return err
	}
	if err = os.Link(tmp, filepath.Join(q.Dir, r.ID+".json")); err != nil {
		return err
	}
	return state.SyncDir(q.Dir)
}
func (q Requests) Pending() ([]Request, error) {
	entries, err := os.ReadDir(q.Dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Request
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(q.Dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var r Request
		if err = json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		if !safeID(r.ID) || e.Name() != r.ID+".json" {
			return nil, fmt.Errorf("request identity mismatch")
		}
		out = append(out, r)
	}
	return out, nil
}
func (q Requests) Done(id string) error {
	if !safeID(id) {
		return fmt.Errorf("invalid request id")
	}
	if err := os.Rename(filepath.Join(q.Dir, id+".json"), filepath.Join(q.Dir, id+".done")); err != nil {
		return err
	}
	return state.SyncDir(q.Dir)
}
