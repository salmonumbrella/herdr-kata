package defs

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type countedCacheKata struct {
	*cacheKata
	lists int
}

func (f *countedCacheKata) ListJobs(ctx context.Context) ([]kata.Job, error) {
	f.lists++
	return f.cacheKata.ListJobs(ctx)
}

// The scheduler loads the rows the cache last refreshed, as they came from
// Kata, so a lane lists its definitions once per refresh.
func TestListingServesTheLatestRefreshWithoutListingAgain(t *testing.T) {
	inner, c, l := cacheFixture(t)
	f := &countedCacheKata{cacheKata: inner}
	f.jobs = []kata.Job{{UID: "job", Definition: json.RawMessage(`{"version":1}`)}}
	// The snapshot stores invalid bytes as a JSON string; the scheduler must
	// still see what Kata returned.
	f.workflows = []kata.Workflow{{UID: "flow", Definition: json.RawMessage(`{`)}}
	cache := NewCache(c, f, state.OSFS{}, l)

	jobs, workflows, err := cache.Listing(t.Context())
	if err != nil || len(jobs) != 1 || len(workflows) != 1 || f.lists != 1 {
		t.Fatalf("listing before a refresh: %d jobs, %d workflows, %d lists, %v", len(jobs), len(workflows), f.lists, err)
	}
	if err := cache.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.jobs = nil
	jobs, workflows, err = cache.Listing(t.Context())
	if err != nil || len(jobs) != 1 || f.lists != 2 {
		t.Fatalf("listing after a refresh: %d jobs, %d lists, %v", len(jobs), f.lists, err)
	}
	if string(workflows[0].Definition) != `{` {
		t.Fatalf("listing workflow definition = %s, want Kata's bytes", workflows[0].Definition)
	}
}
