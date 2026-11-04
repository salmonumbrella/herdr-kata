package worker

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/report"
)

// The loaded project partition must govern launches, automatic settlement and
// delivery, while existing excluded runs continue reserving global capacity.
func TestLoadedQuarantineIsolatesWorkAndRetainsCapacity(t *testing.T) {
	for _, limit := range []int{1, 2} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			w, h, k := fixture(t)
			path := filepath.Join(t.TempDir(), "config.toml")
			text := fmt.Sprintf(`executor="executor"
hc_bin="/bin/sh"
[kata]
bin="/bin/sh"
daemon="fixture"
[limits]
max_active_runs=%d
[checkouts]
example-project=%q
[profiles.default]
argv=["/bin/sh"]
[[projects]]
name="example-project"
actor="worker"
commands=true
[[projects]]
name="broken-project"
actor="worker"
max_active_runs=-1
`, limit, w.Config.Checkouts["example-project"])
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(c.Quarantined) != 1 || len(c.Projects) != 1 {
				t.Fatalf("loaded partition: %+v", c)
			}
			clients := map[string]kata.Kata{}
			for _, p := range c.Projects {
				clients[p.Name] = k
			}
			w = New(*c, w.Store, h, clients, w.Bin)
			tick(t, w, at.Add(-2*time.Hour)) // Establish the installation before seeding historical runs.
			excluded := journal.Run{Version: 1, Key: journal.ManualKey("executor", "broken-project", "prior"), RunUID: "01J00000000000000000000030", Project: "broken-project", Actor: "worker", Phase: "running", ExpiresAt: at.Add(-time.Hour), StartedAt: new(at.Add(-2 * time.Hour))}
			if err := w.Store.Create(excluded); err != nil {
				t.Fatal(err)
			}
			box := report.Outbox{Layout: w.Store.Layout}
			if err := box.Discover(defs.Plan{JobUID: "01J00000000000000000000040", JobEventUID: "01J00000000000000000000041"}, "broken-project", "worker", "executor", []string{"checkout not configured"}, at); err != nil {
				t.Fatal(err)
			}
			records, err := box.All()
			if err != nil || len(records) != 1 {
				t.Fatalf("outbox: %v %v", records, err)
			}
			files := []string{filepath.Join(w.Store.Layout.Run(excluded.Key), "run.json"), filepath.Join(w.Store.Layout.Unsupported(), records[0].Cause+".json")}
			before := make([][]byte, len(files))
			for i, path := range files {
				before[i], err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			request(t, w, journal.Request{ID: "request", Kind: "run", JobUID: k.jobs[0].UID})
			tick(t, w, at)
			tick(t, w, at.Add(time.Second))
			wantRuns := limit - 1
			if h.runs != wantRuns || k.creates != wantRuns || h.kills != 0 {
				t.Fatalf("capacity=%d launches=%d creates=%d kills=%d", limit, h.runs, k.creates, h.kills)
			}
			for i, path := range files {
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before[i], after) {
					t.Fatalf("quarantined state changed at %s: %v", path, err)
				}
			}
		})
	}
}
