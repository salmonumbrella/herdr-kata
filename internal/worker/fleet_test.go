package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/clock"
	"github.com/salmonumbrella/herdr-kata/internal/config"
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
	remote       []kata.CronRequest
	requestCalls int
	requestErr   error
	pause        json.RawMessage
	caps         kata.Caps
	capsErr      error
	capCalls     int
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
	if w.Tick(clock.WithNow(context.Background(), at), at) == nil || h.runs != 0 {
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
	_ = w.Tick(clock.WithNow(context.Background(), at.Add(5*time.Minute)), at.Add(5*time.Minute))
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
	_ = w.Tick(clock.WithNow(context.Background(), at), at)
	k.createErr = nil
	k.pause = json.RawMessage(`{}`)
	w = New(w.Config, w.Store, h, w.Clients, w.Bin)
	tick(t, w, at.Add(time.Minute))
	k.pause = nil
	k.failures = map[string]error{"jobs": errors.New("read unavailable")}
	_ = w.Tick(clock.WithNow(context.Background(), at.Add(2*time.Minute)), at.Add(2*time.Minute))
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

func (k *fleetKata) Requests() ([]kata.CronRequest, error) {
	k.requestCalls++
	return k.remote, k.requestErr
}
func remoteRun(k *fleetKata, expiry time.Time) kata.CronRequest {
	return kata.CronRequest{UID: "request-one", Kind: "run", JobUID: k.jobs[0].UID, DefinitionEventUID: k.jobs[0].DefinitionEventUID, ExpiresAt: expiry}
}
func TestFleetRequests(t *testing.T) {
	for _, scenario := range []string{"eligible", "expired", "boundary", "definition", "excluded", "unsupported", "capacity", "ungated", "paused"} {
		t.Run(scenario, func(t *testing.T) {
			w, h, k := fleetFixture(t)
			k.caps.Controls = []string{"run_request"}
			req := remoteRun(k, at.Add(time.Minute))
			switch scenario {
			case "expired":
				req.ExpiresAt = at.Add(-time.Second)
			case "boundary":
				req.ExpiresAt = at
			case "definition":
				req.DefinitionEventUID = "other"
			case "excluded":
				k.caps.Execution = &kata.Execution{Mode: "none"}
			case "unsupported":
				k.jobs[0].Definition.CheckoutKey = new("missing")
			case "capacity":
				w.Config.Limits.MaxActiveRuns = 0
			case "ungated":
				k.caps.Controls = nil
			case "paused":
				k.pause = json.RawMessage(`{}`)
			}
			k.remote = []kata.CronRequest{req}
			tick(t, w, at)
			runs, err := w.Store.All()
			want := 0
			if scenario == "eligible" {
				want = 1
			}
			if err != nil || len(runs) != want || h.runs != want {
				t.Fatalf("runs=%d launches=%d want=%d err=%v", len(runs), h.runs, want, err)
			}
			if want == 1 {
				if runs[0].Occurrence != "request:request-one" {
					t.Fatal(runs[0].Occurrence)
				}
				w = New(w.Config, w.Store, h, w.Clients, w.Bin)
				tick(t, w, at.Add(2*time.Minute))
				if h.runs != 1 {
					t.Fatal("request relaunched", h.runs)
				}
			}
		})
	}
}
func TestFleetRequestPreparationSurvivesTTL(t *testing.T) {
	w, h, k := fleetFixture(t)
	k.caps.Controls = []string{"run_request"}
	k.remote = []kata.CronRequest{remoteRun(k, at.Add(time.Second))}
	k.createErr = errors.New("admission unavailable")
	_ = w.Tick(clock.WithNow(context.Background(), at), at)
	runs, err := w.Store.All()
	if err != nil || len(runs) != 1 || runs[0].Phase != "prepared" {
		t.Fatal("not durable", len(runs), err)
	}
	k.createErr = nil
	w = New(w.Config, w.Store, h, w.Clients, w.Bin)
	tick(t, w, at.Add(time.Minute))
	if h.runs != 1 {
		t.Fatal("durable acceptance lost", h.runs)
	}
}

// Delays at each external read must not extend the remote acceptance deadline.
type delayedFleet struct {
	*fleetKata
	boundary string
	delay    func()
}

func (k delayedFleet) Jobs() (kata.JobList, error) {
	if k.boundary == "jobs" {
		k.delay()
	}
	return k.fleetKata.Jobs()
}
func (k delayedFleet) Ready() (map[string]bool, error) {
	if k.boundary == "ready" {
		k.delay()
	}
	return k.fleetKata.Ready()
}
func (k delayedFleet) IssueTitle(uid string) (string, error) {
	if k.boundary == "title" {
		k.delay()
	}
	return k.fleetKata.IssueTitle(uid)
}
func TestFleetRequestAcceptanceClock(t *testing.T) {
	for _, boundary := range []string{"jobs", "ready", "title"} {
		t.Run(boundary, func(t *testing.T) {
			w, h, k := fleetFixture(t)
			k.caps.Controls = []string{"run_request"}
			uid := "01J00000000000000000000020"
			k.jobs[0].Definition.Issue = &kata.IssuePolicy{Kind: "existing", UID: &uid}
			k.ready[uid] = true
			k.remote = []kata.CronRequest{remoteRun(k, at.Add(time.Second))}
			current := at
			w.Clients["example-project"] = delayedFleet{k, boundary, func() { current = at.Add(time.Second) }}
			if err := w.Tick(clock.WithSource(context.Background(), func() time.Time { return current }), at); err != nil {
				t.Fatal(err)
			}
			runs, err := w.Store.All()
			if err != nil || len(runs) != 0 || h.runs != 0 || len(k.bodies) != 0 {
				t.Fatalf("expired acceptance: journals=%d launches=%d reports=%d err=%v", len(runs), h.runs, len(k.bodies), err)
			}
		})
	}
}
func TestFleetCancellationGraceAfterSlowRead(t *testing.T) {
	w, h, k := fleetFixture(t)
	k.caps.Controls = []string{"cancel_request"}
	request(t, w, journal.Request{ID: "manual", Kind: "run", JobUID: k.jobs[0].UID})
	tick(t, w, at)
	r := active(t, w)
	k.remote = []kata.CronRequest{{Kind: "cancel", UID: "cancel-one", RunUID: r.RunUID, ExpiresAt: at.Add(time.Minute)}}
	current := at.Add(time.Second)
	ctx := clock.WithSource(context.Background(), func() time.Time { return current })
	w.Clients["example-project"] = delayedFleet{k, "jobs", func() { current = at.Add(40 * time.Second) }}
	if err := w.Tick(ctx, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	w.Clients["example-project"] = k
	current = at.Add(49 * time.Second)
	if err := w.Tick(ctx, current); err != nil {
		t.Fatal(err)
	}
	if h.kills != 0 || h.interrupts != 1 {
		t.Fatal("grace shortened", h.kills, h.interrupts)
	}
	current = at.Add(50 * time.Second)
	if err := w.Tick(ctx, current); err != nil {
		t.Fatal(err)
	}
	saved, err := w.Store.Load(r.Key)
	if err != nil || h.kills != 1 || saved.Outcome != "cancelled" {
		t.Fatal(h.kills, saved.Outcome, err)
	}
}

func TestFleetAdmissionIsolationProperty(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		gate := hegel.Draw(ht, hegel.Integers(0, 6))
		w, h, k := fleetFixture(t)
		k.createErr = errors.New("admission unavailable")
		request(t, w, journal.Request{ID: "prepared", Kind: "run", JobUID: k.jobs[0].UID})
		_ = w.Tick(clock.WithNow(context.Background(), at), at)
		r := active(t, w)
		k.createErr = nil
		switch gate {
		case 1:
			k.pause = json.RawMessage("null")
		case 2:
			k.pause = json.RawMessage("{}")
		case 3:
			k.pause = json.RawMessage("false")
		case 4:
			k.pause = json.RawMessage("[]")
		case 5:
			k.failures = map[string]error{"jobs": errors.New("bad jobs envelope")}
		case 6:
			k.capsErr = errors.New("bad capabilities envelope")
		}
		otherJob := k.jobs[0]
		otherJob.UID = "01J00000000000000000000030"
		other := &fleetKata{fakeKata: &fakeKata{jobs: []kata.CronJob{otherJob}}}
		w.Config.Projects = append(w.Config.Projects, config.Project{Name: "other-project", Actor: "worker", MaxActiveRuns: 1})
		w.Clients["other-project"] = other
		w = New(w.Config, w.Store, h, w.Clients, w.Bin)
		request(t, w, journal.Request{ID: "other", Kind: "run", Project: "other-project", JobUID: otherJob.UID})
		_ = w.Tick(clock.WithNow(context.Background(), at.Add(time.Minute)), at.Add(time.Minute))
		saved, err := w.Store.Load(r.Key)
		if err != nil {
			ht.Fatal(err)
		}
		if gate >= 2 {
			if h.runs != 1 || saved.Phase != "prepared" || k.creates != 1 || len(k.bodies) != 0 {
				ht.Fatal("closed gate leaked", gate, h.runs, saved.Phase, k.creates, len(k.bodies))
			}
		} else if h.runs != 2 || saved.Phase != "running" {
			ht.Fatal("open gate failed", gate, h.runs, saved.Phase)
		}
	}, hegel.WithTestCases(28))
}

