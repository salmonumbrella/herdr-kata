package defs

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// Definition refs cover bindings that have never produced a local run. Resolve
// each bound issue once; refreshes retain its authoritative ref across edits.
// Metadata reads are bounded and never prevent definition dispatch on outage.
func (c *Cache) definitionReferences(ctx context.Context, entries, previous []JobEntry) error {
	known := map[string]string{}
	for _, old := range previous {
		maps.Copy(known, old.IssueRefs)
	}
	reader, canRead := c.k.(interface {
		ShowIssue(context.Context, string) (kata.IssueView, error)
	})
	var pin kata.Installation
	err := state.ReadJSON(c.fs, c.l.StateFile(), 65536, &pin)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	reads := 0
	attempted := map[string]bool{}
	for i := range entries {
		d, err := DecodeJob(entries[i].Definition)
		if err != nil {
			continue
		}
		ids := []string{RunParent(d)}
		if d.Issue != nil {
			ids = append(ids, d.Issue.UID, d.Issue.ParentUID)
		}
		for _, uid := range ids {
			if uid == "" {
				continue
			}
			ref := known[uid]
			if ref == "" && canRead && pin.ProjectUID != "" && reads < 32 && readCtx.Err() == nil && !attempted[uid] {
				reads++
				attempted[uid] = true
				issue, readErr := reader.ShowIssue(readCtx, uid)
				if readErr == nil && issue.UID == uid && issue.ProjectUID == pin.ProjectUID {
					ref = kata.IssueReference(c.cfg.Kata.Project, issue)
					known[uid] = ref
				}
			}
			if ref != "" {
				if entries[i].IssueRefs == nil {
					entries[i].IssueRefs = map[string]string{}
				}
				entries[i].IssueRefs[uid] = ref
			}
		}
	}
	return nil
}
