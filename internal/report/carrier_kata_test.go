//go:build kata && !windows

package report

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type carrierFixture struct {
	bin, root, wrapper string
	env                []string
}

func (f carrierFixture) call(args ...string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.bin, append([]string{"--as", "worker", "--teammate", "herdr-kata", "--json"}, args...)...)
	cmd.Env = f.env
	cmd.Dir = f.root
	b, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("fixture command %q: %w: %s", args, err, b)
	}
	var result map[string]any
	err = json.Unmarshal(b, &result)
	return result, err
}
func (f carrierFixture) run(t *testing.T, args ...string) map[string]any {
	t.Helper()
	v, err := f.call(args...)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func (f carrierFixture) defaults(t *testing.T, priority string) {
	t.Helper()
	s := "[[daemon]]\nname = 'fixture'\nlocal = true\n"
	if priority != "" {
		s += "[create]\ndefault_priority = " + priority + "\n"
	}
	if err := os.WriteFile(filepath.Join(f.root, "kata", "config.toml"), []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
}
func (f carrierFixture) client() kata.Kata {
	return kata.New(f.wrapper, "fixture", nil, "example-project", "worker")
}

func realCarrierFixture(t *testing.T) carrierFixture {
	t.Helper()
	bin := os.Getenv("HERDR_KATA_TEST_BINARY")
	if bin == "" {
		t.Skip("set HERDR_KATA_TEST_BINARY to a Kata binary supporting create --priority none")
	}
	bin, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("/tmp", "hkcarrier-")
	if err != nil {
		t.Fatal(err)
	}
	f := carrierFixture{bin: bin, root: root, wrapper: filepath.Join(root, "kata-wrapper")}
	for key, name := range map[string]string{"HOME": "home", "KATA_HOME": "kata", "XDG_CONFIG_HOME": "config", "XDG_STATE_HOME": "state", "XDG_CACHE_HOME": "cache", "XDG_RUNTIME_DIR": "run"} {
		path := filepath.Join(root, name)
		if err = os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		f.env = append(f.env, key+"="+path)
		t.Setenv("FIXTURE_"+key, path)
	}
	f.env = append(f.env, "PATH=/usr/bin:/bin", "USER=worker", "KATA_AUTHOR=worker")
	t.Setenv("FIXTURE_BINARY", bin)
	f.defaults(t, "")
	script := `#!/bin/sh
exec /usr/bin/env -i PATH=/usr/bin:/bin USER=worker KATA_AUTHOR=worker HOME="$FIXTURE_HOME" KATA_HOME="$FIXTURE_KATA_HOME" XDG_CONFIG_HOME="$FIXTURE_XDG_CONFIG_HOME" XDG_STATE_HOME="$FIXTURE_XDG_STATE_HOME" XDG_CACHE_HOME="$FIXTURE_XDG_CACHE_HOME" XDG_RUNTIME_DIR="$FIXTURE_XDG_RUNTIME_DIR" "$FIXTURE_BINARY" "$@"
`
	if err = os.WriteFile(f.wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(root, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "daemon", "start", "--foreground", "--no-auto-token")
	cmd.Env = f.env
	cmd.Dir = root
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		health, readErr := f.call("health")
		selected, _ := health["selected"].(map[string]any)
		if readErr != nil || selected["home"] != filepath.Join(root, "kata") {
			t.Errorf("scratch identity check: %v %v", selected, readErr)
		}
		if _, stopErr := f.call("daemon", "stop"); stopErr != nil {
			t.Errorf("scratch stop: %v", stopErr)
			_ = cmd.Process.Kill()
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
			_ = log.Close()
			_ = os.RemoveAll(root)
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			_ = log.Close()
			_ = os.RemoveAll(root)
			t.Error("scratch daemon required forced cleanup")
		}
	})
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		health, healthErr := f.call("health")
		if healthErr == nil {
			selected, _ := health["selected"].(map[string]any)
			if selected["home"] != filepath.Join(root, "kata") {
				t.Fatalf("wrong scratch daemon: %v", selected)
			}
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal(healthErr)
		}
	}
	f.run(t, "projects", "create", "example-project")
	return f
}

