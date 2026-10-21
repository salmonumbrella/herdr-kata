package doctor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestDoctorDetectsLostIssueRequest(t *testing.T) {
	c, _, _, l := doctorFixture(t)
	c.Kata.Inbox = "actor/runtime"
	const project = "01K00000000000000000000009"
	const child = "01K00000000000000000000001"
	uid, err := ident.IssueRequestUID(child)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"lost", "foreign", "finished", "local"} {
		t.Run(kind, func(t *testing.T) {
			root := state.Layout{Root: t.TempDir()}
			a := runner.RequestAdmission{Version: 1, RequestUID: "01K00000000000000000000004", RunUID: uid, JournalID: "saved-journal", Executor: runner.RequestExecutor{InstanceUID: "instance", ProjectUID: project, Actor: c.Kata.Actor, ExecutorLabel: c.Executor}}
			if kind == "foreign" {
				a.Executor.ExecutorLabel = "other-worker"
			}
			admission, _ := json.Marshal(a)
			metadata := map[string]json.RawMessage{"herdr_kata.admission": admission}
			if kind == "finished" {
				raw, _ := json.Marshal(runner.RequestOutcome{Version: 1, RequestUID: a.RequestUID, RunUID: uid, JournalID: a.JournalID, Phase: runner.Settled, Outcome: "succeeded"})
				metadata["herdr_kata.outcome"] = raw
			}
			k := kata.New(c.Kata, l.Root, func(_ context.Context, args, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
				var value any
				switch joined := strings.Join(args, " "); {
				case strings.Contains(joined, "--help"):
					return []byte("cron capabilities events --tail"), nil, 0, nil
				case strings.Contains(joined, "health"):
					value = map[string]any{"version": "test", "schema_version": 34}
				case strings.Contains(joined, "capabilities"):
					value = map[string]any{"project_uid": project, "event_features": []string{"cron_v1"}}
				case strings.Contains(joined, "identity"):
					value = map[string]any{"instance_uid": "instance", "auth": map[string]string{"actor": c.Kata.Actor}}
				case strings.Contains(joined, "diagnose"):
					value = map[string]string{"state": "ready", "endpoint": "local", "project_uid": project}
				case strings.Contains(joined, "list --status all --label herdr-kata:lease"):
					value = map[string]any{"issues": []kata.IssueView{}}
				case strings.Contains(joined, "cron job list"):
					value = map[string]any{"jobs": []kata.Job{}}
				case strings.Contains(joined, "cron run list"):
					value = map[string]any{"runs": []kata.Run{}}
				case strings.Contains(joined, "inbox --for actor/runtime"):
					value = map[string]any{"requests": []kata.InboxRequest{{Ref: "example-project#child"}}}
				case strings.Contains(joined, "show -- example-project#child"):
					value = map[string]any{"issue": kata.IssueView{UID: child, ProjectUID: project, Status: "open", Owner: c.Kata.Actor, Metadata: metadata}}
				default:
					t.Errorf("unexpected command %v", args)
					return nil, nil, 1, nil
				}
				b, e := json.Marshal(value)
				return b, nil, 0, e
			}, nil)
			i, route, err := kata.ResolveIdentity(t.Context(), k)
			if err != nil {
				t.Fatal(err)
			}
			pin := kata.Installation{Version: 1, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Executor: c.Executor, Actor: i.Actor, InstanceUID: i.InstanceUID, ProjectUID: i.ProjectUID, Route: route}
			if err := state.WriteJSON(state.OSFS{}, root.Project(project).StateFile(), pin); err != nil {
				t.Fatal(err)
			}
			if kind == "local" {
				if err := (state.OSFS{}).MkdirAll(root.RunDir(uid), 0700); err != nil {
					t.Fatal(err)
				}
			}
			report, _ := checkKata(t.Context(), c, k, root)
			found := false
			for _, check := range report.Checks {
				if check.Name != "lost-runs" {
					continue
				}
				found = true
				wantLost := kind == "lost"
				if check.Passed == wantLost || wantLost && !strings.Contains(check.Detail, "herdr-kata abandon "+uid) {
					t.Fatalf("lost request diagnosis: %+v", check)
				}
			}
			if !found {
				t.Fatalf("lost-runs check missing: %+v", report)
			}
		})
	}
}
