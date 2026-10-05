package store

import (
	"context"
	"errors"
	"hegel.dev/go/hegel"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLeaseIndependentConnectionsContendAndRestart(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, s := range []*Store{a, b} {
		wg.Add(1)
		go func(i int, s *Store) {
			defer wg.Done()
			<-start
			_, e := s.AcquireLease(ctx, LeaseRequest{Scope: "local:example", Resource: "browser", By: Identity{Name: []string{"first", "second"}[i], RunID: "run"}, TTL: time.Hour})
			results <- e
		}(i, s)
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	for e := range results {
		if e == nil {
			wins++
		} else {
			var held *LeaseHeldError
			if !errors.As(e, &held) {
				t.Fatal(e)
			}
		}
	}
	if wins != 1 {
		t.Fatalf("winners=%d", wins)
	}
	leases, err := a.ListLeases(ctx, "local:example", time.Time{})
	if err != nil || len(leases) != 1 {
		t.Fatalf("leases=%v err=%v", leases, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	restored, err := c.ListLeases(ctx, "local:example", time.Time{})
	if err != nil || len(restored) != 1 || restored[0].Holder != leases[0].Holder {
		t.Fatalf("restart leases=%v err=%v", restored, err)
	}
}

func TestLeaseOwnershipRenewalExpiryAndScope(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 0, 0, 0, 123, time.UTC)
	owner := Identity{Name: "worker", RunID: "first", PID: "pane-a"}
	req := LeaseRequest{Scope: "local:example", Resource: "browser", By: owner, TTL: time.Minute, Now: now}
	first, err := s.AcquireLease(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	for _, by := range []Identity{{Name: "worker", RunID: "second", PID: "pane-a"}, {Name: "worker", RunID: "first"}, {Name: "other"}} {
		bad := req
		bad.By = by
		if _, e := s.RenewLease(ctx, bad); e == nil {
			t.Fatalf("renew accepted %v", by)
		}
		if _, e := s.ReleaseLease(ctx, req.Scope, req.Resource, by, now); e == nil {
			t.Fatalf("release accepted %v", by)
		}
	}
	req.Now = now.Add(30 * time.Second)
	req.TTL = 2 * time.Minute
	renewed, err := s.RenewLease(ctx, req)
	if err != nil || !renewed.Since.Equal(first.Since) || !renewed.ExpiresAt.Equal(req.Now.Add(req.TTL)) {
		t.Fatalf("renewed=%v err=%v", renewed, err)
	}
	other := req
	other.Scope = "shared:example"
	other.By = Identity{Name: "other"}
	if _, e := s.AcquireLease(ctx, other); e != nil {
		t.Fatal(e)
	}
	boundary := *renewed.ExpiresAt
	if _, e := s.RenewLease(ctx, LeaseRequest{Scope: req.Scope, Resource: req.Resource, By: owner, TTL: time.Minute, Now: boundary}); e == nil {
		t.Fatal("expired renewal accepted")
	}
	req.Now = boundary
	req.By = Identity{Name: "successor"}
	next, err := s.AcquireLease(ctx, req)
	if err != nil || !next.Since.Equal(boundary) {
		t.Fatalf("successor=%v err=%v", next, err)
	}
	if _, e := s.ReleaseLease(ctx, req.Scope, req.Resource, owner, boundary); e == nil {
		t.Fatal("former holder stole lease")
	}
	if _, e := s.ReleaseLease(ctx, req.Scope, req.Resource, req.By, boundary); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ReleaseLease(ctx, req.Scope, req.Resource, req.By, boundary); e == nil {
		t.Fatal("duplicate release accepted")
	}
}

func TestLeaseWaitAndValidation(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, r := range []LeaseRequest{{Resource: "browser", By: Identity{Name: "a"}}, {Scope: "local:example", By: Identity{Name: "a"}}, {Scope: "local:example", Resource: "browser"}, {Scope: "local:example", Resource: "browser", By: Identity{Name: "a"}, TTL: -1}} {
		if _, e := s.AcquireLease(ctx, r); e == nil {
			t.Fatalf("accepted invalid %v", r)
		}
	}
	req := LeaseRequest{Scope: "local:example", Resource: "browser", By: Identity{Name: "first"}, TTL: 30 * time.Millisecond}
	if _, e := s.AcquireLease(ctx, req); e != nil {
		t.Fatal(e)
	}
	req.By = Identity{Name: "second"}
	req.TTL = time.Hour // The successor stays held for the subsequent contention check.
	if _, e := s.AcquireLeaseWait(ctx, req, time.Second); e != nil {
		t.Fatal(e)
	}
	req.By = Identity{Name: "third"}
	req.TTL = time.Hour
	if _, e := s.AcquireLeaseWait(ctx, req, 5*time.Millisecond); e == nil {
		t.Fatal("wait ignored contention")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := s.AcquireLeaseWait(cancelled, req, time.Second); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancel=%v", e)
	}
}

// Every accepted explicit scope/resource/holder survives the persistence
// round-trip byte-for-byte (apart from documented surrounding-space trimming).
func TestLeasePersistenceProperty(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hegel.Test(t, func(ht *hegel.T) {
		scope := hegel.Draw(ht, hegel.Text())
		resource := hegel.Draw(ht, hegel.Text())
		holder := hegel.Draw(ht, hegel.Text())
		run := hegel.Draw(ht, hegel.Text())
		pid := hegel.Draw(ht, hegel.Text())
		ttl := time.Duration(hegel.Draw(ht, hegel.Integers(0, math.MaxInt64)))
		if _, err := s.db.Exec("DELETE FROM lease_events"); err != nil {
			ht.Fatal(err)
		}
		now := time.Date(2026, 10, 3, 0, 0, 0, 1, time.UTC)
		id := Identity{Name: holder, RunID: run, PID: pid}
		req := LeaseRequest{Scope: scope, Resource: resource, By: id, Now: now, TTL: ttl}
		l, err := s.AcquireLease(context.Background(), req)
		if strings.TrimSpace(scope) == "" || strings.TrimSpace(resource) == "" || strings.TrimSpace(holder) == "" || !time.Unix(0, now.Add(ttl).UnixNano()).Equal(now.Add(ttl)) {
			if err == nil {
				ht.Fatal("invalid identity or scope accepted")
			}
			return
		}
		if err != nil {
			ht.Fatal(err)
		}
		ls, err := s.ListLeases(context.Background(), "", now)
		if err != nil || len(ls) != 1 {
			ht.Fatalf("round-trip leases=%v error=%v ttl=%v", ls, err, ttl)
		}
		got := ls[0]
		if got.Scope != strings.TrimSpace(scope) || got.Resource != strings.TrimSpace(resource) || got.Holder != id || !got.Since.Equal(now) || got.Seq != l.Seq {
			ht.Fatalf("round-trip changed lease: %v -> %v", l, got)
		}
		if ttl == 0 {
			if got.ExpiresAt != nil {
				ht.Fatal("indefinite lease gained expiry")
			}
		} else if got.ExpiresAt == nil || !got.ExpiresAt.Equal(now.Add(ttl)) {
			ht.Fatal("expiry changed")
		}
	})
}

func TestLeaseRejectsUnrepresentableExpiry(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.AcquireLease(context.Background(), LeaseRequest{Scope: "local:example", Resource: "browser", By: Identity{Name: "worker"}, Now: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), TTL: time.Duration(math.MaxInt64)})
	if err == nil {
		t.Fatal("expiry outside signed nanosecond persistence range accepted")
	}
}

func TestLeaseSameHolderReacquisitionPreservesStartAndReason(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 0, 0, 0, 123, time.UTC)
	req := LeaseRequest{Scope: "local:example", Resource: "browser", By: Identity{Name: "worker", JobID: "job-a", RunID: "run-a", PID: "pane-a"}, TTL: time.Minute, Now: now, Why: "inspection"}
	first, err := s.AcquireLease(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	for i, reason := range []string{"", "new inspection"} {
		req.Now = now.Add(time.Duration(i+1) * 10 * time.Second)
		req.TTL = time.Hour
		req.Why = reason
		again, err := s.AcquireLease(ctx, req)
		wantReason := reason
		if wantReason == "" {
			wantReason = first.Why
		}
		if err != nil || !again.Since.Equal(first.Since) || again.Holder != first.Holder || again.Why != wantReason || again.Seq <= first.Seq || again.ExpiresAt == nil || !again.ExpiresAt.Equal(req.Now.Add(req.TTL)) {
			t.Fatalf("same-holder reacquisition changed contract: lease=%+v err=%v", again, err)
		}
		listed, err := s.ListLeases(ctx, req.Scope, req.Now)
		if err != nil || len(listed) != 1 || listed[0].Seq != again.Seq || listed[0].Why != wantReason {
			t.Fatalf("reacquired projection=%+v error=%v", listed, err)
		}
	}
}

func TestLeaseNotHeldErrorDescribesLiveHolder(t *testing.T) {
	expiry := time.Date(2026, 10, 3, 0, 0, 0, 123, time.UTC)
	for _, current := range []*Lease{nil, {Holder: Identity{Name: "owner", JobID: "job-a", RunID: "run-a", PID: "pane-a"}, ExpiresAt: &expiry}, {Holder: Identity{Name: "owner"}}} {
		err := &LeaseNotHeldError{Scope: "local:example", Resource: "browser", By: Identity{Name: "caller"}, Current: current}
		message := err.Error()
		if current == nil {
			if !strings.Contains(message, "no live holder") || strings.Contains(message, "expired") {
				t.Errorf("nil holder implies unjustified cause: %s", message)
			}
			continue
		}
		for _, want := range []string{`name="` + current.Holder.Name + `"`, `job="` + current.Holder.JobID + `"`, `run="` + current.Holder.RunID + `"`, `holder-id="` + current.Holder.PID + `"`} {
			if !strings.Contains(message, want) {
				t.Errorf("current holder missing %q: %s", want, message)
			}
		}
		want := "no expiry"
		if current.ExpiresAt != nil {
			want = current.ExpiresAt.Format(time.RFC3339Nano)
		}
		if !strings.Contains(message, want) {
			t.Errorf("expiry missing %q: %s", want, message)
		}
	}
}
