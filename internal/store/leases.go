package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Lease coordinates an explicit local or shared execution resource. It is not
// an issue reservation or a scheduler occurrence claim.
type Lease struct {
	Seq       int64
	Scope     string
	Resource  string
	Holder    Identity
	Why       string
	Since     time.Time
	ExpiresAt *time.Time
}

type LeaseRequest struct {
	Scope    string
	Resource string
	By       Identity
	Why      string
	TTL      time.Duration
	Now      time.Time
}

type LeaseHeldError struct{ Lease Lease }

func (e *LeaseHeldError) Error() string {
	return fmt.Sprintf("%s/%s is held by %s: %s", e.Lease.Scope, e.Lease.Resource, e.Lease.Holder, e.Lease.Why)
}

type LeaseNotHeldError struct {
	Scope, Resource string
	By              Identity
	Current         *Lease
}

func (e *LeaseNotHeldError) Error() string {
	message := fmt.Sprintf("%s/%s is not held by %s", e.Scope, e.Resource, e.By)
	if e.Current == nil {
		return message + "; no live holder"
	}
	expiry := "no expiry"
	if e.Current.ExpiresAt != nil {
		expiry = e.Current.ExpiresAt.Format(time.RFC3339Nano)
	}
	holder := e.Current.Holder
	return fmt.Sprintf("%s; current holder name=%q job=%q run=%q holder-id=%q; expiry: %s", message, holder.Name, holder.JobID, holder.RunID, holder.PID, expiry)
}

const leaseSchema = `
CREATE TABLE IF NOT EXISTS lease_events (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 scope TEXT NOT NULL, resource TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('acquire','renew','release')),
 holder TEXT NOT NULL, job_id TEXT NOT NULL DEFAULT '',
 run_id TEXT NOT NULL DEFAULT '', pid TEXT NOT NULL DEFAULT '',
 reason TEXT NOT NULL DEFAULT '', since_ns INTEGER NOT NULL,
 expires_ns INTEGER, created_ns INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS lease_events_resource ON lease_events(scope,resource,seq);
CREATE INDEX IF NOT EXISTS lease_events_created ON lease_events(created_ns);
`
const currentLeases = `SELECT e.seq,e.scope,e.resource,e.holder,e.job_id,e.run_id,e.pid,e.reason,e.since_ns,e.expires_ns
 FROM lease_events e JOIN (SELECT scope,resource,MAX(seq) AS seq FROM lease_events GROUP BY scope,resource) latest ON latest.seq=e.seq
 WHERE e.kind IN ('acquire','renew')`

