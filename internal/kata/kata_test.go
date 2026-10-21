package kata

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
)

func fakeCLI(t *testing.T, reply string, check func(context.Context, []string, []string, string, []byte)) *CLI {
	t.Helper()
	return New(config.Kata{Bin: "/bin/kata", Project: "demo", Actor: "worker", Daemon: "local"}, "/state", func(ctx context.Context, argv, env []string, dir string, stdin []byte) ([]byte, []byte, int, error) {
		if check != nil {
			check(ctx, argv, env, dir, stdin)
		}
		return []byte(reply), []byte("Definition UID: ignored"), 0, nil
	}, nil)
}

func TestArgvCommonFlags(t *testing.T) {
	c := fakeCLI(t, `{"project_uid":"p","event_features":["cron_v1"],"future":true}`, func(_ context.Context, argv, _ []string, dir string, _ []byte) {
		want := []string{"/bin/kata", "--json", "--project", "demo", "--as", "worker", "--teammate", "herdr-kata", "--daemon", "local", "cron", "capabilities"}
		if !reflect.DeepEqual(argv, want) || dir != "/state" {
			t.Fatalf("argv=%q dir=%q", argv, dir)
		}
	})
	x, err := c.Capabilities(t.Context())
	if err != nil || x.ProjectUID != "p" {
		t.Fatalf("%+v %v", x, err)
	}
}

func TestEnvAllowlistDropsAmbient(t *testing.T) {
	t.Setenv("KATA_SERVER", "ambient")
	t.Setenv("KATA_AUTH_TOKEN", "secret")
	t.Setenv("OTHER_SECRET", "secret")
	t.Setenv("XDG_STATE_HOME", "/xdg")
	c := New(config.Kata{Env: map[string]string{"KATA_HOME": "/isolated", "KATA_HTTP_TIMEOUT": "9h"}}, "", nil, nil)
	env := strings.Join(c.Env(), "\n")
	for _, key := range []string{"KATA_SERVER=", "KATA_AUTH_TOKEN=", "OTHER_SECRET="} {
		if strings.Contains(env, key) {
			t.Fatal(env)
		}
	}
	for _, pair := range []string{"KATA_HOME=/isolated", "KATA_HTTP_TIMEOUT=45s", "XDG_STATE_HOME=/xdg"} {
		if !strings.Contains(env, pair) {
			t.Fatal(env)
		}
	}
}

func TestEnvCredentialPassthrough(t *testing.T) {
	t.Setenv("TEST_CREDENTIAL", "value")
	c := New(config.Kata{CredentialEnv: "TEST_CREDENTIAL"}, "", nil, nil)
	if !strings.Contains(strings.Join(c.Env(), "\n"), "TEST_CREDENTIAL=value") {
		t.Fatal(c.Env())
	}
}

func TestRouteOmitsCredentialValues(t *testing.T) {
	c := New(config.Kata{CredentialEnv: "KATA_CREDENTIAL", Env: map[string]string{"KATA_CREDENTIAL": "secret", "KATA_HOME": "/isolated"}}, "", nil, nil)
	route := c.ResolveRoute()
	if _, present := route.Env["KATA_CREDENTIAL"]; present || route.Env["KATA_HOME"] != "/isolated" {
		t.Fatal("credential leaked into route")
	}
}

func TestFiniteDeadline60s(t *testing.T) {
	c := fakeCLI(t, `{}`, func(ctx context.Context, _, _ []string, _ string, _ []byte) {
		at, ok := ctx.Deadline()
		d := time.Until(at)
		if !ok || d > time.Minute || d < 59*time.Second {
			t.Fatalf("deadline %v %v", d, ok)
		}
	})
	_, _ = c.Capabilities(t.Context())
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	c.runner = func(got context.Context, _, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
		want, _ := ctx.Deadline()
		at, _ := got.Deadline()
		if !at.Equal(want) {
			t.Fatal(at, want)
		}
		return []byte(`{}`), nil, 0, nil
	}
	_, _ = c.Capabilities(ctx)
}

