package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/daemon"
)

// Status says whether this installation holds each lane, and why not.
func TestStatusShowsLaneLease(t *testing.T) {
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		lease daemon.LeaseStatus
		want  string
	}{
		{daemon.LeaseStatus{Held: true, Owner: "here", Generation: 3, CheckedAt: at}, "lane lease: held at generation 3"},
		{daemon.LeaseStatus{Owner: "elsewhere", Generation: 4, Error: "another installation (elsewhere) owns this lane", CheckedAt: at}, "lane lease: not held: another installation (elsewhere) owns this lane"},
	} {
		var out bytes.Buffer
		printProjectStatus(&out, daemon.Status{Lease: &tc.lease})
		if !strings.Contains(out.String(), tc.want) {
			t.Fatalf("status %q, want %q", out.String(), tc.want)
		}
	}
}
