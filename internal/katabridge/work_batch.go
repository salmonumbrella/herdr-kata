package katabridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/salmonumbrella/herdr-kata/internal/lockfile"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
)

// WorkBatch keeps installation-local retry progress. Its position is never
// execution authority and does not alter any selected work item's identity.
type WorkBatch struct {
	Dir, Name, Teammate string
	Scope               Scope
}
type workPosition struct {
	Version  int    `json:"version"`
	Scope    Scope  `json:"scope"`
	Name     string `json:"name"`
	Teammate string `json:"teammate,omitempty"`
	After    string `json:"after"`
}

func (b WorkBatch) Select(keys []string, limit int) ([]int, error) {
	if limit < 1 || limit > 100 || b.Dir == "" || b.Name == "" || b.Scope.TargetKey == "" || b.Scope.ProjectUID == "" || b.Scope.Actor == "" {
		return nil, errors.New("bounded scoped local work batch required")
	}
	if len(keys) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	for _, key := range keys {
		if key == "" || seen[key] {
			return nil, errors.New("local work identities must be nonempty and distinct")
		}
		seen[key] = true
	}
	f := workPosition{Version: 1, Scope: b.Scope, Name: b.Name, Teammate: b.Teammate}
	raw, _ := json.Marshal(f)
	sum := sha256.Sum256(raw)
	path := filepath.Join(b.Dir, "work-cursors", hex.EncodeToString(sum[:])+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock, err := lockfile.Acquire(path + ".lock")
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	if err := statefs.ReadJSON(path, 98304, &f); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if f.Version != 1 || f.Scope != b.Scope || f.Name != b.Name || f.Teammate != b.Teammate {
		return nil, errors.New("saved local work scope changed")
	}
	start := 0
	for i, key := range keys {
		if key == f.After {
			start = i + 1
			break
		}
	}
	out := make([]int, min(limit, len(keys)))
	for i := range out {
		out[i] = (start + i) % len(keys)
	}
	f.After = keys[out[len(out)-1]]
	if err := statefs.WriteJSON(path, f, 98304); err != nil {
		return nil, err
	}
	return out, nil
}
