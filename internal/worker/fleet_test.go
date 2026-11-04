package worker

import (
	"context"
	"encoding/json"
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
	pause    json.RawMessage
	caps     kata.Caps
	capsErr  error
	capCalls int
}

func (k *fleetKata) Capabilities() (kata.Caps, error) { k.capCalls++; return k.caps, k.capsErr }
func (k *fleetKata) Jobs() (kata.JobList, error) {
	return kata.JobList{Jobs: k.jobs, ProjectPause: k.pause}, errors.Join(k.err, k.failures["jobs"])
}
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

func TestFleetPause(t *testing.T) {
	for _, pause := range []string{"", "null", `{}`, `{"invalid":true}`, "false"} {
		t.Run(pause, func(t *testing.T) {
			w, h, k := fleetFixture(t)
			k.pause = json.RawMessage(pause)
			request(t, w, journal.Request{ID: "manual", Kind: "run", JobUID: k.jobs[0].UID})
			tick(t, w, at)
			paused := pause != "" && pause != "null"
			if paused {
				runs, err := w.Store.All()
				if err != nil || len(runs) != 0 || h.runs != 0 || k.creates != 0 || len(k.bodies) != 0 {
					t.Fatal("paused work escaped", runs, h.runs, k.creates, err)
				}
				done, _, err := (journal.Requests{Dir: w.Store.Layout.Requests()}).Result("manual")
				if done || err != nil {
					t.Fatal("paused request rejected", done, err)
				}
			} else if h.runs != 1 {
				t.Fatal("unpaused did not launch", h.runs)
			}
		})
	}
}
func TestFleetPauseHoldsPreparedAfterRestartAndFailedRead(t *testing.T) {
	w, h, k := fleetFixture(t)
	k.createErr = errors.New("admission unavailable")
	request(t, w, journal.Request{ID: "manual", Kind: "run", JobUID: k.jobs[0].UID})
	_ = w.Tick(context.Background(), at)
	k.createErr = nil
	k.pause = json.RawMessage(`{}`)
	w = New(w.Config, w.Store, h, w.Clients, w.Bin)
	tick(t, w, at.Add(time.Minute))
	k.pause = nil
	k.failures = map[string]error{"jobs": errors.New("read unavailable")}
	_ = w.Tick(context.Background(), at.Add(2*time.Minute))
	if h.runs != 0 || k.creates != 1 {
		t.Fatal("prepared run bypassed pause", h.runs, k.creates)
	}
	k.failures = nil
	tick(t, w, at.Add(3*time.Minute))
	if h.runs != 1 {
		t.Fatal("prepared run did not resume", h.runs)
	}
}

func TestFleetPauseStillSettlesRunning(t *testing.T) {
	w, h, k := fleetFixture(t)
	request(t, w, journal.Request{ID: "manual", Kind: "run", JobUID: k.jobs[0].UID})
	tick(t, w, at)
	r := active(t, w)
	finishAgent(t, w, h, r)
	k.pause = json.RawMessage(`{}`)
	for n := 1; n < 8; n++ {
		tick(t, w, at.Add(time.Duration(n)*time.Second))
	}
	saved, err := w.Store.Load(r.Key)
	if err != nil || saved.Phase != "reported" || !saved.ReportDone || saved.Outcome != "succeeded" || h.runs != 1 {
		t.Fatalf("phase=%s outcome=%s report=%v launches=%d error=%v", saved.Phase, saved.Outcome, saved.ReportDone, h.runs, err)
	}
}

func TestFleetPauseEmptyJobsKeepsRequestPending(t *testing.T) {
	w, _, k := fleetFixture(t)
	request(t, w, journal.Request{ID: "manual", Kind: "run", JobUID: k.jobs[0].UID})
	k.jobs = nil
	k.pause = json.RawMessage(`{}`)
	tick(t, w, at)
	done, _, err := (journal.Requests{Dir: w.Store.Layout.Requests()}).Result("manual")
	if done || err != nil {
		t.Fatal("paused request rejected", done, err)
	}
}
