package daemon

import (
	"maps"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// A healthy lane's start checks its route and lists its definitions once.
// Before this budget, one start made five route checks of four calls and
// three job and workflow listings, plus a sixth check and a capability probe
// for an inbox lane.
func TestLaneStartChecksRouteAndListsDefinitionsOnce(t *testing.T) {
	warmSignals()
	for _, tc := range []struct{ inboxes, resets, replay bool }{{false, false, false}, {true, false, false}, {false, true, false}, {true, true, false}, {false, false, true}, {true, false, true}} {
		inboxes := tc.inboxes
		synctest.Test(t, func(t *testing.T) {
			// A first start streams from event id 0. Kata may open that stream
			// with a reset, or replay the project's definition history.
			h := &budgetHub{inboxes: inboxes, resets: tc.resets, replay: tc.replay}
			stop := startBudgetWorker(t, 1, h)
			time.Sleep(10 * time.Second)
			stop()
			want := map[string]int{
				"health":              1,
				"cron capabilities":   1,
				"federation identity": 1,
				"daemon diagnose --expect-project-uid 01K00000000000000000000000": 1,
				"cron job list":      1,
				"cron workflow list": 1,
				// A project's first start creates its lane lease issue,
				// closes it, finds the oldest lease issue and claims it.
				"list --status all --label herdr-kata:lease --sort oldest --limit 1":                                                                                2,
				"create --body-stdin --label herdr-kata:lease -- herdr-kata lane lease":                                                                             1,
				"close --reason wontfix --message herdr-kata lane lease record; not work, so it stays closed and out of ready lists. -- 01K0000000000000000000LS00": 1,
			}
			for cmd := range h.startup {
				if strings.HasPrefix(cmd, "meta set --if-match 2 --json-value -- 01K0000000000000000000LS00 herdr_kata.lease ") {
					want[cmd] = 1
				}
			}
			if inboxes {
				// The scanner's first scan follows the two-second debounce.
				want["inbox --for worker/runtime"] = 1
			}
			if !maps.Equal(h.startup, want) {
				t.Fatalf("inbox=%t resets=%t replay=%t: startup calls %v, want %v", inboxes, tc.resets, tc.replay, h.startup, want)
			}
		})
	}
}
