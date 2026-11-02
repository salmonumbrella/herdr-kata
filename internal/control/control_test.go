package control

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type fakeHC struct {
	hc.HC
	terms                map[string]hc.Terminal
	runs, kills, removes int
}

func (h *fakeHC) CoreID(context.Context) (string, error) { return "fixture-core", nil }
func (h *fakeHC) Get(_ context.Context, id string) (hc.Terminal, error) {
	if t, ok := h.terms[id]; ok {
		return t, nil
	}
	for _, t := range h.terms {
		if t.Name == id {
			return t, nil
		}
	}
	return hc.Terminal{}, hc.ErrNotFound
}
func (h *fakeHC) Run(context.Context, hc.RunSpec) (hc.Launched, error) {
	h.runs++
	return hc.Launched{}, nil
}
func (h *fakeHC) Kill(_ context.Context, id string) error {
	h.kills++
	t := h.terms[id]
	t.State = "finished"
	h.terms[id] = t
	return nil
}
func (h *fakeHC) Remove(_ context.Context, id string) error {
	h.removes++
	delete(h.terms, id)
	return nil
}
func fixture(t *testing.T) (state.Layout, *fakeHC, func()) {
	t.Helper()
	l := state.Layout{Root: t.TempDir()}
	release, err := journal.Lock(l.Lock())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	if err := os.WriteFile(l.PID(), []byte(fmt.Sprint(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	h := &fakeHC{terms: map[string]hc.Terminal{"t-worker": {Address: "t-worker", Name: Name(l.Root), State: "live", PID: uint32(os.Getpid()), ForegroundPID: uint32(os.Getpid()), CreatedMs: 1, Labels: map[string]string{"kata.worker": Name(l.Root)}}}}
	return l, h, release
}
func TestUpAdoptsOnlyOwnWorker(t *testing.T) {
	for _, kind := range []string{"own", "foreign label", "wrong pid", "unlocked"} {
		t.Run(kind, func(t *testing.T) {
			l, h, release := fixture(t)
			term := h.terms["t-worker"]
			switch kind {
			case "foreign label":
				term.Labels["kata.worker"] = "other"
			case "wrong pid":
				term.ForegroundPID++
			case "unlocked":
				release()
			}
			h.terms["t-worker"] = term
			got, err := Up(context.Background(), h, l, "/usr/local/bin/herdr-kata", "/srv/config.toml", nil)
			if kind == "own" {
				if err != nil || got.Address != "t-worker" {
					t.Fatal(got, err)
				}
			} else if err == nil {
				t.Fatal("adopted foreign or stale worker")
			}
			if h.runs != 0 || h.kills != 0 || h.removes != 0 {
				t.Fatal(h)
			}
		})
	}
}
func TestStopLeavesJobTerminals(t *testing.T) {
	l, h, _ := fixture(t)
	h.terms["t-job"] = hc.Terminal{Address: "t-job", Name: "example-job", State: "live", Labels: map[string]string{"kata.run": "example-run"}}
	if err := Stop(context.Background(), h, l); err != nil {
		t.Fatal(err)
	}
	if h.kills != 1 || h.removes != 1 || h.terms["t-job"].State != "live" {
		t.Fatal(h)
	}
}
