package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/report"
	"os"
	"testing"
	"time"
)

// Historical intents must survive current definitions, and never occupy run capacity.
func TestUnsupportedIntentSurvivesDefinitionChanges(t *testing.T) {
	for _, change := range []string{"repaired", "disabled", "deleted", "repinned", "unreadable", "unready", "unenrolled"} {
		t.Run(change, func(t *testing.T) {
			w, h, k := fixture(t)
			j := &k.jobs[0]
			j.Definition.Options = map[string]any{"agent": "missing"}
			k.err = &kata.Error{Code: 7}
			// Definitions must be readable while the observe boundary alone fails.
			boundary := &outboxKata{fakeKata: k, observeErr: &kata.Error{Code: 7}}
			k.err = nil
			w.Clients["example-project"] = boundary
			if err := w.Tick(t.Context(), at); err == nil {
				t.Fatal("missing delivery error")
			}
			box := report.Outbox{Layout: w.Store.Layout}
			r, err := box.All()
			if err != nil || len(r) != 1 || r[0].State != "pending" {
				t.Fatal(r, err)
			}
			first := bytes.Clone(boundary.bodies[0])
			switch change {
			case "repaired":
				j.Definition.Options = nil
			case "disabled":
				j.Definition.Enabled = false
			case "deleted":
				j.DeletedAt = new(at)
			case "repinned":
				j.Definition.Options = map[string]any{"executor": "other"}
			case "unreadable":
				k.failures = map[string]error{"jobs": errors.New("definitions unavailable")}
			case "unready":
				j.Definition.Issue = &kata.IssuePolicy{Kind: "existing", UID: new("issue")}
			case "unenrolled":
				w.Config.Projects = nil
			}
			boundary.observeErr = nil
			w = New(w.Config, w.Store, h, w.Clients, w.Bin)
			_ = w.Tick(t.Context(), at.Add(time.Minute))
			r, err = box.All()
			if err != nil {
				t.Fatal(err)
			}
			if change == "unenrolled" {
				if r[0].State != "pending" || len(boundary.bodies) != 1 {
					t.Fatal(r, len(boundary.bodies))
				}
			} else if r[0].State != "done" || len(boundary.bodies) != 2 || !bytes.Equal(first, boundary.bodies[1]) {
				t.Fatal(r, boundary.bodies)
			}
			runs, err := w.Store.All()
			if err != nil || len(runs) != 0 || h.runs != 0 || k.creates != 0 {
				t.Fatal(runs, err, h.runs, k.creates)
			}
			if _, err := os.Stat(w.Store.Layout.Job(j.UID)); !os.IsNotExist(err) {
				t.Fatalf("unsupported checkpoint: %v", err)
			}
		})
	}
}

type outboxKata struct {
	*fakeKata
	observeErr        error
	beforeUnsupported func()
}

func (k *outboxKata) Observe(uid string, b []byte, rev int) (int, bool, error) {
	var body kata.ObserveCronRunBody
	if err := json.Unmarshal(b, &body); err != nil {
		panic(err)
	}
	if body.Summary.Message != nil && *body.Summary.Message != "running" && body.Status == "failed" {
		if k.beforeUnsupported != nil {
			k.beforeUnsupported()
		}
		k.bodies = append(k.bodies, bytes.Clone(b))
		return 0, false, k.observeErr
	}
	return k.fakeKata.Observe(uid, b, rev)
}

func TestUnsupportedDeliveryFollowsOrdinaryWork(t *testing.T) {
	w, h, k := fixture(t)
	w.Config.Projects[0].MaxActiveRuns = 4
	// An already running attempt must settle before any unsupported delivery.
	request(t, w, journal.Request{ID: "first", Kind: "run", JobUID: k.jobs[0].UID})
	tick(t, w, at)
	r := active(t, w)
	finishAgent(t, w, h, r)
	supported := k.jobs[0]
	supported.UID = "supported"
	supported.Definition.Trigger = kata.Trigger{Kind: "once", At: new(at.Add(time.Second).Format(time.RFC3339))}
	manual := k.jobs[0]
	manual.UID = "manual"
	k.jobs = append(k.jobs, supported, manual)
	if _, _, err := w.checkpoint(w.Config.Projects[0], supported, defs.Plan{Catchup: "all"}, false, at); err != nil {
		t.Fatal(err)
	}
	for n := range 3 {
		j := manual
		j.UID = fmt.Sprint("unsupported-", n)
		j.Definition.Options = map[string]any{"agent": "missing"}
		k.jobs = append(k.jobs, j)
	}
	request(t, w, journal.Request{ID: "manual", Kind: "run", JobUID: manual.UID})
	calls := 0
	boundary := &outboxKata{fakeKata: k, observeErr: &kata.Error{Code: 7}, beforeUnsupported: func() {
		calls++
		saved, err := w.Store.Load(r.Key)
		if err != nil || saved.Phase != "reported" {
			t.Fatalf("running work not advanced: %+v %v", saved, err)
		}
		done, reason, err := (journal.Requests{Dir: w.Store.Layout.Requests()}).Result("manual")
		if err != nil || !done || reason != "" {
			t.Fatalf("manual not processed %v %s %v", done, reason, err)
		}
		if h.runs != 3 {
			t.Fatalf("scheduled/manual work not launched first: %d", h.runs)
		}
	}}
	w.Clients["example-project"] = boundary
	_ = w.Tick(t.Context(), at.Add(time.Second))
	if calls != 1 {
		t.Fatalf("unsupported calls per tick %d", calls)
	}
}
