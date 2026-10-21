package inbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestScannerSnapshotDispositionsAndError(t *testing.T) {
	s, r, _, _ := scanFixture(t)
	root := t.TempDir()
	s.SaveSnapshot = func(v Snapshot) error { return WriteSnapshot(state.OSFS{}, root, v) }
	r.requests = append(r.requests, kata.InboxRequest{Ref: "prose"}, kata.InboxRequest{Ref: "waiting"}, kata.InboxRequest{Ref: "foreign"})
	r.issues["prose"] = kata.IssueView{UID: parentUID, ProjectUID: projectUID, Status: "open"}
	c := validChild()
	c.QualifiedID = "waiting"
	r.issues["waiting"] = c
	// Remove readiness from the child, then both child aliases wait.
	r.ready = nil
	foreign := validChild()
	foreign.UID = parentUID
	foreign.Owner = "other"
	r.issues["foreign"] = foreign
	r.ready = append(r.ready, foreign)
	if err := s.scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	snap, err := ReadSnapshot(state.OSFS{}, root)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, row := range snap.Requests {
		statuses[row.Ref] = row.Status
	}
	if statuses["prose"] != IgnoredRequest || statuses["waiting"] != WaitingReady || statuses["foreign"] != Unsupported || snap.LastSuccessfulScan.IsZero() {
		t.Fatal(snap)
	}
	previous := snap.LastSuccessfulScan
	r.inboxErr = errors.New("offline")
	if err := s.scan(t.Context()); err == nil {
		t.Fatal("missing scan error")
	}
	snap, err = ReadSnapshot(state.OSFS{}, root)
	if err != nil || snap.Error != "offline" || !snap.LastSuccessfulScan.Equal(previous) || len(snap.Requests) == 0 {
		t.Fatal(snap, err)
	}
	if _, err = (state.OSFS{}).Lstat(filepath.Join(root, "inbox.json")); err != nil {
		t.Fatal(err)
	}
}
func TestScannerSnapshotBound(t *testing.T) {
	s, r, _, got := scanFixture(t)
	original := r.requests[0]
	r.requests = nil
	for n := range MaxSnapshotRequests + 10 {
		r.requests = append(r.requests, kata.InboxRequest{Ref: fmt.Sprintf("unknown-%d", n)})
	}
	// The display bound must not suppress execution beyond the displayed rows.
	r.requests = append(r.requests, original)
	var snapshot Snapshot
	s.SaveSnapshot = func(v Snapshot) error { snapshot = v; return nil }
	if err := s.scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Requests) != MaxSnapshotRequests {
		t.Fatal(len(snapshot.Requests))
	}
	select {
	case d := <-got:
		if d.Child.IssueUID != childUID {
			t.Fatal(d)
		}
	default:
		t.Fatal("snapshot bound suppressed admission")
	}
}

// Long or heavily escaped external text must not prevent scan publication.
func FuzzScannerSnapshotTextBound(f *testing.F) {
	f.Add("\x00", uint32(4<<20))
	f.Add("🙂", uint32(5000))
	f.Add("short", uint32(20))
	f.Fuzz(func(t *testing.T, text string, size uint32) {
		if text == "" {
			text = "x"
		}
		// Limit materialization, keeping the uint32 input domain unrestricted.
		n := min(int(size), 4<<20+1024)
		text = strings.ToValidUTF8(text, "�")
		if len(text) > 1024 {
			text = text[:1024]
		}
		title := strings.Repeat(text, n/len(text)+1)[:n]
		s, r, _, _ := scanFixture(t)
		r.requests[0].Title = title
		root := t.TempDir()
		s.SaveSnapshot = func(v Snapshot) error { return WriteSnapshot(state.OSFS{}, root, v) }
		if err := s.scan(t.Context()); err != nil {
			t.Fatal("external text prevented snapshot", err)
		}
		snap, err := ReadSnapshot(state.OSFS{}, root)
		if err != nil || len(snap.Requests) != 1 || snap.Requests[0].Ref != r.requests[0].Ref || snap.Requests[0].Status != Accepted {
			t.Fatal(snap, err)
		}
	})
}

