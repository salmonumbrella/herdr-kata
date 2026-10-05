package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"strings"
	"testing"
	"time"
)

func TestLeaseCommandExchange(t *testing.T) {
	t.Setenv("HERDR_KATA_HOME", t.TempDir())
	fn := commands()["lease"]
	if fn == nil {
		t.Fatal("lease command unavailable")
	}
	run := func(args ...string) (string, error) { return captureStdout(t, func() error { return fn(args) }) }
	if _, err := run("claim", "browser", "--scope", "local:example", "--as", "first", "--run", "run-a", "--ttl", "1h", "--why", "inspection"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("claim", "browser", "--scope", "local:example", "--as", "second"); err == nil {
		t.Fatal("contending claim accepted")
	}
	if _, err := run("renew", "browser", "--scope", "local:example", "--as", "first", "--run", "run-b", "--ttl", "2h"); err == nil {
		t.Fatal("wrong run renewed lease")
	}
	if _, err := run("renew", "browser", "--scope", "local:example", "--as", "first", "--run", "run-a", "--ttl", "2h"); err != nil {
		t.Fatal(err)
	}
	out, err := run("list", "--scope", "local:example", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var ls []store.Lease
	if err = json.Unmarshal([]byte(out), &ls); err != nil || len(ls) != 1 || ls[0].Holder.RunID != "run-a" || ls[0].Why != "inspection" {
		t.Fatalf("list=%s err=%v", out, err)
	}
	if _, err := run("release", "browser", "--scope", "local:example", "--as", "second"); err == nil {
		t.Fatal("wrong holder released lease")
	}
	start := time.Now()
	if _, err := run("claim", "browser", "--scope", "local:example", "--as", "second", "--wait", "10ms"); err == nil || time.Since(start) < 10*time.Millisecond {
		t.Fatalf("wait returned early: %v", err)
	}
	if _, err := run("release", "browser", "--scope", "local:example", "--as", "first", "--run", "run-a"); err != nil {
		t.Fatal(err)
	}
	out, err = run("list", "--json")
	if err != nil || json.Unmarshal([]byte(out), &ls) != nil || len(ls) != 0 {
		t.Fatalf("empty list=%s err=%v", out, err)
	}
	if _, err := run("claim", "browser", "--as", "first"); err == nil {
		t.Fatal("implicit scope accepted")
	}
	if _, err := run("claim", "browser", "--scope", "local:example", "--as", "first", "--ttl", "1ms"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("claim", "browser", "--scope", "local:example", "--as", "second", "--wait", "1s"); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseRenewalHelpExplainsDurationAndIdentity(t *testing.T) {
	output := captureStderr(t, usage)
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "herdr-kata lease renew ") {
			for _, want := range []string{"--ttl", "--run", "--job", "--holder-id"} {
				if !strings.Contains(line, want) {
					t.Errorf("renewal usage missing %s: %s", want, line)
				}
			}
		}
	}
	if !strings.Contains(output, "herdr-kata lease renew ") {
		t.Error("no separate renewal usage")
	}
	for _, args := range [][]string{{"renew", "--help"}, {"renew", "browser", "--help"}, {"renew"}} {
		var err error
		help := captureStderr(t, func() { err = commands()["lease"](args) })
		if len(args) > 1 && !errors.Is(err, flag.ErrHelp) {
			t.Errorf("help %v: %v", args, err)
		}
		if len(args) == 1 && err != nil {
			help += err.Error()
		}
		for _, want := range []string{"--ttl", "--run", "--job", "--holder-id"} {
			if !strings.Contains(help, want) {
				t.Errorf("help %v missing %s: %q", args, want, help)
			}
		}
		if len(args) > 1 && !strings.Contains(help, "omitted or zero means no expiry") {
			t.Errorf("help hides omitted TTL: %q", help)
		}
	}
}

func TestLeaseCommandNotHeldDiagnostics(t *testing.T) {
	t.Setenv("HERDR_KATA_HOME", t.TempDir())
	s, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	expiry := time.Now().Add(time.Hour)
	holder := store.Identity{Name: "owner", JobID: "job-a", RunID: "run-a", PID: "pane-a"}
	_, err = s.AcquireLease(context.Background(), store.LeaseRequest{Scope: "local:example", Resource: "browser", By: holder, TTL: time.Hour, Now: expiry.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.AcquireLease(context.Background(), store.LeaseRequest{Scope: "local:example", Resource: "expired", By: holder, TTL: time.Second, Now: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"renew", "release"} {
		for _, resource := range []string{"browser", "absent", "expired"} {
			args := []string{action, resource, "--scope", "local:example", "--as", "owner", "--job", "job-a", "--run", "mistyped", "--holder-id", "pane-a"}
			_, err = captureStdout(t, func() error { return commands()["lease"](args) })
			var missing *store.LeaseNotHeldError
			if !errors.As(err, &missing) {
				t.Fatalf("%v error=%v", args, err)
			}
			message := err.Error()
			if resource == "browser" {
				for _, want := range []string{`name="owner"`, `job="job-a"`, `run="run-a"`, `holder-id="pane-a"`, expiry.Format(time.RFC3339Nano)} {
					if !strings.Contains(message, want) {
						t.Errorf("%s wrong-identity message missing %q: %s", action, want, message)
					}
				}
			} else if !strings.Contains(message, "no live holder") || strings.Contains(message, "lease expired") {
				t.Errorf("%s no-holder diagnostic claims a cause: %s", action, message)
			}
		}
	}
}

func TestLeaseRenewOmittedAndZeroTTLRemainIndefinite(t *testing.T) {
	t.Setenv("HERDR_KATA_HOME", t.TempDir())
	for _, ttl := range [][]string{nil, {"--ttl", "0"}} {
		args := []string{"claim", "browser", "--scope", "local:example", "--as", "worker", "--ttl", "1h"}
		if _, err := captureStdout(t, func() error { return leaseCmd(args) }); err != nil {
			t.Fatal(err)
		}
		args = append([]string{"renew", "browser", "--scope", "local:example", "--as", "worker", "--json"}, ttl...)
		out, err := captureStdout(t, func() error { return leaseCmd(args) })
		var renewed store.Lease
		if err != nil || json.Unmarshal([]byte(out), &renewed) != nil || renewed.ExpiresAt != nil {
			t.Fatalf("TTL %v: output=%s error=%v", ttl, out, err)
		}
		if _, err := captureStdout(t, func() error {
			return leaseCmd([]string{"release", "browser", "--scope", "local:example", "--as", "worker"})
		}); err != nil {
			t.Fatal(err)
		}
	}
}
