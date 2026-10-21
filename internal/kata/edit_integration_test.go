//go:build kata

package kata

import (
	"crypto/rand"
	"encoding/json"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"testing"
	"time"
)

func TestIntegBoardCreateUpdateConflictDelete(t *testing.T) {
	c := newKata(t)
	parent := fixtureIssue(t, c)
	for _, kind := range []string{"prompt", "workflow", "notify", "recurring"} {
		t.Run(kind, func(t *testing.T) {
			action := map[string]any{"kind": "execute", "prompt": "work"}
			issue := map[string]any{"kind": "per-run", "title": "Child", "body": "body"}
			trigger := map[string]any{"kind": "manual"}
			parentRef := ""
			if kind == "workflow" {
				w, err := c.SaveWorkflow(t.Context(), Workflow{UID: ident.NewRunUID(time.Now(), rand.Reader), Name: "Flow", Definition: json.RawMessage(`{"version":1,"steps":[{"key":"one","kind":"command","command":"true"}]}`)})
				if err != nil {
					t.Fatal(err)
				}
				action = map[string]any{"kind": "execute", "workflow_uid": w.UID}
			}
			if kind == "notify" || kind == "recurring" {
				action = map[string]any{"kind": "notify", "recipient": "worker/inbox", "message": "due"}
				issue = map[string]any{"kind": "existing", "uid": parent.UID}
			}
			if kind == "recurring" {
				issue = map[string]any{"kind": "per-run", "parent_uid": parent.UID, "title": "Child", "body": "body"}
				trigger = map[string]any{"kind": "interval", "interval_seconds": 60}
				parentRef = parent.QualifiedID
			}
			body, _ := json.Marshal(map[string]any{"version": 1, "kind": "job", "enabled": false, "trigger": trigger, "action": action, "issue": issue, "overlap": "allow", "catchup": "latest"})
			j, err := c.SaveJob(t.Context(), Job{UID: ident.NewRunUID(time.Now(), rand.Reader), Name: kind, Definition: body}, parentRef)
			if err != nil {
				t.Fatal(err)
			}
			old := j
			j.Name = "edited " + kind
			j, err = c.SaveJob(t.Context(), j, parentRef)
			if err != nil || j.Name != "edited "+kind {
				t.Fatal(j, err)
			}
			if _, err = c.SaveJob(t.Context(), old, parentRef); err == nil || Class(err) != Conflict {
				t.Fatal("stale guard accepted", err)
			}
			if err = c.DeleteJob(t.Context(), j); err != nil {
				t.Fatal(err)
			}
		})
	}
}