// These outcomes are the scanner's display contract. In particular, validation
// runs before readiness, and read failures do not become admission rejections.
func TestScannerSnapshotStatusAndReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*scanReader)
		status string
		reason string
	}{
		{"accepted", func(*scanReader) {}, Accepted, ""},
		{"child read", func(r *scanReader) { r.showErrors = map[string]error{"test#cw1k": errors.New("child offline")} }, IgnoredRequest, "child offline"},
		{"invalid child before readiness", func(r *scanReader) {
			c := r.issues["test#cw1k"]
			c.ProjectUID = "other"
			r.issues["test#cw1k"] = c
			r.ready = nil
		}, IgnoredRequest, "unsupported inbox request: child must be open in the configured project"},
		{"invalid occurrence", func(r *scanReader) { delete(r.issues["test#cw1k"].Metadata, "cron.occurrence") }, IgnoredRequest, "unsupported inbox request: invalid cron.occurrence"},
		{"invalid parent link", func(r *scanReader) {
			c := r.issues["test#cw1k"]
			c.ParentUID, c.Links = "", nil
			r.issues["test#cw1k"] = c
		}, IgnoredRequest, "unsupported inbox request: child must have a parent link"},
		{"readiness before ownership", func(r *scanReader) {
			c := r.issues["test#cw1k"]
			c.Owner = "other"
			r.issues["test#cw1k"] = c
			r.ready = nil
		}, WaitingReady, "child is not ready"},
		{"ownership", func(r *scanReader) {
			c := r.issues["test#cw1k"]
			c.Owner = "other"
			r.issues["test#cw1k"] = c
		}, Unsupported, "unsupported inbox request: child belongs to another actor"},
		{"notification", func(r *scanReader) { delete(r.issues["test#cw1k"].Metadata, "notify.d29ya2VyL2luYm94") }, Unsupported, "unsupported inbox request: notification is absent or invalid"},
		{"parent read", func(r *scanReader) { r.showErrors = map[string]error{parentUID: errors.New("parent offline")} }, IgnoredRequest, "parent offline"},
		{"parent project", func(r *scanReader) {
			p := r.issues[parentUID]
			p.ProjectUID = "other"
			r.issues[parentUID] = p
		}, Unsupported, "unsupported inbox request: parent must be in the configured project"},
		{"binding rejection", func(r *scanReader) {
			p := r.issues[parentUID]
			p.Metadata = map[string]json.RawMessage{"herdr_kata.profile": json.RawMessage(`"unknown"`)}
			r.issues[parentUID] = p
		}, Unsupported, `unsupported inbox request: unknown profile "unknown"`},
		{"binding read", func(r *scanReader) {
			p := r.issues[parentUID]
			p.Metadata = map[string]json.RawMessage{"herdr_kata.workflow": json.RawMessage(`"Check"`)}
			r.issues[parentUID] = p
			r.workflowReader.err = errors.New("workflow offline")
		}, IgnoredRequest, "workflow offline"},
		{"oversized input", func(r *scanReader) {
			c := r.issues["test#cw1k"]
			c.Body = strings.Repeat("x", 4097)
			r.issues["test#cw1k"] = c
		}, Unsupported, "unsupported inbox request: invalid workflow input: input must be UTF-8 without NUL, at most 4 KiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, _, got := scanFixture(t)
			tc.change(r)
			var snapshot Snapshot
			s.SaveSnapshot = func(v Snapshot) error { snapshot = v; return nil }
			if err := s.scan(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Requests) != 1 {
				t.Fatalf("snapshot requests: %+v", snapshot)
			}
			row := snapshot.Requests[0]
			if row.Ref != "test#cw1k" || row.Status != tc.status || row.Reason != tc.reason {
				t.Fatalf("request = %+v, want status %q reason %q", row, tc.status, tc.reason)
			}
			if tc.status == Accepted {
				if due := takeDue(t, got); due.Child.IssueUID != childUID {
					t.Fatalf("accepted child: %+v", due.Child)
				}
			}
			select {
			case d := <-got:
				t.Fatalf("unexpected dispatch: %+v", d)
			default:
			}
		})
	}
}
