package doctor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestDoctorTerminalRunMismatch(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	target := runner.Target{Identity: kata.Identity{InstanceUID: "instance", ProjectUID: "project", Actor: "worker"}, Executor: "installation"}
	r := runner.Run{Version: 1, UID: "01K00000000000000000000000", Phase: runner.Settled, Outcome: "succeeded", EndedAt: new(time.Now()), Target: target}
	if err := state.WriteJSON(state.OSFS{}, l.RunFile(r.UID), r); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"running", "succeeded"} {
		reads := 0
		k := kata.New(config.Kata{Project: "example-project", Actor: "worker"}, l.Root, func(_ context.Context, argv, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
			if !strings.Contains(strings.Join(argv, " "), "cron run show "+r.UID) {
				t.Fatalf("unexpected call %v", argv)
			}
			reads++
			body, _ := json.Marshal(map[string]any{"run": kata.Run{UID: r.UID, Actor: "worker", ExecutorLabel: "installation", Status: status, Revision: 1}})
			return body, nil, 0, nil
		}, nil)
		checks := finalRunChecks(t.Context(), k, state.OSFS{}, l, target)
		if reads != 1 || len(checks) != 1 || checks[0].Passed != (status == "succeeded") {
			t.Fatalf("doctor missed mismatch: reads=%d checks=%+v", reads, checks)
		}
		if status == "running" && (!strings.Contains(checks[0].Detail, "running") || !strings.Contains(checks[0].Detail, "herdr-kata up")) {
			t.Fatal(checks)
		}
	}
}
