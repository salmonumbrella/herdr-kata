package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Only scheduling is controlled by the wrapper. Connections, writer locks,
// events and contention errors all come from the actual temporary SQLite file.
func leaseTimingStore(t *testing.T) (*Store, *Store, *sql.Conn, <-chan struct{}) {
	t.Helper()
	dir := t.TempDir()
	base, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	began := make(chan struct{}, 1)
	name := fmt.Sprintf("lease-timing-%d", migrationDriverSequence.Add(1))
	sql.Register(name, &leaseTimingDriver{base: base.db.Driver(), began: began})
	db, err := sql.Open(name, filepath.Join(dir, "herdr-kata.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// Warm the connection before taking any writer lock, to isolate contention
	// in the operation itself from driver setup.
	warm, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	warm.Close()
	owner, err := base.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.ExecContext(context.Background(), "ROLLBACK"); owner.Close() })
	return &Store{db: db}, base, owner, began
}

func leaseBeginWriter(t *testing.T, owner *sql.Conn) {
	t.Helper()
	if _, err := owner.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
}
func leaseEndWriter(t *testing.T, owner *sql.Conn, command string) time.Time {
	t.Helper()
	lowerBound := time.Now()
	if _, err := owner.ExecContext(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	return lowerBound
}
func leaseAwaitBegin(t *testing.T, began <-chan struct{}) {
	t.Helper()
	select {
	case <-began:
	case <-time.After(5 * time.Second):
		t.Fatal("lease operation did not reach writer acquisition")
	}
}

type leaseResult struct {
	lease Lease
	err   error
}

func TestLeaseImplicitClockAfterWriterAcquisition(t *testing.T) {
	s, _, owner, began := leaseTimingStore(t)
	leaseBeginWriter(t, owner)
	req := LeaseRequest{Scope: "local:example", Resource: "browser", By: Identity{Name: "worker"}, TTL: 20 * time.Millisecond}
	result := make(chan leaseResult, 1)
	go func() { l, err := s.AcquireLease(context.Background(), req); result <- leaseResult{l, err} }()
	leaseAwaitBegin(t, began)
	<-time.After(2 * req.TTL)
	released := leaseEndWriter(t, owner, "ROLLBACK")
	got := <-result
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.lease.Since.Before(released) || !got.lease.ExpiresAt.Equal(got.lease.Since.Add(req.TTL)) {
		t.Fatalf("lease uses pre-writer clock: since=%s released=%s expires=%s", got.lease.Since, released, got.lease.ExpiresAt)
	}
}

func TestLeaseDelayedRenewalCannotReviveExpiredHold(t *testing.T) {
	s, base, owner, began := leaseTimingStore(t)
	req := LeaseRequest{Scope: "local:example", Resource: "browser", By: Identity{Name: "worker", RunID: "run-a"}, TTL: time.Hour}
	first, err := base.AcquireLease(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	leaseBeginWriter(t, owner)
	result := make(chan leaseResult, 1)
	go func() { l, err := s.RenewLease(context.Background(), req); result <- leaseResult{l, err} }()
	leaseAwaitBegin(t, began)
	// Set the fixture's expiry after the renewal starts, while the independent
	// writer owns the transaction, then publish it only after it expires.
	expiry := time.Now().Add(40 * time.Millisecond)
	if _, err := owner.ExecContext(context.Background(), "UPDATE lease_events SET expires_ns=? WHERE seq=?", expiry.UnixNano(), first.Seq); err != nil {
		t.Fatal(err)
	}
	<-time.After(max(time.Until(expiry), 0))
	leaseEndWriter(t, owner, "COMMIT")
	got := <-result
	var notHeld *LeaseNotHeldError
	if !errors.As(got.err, &notHeld) {
		t.Fatalf("expired delayed renewal accepted: lease=%+v error=%v", got.lease, got.err)
	}
	var events int
	if err := base.db.QueryRow("SELECT COUNT(*) FROM lease_events").Scan(&events); err != nil || events != 1 {
		t.Fatalf("expired renewal appended event: events=%d err=%v", events, err)
	}
}

func TestLeaseExplicitClockSurvivesWriterDelay(t *testing.T) {
	s, _, owner, began := leaseTimingStore(t)
	leaseBeginWriter(t, owner)
	now := time.Date(2026, 10, 3, 0, 0, 0, 123, time.UTC)
	req := LeaseRequest{Scope: "local:example", Resource: "browser", By: Identity{Name: "worker"}, TTL: time.Hour, Now: now}
	result := make(chan leaseResult, 1)
	go func() { l, err := s.AcquireLease(context.Background(), req); result <- leaseResult{l, err} }()
	leaseAwaitBegin(t, began)
	leaseEndWriter(t, owner, "ROLLBACK")
	got := <-result
	if got.err != nil || !got.lease.Since.Equal(now) || !got.lease.ExpiresAt.Equal(now.Add(req.TTL)) {
		t.Fatalf("explicit clock changed: %+v error=%v", got.lease, got.err)
	}
}

func TestLeaseWaitDeadlinePreventsLateWriterAcquisition(t *testing.T) {
	s, base, owner, began := leaseTimingStore(t)
	leaseBeginWriter(t, owner)
	req := LeaseRequest{Scope: "local:example", Resource: "browser", By: Identity{Name: "worker"}, TTL: time.Hour}
	result := make(chan leaseResult, 1)
	go func() {
		l, err := s.AcquireLeaseWait(context.Background(), req, 100*time.Millisecond)
		result <- leaseResult{l, err}
	}()
	leaseAwaitBegin(t, began)
	var got leaseResult
	late := false
	select {
	case got = <-result:
	case <-time.After(400 * time.Millisecond):
		late = true
	}
	leaseEndWriter(t, owner, "ROLLBACK")
	if late {
		got = <-result
	}
	if !errors.Is(got.err, context.DeadlineExceeded) || late {
		t.Errorf("claim wait exceeded deadline or acquired late: late=%v lease=%+v error=%v", late, got.lease, got.err)
	}
	leases, err := base.ListLeases(context.Background(), "", time.Time{})
	if err != nil || len(leases) != 0 {
		t.Fatalf("deadline committed a late hold: leases=%+v error=%v", leases, err)
	}
	var restoredBusy int
	if err := s.db.QueryRow("PRAGMA busy_timeout").Scan(&restoredBusy); err != nil || restoredBusy != 5000 {
		t.Fatalf("operation changed pooled busy timeout: %d error=%v", restoredBusy, err)
	}
}

func TestLeaseWaitDeadlineBoundsConnectionPool(t *testing.T) {
	s, _, _, _ := leaseTimingStore(t)
	s.db.SetMaxOpenConns(1)
	pool, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	req := LeaseRequest{Scope: "local:example", Resource: "browser", By: Identity{Name: "worker"}, TTL: time.Hour}
	result := make(chan error, 1)
	go func() { _, err := s.AcquireLeaseWait(context.Background(), req, 50*time.Millisecond); result <- err }()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(400 * time.Millisecond):
		pool.Close()
		err := <-result
		t.Fatalf("wait ignored connection deadline: %v", err)
	}
}

func TestLeaseWaitTimeoutRetainsHolderAndZeroWait(t *testing.T) {
	s, base, _, _ := leaseTimingStore(t)
	req := LeaseRequest{Scope: "local:example", Resource: "browser", By: Identity{Name: "first", RunID: "run-a"}, TTL: time.Hour, Why: "inspection"}
	first, err := base.AcquireLease(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.By = Identity{Name: "second", RunID: "run-b"}
	for _, wait := range []time.Duration{0, time.Second} {
		_, err := s.AcquireLeaseWait(context.Background(), req, wait)
		var held *LeaseHeldError
		if !errors.As(err, &held) || held.Lease.Holder != first.Holder || held.Lease.Why != first.Why {
			t.Fatalf("holder information lost: %v", err)
		}
		if wait > 0 && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("positive wait did not report deadline: %v", err)
		}
		if wait == 0 && errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("zero wait became a deadline: %v", err)
		}
	}
}