func scanLease(row interface{ Scan(...any) error }) (Lease, error) {
	var l Lease
	var since int64
	var expires sql.NullInt64
	err := row.Scan(&l.Seq, &l.Scope, &l.Resource, &l.Holder.Name, &l.Holder.JobID, &l.Holder.RunID, &l.Holder.PID, &l.Why, &since, &expires)
	l.Since = time.Unix(0, since)
	if expires.Valid {
		v := time.Unix(0, expires.Int64)
		l.ExpiresAt = &v
	}
	return l, err
}
func leaseOf(ctx context.Context, c *sql.Conn, scope, resource string, now time.Time) (*Lease, error) {
	l, err := scanLease(c.QueryRowContext(ctx, `SELECT * FROM (`+currentLeases+`) WHERE scope=? AND resource=? AND (expires_ns IS NULL OR expires_ns>?)`, scope, resource, now.UnixNano()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}
func validateLease(req LeaseRequest) error {
	if strings.TrimSpace(req.Scope) == "" {
		return errors.New("a lease needs an explicit scope")
	}
	if strings.TrimSpace(req.Resource) == "" {
		return errors.New("a lease needs a resource")
	}
	if !req.By.Valid() {
		return errors.New("a lease needs a holder")
	}
	if req.TTL < 0 {
		return errors.New("a lease ttl cannot be negative")
	}
	return nil
}
func (s *Store) writeLease(ctx context.Context, req LeaseRequest, kind string) (Lease, error) {
	if err := validateLease(req); err != nil {
		return Lease{}, err
	}
	req.Scope = strings.TrimSpace(req.Scope)
	req.Resource = strings.TrimSpace(req.Resource)
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer conn.Close()
	if err := leaseContextError(ctx); err != nil {
		return Lease{}, err
	}
	// SQLite's busy handler can keep waiting despite context cancellation.
	// Bound it by this operation's budget and restore the connection setting
	// before returning it to the pool.
	if deadline, ok := ctx.Deadline(); ok {
		var originalBusy int64
		if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&originalBusy); err != nil {
			return Lease{}, err
		}
		defer conn.ExecContext(context.WithoutCancel(ctx), fmt.Sprintf("PRAGMA busy_timeout=%d", originalBusy))
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return Lease{}, context.DeadlineExceeded
		}
		budgetMs := int64(remaining / time.Millisecond)
		if remaining%time.Millisecond != 0 {
			budgetMs++
		}
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d", min(originalBusy, budgetMs))); err != nil {
			return Lease{}, err
		}
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()
	if err := leaseContextError(ctx); err != nil {
		return Lease{}, err
	}
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return Lease{}, err
	}
	if err := leaseContextError(ctx); err != nil {
		return Lease{}, err
	}
	now := resolveNow(req.Now)
	// SQLite timestamps use signed nanoseconds; refuse an expiry that would
	// wrap and silently free a live resource.
	expiry := now.Add(req.TTL)
	if !time.Unix(0, now.UnixNano()).Equal(now) || (req.TTL > 0 && !time.Unix(0, expiry.UnixNano()).Equal(expiry)) {
		return Lease{}, errors.New("lease time exceeds signed nanosecond persistence range")
	}
	current, err := leaseOf(ctx, conn, req.Scope, req.Resource, now)
	if err != nil {
		return Lease{}, err
	}
	if kind == "acquire" {
		if current != nil && current.Holder != req.By {
			return Lease{}, &LeaseHeldError{Lease: *current}
		}
	} else if current == nil || current.Holder != req.By {
		return Lease{}, &LeaseNotHeldError{Scope: req.Scope, Resource: req.Resource, By: req.By, Current: current}
	}
	l := Lease{Scope: req.Scope, Resource: req.Resource, Holder: req.By, Why: strings.TrimSpace(req.Why), Since: now}
	if current != nil {
		l.Since = current.Since
		if l.Why == "" {
			l.Why = current.Why
		}
	}
	var expires any
	if kind != "release" && req.TTL > 0 {
		v := now.Add(req.TTL)
		l.ExpiresAt = &v
		expires = v.UnixNano()
	}
	res, err := conn.ExecContext(ctx, `INSERT INTO lease_events(scope,resource,kind,holder,job_id,run_id,pid,reason,since_ns,expires_ns,created_ns) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, l.Scope, l.Resource, kind, l.Holder.Name, l.Holder.JobID, l.Holder.RunID, l.Holder.PID, l.Why, l.Since.UnixNano(), expires, now.UnixNano())
	if err != nil {
		return Lease{}, err
	}
	l.Seq, err = res.LastInsertId()
	if err != nil {
		return Lease{}, err
	}
	if err := leaseContextError(ctx); err != nil {
		return Lease{}, err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return Lease{}, err
	}
	committed = true
	if kind == "release" {
		return *current, nil
	}
	return l, nil
}
func (s *Store) AcquireLease(ctx context.Context, req LeaseRequest) (Lease, error) {
	return s.writeLease(ctx, req, "acquire")
}
func (s *Store) RenewLease(ctx context.Context, req LeaseRequest) (Lease, error) {
	return s.writeLease(ctx, req, "renew")
}
func (s *Store) ReleaseLease(ctx context.Context, scope, resource string, by Identity, now time.Time) (Lease, error) {
	return s.writeLease(ctx, LeaseRequest{Scope: scope, Resource: resource, By: by, Now: now}, "release")
}

// ListLeases returns live leases; an empty scope lists every explicit scope.
func (s *Store) ListLeases(ctx context.Context, scope string, now time.Time) ([]Lease, error) {
	now = resolveNow(now)
	rows, err := s.db.QueryContext(ctx, `SELECT * FROM (`+currentLeases+`) WHERE (?='' OR scope=?) AND (expires_ns IS NULL OR expires_ns>?) ORDER BY since_ns,seq`, scope, scope, now.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Lease{}
	for rows.Next() {
		l, e := scanLease(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// leaseContextError also checks the wall-clock deadline: cancellation delivery
// can lag while the process is scheduled away, but a late hold must not commit.
func leaseContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func (s *Store) AcquireLeaseWait(ctx context.Context, req LeaseRequest, wait time.Duration) (Lease, error) {
	if wait <= 0 {
		return s.AcquireLease(ctx, req)
	}
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	deadline, _ := waitCtx.Deadline()
	var lastHeld *LeaseHeldError
	timeout := func(err error) (Lease, error) {
		if lastHeld != nil {
			return Lease{}, errors.Join(lastHeld, err)
		}
		return Lease{}, err
	}
	for {
		if err := leaseContextError(waitCtx); err != nil {
			return timeout(err)
		}
		l, err := s.AcquireLease(waitCtx, req)
		if err == nil {
			return l, nil
		}
		var held *LeaseHeldError
		if errors.As(err, &held) {
			lastHeld = held
		} else {
			if ctxErr := leaseContextError(waitCtx); ctxErr != nil {
				return timeout(ctxErr)
			}
			var sqliteErr interface{ Code() int }
			if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != 5 { // SQLITE_BUSY
				return Lease{}, err
			}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return timeout(context.DeadlineExceeded)
		}
		timer := time.NewTimer(min(250*time.Millisecond, remaining))
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return timeout(waitCtx.Err())
		case <-timer.C:
		}
	}
}