func TestObserveBodyOnStdinKeepsZeroExpectedRevision(t *testing.T) {
	c := fakeCLI(t, `{"run":{"uid":"r","revision":1},"replayed":true}`, func(_ context.Context, argv, _ []string, _ string, body []byte) {
		if !reflect.DeepEqual(argv[len(argv)-6:], []string{"cron", "run", "observe", "r", "--json-input", "-"}) {
			t.Fatal(argv)
		}
		var fields map[string]json.RawMessage
		requireNoError(t, json.Unmarshal(body, &fields))
		if string(fields["expected_revision"]) != "0" || string(fields["teammate"]) != `"herdr-kata"` {
			t.Fatal(string(body))
		}
	})
	r, replay, err := c.Observe(t.Context(), "r", Observation{Status: "running", Summary: Summary{Version: 1, Message: "admitted"}})
	if err != nil || !replay || r.UID != "r" {
		t.Fatal(r, replay, err)
	}
}

func TestShowEnvelopeDecodesParentLinksLabels(t *testing.T) {
	c := fakeCLI(t, `{"issue":{"uid":"child","short_id":"abcd","project_uid":"p","owner":null,"revision":3,"labels":["review"],"metadata":{"cron.occurrence":{"version":1}}},"parent":{"uid":"parent"},"links":[{"type":"blocked-by","from":{"uid":"child"},"to":{"uid":"blocker"}}],"labels":[{"label":"review","author":"worker"}]}`, nil)
	i, err := c.ShowIssue(t.Context(), "abcd")
	if err != nil || i.ParentUID != "parent" || i.QualifiedID != "demo#abcd" || len(i.Links) != 1 || i.Links[0].To.UID != "blocker" || !reflect.DeepEqual(i.Labels, []string{"review"}) || i.Owner != "" {
		t.Fatalf("%+v %v", i, err)
	}
}

func TestErrorCandidatesExtracted(t *testing.T) {
	for _, file := range []string{"duplicate_candidates", "idempotency_mismatch", "validation", "daemon_unavailable"} {
		t.Run(file, func(t *testing.T) {
			body, err := os.ReadFile("testdata/" + file + ".json")
			requireNoError(t, err)
			c := fakeCLI(t, "", nil)
			c.runner = func(context.Context, []string, []string, string, []byte) ([]byte, []byte, int, error) {
				return nil, body, 5, nil
			}
			_, err = c.CreateIssue(t.Context(), CreateReq{Title: "title"})
			var e *Error
			if !errors.As(err, &e) || len(e.Raw) == 0 {
				t.Fatal(err)
			}
			want := Fatal
			if file == "duplicate_candidates" || file == "idempotency_mismatch" {
				want = Conflict
				if len(e.CandidateUIDs) != 1 {
					t.Fatal(e)
				}
			}
			if file == "daemon_unavailable" {
				want = Retry
			}
			if Class(err) != want {
				t.Fatal(e, Class(err))
			}
		})
	}
}

func TestDeadlineGuardedArgv(t *testing.T) {
	c := fakeCLI(t, `{}`, func(_ context.Context, argv, _ []string, _ string, _ []byte) {
		want := []string{"deadline", "--if-match", "7", "--", "abcd", "2030-01-01T00:00:00Z"}
		if !reflect.DeepEqual(argv[len(argv)-len(want):], want) {
			t.Fatal(argv)
		}
	})
	requireNoError(t, c.Deadline(t.Context(), "abcd", "2030-01-01T00:00:00Z", Cond{IfMatch: "7"}))
}

func TestSafetyTable(t *testing.T) {
	for k, want := range map[string]Safety{"observe": ReplaySafe, "create": ReplaySafe, "comment": ReplaySafe, "close": ReplaySafe, "meta": Guarded, "deadline": Guarded, "notify": Unguarded, "label": Unguarded, "claim": ReadBack} {
		if got := SafetyOf(k); got != want {
			t.Fatalf("%s: %v != %v", k, got, want)
		}
	}
	if SafetyOf("unknown") != ReadBack {
		t.Fatal("unknown operations must not be replayed")
	}
}

