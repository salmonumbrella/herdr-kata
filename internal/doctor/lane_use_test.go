package doctor

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

// Doctor reports who holds the lane lease, read-only: a live lease of
// another installation fails with the takeover remedy; this installation's
// or an expired lease passes.
func TestDoctorReportsLaneLeaseWithoutWrites(t *testing.T) {
	for _, tc := range []struct {
		name, owner string
		age         time.Duration
		passed      bool
		detail      string
	}{
		{"foreign live", "elsewhere", time.Minute, false, "another installation (elsewhere) owns this lane"},
		{"own", "here", time.Hour, true, "held by this installation at generation 7"},
		{"foreign expired", "elsewhere", time.Hour, true, "expired lease of elsewhere"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{Executor: "here", Kata: config.Kata{Actor: "worker", Project: "example-project"}}
			heartbeat := time.Now().UTC().Add(-tc.age)
			k := kata.New(cfg.Kata, t.TempDir(), func(_ context.Context, args, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
				var v any
				switch cmd := strings.Join(args[8:], " "); {
				case strings.Contains(cmd, "--help"):
					return []byte("cron capabilities events --tail"), nil, 0, nil
				case cmd == "health":
					v = kata.Health{SchemaVersion: 33}
				case cmd == "cron capabilities":
					v = kata.Capabilities{ProjectUID: "project", EventFeatures: []string{"cron_v1"}}
				case cmd == "federation identity":
					v = map[string]any{"instance_uid": "instance", "auth": map[string]string{"actor": "worker"}}
				case strings.HasPrefix(cmd, "daemon diagnose"):
					v = map[string]string{"state": "ready", "endpoint": "local", "project_uid": "project"}
				case cmd == "list --status all --label herdr-kata:lease --sort oldest --limit 1":
					v = map[string]any{"issues": []any{map[string]any{"uid": "lease", "status": "closed", "revision": 3, "metadata": map[string]any{"herdr_kata.lease": map[string]any{"version": 1, "owner": tc.owner, "generation": 7, "heartbeat": heartbeat}}}}}
				default:
					t.Errorf("unexpected call (must remain read-only): %s", cmd)
				}
				b, err := json.Marshal(v)
				return b, nil, 0, err
			}, nil)
			report, _ := checkKata(t.Context(), cfg, k, state.Layout{Root: t.TempDir()})
			for _, check := range report.Checks {
				if check.Name == "lane lease" {
					if check.Passed != tc.passed || !strings.Contains(check.Detail, tc.detail) {
						t.Fatalf("%+v", check)
					}
					return
				}
			}
			t.Fatalf("no lane lease check: %+v", report)
		})
	}
}
