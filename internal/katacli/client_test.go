package katacli

import (
	"context"
	"encoding/json"
	"errors"
	"hegel.dev/go/hegel"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var fixture string

func TestMain(m *testing.M) {
	dir, e := os.MkdirTemp("", "native-command-")
	if e != nil {
		panic(e)
	}
	fixture = filepath.Join(dir, "kata")
	if runtime.GOOS == "windows" {
		fixture += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", fixture, "./testdata/command")
	if out, e := cmd.CombinedOutput(); e != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
func testClient(t *testing.T) *Client {
	t.Helper()
	return &Client{Executable: fixture, Target: Target{Server: "http://127.0.0.1:7777", Project: "spoke-project", Workspace: t.TempDir(), Actor: "worker", Teammate: "adapter", Home: t.TempDir()}, Timeout: time.Second, OutputLimit: 4096}
}
func mode(t *testing.T, s string) {
	t.Helper()
	p := filepath.Join(filepath.Dir(fixture), "mode")
	if e := os.WriteFile(p, []byte(s), 0600); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.Remove(p) })
}

type echo struct {
	Argv       []string `json:"argv"`
	Stdin      string   `json:"stdin"`
	Env        []string `json:"env"`
	Generation string   `json:"generation"`
	LastBytes  []byte   `json:"last_bytes"`
}

func TestExplicitRouting(t *testing.T) {
	t.Setenv("KATA_SERVER", "http://daemon.example")
	t.Setenv("KATA_AUTHOR", "intruder")
	t.Setenv("KATA_TEAMMATE", "other")
	t.Setenv("KATA_DB_PATH", "private.db")
	c := testClient(t)
	var out echo
	body := json.RawMessage(`{"body":"quotes ' ; $(echo injected)\nnew line","counter":9007199254740993}`)
	e := c.Call(t.Context(), []string{"comment", "issue with spaces", "--body-file", "-"}, body, &out)
	if e != nil {
		t.Fatal(e)
	}
	want := []string{"--project", "spoke-project", "--workspace", c.Target.Workspace, "--as", "worker", "--teammate", "adapter", "--json", "comment", "issue with spaces", "--body-file", "-"}
	if !reflect.DeepEqual(out.Argv, want) || out.Stdin != string(body) {
		t.Fatalf("argv/data changed: %+v", out)
	}
	for _, v := range out.Env {
		if strings.HasPrefix(v, "KATA_") && v != "KATA_SERVER="+c.Target.Server && v != "KATA_HOME="+c.Target.Home {
			t.Fatalf("ambient leak %q", v)
		}
	}
}
func TestCommandFailureBoundaries(t *testing.T) {
	for _, s := range []string{"malformed", "oversized", "oversizedstderr", "nonzero", "sleep"} {
		t.Run(s, func(t *testing.T) {
			mode(t, s)
			c := testClient(t)
			c.Timeout = 2 * time.Second
			if s == "sleep" {
				c.Timeout = 50 * time.Millisecond
			}
			if strings.HasPrefix(s, "oversized") {
				c.OutputLimit = 1024
			}
			var out any
			e := c.Call(t.Context(), []string{"show", "abc4"}, nil, &out)
			if e == nil {
				t.Fatal("failure accepted")
			}
			if strings.HasPrefix(s, "oversized") && !errors.Is(e, ErrOutputLimit) {
				t.Fatalf("output bound lost: %v", e)
			}
			if strings.HasPrefix(s, "oversized") {
				var ce *CommandError
				if !errors.As(e, &ce) || len(ce.Stdout) > c.OutputLimit || len(ce.Stderr) > c.OutputLimit {
					t.Fatalf("unbounded failure receipt %v", e)
				}
			}
			if s == "malformed" && !strings.Contains(e.Error(), "invalid Kata JSON") {
				t.Fatalf("malformed reply lost: %v", e)
			}
			if s == "sleep" && !errors.Is(e, context.DeadlineExceeded) {
				t.Fatalf("deadline lost: %v", e)
			}
			if s == "nonzero" {
				var ce *CommandError
				if !errors.As(e, &ce) || ce.ExitCode != 5 || !strings.Contains(string(ce.Stdout), "9007199254740993") {
					t.Fatalf("receipt lost: %v", e)
				}
			}
		})
	}
}
func assertSeparation(t testing.TB, text string) {
	t.Helper()
	if strings.IndexByte(text, 0) >= 0 || (runtime.GOOS == "windows" && !utf8.ValidString(text)) {
		if e := testClientTB(t).Call(context.Background(), []string{"show", text}, nil, new(any)); e == nil {
			t.Fatal("unsupported process argument accepted")
		}
		return
	}
	c := testClientTB(t)
	var out echo
	e := c.Call(context.Background(), []string{"show", text}, nil, &out)
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Argv) != 11 || string(out.LastBytes) != text {
		t.Fatalf("input became syntax: %#v", out.Argv)
	}
}
func testClientTB(t testing.TB) *Client {
	return &Client{Executable: fixture, Target: Target{Server: "http://127.0.0.1:7777", Project: "spoke-project", Workspace: ".", Actor: "worker", Teammate: ""}, Timeout: time.Second, OutputLimit: 65536}
}
func TestArgumentSeparationProperty(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) { assertSeparation(ht, hegel.Draw(ht, hegel.Text())) })
}
func FuzzArgumentSeparation(f *testing.F) {
	f.Add("'; $(touch nope)\n--daemon other")
	f.Add("")
	f.Add("\xff")
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 16384 {
			return
		}
		if strings.ContainsRune(s, 0) {
			c := testClient(t)
			if e := c.Call(t.Context(), []string{"show", s}, nil, new(any)); e == nil {
				t.Fatal("unsupported process argument accepted")
			}
			return
		}
		assertSeparation(t, s)
	})
}
func TestTUIResolvesReplacement(t *testing.T) {
	c := testClient(t)
	path := filepath.Join(t.TempDir(), "kata")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	copy := func() {
		raw, _ := os.ReadFile(fixture)
		if e := os.WriteFile(path, raw, 0700); e != nil {
			t.Fatal(e)
		}
	}
	copy()
	c.Executable = path
	for _, generation := range []string{"first", "second"} {
		if generation == "second" {
			replacement := path + ".new"
			build := exec.Command("go", "build", "-ldflags=-X main.buildGeneration=second", "-o", replacement, "./testdata/command")
			if raw, e := build.CombinedOutput(); e != nil {
				t.Fatalf("build replacement: %v %s", e, raw)
			}
			if e := os.Remove(path); e != nil {
				t.Fatal(e)
			}
			if e := os.Rename(replacement, path); e != nil {
				t.Fatal(e)
			}
		}

		cmd, e := c.TUICommand(t.Context(), "abc4")
		if e != nil {
			t.Fatal(e)
		}
		raw, e := cmd.Output()
		if e != nil {
			t.Fatal(e)
		}
		var out echo
		if e := json.Unmarshal(raw, &out); e != nil {
			t.Fatal(e)
		}
		want := []string{"--project", "spoke-project", "--workspace", c.Target.Workspace, "--as", "worker", "--teammate", "adapter", "tui", "--", "abc4"}
		if out.Generation != generation || !reflect.DeepEqual(out.Argv, want) {
			t.Fatalf("replacement/routing: %+v", out)
		}
	}
}
func TestTUIIssueCannotOverrideRoutingFlags(t *testing.T) {
	c := testClient(t)
	cmd, e := c.TUICommand(t.Context(), "--daemon=other")
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(cmd.Args[len(cmd.Args)-3:], []string{"tui", "--", "--daemon=other"}) {
		t.Fatalf("issue interpreted as flag: %v", cmd.Args)
	}
}