// JSON encoding must preserve every string verbatim, including hostile text.
func FuzzObservationMessageRoundTrip(f *testing.F) {
	f.Add("summary\nwith quotes \" and $()", int64(0))
	f.Add("", int64(8))
	f.Fuzz(func(t *testing.T, message string, revision int64) {
		// encoding/json replaces invalid UTF-8, so compare its canonical string.
		canonical, _ := json.Marshal(message)
		var want string
		_ = json.Unmarshal(canonical, &want)
		c := fakeCLI(t, `{"run":{}}`, func(_ context.Context, _, _ []string, _ string, body []byte) {
			var decoded Observation
			requireNoError(t, json.Unmarshal(body, &decoded))
			if decoded.Summary.Message != want || decoded.ExpectedRevision != revision {
				t.Fatal(string(body))
			}
		})
		if _, _, err := c.Observe(t.Context(), "run", Observation{Summary: Summary{Version: 1, Message: message}, ExpectedRevision: revision}); err != nil {
			t.Fatal(err)
		}
	})
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosisSigningAndReplicaHint(t *testing.T) {
	c := fakeCLI(t, `{"state":"ready","endpoint":"local","project_uid":"p","federation_role":"spoke","hub_origin":"https://hub.example","root_signing":{"projects":["p"]}}`, nil)
	d, err := c.DaemonDiagnose(t.Context(), "p")
	if err != nil || d.Authoritative {
		t.Fatal(d, err)
	}
	if d.HubOrigin != "https://hub.example" || d.RootSigning == nil || len(d.RootSigning.Projects) != 1 || d.RootSigning.Projects[0] != "p" {
		t.Fatal("diagnosis omitted signing/replica evidence", d)
	}
}

func TestDefinitionsListRequiresCompleteEnvelope(t *testing.T) {
	for _, body := range []string{`{}`, `{"jobs":null,"workflows":null}`} {
		t.Run(body, func(t *testing.T) {
			c := fakeCLI(t, body, nil)
			if _, err := c.ListJobs(t.Context()); err == nil {
				t.Fatal("incomplete jobs response supplied absence proof")
			}
			if _, err := c.ListWorkflows(t.Context()); err == nil {
				t.Fatal("incomplete workflows response supplied absence proof")
			}
		})
	}
	c := fakeCLI(t, `{"jobs":[],"workflows":[]}`, nil)
	if _, err := c.ListJobs(t.Context()); err != nil {
		t.Fatal("complete empty list rejected", err)
	}
	if _, err := c.ListWorkflows(t.Context()); err != nil {
		t.Fatal("complete empty list rejected", err)
	}
}

func TestAccessLossWritesOnlyItsLaneStatus(t *testing.T) {
	root := t.TempDir()
	lane := filepath.Join(root, "projects", "project-a")
	if err := state.WriteJSON(state.OSFS{}, filepath.Join(lane, "state.json"), map[string]any{"version": 1, "project_uid": "project-a"}); err != nil {
		t.Fatal(err)
	}
	k := New(config.Kata{Bin: "/bin/kata", Project: "example-project", Actor: "worker"}, root, func(context.Context, []string, []string, string, []byte) ([]byte, []byte, int, error) {
		return []byte(`{"error":{"code":"not_found","message":"access withheld"}}`), nil, 1, nil
	}, nil)
	guard := NewRouteGuard(config.Config{Kata: config.Kata{Project: "example-project"}}, k, state.OSFS{}, state.Layout{Root: lane})
	if e := guard.ProbeAccess(t.Context()); e == nil {
		t.Fatal("lost access accepted")
	}
	var row map[string]any
	if e := state.ReadJSON(state.OSFS{}, filepath.Join(lane, "route.json"), 65536, &row); e != nil || row["access_lost"] != true || row["project"] != "example-project" {
		t.Fatal(row, e)
	}
	if _, e := os.Stat(filepath.Join(root, "route.json")); !os.IsNotExist(e) {
		t.Fatal("lane refusal overwrote global status", e)
	}
}

func TestRemoteAccessProbeUsesPinnedProject(t *testing.T) {
	for _, project := range []string{"project-a", "project-b"} {
		t.Run(project, func(t *testing.T) {
			root := t.TempDir()
			lane := filepath.Join(root, "projects", "project-a")
			if err := state.WriteJSON(state.OSFS{}, filepath.Join(lane, "state.json"), map[string]any{"version": 1, "project_uid": "project-a"}); err != nil {
				t.Fatal(err)
			}
			statusPath := filepath.Join(lane, "route.json")
			if err := state.WriteJSON(state.OSFS{}, statusPath, map[string]any{"allowed": true}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			k := New(config.Kata{Bin: "/bin/kata", Project: "example-project", Actor: "worker"}, root, func(_ context.Context, args, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
				calls++
				switch strings.Join(args[8:], " ") {
				case "daemon diagnose --expect-project-uid project-a":
					return []byte(`{"state":"project_unverifiable","endpoint":"https://hub.example"}`), nil, 0, nil
				case "cron capabilities":
					return []byte(`{"project_uid":"` + project + `","event_features":["cron_v1"]}`), nil, 0, nil
				default:
					t.Fatalf("unexpected probe command %v", args)
					return nil, nil, 1, nil
				}
			}, nil)
			guard := NewRouteGuard(config.Config{Kata: config.Kata{Project: "example-project"}}, k, state.OSFS{}, state.Layout{Root: lane})
			if _, err := k.DaemonDiagnose(t.Context(), "project-a"); err != nil {
				t.Fatal(err)
			}
			err := guard.ProbeAccess(t.Context())
			if (err != nil) != (project == "project-b") {
				t.Fatal("access probe did not compare capabilities with pin", err)
			}
			var row struct {
				Allowed    bool   `json:"allowed"`
				AccessLost bool   `json:"access_lost"`
				Error      string `json:"error"`
				Project    string `json:"project"`
			}
			if err := state.ReadJSON(state.OSFS{}, statusPath, 65536, &row); err != nil {
				t.Fatal(err)
			}
			if calls != 2 || row.Allowed != (project == "project-a") || row.AccessLost != (project == "project-b") {
				t.Fatal("access status did not follow the pinned project", calls, row)
			}
		})
	}
}

func TestProjectViewsKeepOtherCredentialsOutOfKata(t *testing.T) {
	t.Setenv("KATA_TOKEN_A", "process-a")
	t.Setenv("KATA_TOKEN_B", "process-b")
	cfg := config.Config{Kata: config.Kata{Bin: "/bin/kata", Env: map[string]string{"KATA_HOME": "/tmp/example", "KATA_TOKEN_A": "shared-a", "KATA_TOKEN_B": "shared-b"}}, Projects: []config.Project{{Name: "project-a", Actor: "actor-a", Daemon: "hub-a", CredentialEnv: "KATA_TOKEN_A"}, {Name: "project-b", Actor: "actor-b", Daemon: "hub-b", CredentialEnv: "KATA_TOKEN_B"}}}
	for _, entry := range cfg.Projects {
		view := cfg.View(entry)
		k := New(view.Kata, t.TempDir(), func(_ context.Context, args, env []string, _ string, _ []byte) ([]byte, []byte, int, error) {
			own, other := "KATA_TOKEN_A=process-a", "KATA_TOKEN_B="
			if entry.Name == "project-b" {
				own, other = "KATA_TOKEN_B=process-b", "KATA_TOKEN_A="
			}
			found := false
			for _, item := range env {
				if item == own {
					found = true
				}
				if strings.HasPrefix(item, other) {
					t.Error("another project's credential reached Kata")
				}
			}
			if !found {
				t.Error("own credential missing")
			}
			joined := " " + strings.Join(args, " ") + " "
			for _, flag := range []string{" --project " + entry.Name + " ", " --as " + entry.Actor + " ", " --daemon " + entry.Daemon + " "} {
				if !strings.Contains(joined, flag) {
					t.Error("wrong lane arguments", args)
				}
			}
			return []byte(`{"project_uid":"project","event_features":["cron_v1"]}`), nil, 0, nil
		}, nil)
		if _, e := k.Capabilities(t.Context()); e != nil {
			t.Fatal(e)
		}
	}
}

func TestProjectCredentialExcludesAmbientXDGAllowlist(t *testing.T) {
	t.Setenv("XDG_PROJECT_A_TOKEN", "process-a")
	t.Setenv("XDG_PROJECT_B_TOKEN", "process-b")
	cfg := config.Config{Projects: []config.Project{{Name: "project-a", CredentialEnv: "XDG_PROJECT_A_TOKEN"}, {Name: "project-b", CredentialEnv: "XDG_PROJECT_B_TOKEN"}}}
	for _, entry := range cfg.Projects {
		other := "XDG_PROJECT_B_TOKEN="
		if entry.Name == "project-b" {
			other = "XDG_PROJECT_A_TOKEN="
		}
		cli := New(cfg.View(entry).Kata, t.TempDir(), func(_ context.Context, _, env []string, _ string, _ []byte) ([]byte, []byte, int, error) {
			own := false
			for _, pair := range env {
				if strings.HasPrefix(pair, other) {
					t.Error("ambient allowlist leaked another project's credential")
				}
				own = own || strings.HasPrefix(pair, entry.CredentialEnv+"=")
			}
			if !own {
				t.Error("selected project's credential missing")
			}
			return []byte(`{}`), nil, 0, nil
		}, nil)
		if _, e := cli.Capabilities(t.Context()); e != nil {
			t.Fatal(e)
		}
	}
}

func TestPlanningDatesDecodesScheduledInstant(t *testing.T) {
	c := fakeCLI(t, `{"kata_api_version":1,"project_id":2,"issue_uid":"child","revision":2,"scheduled_on":{"field":"scheduled_on","value":"2026-12-01","timezone":"Europe/Paris","instant":"2026-11-30T23:00:00Z"},"deadline_on":null}`, func(_ context.Context, argv, _ []string, _ string, _ []byte) {
		want := []string{"show", "--planning-dates", "--", "abcd"}
		if !reflect.DeepEqual(argv[len(argv)-len(want):], want) {
			t.Fatal(argv)
		}
	})
	p, err := c.PlanningDates(t.Context(), "abcd")
	if err != nil || p.IssueUID != "child" || p.Revision != 2 || p.Scheduled == nil {
		t.Fatalf("%+v %v", p, err)
	}
	if p.Scheduled.Value != "2026-12-01" || p.Scheduled.Timezone != "Europe/Paris" || !p.Scheduled.Instant.Equal(time.Date(2026, 11, 30, 23, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v", *p.Scheduled)
	}
}

func TestPlanningDatesWithoutScheduleIsNil(t *testing.T) {
	c := fakeCLI(t, `{"issue_uid":"child","revision":1,"scheduled_on":null,"deadline_on":null}`, nil)
	p, err := c.PlanningDates(t.Context(), "abcd")
	if err != nil || p.Scheduled != nil {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestGuardedPlanningDatesRefusedRouteSendsNothing(t *testing.T) {
	c := fakeCLI(t, `{}`, func(context.Context, []string, []string, string, []byte) {
		t.Fatal("refused route read reached Kata")
	})
	r := GuardedReader{CLI: c, Guard: &RouteGuard{}}
	if _, err := r.PlanningDates(t.Context(), "abcd"); err == nil {
		t.Fatal("refused route returned planning dates")
	}
}
