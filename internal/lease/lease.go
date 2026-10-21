// Package lease records which installation owns a project lane in Kata.
//
// The lease is the herdr_kata.lease metadata of one closed issue labelled
// herdr-kata:lease, the project's oldest such issue. Every write is
// conditional on the issue revision it read, so two installations never both
// believe they hold a lane. An owner renews the lease with a heartbeat and
// before each admission; a higher generation or another owner ends its hold.
package lease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

const (
	Key   = "herdr_kata.lease"
	Label = "herdr-kata:lease"
	// Heartbeat is how often an owner renews its lease while idle.
	Heartbeat = 3 * time.Minute
	// TTL is how long a lease without a renewal stays live. A lane whose owner
	// stopped renewing for TTL may be taken by a normal start.
	TTL = 10 * time.Minute
)

// Lease is the stored record. Owner is the installation's executor label.
type Lease struct {
	Version    int       `json:"version"`
	Owner      string    `json:"owner"`
	Generation int64     `json:"generation"`
	Heartbeat  time.Time `json:"heartbeat"`
}

// Live reports a lease renewed within TTL of now.
func (l Lease) Live(now time.Time) bool {
	return l.Owner != "" && now.Sub(l.Heartbeat) < TTL
}

type Kata interface {
	OldestLabeled(context.Context, string) (kata.IssueView, bool, error)
	CreateIssue(context.Context, kata.CreateReq) (kata.IssueView, error)
	Close(context.Context, kata.CloseReq) error
	MetaSet(context.Context, string, string, string, kata.Cond) (int64, error)
}

var (
	// ErrHeld marks a lane another live installation owns.
	ErrHeld = errors.New("lane owned by another installation")
	// ErrLost marks a hold that another installation has ended.
	ErrLost = errors.New("lane lease lost")
)

// HeldError is a live lease of another installation.
type HeldError struct{ Lease Lease }

func (e *HeldError) Error() string {
	return fmt.Sprintf("another installation (%s) owns this lane: lease generation %d, last heartbeat %s; stop it there, or run herdr-kata up --takeover here", e.Lease.Owner, e.Lease.Generation, e.Lease.Heartbeat.UTC().Format(time.RFC3339))
}
func (e *HeldError) Is(target error) bool { return target == ErrHeld }

// LostError reports the lease that ended this installation's hold.
type LostError struct{ Lease Lease }

func (e *LostError) Error() string {
	return fmt.Sprintf("another installation (%s) took this lane over at lease generation %d", e.Lease.Owner, e.Lease.Generation)
}
func (e *LostError) Is(target error) bool { return target == ErrLost }

// record is the lease issue as last read or written.
type record struct {
	issue    string
	revision int64
	lease    Lease
	set      bool // the issue carries a lease
	open     bool // left open by an interrupted creation
}

func read(ctx context.Context, k Kata) (record, bool, error) {
	issue, found, err := k.OldestLabeled(ctx, Label)
	if err != nil || !found {
		return record{}, found, err
	}
	r := record{issue: issue.UID, revision: issue.Revision, open: issue.Status != "closed"}
	if raw, ok := issue.Metadata[Key]; ok {
		// An unreadable lease belongs to no one; the next claim replaces it.
		r.set = json.Unmarshal(raw, &r.lease) == nil && r.lease.Version == 1
	}
	return r, true, nil
}

// Read returns the current lease, if any. A failed read is an error, never
// a refusal.
func Read(ctx context.Context, k Kata) (Lease, bool, error) {
	r, found, err := read(ctx, k)
	return r.lease, found && r.set, err
}

// Release clears only this installation's existing lease. It never creates a
// lease issue or takes over another owner. A lost reply is resolved by the
// next read; the conditional write is never blindly repeated.
func Release(ctx context.Context, k Kata, owner string) error {
	r, found, err := read(ctx, k)
	if err != nil || !found || !r.set || r.lease.Owner != owner {
		return err
	}
	next := r.lease
	next.Owner = ""
	h := &Hold{k: k, rec: r, owner: owner}
	return h.write(ctx, next)
}

func closeIssue(ctx context.Context, k Kata, uid string) error {
	return k.Close(ctx, kata.CloseReq{Ref: uid, Reason: "wontfix", Message: "herdr-kata lane lease record; not work, so it stays closed and out of ready lists."})
}