func TestFleetAcceptanceBoundaryProperty(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		delay := hegel.Draw(ht, hegel.Integers[int64](0, 2*int64(time.Second)))
		boundary := []string{"jobs", "ready", "title"}[hegel.Draw(ht, hegel.Integers(0, 2))]
		w, h, k := fleetFixture(t)
		k.caps.Controls = []string{"run_request"}
		uid := "01J00000000000000000000020"
		k.jobs[0].Definition.Issue = &kata.IssuePolicy{Kind: "existing", UID: &uid}
		k.ready[uid] = true
		k.remote = []kata.CronRequest{remoteRun(k, at.Add(time.Second)), remoteRun(k, at.Add(time.Second))}
		current := at
		w.Clients["example-project"] = delayedFleet{k, boundary, func() { current = at.Add(time.Duration(delay)) }}
		ctx := clock.WithSource(context.Background(), func() time.Time { return current })
		if err := w.Tick(ctx, at); err != nil {
			ht.Fatal(err)
		}
		runs, err := w.Store.All()
		if err != nil {
			ht.Fatal(err)
		}
		accepted := delay < int64(time.Second)
		if accepted {
			if len(runs) != 1 || h.runs != 1 || runs[0].Occurrence != "request:request-one" {
				ht.Fatal("acceptance identity", len(runs), h.runs)
			}
			original := runs[0].RunUID
			w = New(w.Config, w.Store, h, w.Clients, w.Bin)
			current = at.Add(time.Minute)
			w.Clients["example-project"] = k
			if err := w.Tick(ctx, current); err != nil {
				ht.Fatal(err)
			}
			runs, err = w.Store.All()
			if err != nil || len(runs) != 1 || runs[0].RunUID != original || h.runs != 1 {
				ht.Fatal("duplicate after restart", len(runs), h.runs, err)
			}
		} else if len(runs) != 0 || h.runs != 0 {
			ht.Fatal("late acceptance", delay, len(runs), h.runs)
		}
	}, hegel.WithTestCases(28))
}

