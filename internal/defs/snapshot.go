package defs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"unicode/utf8"
)

// ReadSnapshot supports the previous summary-only array during worker upgrades.
func ReadSnapshot(fs state.FS, path string) (Snapshot, error) {
	var raw json.RawMessage
	if err := state.ReadJSON(fs, path, 16<<20, &raw); err != nil {
		return Snapshot{}, err
	}
	raw = bytes.TrimSpace(raw)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var s Snapshot
	if len(raw) > 0 && raw[0] == '[' {
		s.Version = 1
		if err := decoder.Decode(&s.Jobs); err != nil {
			return s, &state.CorruptError{Path: path, Err: err}
		}
		return s, nil
	}
	if err := decoder.Decode(&s); err != nil {
		return s, &state.CorruptError{Path: path, Err: err}
	}
	if s.Version != 2 {
		return s, fmt.Errorf("unsupported definitions snapshot version %d", s.Version)
	}
	return s, nil
}

// Invalid definitions stay visible without making siblings unpersistable.
func snapshotDefinition(raw json.RawMessage) json.RawMessage {
	if utf8.Valid(raw) && json.Valid(raw) {
		return raw
	}
	encoded, _ := json.Marshal(string(raw))
	return encoded
}