func TestLeaseWaitRestoresBusyTimeoutWhenSetupIsCancelled(t *testing.T) {
	s, _, _, _ := leaseTimingStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = conn.Raw(func(raw any) error {
		raw.(*leaseTimingConn).afterBusySet = cancel
		return nil
	})
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	req := LeaseRequest{Scope: "local:example", Resource: "browser", By: Identity{Name: "worker"}, TTL: time.Hour}
	_, err = s.AcquireLeaseWait(ctx, req, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("setup cancellation=%v", err)
	}
	var timeout int
	if err := s.db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil || timeout != 5000 {
		t.Fatalf("setup cancellation leaked pooled timeout: %d error=%v", timeout, err)
	}
}

type leaseTimingDriver struct {
	base  driver.Driver
	began chan<- struct{}
}

func (d *leaseTimingDriver) Open(name string) (driver.Conn, error) {
	c, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &leaseTimingConn{Conn: c, began: d.began}, nil
}

type leaseTimingConn struct {
	driver.Conn
	began        chan<- struct{}
	once         sync.Once
	afterBusySet func()
	busySetOnce  sync.Once
}

func (c *leaseTimingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if query == "BEGIN IMMEDIATE" {
		c.once.Do(func() { c.began <- struct{}{} })
	}
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
	if err == nil && strings.HasPrefix(query, "PRAGMA busy_timeout=") && c.afterBusySet != nil {
		c.busySetOnce.Do(c.afterBusySet)
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
	}
	return result, err
}
func (c *leaseTimingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}
