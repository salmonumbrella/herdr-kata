package sched

import (
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"io/fs"
	"strings"
	"time"
)

type Checkpoint struct {
	Version            int       `json:"version"`
	JobUID             string    `json:"job_uid"`
	DefinitionEventUID string    `json:"definition_event_uid"`
	HandledThrough     time.Time `json:"handled_through"`
	Pending            []string  `json:"pending"`
}

func validUID(uid string) bool {
	return len(uid) == 26 && strings.Trim(uid, "0123456789ABCDEFGHJKMNPQRSTVWXYZ") == ""
}
func LoadCheckpoint(fsys state.FS, l state.Layout, uid string, firstSight time.Time) (*Checkpoint, error) {
	if !validUID(uid) {
		return nil, fmt.Errorf("invalid job UID")
	}
	c := &Checkpoint{}
	err := state.ReadJSON(fsys, l.JobCheckpoint(uid), 64*1024, c)
	if errors.Is(err, fs.ErrNotExist) {
		return &Checkpoint{Version: 1, JobUID: uid, HandledThrough: firstSight}, nil
	}
	if err != nil {
		return nil, err
	}
	if err = c.validate(uid); err != nil {
		return nil, &state.CorruptError{Path: l.JobCheckpoint(uid), Err: err}
	}
	return c, nil
}
func (c Checkpoint) validate(uid string) error {
	if c.Version != 1 || c.JobUID != uid || !validUID(uid) || c.HandledThrough.IsZero() || len(c.Pending) > 100 {
		return fmt.Errorf("invalid checkpoint identity, version or bounds")
	}
	var prev time.Time
	for _, key := range c.Pending {
		at, e := ident.ParseOccurrenceKey(key)
		if e != nil || !at.After(prev) || at.After(c.HandledThrough) {
			return fmt.Errorf("invalid pending occurrence %q", key)
		}
		prev = at
	}
	return nil
}
func (c Checkpoint) Save(fsys state.FS, l state.Layout) error {
	if e := c.validate(c.JobUID); e != nil {
		return e
	}
	return state.WriteJSON(fsys, l.JobCheckpoint(c.JobUID), c)
}
func (c *Checkpoint) Supersede(eventUID string, cutover time.Time) []string {
	c.HandledThrough = c.HandledThrough.UTC()
	cutover = cutover.UTC()
	if c.DefinitionEventUID == eventUID {
		return nil
	}
	dropped := c.Pending
	c.Pending = nil
	c.DefinitionEventUID = eventUID
	if cutover.After(c.HandledThrough) {
		c.HandledThrough = cutover
	}
	return dropped
}
