package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestWorkerPublishesMissingStartupCredentialInStatus(t *testing.T) {
	f := workerFixtureFor(t)
	f.cfg.Kata.CredentialEnv = "KATA_STARTUP_TEST_TOKEN"
	t.Setenv("KATA_STARTUP_TEST_TOKEN", "")
	f.k = kata.New(f.cfg.Kata, f.l.Root, nil, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	done := make(chan error, 1)
	go func() { done <- workerWithCLI(ctx, f.cfg, f.l, f.k) }()
	defer func() { cancel(); <-done }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := ReadStatus(f.l, f.cfg)
		if err == nil && status.Route != nil && strings.Contains(status.Route.Error, "credential_env KATA_STARTUP_TEST_TOKEN") {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("status hid startup credential failure", status.Route, err)
		case <-ticker.C:
		}
	}
}

func TestUpRepairsSupervisorWithMissingEnvironmentCredential(t *testing.T) {
	c, l, exe := processFixture(t)
	c.Kata = config.Kata{Project: "example-project", CredentialEnv: "KATA_STARTUP_TEST_TOKEN"}
	t.Setenv("KATA_STARTUP_TEST_TOKEN", "")
	if _, err := Up(t.Context(), c, l, exe); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return exists(l.ServeInfo()) })
	// The failed startup publishes the cause without a resolved project UID.
	if err := saveStartupErrors(l, map[string]string{"example-project": "credential_env KATA_STARTUP_TEST_TOKEN: unset or empty"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KATA_STARTUP_TEST_TOKEN", "fixture-token")
	if result, err := Up(t.Context(), c, l, exe); err != nil || result != Started {
		t.Fatal("valid up did not repair inherited credentials", result, err)
	}
}

func TestCredentialRepairPreservesSigningLaneAllowed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		route *RouteStatus
		want  bool
	}{
		{name: "no saved route", want: false},
		{name: "allowed route", route: &RouteStatus{Allowed: true}, want: false},
		{name: "refused route", route: &RouteStatus{Allowed: false}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := state.Layout{Root: t.TempDir()}
			pl := statusTestLane(t, l)
			if tc.route != nil {
				if err := state.WriteJSON(state.OSFS{}, pl.RouteFile(), tc.route); err != nil {
					t.Fatal(err)
				}
			}
			if err := state.WriteJSON(state.OSFS{}, l.RunFile("run"), runner.Run{Version: 1, UID: "run", Target: runner.Target{Identity: kata.Identity{ProjectUID: "project"}}, Ops: []runner.Op{{SigningRefused: true}}}); err != nil {
				t.Fatal(err)
			}
			if err := saveStartupErrors(l, map[string]string{"waiting-project": "credential_env KATA_STARTUP_TEST_TOKEN: unset or empty"}); err != nil {
				t.Fatal(err)
			}
			t.Setenv("KATA_STARTUP_TEST_TOKEN", "fixture-token")
			cfg := config.Config{Projects: []config.Project{{Name: "example-project"}, {Name: "waiting-project", CredentialEnv: "KATA_STARTUP_TEST_TOKEN"}}}
			if got := credentialRepairReady(cfg, l); got != tc.want {
				t.Fatalf("credential repair ready = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWorkerProcessesUnsignalledControlWithinAcceptanceBudget(t *testing.T) {
	f := workerFixtureFor(t)
	stop := startWorkerFixture(t, f)
	defer stop()
	waitWorker(t, func() bool { return f.lists.Load() > 0 })
	// A saved request must still get a prompt response when its wake signal is
	// lost, or when a previous resume pass waited for asynchronous reporting.
	req := runner.Request{Version: 1, UID: "01K00000000000000000000000", Kind: "release", RunUID: "01K00000000000000000000009"}
	if err := state.WriteJSON(state.OSFS{}, filepath.Join(f.l.RequestsDir(), req.UID+".json"), req); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var result runner.RequestResult
		if err := state.ReadJSON(state.OSFS{}, runner.RequestResultPath(f.l, req.UID), 64<<10, &result); err == nil {
			if result.Error == "" || result.RunUID != req.RunUID {
				t.Fatal("missing-run control was not refused", result)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("durable control is stranded until the minute scan")
		}
		<-ticker.C
	}
}
