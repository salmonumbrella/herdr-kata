// Package inbox validates native recurring children; inbox prose never authorizes work.
package inbox

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

var ErrUnsupported = errors.New("unsupported inbox request")

type Occurrence struct {
	Version            int       `json:"version"`
	JobUID             string    `json:"job_uid"`
	DefinitionEventUID string    `json:"definition_event_uid"`
	ScheduledAt        time.Time `json:"scheduled_at"`
}
type Binding = sched.Binding
type Ownership = sched.Ownership

func Validate(child kata.IssueView, projectUID string) (Occurrence, error) {
	if err := ValidateNativeSource(child); err != nil {
		return Occurrence{}, err
	}
	bad := func(reason string) (Occurrence, error) {
		return Occurrence{}, fmt.Errorf("%w: %s", ErrUnsupported, reason)
	}
	if projectUID == "" || child.ProjectUID != projectUID || !validUID(child.UID) || child.Status != "open" {
		return bad("child must be open in the configured project")
	}
	parent, err := parentOf(child)
	if err != nil || !validUID(parent) {
		return bad("child must have a parent link")
	}
	var o Occurrence
	if err := json.Unmarshal(child.Metadata["cron.occurrence"], &o); err != nil || o.Version != 1 || !validUID(o.JobUID) || !validUID(o.DefinitionEventUID) || o.ScheduledAt.IsZero() {
		return bad("invalid cron.occurrence")
	}
	// RunUID embeds milliseconds in 48 bits; reject instants it cannot encode.
	if ms := o.ScheduledAt.UnixMilli(); ms < 0 || ms > 1<<48-1 {
		return bad("scheduled_at outside run UID range")
	}
	o.ScheduledAt = o.ScheduledAt.UTC()
	return o, nil
}

func validUID(uid string) bool {
	if len(uid) != 26 || uid[0] > '7' {
		return false
	}
	for _, c := range uid {
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", c) {
			return false
		}
	}
	return true
}

// ParentUID is normalized from Kata's show envelope. Links also permit direct
// IssueView callers, while conflicting parent information is unsupported.
func parentOf(child kata.IssueView) (string, error) {
	parent := child.ParentUID
	for _, l := range child.Links {
		if l.Type != "parent" || l.From.UID != child.UID {
			continue
		}
		if l.To.UID == "" || (parent != "" && parent != l.To.UID) {
			return "", ErrUnsupported
		}
		parent = l.To.UID
	}
	return parent, nil
}

// Takeover plans ownership only. The caller must execute a requested claim
// with --if-unowned and save the returned revision before writing attention.
func Takeover(child kata.IssueView, actor string) (decision string, needsClaim bool, err error) {
	if strings.TrimSpace(actor) == "" {
		return "", false, fmt.Errorf("%w: actor is required", ErrUnsupported)
	}
	if child.Owner == "" {
		return "claimed", true, nil
	}
	if child.Owner == actor {
		return "own-actor", false, nil
	}
	return "", false, fmt.Errorf("%w: child belongs to another actor", ErrUnsupported)
}

// AckOp freezes the exact value used by a later --json-value --if-value unset.
// A fresh issue revision must accompany that operation at settlement.
func AckOp(child kata.IssueView, inbox string) (key string, savedJSON json.RawMessage, err error) {
	if strings.TrimSpace(inbox) == "" {
		return "", nil, fmt.Errorf("%w: inbox is required", ErrUnsupported)
	}
	key = "notify." + base64.RawURLEncoding.EncodeToString([]byte(inbox))
	raw, ok := child.Metadata[key]
	if !ok || !json.Valid(raw) {
		return "", nil, fmt.Errorf("%w: notification is absent or invalid", ErrUnsupported)
	}
	return key, append(json.RawMessage(nil), raw...), nil
}
