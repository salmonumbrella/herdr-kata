package inbox

import (
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"strings"
	"testing"
	"time"
)

func TestSnapshotRoundTripAndAbsent(t *testing.T) {
	root := t.TempDir()
	fs := state.OSFS{}
	s, err := ReadSnapshot(fs, root)
	if err != nil || len(s.Requests) != 0 {
		t.Fatal(s, err)
	}
	s.LastSuccessfulScan = time.Now().UTC()
	s.Error = "latest scan failed"
	for _, status := range []string{Accepted, WaitingReady, Unsupported, IgnoredRequest} {
		s.Requests = append(s.Requests, Request{Ref: "test#ref", Status: status, Reason: "evidence"})
	}
	if err := WriteSnapshot(fs, root, s); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSnapshot(fs, root)
	if err != nil || len(got.Requests) != 4 || got.Error != s.Error || !got.LastSuccessfulScan.Equal(s.LastSuccessfulScan) {
		t.Fatal(got, err)
	}
}
func TestSnapshotBounds(t *testing.T) {
	for _, s := range []Snapshot{{Version: 2}, {Version: 1, Requests: make([]Request, MaxSnapshotRequests+1)}, {Version: 1, Requests: []Request{{Ref: "test#ref", Status: "invalid"}}}} {
		if err := WriteSnapshot(state.OSFS{}, t.TempDir(), s); err == nil {
			t.Fatal("invalid snapshot accepted")
		}
	}
}

func TestSnapshotWriteSizeBound(t *testing.T) {
	s := Snapshot{Version: 1, Error: strings.Repeat("x", 4<<20)}
	if err := WriteSnapshot(state.OSFS{}, t.TempDir(), s); err == nil {
		t.Fatal("oversized snapshot written")
	}
}
