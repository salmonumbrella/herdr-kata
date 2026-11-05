package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/attempt"
	"github.com/salmonumbrella/herdr-kata/internal/clock"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type cancelProofHC struct {
	*fakeHC
	keepLive   bool
	killErr    error
	afterKill  func()
	lostRemove bool
	removes    int
}

func (h *cancelProofHC) Kill(ctx context.Context, id string) error {
	if h.keepLive {
		h.kills++
	} else if err := h.fakeHC.Kill(ctx, id); err != nil {
		return err
	}
	if h.afterKill != nil {
		h.afterKill()
	}
	return h.killErr
}
func (h *cancelProofHC) Remove(ctx context.Context, id string) error {
	h.removes++
	if err := h.fakeHC.Remove(ctx, id); err != nil {
		return err
	}
	if h.lostRemove {
		return errors.New("remove reply lost")
	}
	return nil
}
func cancelRemote(k *fleetKata, r journal.Run) {
	k.caps.Controls = []string{"cancel_request"}
	k.remote = []kata.CronRequest{{Kind: "cancel", UID: "cancel-proof", RunUID: r.RunUID, ExpiresAt: at.Add(5 * time.Minute)}}
}
func commandFleet(k *fleetKata) {
	k.jobs[0].Definition.Action = kata.Action{Kind: "execute", WorkflowUID: new("01J00000000000000000000012")}
	k.workflows = []kata.CronWorkflow{{UID: "01J00000000000000000000012", DefinitionEventUID: "01J00000000000000000000013", Definition: kata.WorkflowDefinition{Version: 1, Steps: []kata.WorkflowStep{{Key: "main", Kind: "command", Command: new("exit 0")}}}}}
}
func TestFleetCancellationRequiresStopProof(t *testing.T) {
	for _, scenario := range []string{"live", "saved-timeout", "changed", "core", "missing", "lost", "lost-reply"} {
		t.Run(scenario, func(t *testing.T) {
			w, h, k := fleetFixture(t)
			commandFleet(k)
			k.caps.Controls = []string{"cancel_request"}
			c := &cancelProofHC{fakeHC: h}
			w.HC = c
			request(t, w, journal.Request{ID: "first", Kind: "run", JobUID: k.jobs[0].UID})
			tick(t, w, at)
			r := active(t, w)
			switch scenario {
			case "live":
				c.keepLive = true
			case "saved-timeout":
				if err := w.Store.Update(r.Key, func(r *journal.Run) error { r.Attempts[0].Hold = "timeout"; return nil }); err != nil {
					t.Fatal(err)
				}
			case "changed":
				c.afterKill = func() { v := h.terms["t1"]; v.CreatedMs++; v.State = "live"; h.terms["t1"] = v }
			case "core":
				c.afterKill = func() { h.core = "other-core" }
			case "missing":
				c.afterKill = func() { delete(h.terms, "t1") }
			case "lost":
				c.afterKill = func() { v := h.terms["t1"]; v.Reason = "lost"; h.terms["t1"] = v }
			case "lost-reply":
				c.killErr = errors.New("kill reply lost")
			}
			cancelRemote(k, r)
			tick(t, w, at.Add(time.Second))
			saved, err := w.Store.Load(r.Key)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "lost-reply" {
				if saved.Outcome != "cancelled" || !saved.ReportDone || h.terms["t1"].State != "finished" || c.removes != 0 {
					t.Fatalf("proven cancellation lost: phase=%s outcome=%s hold=%s", saved.Phase, saved.Outcome, saved.Hold)
				}
				return
			}
			if saved.Phase != "held" || saved.Outcome == "cancelled" || c.removes != 0 {
				t.Fatalf("unproven cancellation freed capacity: phase=%s outcome=%s hold=%s", saved.Phase, saved.Outcome, saved.Hold)
			}
			request(t, w, journal.Request{ID: "second", Kind: "run", JobUID: k.jobs[0].UID})
			tick(t, w, at.Add(2*time.Second))
			if h.runs != 1 {
				t.Fatal("held execution freed overlap", h.runs)
			}
		})
	}
}
func TestFleetCancelAfterLostRemovalReply(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted-result", true: "confirmed-timeout"}[stopped], func(t *testing.T) {
			w, h, k := fleetFixture(t)
			c := &cancelProofHC{fakeHC: h, lostRemove: true}
			w.HC = c
			request(t, w, journal.Request{ID: "first", Kind: "run", JobUID: k.jobs[0].UID})
			tick(t, w, at)
			r := active(t, w)
			when := at.Add(time.Second)
			if stopped {
				if err := w.Store.Update(r.Key, func(r *journal.Run) error { r.ExpiresAt = at.Add(2 * time.Minute); return nil }); err != nil {
					t.Fatal(err)
				}
				when = at.Add(2 * time.Minute)
				if _, err := attempt.Settle(clock.WithNow(context.Background(), when), c, w.Store, r.Key, 0, when); err != nil {
					t.Fatal(err)
				}
				when = when.Add(10 * time.Second)
			} else {
				finishAgent(t, w, h, r)
			}
			if _, err := attempt.Settle(clock.WithNow(context.Background(), when), c, w.Store, r.Key, 0, when); err == nil {
				t.Fatal("expected lost remove reply")
			}
			saved, err := w.Store.Load(r.Key)
			if err != nil || saved.Attempts[0].Cleanup != "remove" || len(h.terms) != 0 {
				t.Fatal("removal intent missing", err, saved.Attempts)
			}
			cancelRemote(k, r)
			w = New(w.Config, journal.New(w.Store.Layout), c, w.Clients, w.Bin)
			tick(t, w, when.Add(time.Second))
			saved, err = w.Store.Load(r.Key)
			if err != nil || saved.Outcome != "cancelled" || !saved.ReportDone || saved.Attempts[0].Cleanup != "" || saved.Attempts[0].Hold != "" || h.runs != 1 || c.removes != 1 {
				t.Fatalf("known removal became loss: phase=%s outcome=%s hold=%s cleanup=%s err=%v", saved.Phase, saved.Outcome, saved.Hold, saved.Attempts[0].Cleanup, err)
			}
			want := "succeeded"
			if stopped {
				want = "failed"
			}
			if saved.Attempts[0].Status != want {
				t.Fatal("known outcome lost", saved.Attempts[0])
			}
		})
	}
}
func TestFleetPreparationUsesConsistentObservationTime(t *testing.T) {
	w, h, k := fleetFixture(t)
	k.createErr = &kata.Error{Code: 3}
	current := at
	w.Clients["example-project"] = delayedFleet{k, "jobs", func() { current = at.Add(time.Second) }}
	request(t, w, journal.Request{ID: "refused", Kind: "run", JobUID: k.jobs[0].UID})
	ctx := clock.WithSource(context.Background(), func() time.Time { return current })
	if err := w.Tick(ctx, at); err == nil {
		t.Fatal("expected definitive admission refusal")
	}
	if h.runs != 0 || len(k.bodies) != 1 {
		t.Fatal(h.runs, len(k.bodies))
	}
	var body kata.ObserveCronRunBody
	if err := json.Unmarshal(k.bodies[0], &body); err != nil {
		t.Fatal(err)
	}
	if body.StartedAt == nil || body.EndedAt == nil || body.EndedAt.Before(*body.StartedAt) || !body.StartedAt.Equal(at) || !body.EndedAt.Equal(at) {
		t.Fatalf("inverted/floating observation timestamps: %s", k.bodies[0])
	}
}
