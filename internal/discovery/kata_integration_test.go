//go:build kata

package discovery

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/testenv"
)

func TestIntegDiscoveryStartsStoppedLocalKata(t *testing.T) {
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	for _, pair := range os.Environ() {
		name, _, _ := strings.Cut(pair, "=")
		if strings.HasPrefix(name, "KATA_") {
			t.Setenv(name, "")
		}
	}
	root, err := os.MkdirTemp("/tmp", "hk-discovery-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	t.Setenv("KATA_HOME", root)
	t.Setenv("KATA_AUTHOR", "worker")
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Chdir(root)
	command := func(ctx context.Context, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, bin, append(args, "--json")...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("kata %v: %v %s", args, err, out)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	command(ctx, "daemon", "start")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		command(ctx, "daemon", "stop")
	})
	command(ctx, "projects", "create", "example-project")
	command(ctx, "daemon", "stop")
	client, err := NewCommandKata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if client.route.State != "local_stopped" {
		t.Fatalf("setup did not stop local route: %+v", client.route)
	}
	actor, err := client.WhoAmI(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if instance, err := client.RouteIdentity(ctx, actor); err != nil || instance == "" {
		refreshed, _ := client.selectedRoute(ctx)
		t.Fatalf("stopped-daemon discovery identity: %q %v (initial %+v, refreshed %+v)", instance, err, client.route, refreshed)
	}
	project, err := client.ProbeProject(ctx, "example-project", actor)
	if err != nil || !project.CronV1 || project.UID == "" {
		t.Fatalf("stopped-daemon project probe: %+v %v", project, err)
	}
	if defined, err := client.HasDefinitions(ctx, "example-project", actor); err != nil || defined {
		t.Fatalf("new project definitions = %t, %v; want none", defined, err)
	}
	job := filepath.Join(root, "job.json")
	if err := os.WriteFile(job, []byte(`{"name":"example-job","definition":{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Report succeeded."},"issue":{"kind":"per-run","title":"Example","body":"Example run."},"overlap":"forbid","catchup":"skip","timeout_seconds":600,"options":{"agent":"default"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	command(ctx, "--project", "example-project", "--as", actor, "cron", "job", "create", "--file", job)
	if defined, err := client.HasDefinitions(ctx, "example-project", actor); err != nil || !defined {
		t.Fatalf("project with a job definitions = %t, %v; want some", defined, err)
	}
}
