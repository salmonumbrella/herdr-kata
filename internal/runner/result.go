package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

const maxResultBytes = 64 << 10

// Result is the agent's attempt-bound report. A report is evidence only after
// the caller has independently confirmed that the agent settled.
type Result struct {
	Version     int      `json:"version"`
	Run         string   `json:"run"`
	Step        string   `json:"step"`
	Attempt     int      `json:"attempt"`
	Status      string   `json:"status"`
	Summary     string   `json:"summary"`
	Note        string   `json:"note,omitempty"`
	Assert      string   `json:"assert,omitempty"`
	Target      string   `json:"target,omitempty"`
	Evidence    []string `json:"evidence"`
	CloseReason string   `json:"close_reason"`
}

func validateResult(r *Result) error {
	if r == nil || r.Version != 1 || !validUID(r.Run) || !validUID(r.Step) || r.Attempt < 1 {
		return errors.New("invalid result identity or schema")
	}
	if !validText(reflect.ValueOf(r)) {
		return errors.New("invalid UTF-8 in result")
	}
	if e := NormalizeAssertion(r); e != nil {
		return e
	}
	if e := defs.ValidateNote(r.Note); e != nil {
		return e
	}
	switch r.Status {
	case "succeeded", "failed", "needs-human":
	default:
		return fmt.Errorf("invalid result status %q", r.Status)
	}
	switch r.CloseReason {
	case "", "done", "audit-no-change":
	default:
		return fmt.Errorf("invalid close reason %q", r.CloseReason)
	}
	for _, e := range r.Evidence {
		kind, _, ok := strings.Cut(e, ":")
		if !ok {
			return fmt.Errorf("evidence %q: expected type:value", e)
		}
		switch kind {
		case "commit", "pr", "test", "reviewed-paths", "external", "no-change-audit":
		default:
			return fmt.Errorf("unknown result evidence type %q", kind)
		}
	}
	return nil
}

// WriteResult validates before atomically replacing the report. The cap includes
// JSON escaping and the newline written by state.WriteJSON.
func WriteResult(fsys state.FS, path string, r *Result) error {
	if err := validateResult(r); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(data)+1 > maxResultBytes {
		return errors.New("result exceeds 64 KiB")
	}
	return state.WriteJSON(fsys, path, r)
}

// ReadResult preserves missing/I/O errors and rejects corrupt, non-regular,
// oversized or stale reports. It never quarantines agent artifacts.
func ReadResult(fsys state.FS, path, runUID, step string, attempt int) (*Result, error) {
	var r Result
	if err := state.ReadJSON(fsys, path, maxResultBytes, &r); err != nil {
		return nil, err
	}
	if err := validateResult(&r); err != nil {
		return nil, &state.CorruptError{Path: path, Err: err}
	}
	if r.Run != runUID || r.Step != step || r.Attempt != attempt {
		return nil, &state.CorruptError{Path: path, Err: errors.New("result identity does not match attempt")}
	}
	return &r, nil
}

// NormalizeAssertion accepts only canonical full comment UIDs. Short handles
// cannot identify the exact maker claim across routes.
func NormalizeAssertion(r *Result) error {
	if r.Assert == "" && r.Target == "" {
		return nil
	}
	if r.Assert != "confirm" && r.Assert != "refute" {
		return errors.New("--assert requires confirm or refute and --target")
	}
	target := strings.TrimPrefix(strings.ToUpper(r.Target), "C:")
	if len(target) != 26 || target[0] > '7' || strings.IndexFunc(target, func(r rune) bool { return !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", r) }) >= 0 {
		return errors.New("--target requires a full comment ULID and --assert")
	}
	r.Target = target
	return nil
}
