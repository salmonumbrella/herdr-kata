package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/report"
	"hegel.dev/go/hegel"
)

type fleetKata struct {
	*fakeKata
	caps     kata.Caps
	capsErr  error
	capCalls int
}

func (k *fleetKata) Capabilities() (kata.Caps, error) { k.capCalls++; return k.caps, k.capsErr }
func fleetFixture(t *testing.T) (*Worker, *fakeHC, *fleetKata) {
	w, h, k := fixture(t)
	f := &fleetKata{fakeKata: k}
	w.Clients["example-project"] = f
	return w, h, f
}

// A restricted job must produce neither execution nor unsupported evidence.
func TestFleetExecutionExclusions(t *testing.T) {
	for _, tt := range []struct {
		mode, member, author string
		allowed              bool
	}{
		{"none", "worker", "worker", false}, {"future", "worker", "worker", false},
		{"owned", "worker", "other", false}, {"owned", "", "", false},
		{"owned", "worker", "worker", true}, {"all", "", "other", true}, {"legacy", "", "other", true},
	} {
		t.Run(tt.mode+tt.member+tt.author, func(t *testing.T) {
			w, h, k := fleetFixture(t)
			if tt.mode != "legacy" {
				k.caps.Execution = &kata.Execution{Mode: tt.mode, MemberActor: tt.member}
			}
			k.jobs[0].Author = tt.author
			request(t, w, journal.Request{ID: "manual", Kind: "run", JobUID: k.jobs[0].UID})
			tick(t, w, at)
			if tt.allowed {
				if h.runs != 1 {
					t.Fatalf("launches %d", h.runs)
				}
				return
			}
			done, reason, err := (journal.Requests{Dir: w.Store.Layout.Requests()}).Result("manual")
			if err != nil || !done || !strings.HasPrefix(reason, "replica execution") {
				t.Fatalf("rejection %v %q %v", done, reason, err)
			}
			runs, err := w.Store.All()
			if err != nil || len(runs) != 0 || h.runs != 0 || len(k.bodies) != 0 {
				t.Fatalf("work escaped: %v %d %d %v", runs, h.runs, len(k.bodies), err)
			}
			out, err := (report.Outbox{Layout: w.Store.Layout}).All()
			if err != nil || len(out) != 0 {
				t.Fatal(out, err)
			}
		})
	}
}
func TestFleetUnknownDiscoveryAndRefresh(t *testing.T) {
	w, h, k := fleetFixture(t)
	k.capsErr = errors.New("discovery unavailable")
	request(t, w, journal.Request{ID: "manual", Kind: "run", JobUID: k.jobs[0].UID})
	if w.Tick(context.Background(), at) == nil || h.runs != 0 {
		t.Fatal("unknown discovery launched")
	}
	k.capsErr = nil
	tick(t, w, at.Add(time.Minute))
	if k.capCalls != 1 || h.runs != 0 {
		t.Fatal("failed discovery busy loop")
	}
	tick(t, w, at.Add(5*time.Minute))
	if k.capCalls != 2 || h.runs != 1 {
		t.Fatalf("legacy discovery calls=%d launches=%d", k.capCalls, h.runs)
	}
}
func TestFleetLastGoodPolicy(t *testing.T) {
	w, h, k := fleetFixture(t)
	k.caps.Execution = &kata.Execution{Mode: "none"}
	tick(t, w, at)
	k.capsErr = errors.New("refresh unavailable")
	request(t, w, journal.Request{ID: "manual", Kind: "run", JobUID: k.jobs[0].UID})
	_ = w.Tick(context.Background(), at.Add(5*time.Minute))
	tick(t, w, at.Add(6*time.Minute))
	if h.runs != 0 || k.capCalls != 2 {
		t.Fatal(h.runs, k.capCalls)
	}
}
func TestFleetUnknownModesExclude(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		mode := "future-" + hegel.Draw(ht, hegel.Text())
		if reason := (kata.Caps{Execution: &kata.Execution{Mode: mode}}).Excludes("worker"); reason == "" {
			ht.Fatalf("unknown mode %q allowed", mode)
		}
	})
}

func TestFleetOwnedRequiresExactAuthor(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		member := hegel.Draw(ht, hegel.Text())
		author := hegel.Draw(ht, hegel.Text())
		allowed := (kata.Caps{Execution: &kata.Execution{Mode: "owned", MemberActor: member}}).Excludes(author) == ""
		if allowed != (member != "" && member == author) {
			ht.Fatalf("member %q author %q allowed %v", member, author, allowed)
		}
	})
}
