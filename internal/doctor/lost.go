package doctor

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type runLister interface {
	ListRuns(context.Context, string, string, int) ([]kata.Run, string, error)
}

// LostRuns scans at most ten pages per invocation, preserving the next cursor
// after each completed page. An absent installation has no owned lost runs.
func LostRuns(ctx context.Context, k runLister, fsys state.FS, l state.Layout, executor, actor string) ([]string, error) {
	return lostRunsAt(ctx, k, fsys, l, l, executor, actor)
}
func lostRunsAt(ctx context.Context, k runLister, fsys state.FS, l, pinLayout state.Layout, executor, actor string) ([]string, error) {
	var cursor struct {
		Version int    `json:"version"`
		Before  string `json:"before"`
	}
	path := filepath.Join(pinLayout.Root, "doctor-lost.cursor")
	e := state.ReadJSON(fsys, path, 4096, &cursor)
	if e != nil && !errors.Is(e, fs.ErrNotExist) {
		return nil, e
	}
	var lost []string
	for range 10 {
		runs, next, e := k.ListRuns(ctx, "", cursor.Before, 100)
		if e != nil {
			return lost, e
		}
		for _, r := range runs {
			if r.Status != "running" || r.ExecutorLabel != executor || r.Actor != actor {
				continue
			}
			// Do not allow an upstream identity to escape the local run directory.
			if r.UID == "" || filepath.Base(r.UID) != r.UID || r.UID == "." || r.UID == ".." {
				continue
			}
			if _, e := fsys.Lstat(l.RunDir(r.UID)); errors.Is(e, fs.ErrNotExist) {
				lost = append(lost, r.UID)
			} else if e != nil {
				return lost, e
			}
		}
		if next != "" && next == cursor.Before {
			return lost, errors.New("lost-runs cursor did not advance")
		}
		cursor.Version = 1
		cursor.Before = next
		if e = state.WriteJSON(fsys, path, cursor); e != nil {
			return lost, e
		}
		if next == "" {
			break
		}
	}
	return lost, nil
}
