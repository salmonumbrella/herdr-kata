package kata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Error struct {
	Code, Message               string
	ExistingUID, ExistingHandle string
	Exit                        int
	CandidateUIDs               []string
	Raw                         json.RawMessage
}

func (e *Error) Error() string {
	return fmt.Sprintf("kata %s (exit %d): %s", e.Code, e.Exit, e.Message)
}

type RetryClass int

const (
	Retry RetryClass = iota
	Conflict
	Fatal
	SigningRefused
)

type Safety int

const (
	ReplaySafe Safety = iota
	Guarded
	Unguarded
	ReadBack
)

func decodeError(stdout, stderr []byte, exit int, cause error) error {
	for _, raw := range [][]byte{stdout, stderr} {
		var envelope struct {
			Error *struct {
				Code    string `json:"code"`
				Kind    string `json:"kind"`
				Message string `json:"message"`
				Exit    int    `json:"exit_code"`
				Data    struct {
					UID            string `json:"uid"`
					ExistingUID    string `json:"existing_uid"`
					ExistingHandle string `json:"existing_handle"`
					Candidates     []struct {
						UID string `json:"uid"`
					} `json:"candidates"`
				} `json:"data"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &envelope) != nil || envelope.Error == nil {
			continue
		}
		x := envelope.Error
		code := x.Code
		if code == "" {
			code = x.Kind
		}
		if x.Exit != 0 {
			exit = x.Exit
		}
		e := &Error{Code: code, Message: x.Message, Exit: exit, Raw: append(json.RawMessage(nil), raw...), ExistingUID: x.Data.ExistingUID, ExistingHandle: x.Data.ExistingHandle}
		if x.Data.UID != "" {
			e.CandidateUIDs = append(e.CandidateUIDs, x.Data.UID)
		}
		for _, candidate := range x.Data.Candidates {
			if candidate.UID != "" {
				e.CandidateUIDs = append(e.CandidateUIDs, candidate.UID)
			}
		}
		return e
	}
	message := strings.TrimSpace(string(stderr))
	if cause != nil {
		message = fmt.Sprintf("%v: %s", cause, message)
	}
	return &Error{Code: "process_error", Message: message, Exit: exit}
}

func Class(err error) RetryClass {
	if e, ok := errors.AsType[*Error](err); ok {
		switch e.Code {
		case "root_signing_key_unavailable":
			return SigningRefused
		case "not_found", "issue_not_found", "internal_error", "server_error":
			return Retry
		case "conflict", "cron_conflict", "revision_conflict", "idempotency_mismatch", "idempotency_deleted", "duplicate_candidates", "already_owned":
			return Conflict
		case "daemon_unavailable", "transport", "timeout", "sibling_throttle", "duplicate_message":
			return Retry
		}
		if e.Exit == 5 {
			return Conflict
		}
		if e.Exit == 7 {
			return Retry
		}
		return Fatal
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Retry
	}
	return Fatal
}

// Ambiguous reports whether a failed write may have reached Kata.
// Callers must read back its outcome before deciding to retry.
func Ambiguous(err error) bool {
	if err == nil {
		return false
	}
	var e *Error
	if errors.As(err, &e) && (e.Code == "sibling_throttle" || e.Code == "duplicate_message") {
		return false // Close guards refuse before committing.
	}
	return !errors.As(err, &e) || e.Code == "process_error" || Class(err) == Retry
}

func SafetyOf(kind string) Safety {
	switch kind {
	case "observe", "create", "comment", "close":
		return ReplaySafe
	case "meta", "meta.set", "meta.unset", "deadline":
		return Guarded
	case "notify", "label", "label.add":
		return Unguarded
	default:
		return ReadBack
	}
}

func NotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Code == "not_found" || e.Code == "issue_not_found")
}
