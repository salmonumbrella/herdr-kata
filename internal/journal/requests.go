package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type Request struct {
	ID, Kind, Project, JobUID, Run, Input string
	InputSet                              bool
	Reason                                string
}
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
	var errs []error
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(q.Dir, e.Name()))
		var r Request
		if err == nil {
			err = json.Unmarshal(b, &r)
		}
		if err == nil && (!safeID(r.ID) || e.Name() != r.ID+".json" || r.Reason == "" && r.Kind != "run" && r.Kind != "release") {
			err = fmt.Errorf("request identity mismatch")
		}
		if err != nil {
			slog.Error("skip malformed request", "file", e.Name(), "error", err)
			if e := q.rejectFile(e.Name(), "malformed request: "+err.Error()); e != nil {
				errs = append(errs, e)
			}
			continue
		}
		if r.Reason != "" {
			if e := q.rejectFile(e.Name(), r.Reason); e != nil {
				errs = append(errs, e)
			}
			continue
		}
		out = append(out, r)
	}
	return out, errors.Join(errs...)
}

func (q Requests) rejectFile(name, reason string) error {
	// Persist rejection before moving it, so a crash cannot readmit the request.
	r := Request{ID: strings.TrimSuffix(name, ".json"), Reason: strings.Join(strings.Fields(reason), " ")}
	if len(r.Reason) > 4096 {
		r.Reason = r.Reason[:4096]
	}
	if err := state.WriteJSON(filepath.Join(q.Dir, name), r); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(q.Dir, name), filepath.Join(q.Dir, strings.TrimSuffix(name, ".json")+".rejected")); err != nil {
		return err
	}
	return state.SyncDir(q.Dir)
}

func (q Requests) Reject(id, reason string) error {
	if !safeID(id) {
		return fmt.Errorf("invalid request id")
	}
	return q.rejectFile(id+".json", reason)
}

func (q Requests) Result(id string) (done bool, reason string, err error) {
	if !safeID(id) {
		return false, "", fmt.Errorf("invalid request id")
	}
	b, err := os.ReadFile(filepath.Join(q.Dir, id+".rejected"))
	if err == nil {
		var r Request
		err = json.Unmarshal(b, &r)
		return true, r.Reason, err
	}
	if !os.IsNotExist(err) {
		return false, "", err
	}
	_, err = os.Stat(filepath.Join(q.Dir, id+".done"))
	if os.IsNotExist(err) {
		return false, "", nil
	}
	return err == nil, "", err
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
