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

// Each project's capability pair gates its own final report independently.
func TestFleetRunSessionsPerProject(t *testing.T) {
	w, h, k := fleetFixture(t)
	k.caps.CLIFeatures = []string{"cron_run_sessions_v1"}
	k.caps.EventFeatures = []string{"cron_run_sessions_v1"}
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
		var want []map[string]string
		if project == "example-project" {
			want = []map[string]string{{"agent": "pi", "session_id": "fixture-session", "step": "main"}}
		}
		if !reflect.DeepEqual(wire.Summary.Sessions, want) {
			t.Fatal(project, string(client.bodies[1]))
		}
	}
}
