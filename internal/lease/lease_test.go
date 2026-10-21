package lease

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

func TestReleaseOnlyOwnLaneWithoutClaiming(t *testing.T) {
	for _, owner := range []string{"installation", "other-installation"} {
		t.Run(owner, func(t *testing.T) {
			s := &store{}
			s.put(t, Lease{Version: 1, Owner: owner, Generation: 4, Heartbeat: time.Now()})
			if err := Release(t.Context(), s, "installation"); err != nil {
				t.Fatal(err)
			}
			got, found, err := Read(t.Context(), s)
			if err != nil || !found {
				t.Fatal(got, found, err)
			}
			want := owner
			if owner == "installation" {
				want = ""
			}
			if got.Owner != want || got.Generation != 4 || s.creates != 0 || s.closes != 0 {
				t.Fatalf("wrong lease after retirement: %+v creates=%d closes=%d", got, s.creates, s.closes)
			}
		})
	}
}

// store is one project's lease issue with Kata's revision checks.
type store struct {
	mu       sync.Mutex
	issues   []kata.IssueView
	calls    int
	readErr  error
	creates  int
	closes   int
	onCreate func()
}

func (s *store) OldestLabeled(_ context.Context, label string) (kata.IssueView, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.readErr != nil {
		return kata.IssueView{}, false, s.readErr
	}
	for _, i := range s.issues {
		if i.Labels[0] == label {
			return i, true, nil
		}
	}
	return kata.IssueView{}, false, nil
}
func (s *store) CreateIssue(_ context.Context, req kata.CreateReq) (kata.IssueView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.creates++
	if s.onCreate != nil {
		s.onCreate()
	}
	i := kata.IssueView{UID: "01K0000000000000000000L" + strconv.Itoa(100+len(s.issues)), Status: "open", Revision: 1, Labels: req.Labels, Metadata: map[string]json.RawMessage{}}
	s.issues = append(s.issues, i)
	return i, nil
}
func (s *store) Close(_ context.Context, req kata.CloseReq) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.closes++
	for n := range s.issues {
		if s.issues[n].UID == req.Ref {
			s.issues[n].Status = "closed"
			s.issues[n].Revision++
		}
	}
	return nil
}
func (s *store) MetaSet(_ context.Context, ref, key, value string, cond kata.Cond) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	for n := range s.issues {
		if s.issues[n].UID != ref {
			continue
		}
		if cond.IfMatch != strconv.FormatInt(s.issues[n].Revision, 10) || !cond.JSONValue {
			return 0, &kata.Error{Code: "revision_conflict"}
		}
		s.issues[n].Metadata[key] = json.RawMessage(value)
		s.issues[n].Revision++
		return s.issues[n].Revision, nil
	}
	return 0, &kata.Error{Code: "not_found"}
}

// put writes a lease directly, as another installation would.
func (s *store) put(t *testing.T, l Lease) {
	t.Helper()
	if len(s.issues) == 0 {
		s.issues = append(s.issues, kata.IssueView{UID: "01K0000000000000000000LSE0", Status: "closed", Revision: 1, Labels: []string{Label}, Metadata: map[string]json.RawMessage{}})
	}
	b, _ := json.Marshal(l)
	s.issues[0].Metadata[Key] = b
	s.issues[0].Revision++
}
func (s *store) lease(t *testing.T) Lease {
	t.Helper()
	var l Lease
	if err := json.Unmarshal(s.issues[0].Metadata[Key], &l); err != nil {
		t.Fatal(err)
	}
	return l
}

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

// A project without a lease gets one lease issue, closed so it stays out of
// ready lists, and this installation holds generation 1.
func TestClaimCreatesTheLease(t *testing.T) {
	s := &store{}
	h, err := Claim(t.Context(), s, "worker", false, now)
	if err != nil || h.Lease() != (Lease{Version: 1, Owner: "worker", Generation: 1, Heartbeat: now}) || s.creates != 1 || s.closes != 1 || s.issues[0].Status != "closed" {
		t.Fatalf("%+v %v creates=%d closes=%d", h, err, s.creates, s.closes)
	}
	if got := s.lease(t); got.Owner != "worker" || got.Generation != 1 {
		t.Fatal(got)
	}
}

