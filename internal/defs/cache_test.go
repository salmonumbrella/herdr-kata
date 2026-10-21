package defs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type cacheKata struct {
	jobs             []kata.Job
	workflows        []kata.Workflow
	items            chan kata.TailItem
	entered, release chan struct{}
}

func (f *cacheKata) ListJobs(context.Context) ([]kata.Job, error) {
	if f.entered != nil {
		f.entered <- struct{}{}
		<-f.release
	}
	return f.jobs, nil
}
func (f *cacheKata) ListWorkflows(context.Context) ([]kata.Workflow, error) { return f.workflows, nil }
func (f *cacheKata) Tail(ctx context.Context, _ int64) (<-chan kata.TailItem, <-chan error) {
	errs := make(chan error)
	go func() { <-ctx.Done(); close(f.items); close(errs) }()
	return f.items, errs
}
func cacheFixture(t *testing.T) (*cacheKata, config.Config, state.Layout) {
	t.Helper()
	return &cacheKata{items: make(chan kata.TailItem, 8)}, config.Config{Executor: "test", Kata: config.Kata{Actor: "actor", Env: map[string]string{"KATA_HOME": t.TempDir()}}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}, state.Layout{Root: t.TempDir()}
}

func TestRefreshInvalidSibling(t *testing.T) {
	f, c, l := cacheFixture(t)
	f.jobs = []kata.Job{{UID: "bad", Definition: json.RawMessage(`{`)}, {UID: "good", Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"work"},"issue":{"kind":"per-run","title":"test"},"overlap":"forbid","catchup":"latest"}`)}}
	cache := NewCache(c, f, state.OSFS{}, l)
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries := cache.Snapshot()
	if len(entries) != 2 || len(entries[0].Reasons) == 0 || !entries[1].Supported {
		t.Fatalf("entries: %+v", entries)
	}
}
func TestEventsDuringSnapshotNotLost(t *testing.T) {
	f, c, l := cacheFixture(t)
	f.entered = make(chan struct{}, 1)
	f.release = make(chan struct{})
	cache := NewCache(c, f, state.OSFS{}, l)
	dirty := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	// A definition event before the stream's start snapshot is covered by
	// it; this one arrives after, during a refresh.
	started := make(chan struct{}, 1)
	go func() {
		done <- kata.RunEvents(ctx, f, l, kata.EventHooks{OnReset: func() { started <- struct{}{} }, OnDefs: func(kata.Event) {
			select {
			case dirty <- struct{}{}:
			default:
			}
		}})
	}()
	<-started
	refreshed := make(chan error, 1)
	go func() { refreshed <- cache.Refresh(ctx) }()
	<-f.entered
	f.items <- kata.TailItem{Event: kata.Event{EventID: 3, Type: "cron.workflow.updated"}}
	select {
	case <-dirty:
		dirty <- struct{}{}
	case <-time.After(time.Second):
		t.Fatal("event lost during snapshot")
	}
	close(f.release)
	if err := <-refreshed; err != nil {
		t.Fatal(err)
	}
	select {
	case <-dirty:
	default:
		t.Fatal("dirty flag lost")
	}
	cancel()
	<-done
}
func TestRefreshMalformedWorkflowReason(t *testing.T) {
	f, c, l := cacheFixture(t)
	f.workflows = []kata.Workflow{{UID: "workflow", Definition: json.RawMessage(`{`)}}
	f.jobs = []kata.Job{{UID: "job", Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","workflow_uid":"workflow"},"issue":{"kind":"per-run","title":"test"},"overlap":"forbid","catchup":"latest"}`)}}
	cache := NewCache(c, f, state.OSFS{}, l)
	if err := cache.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	entry := cache.Snapshot()[0]
	if entry.Supported || !strings.Contains(strings.Join(entry.Reasons, " "), "workflow decode") {
		t.Fatalf("malformed workflow reason lost: %+v", entry)
	}
}

func TestCacheExplainsAutomaticAuthorGate(t *testing.T) {
	for _, tc := range []struct {
		author, trigger string
		gated           bool
	}{
		{"actor", `{"kind":"interval","interval_seconds":60}`, false},
		{"other", `{"kind":"interval","interval_seconds":60}`, true},
		{"Actor", `{"kind":"interval","interval_seconds":60}`, true},
		{"", `{"kind":"interval","interval_seconds":60}`, true},
		{"other", `{"kind":"manual"}`, false},
	} {
		t.Run(tc.author+tc.trigger, func(t *testing.T) {
			f, c, l := cacheFixture(t)
			f.jobs = []kata.Job{{UID: "job", Author: tc.author, Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":true,"trigger":` + tc.trigger + `,"action":{"kind":"execute","prompt":"work"},"issue":{"kind":"per-run","title":"test"},"overlap":"forbid","catchup":"latest"}`)}}
			cache := NewCache(c, f, state.OSFS{}, l)
			for range 2 {
				if err := cache.Refresh(t.Context()); err != nil {
					t.Fatal(err)
				}
				snapshot, err := ReadSnapshot(state.OSFS{}, l.DefsSnapshot())
				if err != nil {
					t.Fatal(err)
				}
				entry := snapshot.Jobs[0]
				if !entry.Supported || strings.Contains(strings.Join(entry.Warnings, " "), "automatic scheduling disabled: original author does not match lane actor") != tc.gated {
					t.Fatalf("entry=%+v", entry)
				}
			}
		})
	}
}
