package prompt

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/salmonumbrella/herdr-kata/internal/session"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type Result struct {
	Session *session.Session `json:"session,omitempty"`
	RunUID  string           `json:"run_uid"`
	Step    string           `json:"step"`
	Attempt int              `json:"attempt"`
	Status  string           `json:"status"`
	Summary string           `json:"summary"`
	Note    string           `json:"note,omitempty"`
}

func ValidNote(note string) bool {
	return utf8.ValidString(note) && len(note) <= 280 && !strings.ContainsAny(note, "\r\n")
}
func ReadRegular(path string, max int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > max {
		return nil, fmt.Errorf("invalid regular file or size: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max || !utf8.Valid(b) {
		return nil, fmt.Errorf("invalid file size or UTF-8")
	}
	return b, nil
}
func validate(r Result) error {
	if r.RunUID == "" || r.Step == "" || r.Attempt < 1 {
		return fmt.Errorf("invalid result identity")
	}
	switch r.Status {
	case "succeeded", "failed", "needs-human":
	default:
		return fmt.Errorf("invalid result status")
	}
	if !ValidNote(r.Note) {
		return fmt.Errorf("invalid_note")
	}
	return nil
}
func ReadResult(path, runUID, step string, n int) (Result, error) {
	b, err := ReadRegular(path, 64*1024)
	if err != nil {
		return Result{}, err
	}
	return ParseResult(b, runUID, step, n)
}
func ParseResult(b []byte, runUID, step string, n int) (Result, error) {
	var r Result
	type resultAlias Result
	wire := struct {
		*resultAlias
		Session json.RawMessage `json:"session"`
	}{resultAlias: (*resultAlias)(&r)}
	if err := json.Unmarshal(b, &wire); err != nil {
		return r, err
	}
	var captured session.Session
	if json.Unmarshal(wire.Session, &captured) == nil && captured.Valid() {
		r.Session = &captured
	}
	if r.RunUID != runUID || r.Step != step || r.Attempt != n {
		return r, fmt.Errorf("stale result identity")
	}
	r.Note = NormalizeNote(r.Note)
	return r, validate(r)
}
func WriteResult(path string, r Result) error {
	if err := validate(r); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(b)+1 > 64*1024 {
		if r.Session == nil {
			return fmt.Errorf("result exceeds 64 KiB")
		}
		r.Session = nil
		b, err = json.Marshal(r)
		if err != nil {
			return err
		}
		if len(b)+1 > 64*1024 {
			return fmt.Errorf("result exceeds 64 KiB")
		}
	}
	return state.WriteJSON(path, r)
}