func TestFleetCapabilityAttemptClock(t *testing.T) {
	w, _, k := fleetFixture(t)
	other := &fleetKata{fakeKata: &fakeKata{}}
	w.Config.Projects = append([]config.Project{{Name: "other-project", Actor: "worker"}}, w.Config.Projects...)
	current := at
	w.Clients["other-project"] = delayedFleet{other, "jobs", func() { current = at.Add(70 * time.Second) }}
	ctx := clock.WithSource(context.Background(), func() time.Time { return current })
	if err := w.Tick(ctx, at); err != nil {
		t.Fatal(err)
	}
	w.Clients["other-project"] = other
	k.capsErr = errors.New("refresh unavailable")
	current = at.Add(369 * time.Second)
	_ = w.Tick(ctx, current)
	if k.capCalls != 1 {
		t.Fatal("early refresh", k.capCalls)
	}
	current = at.Add(370 * time.Second)
	_ = w.Tick(ctx, current)
	if k.capCalls != 2 {
		t.Fatal("missing boundary refresh", k.capCalls)
	}
	current = at.Add(669 * time.Second)
	_ = w.Tick(ctx, current)
	if k.capCalls != 2 {
		t.Fatal("failed refresh busy loop", k.capCalls)
	}
}

func TestFleetCancel(t *testing.T) {
	for _, scenario := range []string{"live", "cleanup", "completed", "unlaunched", "foreign", "lost", "core", "ambiguous", "finished-lost", "unobserved-address"} {
		t.Run(scenario, func(t *testing.T) {
			w, h, k := fleetFixture(t)
			k.caps.Controls = []string{"cancel_request"}
			request(t, w, journal.Request{ID: "manual", Kind: "run", JobUID: k.jobs[0].UID})
			tick(t, w, at)
			r := active(t, w)
			if scenario == "cleanup" || scenario == "completed" {
				finishAgent(t, w, h, r)
			}
			if err := w.Store.Update(r.Key, func(saved *journal.Run) error {
				switch scenario {
				case "cleanup":
					saved.Attempts[0].Cleanup = "remove"
					saved.Attempts[0].Status = "settling"
				case "completed":
					saved.Attempts[0].Status = "needs-human"
				case "unobserved-address":
					saved.Attempts[0].Address = ""
				case "unlaunched":
					saved.Attempts = nil
				case "ambiguous":
					saved.Attempts[0].Hold = "launch_ambiguous"
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "foreign":
				term := h.terms["t1"]
				term.CreatedMs++
				h.terms["t1"] = term
			case "finished-lost":
				term := h.terms["t1"]
				term.State, term.Reason = "finished", "lost"
				h.terms["t1"] = term
			case "lost", "unobserved-address":
				delete(h.terms, "t1")
			case "core":
				h.core = "replacement-core"
			}
			k.remote = []kata.CronRequest{{UID: "cancel-one", Kind: "cancel", RunUID: r.RunUID, ExpiresAt: at.Add(time.Minute)}}
			k.pause = json.RawMessage(`{}`)
			k.failures = map[string]error{"jobs": errors.New("definitions unavailable")}
			_ = w.Tick(clock.WithNow(context.Background(), at.Add(time.Second)), at.Add(time.Second))
			k.remote = nil
			w = New(w.Config, w.Store, h, w.Clients, w.Bin)
			_ = w.Tick(clock.WithNow(context.Background(), at.Add(12*time.Second)), at.Add(12*time.Second))
			saved, err := w.Store.Load(r.Key)
			unsafe := scenario == "foreign" || scenario == "lost" || scenario == "core" || scenario == "ambiguous" || scenario == "finished-lost" || scenario == "unobserved-address"
			if err != nil || h.runs != 1 || saved.Detail != "cancel requested: request:cancel-one" {
				t.Fatal(saved.Phase, saved.Detail, h.runs, err)
			}
			if unsafe {
				if saved.Phase != "held" || saved.Outcome == "cancelled" || h.kills != 0 {
					t.Fatal("unsafe cancellation", saved.Phase, saved.Hold, h.kills)
				}
			} else if saved.Outcome != "cancelled" || !saved.ReportDone {
				t.Fatal("cancel not reported", saved.Phase, saved.Hold, saved.Outcome)
			}
			if scenario != "lost" && scenario != "unobserved-address" && len(h.terms) != 1 {
				t.Fatal("evidence removed", h.terms)
			}
		})
	}
}

func TestFleetCancelCommandAndGrace(t *testing.T) {
	for _, command := range []bool{false, true} {
		t.Run(fmt.Sprint(command), func(t *testing.T) {
			w, h, k := fleetFixture(t)
			if command {
				k.jobs[0].Definition.Action = kata.Action{Kind: "execute", WorkflowUID: new("01J00000000000000000000012")}
				k.workflows = []kata.CronWorkflow{{UID: "01J00000000000000000000012", DefinitionEventUID: "01J00000000000000000000013", Definition: kata.WorkflowDefinition{Version: 1, Steps: []kata.WorkflowStep{{Key: "main", Kind: "command", Command: new("exit 0")}}}}}
			}
			k.caps.Controls = []string{"cancel_request"}
			request(t, w, journal.Request{ID: "manual", Kind: "run", JobUID: k.jobs[0].UID})
			tick(t, w, at)
			r := active(t, w)
			k.remote = []kata.CronRequest{{Kind: "cancel", UID: "cancel-one", RunUID: r.RunUID, ExpiresAt: at.Add(time.Minute)}}
			tick(t, w, at.Add(time.Second))
			if command {
				if h.kills != 1 {
					t.Fatal("command not killed", h.kills)
				}
			} else {
				if h.kills != 0 || h.interrupts != 1 {
					t.Fatal("grace skipped or interrupt missing")
				}
				tick(t, w, at.Add(10*time.Second))
				if h.kills != 0 {
					t.Fatal("grace shortened")
				}
				tick(t, w, at.Add(11*time.Second))
				if h.kills != 1 {
					t.Fatal("prompt not killed")
				}
			}
			saved, err := w.Store.Load(r.Key)
			if err != nil || saved.Outcome != "cancelled" || h.runs != 1 || len(h.terms) != 1 {
				t.Fatal(saved.Outcome, err)
			}
		})
	}
}
