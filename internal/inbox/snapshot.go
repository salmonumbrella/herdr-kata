package inbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"io/fs"
	"path/filepath"
	"time"
)

const MaxSnapshotRequests = 1000
const (
	Accepted       = "accepted"
	WaitingReady   = "waiting-ready"
	Unsupported    = "unsupported"
	IgnoredRequest = "ignored"
)

// Snapshot records scan evidence, never admission or ownership authority.
type Snapshot struct {
	Version            int       `json:"version"`
	LastSuccessfulScan time.Time `json:"last_successful_scan"`
	Error              string    `json:"error,omitempty"`
	Requests           []Request `json:"requests"`
}
type Request struct {
	ProjectUID string `json:"-"`
	Project    string `json:"-"`
	Ref        string `json:"ref"`
	Title      string `json:"title,omitempty"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
}

func (s Snapshot) validate() error {
	if s.Version != 1 || len(s.Requests) > MaxSnapshotRequests {
		return fmt.Errorf("invalid inbox snapshot version or request bound")
	}
	for _, r := range s.Requests {
		if r.Ref == "" {
			return fmt.Errorf("inbox snapshot request has no identity")
		}
		switch r.Status {
		case Accepted, WaitingReady, Unsupported, IgnoredRequest:
		default:
			return fmt.Errorf("invalid inbox disposition %q", r.Status)
		}
	}
	return nil
}
func WriteSnapshot(fsys state.FS, root string, s Snapshot) error {
	if err := s.validate(); err != nil {
		return err
	}
	body, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if len(body)+1 > 4<<20 {
		return fmt.Errorf("inbox snapshot exceeds size bound")
	}
	return state.WriteJSON(fsys, filepath.Join(root, "inbox.json"), s)
}
func ReadSnapshot(fsys state.FS, root string) (Snapshot, error) {
	s := Snapshot{Version: 1, Requests: []Request{}}
	err := state.ReadJSON(fsys, filepath.Join(root, "inbox.json"), 4<<20, &s)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	return s, s.validate()
}