func TestClaimRules(t *testing.T) {
	for _, tc := range []struct {
		name     string
		held     Lease
		takeover bool
		owner    string
		gen      int64
		refused  bool
	}{
		{"own lease resumes", Lease{1, "worker", 3, now.Add(-time.Hour)}, false, "worker", 3, false},
		{"live foreign lease refuses", Lease{1, "rival", 3, now.Add(-time.Minute)}, false, "rival", 3, true},
		{"expired foreign lease is taken", Lease{1, "rival", 3, now.Add(-TTL)}, false, "worker", 4, false},
		{"takeover outranks a live owner", Lease{1, "rival", 3, now.Add(-time.Minute)}, true, "worker", 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &store{}
			s.put(t, tc.held)
			_, err := Claim(t.Context(), s, "worker", tc.takeover, now)
			var held *HeldError
			if errors.As(err, &held) != tc.refused || (err != nil && !tc.refused) {
				t.Fatalf("err %v", err)
			}
			if tc.refused && (held.Lease.Owner != "rival" || !errors.Is(err, ErrHeld)) {
				t.Fatalf("held %+v", held)
			}
			if got := s.lease(t); got.Owner != tc.owner || got.Generation != tc.gen {
				t.Fatalf("lease %+v, want owner %s generation %d", got, tc.owner, tc.gen)
			}
		})
	}
}

// A failed read is unknown: it is neither a refusal nor a claim.
func TestUnreadableLeaseIsUnknown(t *testing.T) {
	s := &store{readErr: &kata.Error{Code: "daemon_unavailable"}}
	_, err := Claim(t.Context(), s, "worker", false, now)
	if err == nil || errors.Is(err, ErrHeld) || errors.Is(err, ErrLost) {
		t.Fatal(err)
	}
}

// Heartbeats and pre-admission confirmations renew the lease. A takeover by
// another installation ends the hold at the next renewal.
func TestRenewSeesTakeover(t *testing.T) {
	s := &store{}
	h, err := Claim(t.Context(), s, "worker", false, now)
	if err != nil {
		t.Fatal(err)
	}
	before := s.calls
	if err := h.Renew(t.Context(), now.Add(time.Minute)); err != nil || s.calls != before+1 || s.lease(t).Heartbeat != now.Add(time.Minute) {
		t.Fatalf("renew: %v, %d calls", err, s.calls-before)
	}
	if _, err := Claim(t.Context(), s, "rival", true, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	err = h.Renew(t.Context(), now.Add(3*time.Minute))
	var lost *LostError
	if !errors.As(err, &lost) || !errors.Is(err, ErrLost) || lost.Lease.Owner != "rival" || lost.Lease.Generation != 2 {
		t.Fatalf("renew after takeover: %v", err)
	}
	if err := h.Renew(t.Context(), now.Add(4*time.Minute)); !errors.Is(err, ErrLost) {
		t.Fatalf("a lost hold renewed: %v", err)
	}
	if got := s.lease(t); got.Owner != "rival" {
		t.Fatalf("stood-down owner overwrote the lease: %+v", got)
	}
}

// Two installations that create the lease issue at once agree on the oldest
// one; the extra issue is closed and never holds the lease.
func TestConcurrentCreationUsesTheOldestIssue(t *testing.T) {
	s := &store{}
	s.onCreate = func() {
		if len(s.issues) == 0 {
			s.issues = append(s.issues, kata.IssueView{UID: "01K0000000000000000000L000", Status: "open", Revision: 1, Labels: []string{Label}, Metadata: map[string]json.RawMessage{}})
		}
	}
	if _, err := Claim(t.Context(), s, "worker", false, now); err != nil {
		t.Fatal(err)
	}
	if s.issues[0].UID != "01K0000000000000000000L000" || s.lease(t).Owner != "worker" {
		t.Fatalf("lease on %s", s.issues[0].UID)
	}
	if _, set := s.issues[1].Metadata[Key]; set || s.issues[1].Status != "closed" {
		t.Fatalf("extra issue %+v", s.issues[1])
	}
}

// An event on the lease issue prompts a read-only check: the holder's own
// renewal leaves the hold in place, a takeover ends it.
func TestCheckSeesTakeoverWithoutWriting(t *testing.T) {
	s := &store{}
	h, err := Claim(t.Context(), s, "worker", false, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Renew(t.Context(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	revision := s.issues[0].Revision
	if h.Newer(revision) || h.Newer(revision-1) || !h.Newer(revision+1) {
		t.Fatalf("revision %d: own or older revisions must need no check, newer ones must", revision)
	}
	if err := h.Check(t.Context()); err != nil || s.issues[0].Revision != revision {
		t.Fatalf("own lease: %v, revision %d -> %d", err, revision, s.issues[0].Revision)
	}
	if _, err := Claim(t.Context(), s, "rival", true, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := h.Check(t.Context()); !errors.Is(err, ErrLost) {
		t.Fatalf("check after takeover: %v", err)
	}
	s.readErr = errors.New("daemon unavailable")
	if err := h.Check(t.Context()); !errors.Is(err, ErrLost) {
		t.Fatalf("a lost hold must stay lost: %v", err)
	}
}
