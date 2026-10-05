package runner

import (
	"errors"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/lockfile"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"os"
	"path/filepath"
)

type nativeIssueBinding struct {
	Version  int    `json:"version"`
	RunUID   string `json:"run_uid"`
	IssueUID string `json:"issue_uid"`
}

// BindIssue records one immutable issue result beside the immutable create intent.
func (c NativeExecutionContext) BindIssue(dir, uid string) error {
	if id, err := katacli.NormalizeUID(uid); err != nil || id != uid {
		return errors.New("created issue identity must be canonical")
	}
	path := filepath.Join(dir, "native-issue.json")
	lock, err := lockfile.Acquire(path + ".lock")
	if err != nil {
		return err
	}
	defer lock.Release()
	want := nativeIssueBinding{Version: 1, RunUID: c.RunUID, IssueUID: uid}
	var saved nativeIssueBinding
	if err := statefs.ReadJSON(path, 98304, &saved); err == nil {
		if saved != want {
			return errors.New("immutable local run issue identity changed")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return statefs.WriteJSON(path, want, 98304)
}
func (c *NativeExecutionContext) loadIssueBinding(dir string) error {
	var saved nativeIssueBinding
	if err := statefs.ReadJSON(filepath.Join(dir, "native-issue.json"), 98304, &saved); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	uid, err := katacli.NormalizeUID(saved.IssueUID)
	if err != nil || uid != saved.IssueUID || saved.Version != 1 || saved.RunUID != c.RunUID || c.IssueUID != "" && c.IssueUID != uid {
		return errors.New("saved local run issue identity mismatch")
	}
	c.IssueUID = uid
	return nil
}
