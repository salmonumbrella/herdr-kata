package hc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hegel.dev/go/hegel"
)

func stub(t *testing.T, fixture string, code string) (HC, func() []string) {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "hc")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$HC_TEST_ARGS"
printf '%s\n%s\n' "$HC_SESSION" "${HC_SOCKET-unset}" > "$HC_TEST_ENV"
if [ "$HC_TEST_CODE" = 0 ]; then cat "$HC_TEST_REPLY"; else cat "$HC_TEST_REPLY" >&2; fi
exit "$HC_TEST_CODE"
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	reply, err := filepath.Abs(filepath.Join("testdata", fixture+".json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HC_TEST_ARGS", filepath.Join(dir, "args"))
	t.Setenv("HC_TEST_ENV", filepath.Join(dir, "env"))
	t.Setenv("HC_TEST_REPLY", reply)
	t.Setenv("HC_TEST_CODE", code)
	return New(binary, "fixture-session"), func() []string {
		b, e := os.ReadFile(filepath.Join(dir, "args"))
		if e != nil {
			t.Fatal(e)
		}
		return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	}
}

func TestRunArgvExact(t *testing.T) {
	h, args := stub(t, "run", "0")
	got, err := h.Run(context.Background(), RunSpec{Name: "example", Cwd: "/srv/projects/example", Labels: map[string]string{"kata.step": "main", "kata.run": "run"}, Prefix: []string{"env", "-u", "HC_SOCKET", "HOME=/srv/home"}, Argv: []string{"/bin/sh", "-c", "echo hello world"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run", "--json", "--bg", "--no-parent", "--name", "example", "--cwd", "/srv/projects/example", "--label", "kata.run=run", "--label", "kata.step=main", "--", "env", "-u", "HC_SOCKET", "HOME=/srv/home", "/bin/sh", "-c", "echo hello world"}
	if !reflect.DeepEqual(args(), want) {
		t.Fatalf("argv %q != %q", args(), want)
	}
	if got.Address != "t1" || got.Name != "fixture-live" || got.State != "live" {
		t.Fatalf("launch: %+v", got)
	}
}

func TestSessionSetSocketDropped(t *testing.T) {
	t.Setenv("HC_SESSION", "poisoned")
	t.Setenv("HC_SOCKET", "/nonexistent/poisoned.sock")
	h, _ := stub(t, "get-live", "0")
	if _, err := h.Get(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(os.Getenv("HC_TEST_ENV"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "fixture-session\nunset\n" {
		t.Fatalf("child env: %q", b)
	}
	if os.Getenv("HC_SESSION") != "poisoned" || os.Getenv("HC_SOCKET") == "" {
		t.Fatal("mutated parent environment")
	}
}

func TestGetParsesFullRecord(t *testing.T) {
	for _, tt := range []struct {
		file, state, status, app string
		reported                 bool
		pid                      uint32
		created                  int64
		code                     *int
	}{
		{"get-live", "live", "", "", false, 2014986, 1793623117845, nil},
		{"get-reported", "live", "working", "fixture", true, 2015035, 1793623118254, nil},
		{"get-finished", "finished", "", "", false, 2015050, 1793623118708, new(7)},
	} {
		t.Run(tt.file, func(t *testing.T) {
			h, args := stub(t, tt.file, "0")
			got, err := h.Get(context.Background(), "example")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(args(), []string{"get", "--json", "example"}) {
				t.Fatalf("argv: %q", args())
			}
			if got.State != tt.state || got.Status != tt.status || got.App != tt.app || got.Reported != tt.reported || got.PID != tt.pid || got.CreatedMs != tt.created || !reflect.DeepEqual(got.ExitCode, tt.code) || got.Signal != nil {
				t.Fatalf("terminal: %+v", got)
			}
			if got.Labels["kata.run"] != "example-run" || len(got.Argv) != 3 || got.Argv[0] != "/bin/sh" {
				t.Fatalf("identity: %+v", got)
			}
			if tt.file == "get-reported" && (got.ForegroundPID != 2015035 || got.ForegroundArgv[0] != "/bin/sh") {
				t.Fatalf("foreground: %+v", got)
			}
			if tt.file == "get-finished" && got.Reason != "exited" {
				t.Fatalf("reason: %+v", got)
			}
		})
	}
}

func TestNotFoundExit3(t *testing.T) {
	h, _ := stub(t, "not-found-error", "3")
	_, err := h.Get(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error: %v", err)
	}
}

func TestRemoveLive(t *testing.T) {
	h, args := stub(t, "remove-live-error", "1")
	if err := h.Remove(context.Background(), "t1"); !errors.Is(err, ErrLive) {
		t.Fatalf("error: %v", err)
	}
	if !reflect.DeepEqual(args(), []string{"rm", "--json", "t1"}) {
		t.Fatalf("argv: %q", args())
	}
}

func TestListAllLabels(t *testing.T) {
	h, args := stub(t, "list", "0")
	got, err := h.List(context.Background(), true, map[string]string{"kata.run": "example-run"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"t1"}) {
		t.Fatalf("addresses: %q", got)
	}
	if !reflect.DeepEqual(args(), []string{"ls", "--json", "--all", "--label", "kata.run=example-run"}) {
		t.Fatalf("argv: %q", args())
	}
}

func TestReadCompleteness(t *testing.T) {
	h, args := stub(t, "read", "0")
	got, complete, err := h.Read(context.Background(), "t1", 40)
	if err != nil || got != "example output" || !complete {
		t.Fatalf("read: %q %v %v", got, complete, err)
	}
	if !reflect.DeepEqual(args(), []string{"read", "--json", "--lines", "40", "t1"}) {
		t.Fatalf("argv: %q", args())
	}
}

func TestSendIdle(t *testing.T) {
	for _, tc := range []struct {
		status string
		args   []string
	}{
		{"idle", []string{"send", "--json", "--if", "idle", "t1", "Read prompt"}},
		{"done", []string{"send", "--json", "t1", "Read prompt"}},
	} {
		t.Run(tc.status, func(t *testing.T) {
			h, args := stub(t, "send", "0")
			turn, err := h.Send(context.Background(), "t1", "Read prompt", tc.status)
			if err != nil || turn != 7 || !reflect.DeepEqual(args(), tc.args) {
				t.Fatalf("turn %d error %v argv %q", turn, err, args())
			}
		})
	}
}

func TestSendRefused(t *testing.T) {
	for _, kind := range []string{"status_mismatch", "blocked", "not_found", "finished"} {
		t.Run(kind, func(t *testing.T) {
			code := "1"
			if kind == "not_found" {
				code = "3"
			}
			h, _ := stub(t, "send", code)
			file := filepath.Join(t.TempDir(), "reply.json")
			if err := os.WriteFile(file, []byte(`{"error":{"kind":"`+kind+`"}}`), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HC_TEST_REPLY", file)
			_, err := h.Send(context.Background(), "t1", "Read prompt", "idle")
			if !errors.Is(err, ErrNotSent) {
				t.Fatalf("refusal %s: %v", kind, err)
			}
		})
	}
}

func TestEnvPrefixSortedAndUnsets(t *testing.T) {
	got := EnvPrefix([]string{"Z", "HC_SOCKET", "A"}, map[string]string{"Z": "last value", "A": "first"})
	want := []string{"env", "-u", "A", "-u", "HC_SOCKET", "-u", "KATA_AUTH_TOKEN", "-u", "Z", "A=first", "Z=last value"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prefix: %q", got)
	}
}

// The environment prefix must preserve arbitrary values as single argv elements.
func TestEnvPrefixPreservesValues(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		value := hegel.Draw(ht, hegel.Text())
		got := EnvPrefix(nil, map[string]string{"VALUE": value})
		if got[len(got)-1] != "VALUE="+value {
			ht.Fatalf("changed value: %q", got)
		}
	})
}

func TestCoreID(t *testing.T) {
	h, args := stub(t, "status", "0")
	got, err := h.CoreID(context.Background())
	if err != nil || got != "a3de211f2f6a26c708595d9d71fb034d" {
		t.Fatalf("core: %q %v", got, err)
	}
	if !reflect.DeepEqual(args(), []string{"server", "status", "--json"}) {
		t.Fatalf("argv: %q", args())
	}
}

func TestControlArgv(t *testing.T) {
	for _, name := range []string{"interrupt", "kill"} {
		t.Run(name, func(t *testing.T) {
			h, args := stub(t, "run", "0")
			var err error
			if name == "interrupt" {
				err = h.Interrupt(context.Background(), "t1")
			} else {
				err = h.Kill(context.Background(), "t1")
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(args(), []string{name, "--json", "t1"}) {
				t.Fatalf("argv: %q", args())
			}
		})
	}
}

func TestExecArgv(t *testing.T) {
	h, args := stub(t, "read", "0")
	got, err := h.Exec(context.Background(), []string{"env", "-u", "HC_SOCKET", "/usr/local/bin/kata", "whoami"})
	if err != nil || got != "{\"complete\":true,\"how\":\"history\",\"text\":\"example output\"}\n" {
		t.Fatalf("exec %q %v", got, err)
	}
	if !reflect.DeepEqual(args(), []string{"exec", "--", "env", "-u", "HC_SOCKET", "/usr/local/bin/kata", "whoami"}) {
		t.Fatal(args())
	}
}
