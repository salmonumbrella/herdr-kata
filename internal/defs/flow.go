package defs

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const MaxInputBytes = 4 << 10
const MaxNoteBytes = 280
const MaxPromptBytes = 256 << 10

func ValidateInput(s string) error {
	if len(s) > MaxInputBytes || !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return fmt.Errorf("input must be UTF-8 without NUL, at most 4 KiB")
	}
	return nil
}
func ValidateNote(s string) error {
	if len(s) > MaxNoteBytes || !utf8.ValidString(s) || strings.ContainsAny(s, "\x00\r\n\u0085\u2028\u2029") {
		return fmt.Errorf("note must be one UTF-8 line without NUL, at most 280 bytes")
	}
	return nil
}
func ValidatePlaceholders(s string, first bool) error {
	for {
		_, rest, found := strings.Cut(s, "{{")
		if !found {
			return nil
		}
		key, tail, closed := strings.Cut(rest, "}}")
		if !closed {
			return fmt.Errorf("unclosed placeholder")
		}
		if key != "input" && key != "previous" {
			return fmt.Errorf("unsupported placeholder {{%s}}", key)
		}
		if first && key == "previous" {
			return fmt.Errorf("{{previous}} unsupported in first step")
		}
		s = tail
	}
}
func JobInput(raw json.RawMessage) (*string, error) {
	var opts map[string]json.RawMessage
	if len(raw) == 0 {
		return nil, nil
	}
	if e := json.Unmarshal(raw, &opts); e != nil {
		return nil, e
	}
	v, ok := opts["input"]
	if !ok {
		return nil, nil
	}
	var value string
	if string(v) == "null" {
		return nil, fmt.Errorf("options.input must be a string")
	}
	if e := json.Unmarshal(v, &value); e != nil {
		return nil, e
	}
	return &value, ValidateInput(value)
}
func FreshAgent(s Step) bool {
	return OptionString(s.Options, "agent_mode") == "fresh"
}

// StepProfile returns the profile a step runs on: its own options.agent, else the run's.
func StepProfile(s Step, run string) string {
	if p := OptionString(s.Options, "agent"); p != "" {
		return p
	}
	return run
}

// FreshStep reports whether a step needs its own agent: fresh by option or by profile.
func FreshStep(s Step, run string) bool {
	return FreshAgent(s) || StepProfile(s, run) != run
}

var ErrInput = errors.New("invalid workflow input")

// EffectiveInput applies precedence before checking a workflow's requirement.
func EffectiveInput(options json.RawMessage, manual, child *string, required bool) (string, error) {
	input, e := JobInput(options)
	if e != nil {
		return "", fmt.Errorf("%w: %v", ErrInput, e)
	}
	if child != nil {
		input = child
	}
	if manual != nil {
		input = manual
	}
	value := ""
	if input != nil {
		value = *input
	}
	if e = ValidateInput(value); e != nil {
		return "", fmt.Errorf("%w: %v", ErrInput, e)
	}
	if required && value == "" {
		return "", fmt.Errorf("%w: workflow requires input", ErrInput)
	}
	return value, nil
}

func ParkFailure(w *WorkflowDef) bool {
	return w != nil && OptionString(w.Options, "on_failure") == "park"
}
