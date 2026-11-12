package worker

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/prompt"
	"github.com/salmonumbrella/herdr-kata/internal/session"
)

type sessionsCapabilityKata struct{ *fleetKata }

func (k sessionsCapabilityKata) Capabilities() (kata.Caps, error) {
	var caps kata.Caps
	err := json.Unmarshal([]byte(`{"cli_features":["cron_run_sessions_v1"],"event_features":["cron_run_sessions_v1"]}`), &caps)
	return caps, err
}

// A settled prompt's recorded identity reaches Kata when both peers advertise sessions.
func TestFleetFinalObservationIncludesRecordedSession(t *testing.T) {
	w, h, k := fleetFixture(t)
	// The fixture uses fake HC and Kata; no provider process is launched.
	w.Clients["example-project"] = sessionsCapabilityKata{k}
	request(t, w, journal.Request{ID: "manual", Kind: "run", JobUID: k.jobs[0].UID})
	tick(t, w, at)
	r := active(t, w)
	a := r.Attempts[0]
	path := filepath.Join(w.Store.Layout.Attempt(r.Key, a.Step, a.N), "result.json")
	result := prompt.Result{RunUID: r.RunUID, Step: a.Step, Attempt: a.N, Status: "succeeded", Summary: "Complete", Note: "Done", Session: &session.Session{Agent: "codex", SessionID: "00000000-0000-4000-8000-000000000002"}}
	if err := prompt.WriteResult(path, result); err != nil {
		t.Fatal(err)
	}
	term := h.terms[a.Address]
	term.State, term.Reason = "finished", "exited"
	h.terms[a.Address] = term
	tick(t, w, at.Add(time.Second))
	saved, err := w.Store.Load(r.Key)
	if err != nil || saved.Phase != "reported" || !saved.ReportDone || saved.Outcome != "succeeded" || len(saved.Attempts) != 1 || saved.Attempts[0].Session == nil || *saved.Attempts[0].Session != *result.Session {
		t.Fatalf("fixture did not settle recorded session: %+v, %v", saved, err)
	}
	if len(k.bodies) != 2 {
		t.Fatalf("expected admission and final observations, got %d", len(k.bodies))
	}
	var wire struct {
		Status  string                     `json:"status"`
		Summary map[string]json.RawMessage `json:"summary"`
	}
	if err := json.Unmarshal(k.bodies[1], &wire); err != nil || wire.Status != "succeeded" {
		t.Fatalf("invalid final observation: %s: %v", k.bodies[1], err)
	}
	var got []map[string]string
	if raw := wire.Summary["sessions"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("invalid sessions: %s: %v", raw, err)
		}
	}
	want := []map[string]string{{"agent": "codex", "session_id": "00000000-0000-4000-8000-000000000002", "step": "main"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("final observation sessions = %v, want %v", got, want)
	}
}

// Each project's capability pair gates its own final report independently.
func TestFleetRunSessionsCapabilityCombinations(t *testing.T) {
	for _, tt := range []struct {
		name             string
		cli, event, want bool
	}{
		{"neither", false, false, false}, {"cli-only", true, false, false}, {"event-only", false, true, false}, {"both", true, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w, h, k := fleetFixture(t)
			if tt.cli {
				k.caps.CLIFeatures = []string{"cron_run_sessions_v1"}
			}
			if tt.event {
				k.caps.EventFeatures = []string{"cron_run_sessions_v1"}
			}
			otherJob := k.jobs[0]
			other := &fleetKata{fakeKata: &fakeKata{jobs: []kata.CronJob{otherJob}}}
			w.Clients["other-project"] = other
			w.Config.Projects = append(w.Config.Projects, config.Project{Name: "other-project", Actor: "worker", MaxActiveRuns: 1})
			request(t, w, journal.Request{ID: "one", Kind: "run", Project: "example-project", JobUID: k.jobs[0].UID})
			request(t, w, journal.Request{ID: "two", Kind: "run", Project: "other-project", JobUID: otherJob.UID})
			tick(t, w, at)
			runs, err := w.Store.Active()
			if err != nil || len(runs) != 2 {
				t.Fatal(runs, err)
			}
			for _, r := range runs {
				a := r.Attempts[0]
				path := filepath.Join(w.Store.Layout.Attempt(r.Key, a.Step, a.N), "result.json")
				if err := prompt.WriteResult(path, prompt.Result{RunUID: r.RunUID, Step: a.Step, Attempt: a.N, Status: "succeeded", Summary: "Complete", Session: &session.Session{Agent: "pi", SessionID: "fixture-session"}}); err != nil {
					t.Fatal(err)
				}
				term := h.terms[a.Address]
				term.State, term.Reason = "finished", "exited"
				h.terms[a.Address] = term
			}
			tick(t, w, at.Add(time.Second))
			for project, client := range map[string]*fleetKata{"example-project": k, "other-project": other} {
				if len(client.bodies) != 2 {
					t.Fatal(project, len(client.bodies))
				}
				var wire struct {
					Summary struct {
						Sessions []map[string]string `json:"sessions"`
					} `json:"summary"`
				}
				if err := json.Unmarshal(client.bodies[1], &wire); err != nil {
					t.Fatal(err)
				}
				want := tt.want && project == "example-project"
				if (len(wire.Summary.Sessions) == 1) != want {
					t.Fatal(project, string(client.bodies[1]))
				}
			}
		})
	}
}