func TestNativeLauncherPreservesPreferencesAndCaseInsensitiveOSKeys(t *testing.T) {
	t.Setenv("VISUAL", "code --wait")
	t.Setenv("EDITOR", "nano")
	t.Setenv("TERM_PROGRAM", "terminal-example")
	t.Setenv("Path", "fixture-windows-path")
	t.Setenv("Pathext", ".EXE;.CMD")
	t.Setenv("kata_server", "http://wrong.example")
	t.Setenv("kata_auth_token", "ambient-secret")
	t.Setenv("http_proxy", "http://wrong.example")
	c := testClient(t)
	var out echo
	if e := c.Call(t.Context(), []string{"show", "issue"}, nil, &out); e != nil {
		t.Fatal(e)
	}
	for _, entry := range []string{"VISUAL=code --wait", "EDITOR=nano", "TERM_PROGRAM=terminal-example", "Path=fixture-windows-path", "Pathext=.EXE;.CMD"} {
		key, value, _ := strings.Cut(entry, "=")
		found := false
		for _, actual := range out.Env {
			actualKey, actualValue, _ := strings.Cut(actual, "=")
			if strings.EqualFold(key, actualKey) && value == actualValue {
				found = true
			}
		}
		if !found {
			t.Fatalf("native preference dropped %s: %v", entry, out.Env)
		}
	}
	for _, entry := range out.Env {
		if strings.Contains(entry, "ambient-secret") || strings.Contains(entry, "wrong.example") {
			t.Fatalf("ambient authority leaked %s", entry)
		}
	}
}

func TestProcessArgumentEncodingContract(t *testing.T) {
	for _, tc := range []struct {
		windows bool
		text    string
		reject  bool
	}{{true, "\xff", true}, {false, "\xff", false}, {true, "Inspect λ 🦀", false}} {
		e := validateProcessArguments([]string{"show", tc.text}, tc.windows)
		if (e != nil) != tc.reject {
			t.Fatalf("platform encoding windows=%v text=%q reject=%v error=%v", tc.windows, tc.text, tc.reject, e)
		}
	}
	assertSeparation(t, "\xff")
	assertSeparation(t, "Inspect λ 🦀")
}