// create makes the lease issue and closes it, then returns the project's
// oldest lease issue, which may be one another installation created first.
// A create whose reply was lost is found by the next read, never retried.
func create(ctx context.Context, k Kata) (record, error) {
	issue, err := k.CreateIssue(ctx, kata.CreateReq{Title: "herdr-kata lane lease", Body: "Records which herdr-kata installation owns this project's lane. Not work: it stays closed.", Labels: []string{Label}})
	if err != nil {
		return record{}, err
	}
	if issue.Status != "closed" {
		if err := closeIssue(ctx, k, issue.UID); err != nil {
			return record{}, err
		}
	}
	r, found, err := read(ctx, k)
	if err == nil && !found {
		err = errors.New("lane lease issue missing after creation")
	}
	return r, err
}

// Hold is this installation's claim on a lane. Its methods are safe for
// concurrent use; once lost, it stays lost.
type Hold struct {
	k     Kata
	mu    sync.Mutex
	rec   record
	lost  *LostError
	owner string
}

// Lease returns the lease as last written by this hold.
func (h *Hold) Lease() Lease { h.mu.Lock(); defer h.mu.Unlock(); return h.rec.lease }

// Issue returns the lease issue UID, so its own events can be ignored.
func (h *Hold) Issue() string { h.mu.Lock(); defer h.mu.Unlock(); return h.rec.issue }

// Claim takes the lane for owner unless another installation holds a live
// lease. A takeover outranks any lease with a higher generation; a normal
// start takes a free or expired lease and resumes its own.
func Claim(ctx context.Context, k Kata, owner string, takeover bool, now time.Time) (*Hold, error) {
	for range 3 {
		r, found, err := read(ctx, k)
		if err != nil {
			return nil, err
		}
		if !found {
			if r, err = create(ctx, k); err != nil {
				return nil, err
			}
		}
		if r.open {
			if err := closeIssue(ctx, k, r.issue); err != nil {
				return nil, err
			}
			continue
		}
		next := Lease{Version: 1, Owner: owner, Generation: 1, Heartbeat: now.UTC()}
		if r.set {
			next.Generation = r.lease.Generation
			switch {
			case takeover:
				next.Generation++
			case r.lease.Owner == owner:
			case r.lease.Live(now):
				return nil, &HeldError{Lease: r.lease}
			default:
				next.Generation++
			}
		}
		h := &Hold{k: k, rec: r, owner: owner}
		err = h.write(ctx, next)
		if err == nil {
			return h, nil
		}
		if kata.Class(err) != kata.Conflict {
			return nil, err
		}
		// Another installation wrote first; decide again on its lease.
	}
	return nil, errors.New("lane lease kept changing during claim")
}

func (h *Hold) write(ctx context.Context, next Lease) error {
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	revision, err := h.k.MetaSet(ctx, h.rec.issue, Key, string(b), kata.Cond{IfMatch: strconv.FormatInt(h.rec.revision, 10), JSONValue: true})
	if err != nil {
		return err
	}
	h.rec.revision, h.rec.lease, h.rec.set = revision, next, true
	return nil
}

// Renew proves the hold at now and extends it. It returns a LostError once
// another installation owns the lease, and any other error unchanged: an
// unproven renewal is not a hold, so callers must not admit after one.
func (h *Hold) Renew(ctx context.Context, now time.Time) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lost != nil {
		return h.lost
	}
	next := h.rec.lease
	next.Heartbeat = now.UTC()
	for range 3 {
		err := h.write(ctx, next)
		if err == nil || kata.Class(err) != kata.Conflict {
			return err
		}
		r, found, err := read(ctx, h.k)
		if err != nil {
			return err
		}
		if !found || !r.set || r.issue != h.rec.issue || r.lease.Owner != h.owner || r.lease.Generation != h.rec.lease.Generation {
			h.lost = &LostError{Lease: r.lease}
			return h.lost
		}
		// The issue changed for another reason; retry on its revision.
		h.rec.revision = r.revision
	}
	return errors.New("lane lease kept changing during renewal")
}

// Check reads the lease without writing it and ends the hold if another
// installation now owns it. A failed read leaves the hold as it was.
func (h *Hold) Check(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lost != nil {
		return h.lost
	}
	r, found, err := read(ctx, h.k)
	if err != nil {
		return err
	}
	if !found || !r.set || r.issue != h.rec.issue || r.lease.Owner != h.owner || r.lease.Generation != h.rec.lease.Generation {
		h.lost = &LostError{Lease: r.lease}
		return h.lost
	}
	return nil
}

// Newer reports whether revision is later than this hold's last write.
// Events up to that revision, including its own writes and a replay of the
// lease issue's history, need no check.
func (h *Hold) Newer(revision int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lost == nil && revision > h.rec.revision
}

// Held reports the generation of this hold and whether it still holds.
func (h *Hold) Held() (int64, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rec.lease.Generation, h.lost == nil
}
