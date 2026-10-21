package defs

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type bindingRefReader struct {
	*cacheKata
	calls   int
	project string
}

func (r *bindingRefReader) ShowIssue(_ context.Context, uid string) (kata.IssueView, error) {
	r.calls++
	return kata.IssueView{UID: uid, ShortID: "abc45", ProjectUID: r.project}, nil
}
func TestNativeBindingReferenceIsSavedWithoutRuns(t *testing.T) {
	k, cfg, l := cacheFixture(t)
	cfg.Kata.Project = "example-project"
	if err := state.WriteJSON(state.OSFS{}, l.StateFile(), kata.Installation{Version: 1, ProjectUID: "project"}); err != nil {
		t.Fatal(err)
	}
	k.jobs = []kata.Job{{UID: "job", Name: "Example native job", DefinitionEventUID: "event", Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":false,"trigger":{"kind":"interval","interval_seconds":60},"action":{"kind":"notify","recipient":"worker/inbox"},"issue":{"kind":"per-run","parent_uid":"parent","title":"Example child"},"overlap":"allow","catchup":"latest"}`)}}
	reader := &bindingRefReader{cacheKata: k, project: "project"}
	cache := NewCache(cfg, reader, state.OSFS{}, l)
	for range 2 {
		if err := cache.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := ReadSnapshot(state.OSFS{}, l.DefsSnapshot())
	if err != nil || len(snap.Jobs) != 1 || snap.Jobs[0].IssueRefs["parent"] != "example-project#abc45" || reader.calls != 1 {
		t.Fatal(snap, err, reader.calls)
	}
	k.jobs[0].DefinitionEventUID = "new-event"
	if err := cache.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 {
		t.Fatal("unchanged parent looked up again", reader.calls)
	}
	// A failed project check must not decorate another project's binding.
	k.jobs[0].Definition = json.RawMessage(`{"issue":{"kind":"existing","uid":"other"}}`)
	reader.project = "other-project"
	if err := cache.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	snap, err = ReadSnapshot(state.OSFS{}, l.DefsSnapshot())
	if err != nil || snap.Jobs[0].IssueRefs["other"] != "" || snap.Jobs[0].IssueRefs["parent"] != "" {
		t.Fatal(snap, err)
	}
}

func TestDefinitionReferenceBackfillIsBounded(t *testing.T) {
	k, cfg, l := cacheFixture(t)
	cfg.Kata.Project = "example-project"
	if err := state.WriteJSON(state.OSFS{}, l.StateFile(), kata.Installation{Version: 1, ProjectUID: "project"}); err != nil {
		t.Fatal(err)
	}
	for n := range 33 {
		k.jobs = append(k.jobs, kata.Job{UID: fmt.Sprint(n), Definition: json.RawMessage(fmt.Sprintf(`{"issue":{"kind":"existing","uid":"issue-%d"}}`, n))})
	}
	reader := &bindingRefReader{cacheKata: k, project: "project"}
	cache := NewCache(cfg, reader, state.OSFS{}, l)
	if err := cache.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if reader.calls != 32 {
		t.Fatal(reader.calls)
	}
	if err := cache.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if reader.calls != 33 {
		t.Fatal("saved refs looked up again", reader.calls)
	}
}