func TestDailyCarrierRealKataDefaultChange(t *testing.T) {
	for _, historical := range []bool{false, true} {
		t.Run(fmt.Sprint("historical=", historical), func(t *testing.T) {
			f := realCarrierFixture(t)
			existing := f.run(t, "--project", "example-project", "create", "Existing work", "--body", "Existing work body", "--force-new")["issue"].(map[string]any)["uid"].(string)
			if historical {
				title := "cron alerts: 01J00000000000000000000010 2026-11-02"
				body := "herdr-kata requests attention here when a run of job 01J00000000000000000000010 settles failed or unknown in the 2026-11-02 UTC start-date group. Existing issues receive no adapter writes. Inspect runs with kata cron run list --job-uid 01J00000000000000000000010; close this issue when handled."
				f.run(t, "--project", "example-project", "create", title, "--body", body, "--force-new", "--idempotency-key", "cron-worker:01J00000000000000000000010:alert:2026-11-02")
			}
			f.defaults(t, "2")
			ctx, st, _, r := setup(t)
			if err := st.Update(r.Key, func(r *journal.Run) error {
				r.Plan.Issue.Kind = "existing"
				r.Plan.Issue.UID = existing
				r.IssueRef = existing
				r.IssueUID = existing
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := NotifyOnce(ctx, f.client(), st, r.Key, "settled", "first synthetic failure"); err != nil {
				t.Fatal(err)
			}
			list := f.run(t, "--project", "example-project", "list", "--limit", "0")["issues"].([]any)
			var carrier string
			for _, v := range list {
				i := v.(map[string]any)
				if strings.HasPrefix(i["title"].(string), "cron alerts:") {
					carrier = i["uid"].(string)
					if i["priority"] != nil {
						t.Fatal("carrier inherited client priority")
					}
				}
			}
			if carrier == "" {
				t.Fatal("missing carrier")
			}
			f.defaults(t, "3")
			saved, err := st.Load(r.Key)
			if err != nil {
				t.Fatal(err)
			}
			saved.Key = journal.ManualKey("executor", "example-project", "second")
			saved.RunUID = "01J00000000000000000000002"
			saved.Notified = nil
			if err = st.Create(saved); err != nil {
				t.Fatal(err)
			}
			// New CLI client and journal store model recovery after a worker restart.
			if err = NotifyOnce(ctx, f.client(), journal.New(st.Layout), saved.Key, "settled", "second synthetic failure"); err != nil {
				t.Fatal(err)
			}
			list = f.run(t, "--project", "example-project", "list", "--limit", "0")["issues"].([]any)
			count := 0
			for _, v := range list {
				if strings.HasPrefix(v.(map[string]any)["title"].(string), "cron alerts:") {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("carrier count %d", count)
			}
			issue := f.run(t, "--project", "example-project", "show", carrier)["issue"].(map[string]any)
			delivered := false
			for key, v := range issue["metadata"].(map[string]any) {
				if strings.HasPrefix(key, "notify.") && v.(map[string]any)["message"] == "second synthetic failure" {
					delivered = true
				}
			}
			if !delivered {
				t.Fatal("later failure did not request attention")
			}
			plain := f.run(t, "--project", "example-project", "create", "Unrelated new work", "--body", "Another body", "--force-new")["issue"].(map[string]any)
			if plain["priority"] != float64(3) {
				t.Fatal("operator default was changed")
			}
			untouched := f.run(t, "--project", "example-project", "show", existing)["issue"].(map[string]any)
			for key := range untouched["metadata"].(map[string]any) {
				if strings.HasPrefix(key, "notify.") {
					t.Fatal("notified existing work issue")
				}
			}
			f.run(t, "--project", "example-project", "close", carrier, "--done", "--message", "Synthetic carrier handled; fixture identity reviewed completely.", "--reviewed", "fixture")
			if ref, err := f.client().CreateAlertCarrier(issue["title"].(string), issue["body"].(string), "cron-worker:01J00000000000000000000010:alert:2026-11-02"); err == nil || ref != "" {
				t.Fatalf("adopted closed carrier %q: %v", ref, err)
			}
		})
	}
}

func TestDailyCarrierRealKataRejectsMutatedOriginal(t *testing.T) {
	f := realCarrierFixture(t)
	key := "cron-worker:fixture:alert:2026-11-02"
	issue := f.run(t, "--project", "example-project", "create", "Example carrier", "--body", "Unrelated original body", "--force-new", "--idempotency-key", key)["issue"].(map[string]any)
	f.run(t, "--project", "example-project", "edit", issue["uid"].(string), "--body", "Expected carrier body")
	f.defaults(t, "2")
	if ref, err := f.client().CreateAlertCarrier("Example carrier", "Expected carrier body", key); err == nil || ref != "" || kata.Classify(err) != kata.Conflict {
		t.Fatalf("adopted mutated original %q: %v", ref, err)
	}
}

// An installed numeric-only CLI keeps its prior create behavior. Default drift
// must remain a conflict, never a reason to adopt a different fingerprint.
func TestDailyCarrierRealKataLegacyContinuity(t *testing.T) {
	legacy := os.Getenv("HERDR_KATA_LEGACY_BINARY")
	if legacy == "" {
		t.Skip("set HERDR_KATA_LEGACY_BINARY to the older Kata CLI")
	}
	t.Setenv("HERDR_KATA_TEST_BINARY", legacy)
	f := realCarrierFixture(t)
	f.defaults(t, "2")
	ctx, st, _, r := setup(t)
	if err := st.Update(r.Key, func(r *journal.Run) error { r.Plan.Issue.Kind = "existing"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := NotifyOnce(ctx, f.client(), st, r.Key, "settled", "first legacy failure"); err != nil {
		t.Fatal(err)
	}
	issues := f.run(t, "--project", "example-project", "list", "--limit", "0")["issues"].([]any)
	if len(issues) != 1 {
		t.Fatalf("legacy carrier count %d", len(issues))
	}
	issue := issues[0].(map[string]any)
	if issue["priority"] != float64(2) {
		t.Fatal("legacy default was not preserved")
	}
	saved, err := st.Load(r.Key)
	if err != nil {
		t.Fatal(err)
	}
	saved.Key = journal.ManualKey("executor", "example-project", "legacy-second")
	saved.Notified = nil
	if err = st.Create(saved); err != nil {
		t.Fatal(err)
	}
	if err = NotifyOnce(ctx, f.client(), st, saved.Key, "settled", "second legacy failure"); err != nil {
		t.Fatal(err)
	}
	f.defaults(t, "3")
	if ref, err := f.client().CreateAlertCarrier(issue["title"].(string), issue["body"].(string), "cron-worker:01J00000000000000000000010:alert:2026-11-02"); err == nil || ref != "" || kata.Classify(err) != kata.Conflict {
		t.Fatalf("legacy drift adopted %q: %v", ref, err)
	}
	issues = f.run(t, "--project", "example-project", "list", "--limit", "0")["issues"].([]any)
	if len(issues) != 1 {
		t.Fatal("legacy drift duplicated carrier")
	}
}
