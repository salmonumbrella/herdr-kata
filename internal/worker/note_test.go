package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// A successful gate's note must reach dependent work and cannot strand queued runs.
func TestBadGateNoteReleasesCapacityAndOverlap(t *testing.T) {
	for _, note := range []struct{ name, raw, want string }{
		{"multiline", "ready\ncontinue\r\n", "ready continue"},
		{"long", strings.Repeat("a", 900), strings.Repeat("a", 268) + " [truncated]"},
	} {
		for _, gate := range []string{"global", "project", "overlap"} {
			t.Run(note.name+"/"+gate, func(t *testing.T) {
				w, h, k := fixture(t)
				workflow(k)
				k.workflows[0].Definition.Steps[1].Prompt = new("Task {{previous}}")
				w.Config.Projects[0].MaxActiveRuns = 2
				k.jobs[0].Definition.Overlap = "allow"
				switch gate {
				case "global":
					w.Config.Limits.MaxActiveRuns = 1
				case "project":
					w.Config.Projects[0].MaxActiveRuns = 1
				case "overlap":
					k.jobs[0].Definition.Overlap = "forbid"
				}
				request(t, w, journal.Request{ID: "first", Kind: "run", JobUID: k.jobs[0].UID})
				tick(t, w, at)
				first := active(t, w)
				request(t, w, journal.Request{ID: "waiting", Kind: "run", JobUID: k.jobs[0].UID})
				tick(t, w, at.Add(time.Second))
				q := journal.Requests{Dir: w.Store.Layout.Requests()}
				if done, reason, err := q.Result("waiting"); err != nil || done || reason == "" {
					t.Fatalf("request should wait for %s: %v %q %v", gate, done, reason, err)
				}
				a := first.Attempts[0]
				dir := w.Store.Layout.Attempt(first.Key, a.Step, a.N)
				if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte(note.raw), 0600); err != nil {
					t.Fatal(err)
				}
				if err := state.WriteJSON(filepath.Join(dir, "receipt.json"), journal.Receipt{ExitCode: 0, StartedAt: a.LaunchedAt, EndedAt: at.Add(2 * time.Second)}); err != nil {
					t.Fatal(err)
				}
				term := h.terms[a.Address]
				term.State, term.Reason = "finished", "exited"
				h.terms[a.Address] = term
				tick(t, w, at.Add(2*time.Second))
				r := active(t, w)
				if h.runs != 2 || len(r.Attempts) != 2 || r.Attempts[0].Status != "succeeded" || r.Attempts[0].Note != note.want || r.Attempts[1].Step != "work" {
					t.Fatalf("gate did not dispatch dependent: launches=%d attempts=%+v", h.runs, r.Attempts)
				}
				body, err := os.ReadFile(filepath.Join(w.Store.Layout.Attempt(r.Key, "work", 1), "prompt.md"))
				if err != nil || !strings.Contains(string(body), "Task "+note.want+"\n") {
					t.Fatalf("dependent did not receive normalized previous: %q %v", body, err)
				}
				finishAgent(t, w, h, r)
				for n := 3; n <= 6; n++ {
					tick(t, w, at.Add(time.Duration(n)*time.Second))
				}
				saved, err := w.Store.Load(first.Key)
				if err != nil || saved.Phase != "reported" || saved.Outcome != "succeeded" || saved.Hold != "" {
					t.Fatalf("first run did not settle: %+v %v", saved, err)
				}
				if done, reason, err := q.Result("waiting"); err != nil || !done || reason != "" || h.runs != 3 {
					t.Fatalf("queued run did not proceed: %v %q %v launches=%d", done, reason, err, h.runs)
				}
				if r := active(t, w); r.RequestID != "waiting" || r.Attempts[0].Step != "gate" {
					t.Fatalf("wrong queued run: %+v", r)
				}
			})
		}
	}
}
