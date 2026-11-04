package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const fixtureRoot = "/srv/projects/example/state"
const fixtureName = "herdr-kata-d7dbce3b"

type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", e) }
func (e exitStatus) ExitCode() int { return int(e) }

func installation(t *testing.T, platform string) (Manager, string) {
	t.Helper()
	m := manager(platform)
	m.Home = t.TempDir()
	get := m.Getenv
	m.Getenv = func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return filepath.Join(m.Home, "config")
		}
		return get(k)
	}
	path := filepath.Join(m.Home, "Library", "LaunchAgents", fixtureName+".plist")
	if platform == "linux" {
		path = filepath.Join(m.Home, "config", "systemd", "user", fixtureName+".service")
	}
	return m, path
}

func call(t *testing.T, m Manager, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := m.Command(context.Background(), args, "/srv/config.toml", fixtureRoot, "/srv/bin/herdr-kata", &out)
	return code, out.String()
}

func readService(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("service permissions: %v %v", info, err)
	}
	return b
}

// Missing writes, wrong target/manager order or start instead of restart fails.
func TestInstallRefreshAndUninstall(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			m, path := installation(t, platform)
			var calls [][]string
			m.Run = func(_ context.Context, bin string, args ...string) error {
				calls = append(calls, append([]string{bin}, args...))
				if bin == "launchctl" && args[0] == "bootstrap" {
					if info, err := os.Stat(filepath.Join(m.Home, "Library", "Logs")); err != nil || !info.IsDir() {
						t.Fatalf("missing log parent at activation: %v", err)
					}
				}
				return nil
			}
			if code, out := call(t, m, "install"); code != 0 || !strings.Contains(out, "installed: "+path) {
				t.Fatalf("install: %d %s", code, out)
			}
			before := readService(t, path)
			get := m.Getenv
			m.Getenv = func(key string) string {
				if key == "PATH" {
					return "/srv/new/bin:/usr/bin"
				}
				return get(key)
			}
			if code, out := call(t, m, "install"); code != 0 {
				t.Fatalf("refresh: %d %s", code, out)
			}
			if after := readService(t, path); bytes.Equal(before, after) || !bytes.Contains(after, []byte("/srv/new/bin:/usr/bin")) {
				t.Fatalf("refresh did not replace environment: %s", after)
			}
			wantInstall := [][]string{{"launchctl", "bootout", "gui/501/" + fixtureName}, {"launchctl", "bootstrap", "gui/501", path}}
			wantRemove := [][]string{{"launchctl", "bootout", "gui/501/" + fixtureName}}
			if platform == "linux" {
				wantInstall = [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", fixtureName + ".service"}, {"systemctl", "--user", "restart", fixtureName + ".service"}}
				wantRemove = [][]string{{"systemctl", "--user", "disable", fixtureName + ".service"}, {"systemctl", "--user", "daemon-reload"}}
			}
			if want := append(append([][]string{}, wantInstall...), wantInstall...); !reflect.DeepEqual(calls, want) {
				t.Fatalf("activation sequence: got %q want %q", calls, want)
			}
			calls = nil
			// Removal must work even after the install shell's PATH becomes unusable.
			m.Getenv = func(key string) string {
				if key == "PATH" {
					return "relative"
				}
				return get(key)
			}
			if code, out := call(t, m, "uninstall"); code != 0 || !strings.Contains(out, "worker keeps running") {
				t.Fatalf("uninstall: %d %s", code, out)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("service remains: %v", err)
			}
			if !reflect.DeepEqual(calls, wantRemove) {
				t.Fatalf("removal sequence: got %q want %q", calls, wantRemove)
			}
			calls = nil
			if code, out := call(t, m, "uninstall"); code != 0 || !strings.Contains(out, "not installed") || len(calls) != 0 {
				t.Fatalf("absent uninstall: %d %s calls=%q", code, out, calls)
			}
		})
	}
}

func TestPrintHasNoEffectsOnEitherPlatform(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		m, path := installation(t, platform)
		code, out := call(t, m, "install", "--print")
		if code != 0 {
			t.Fatalf("%s print: %d %s", platform, code, out)
		}
		if platform == "darwin" {
			if got := decodePlist(t, []byte(out))["ProgramArguments"].([]string); !reflect.DeepEqual(got, []string{"/srv/bin/herdr-kata", "--config", "/srv/config.toml", "--state", fixtureRoot, "up"}) {
				t.Fatalf("print argv: %q", got)
			}
		} else {
			_, argv, _ := decodeUnit(t, []byte(out))
			if !reflect.DeepEqual(argv, []string{"/srv/bin/herdr-kata", "--config", "/srv/config.toml", "--state", fixtureRoot, "up"}) {
				t.Fatalf("print argv: %q", argv)
			}
		}
		for _, p := range []string{filepath.Dir(path), filepath.Join(m.Home, "Library", "Logs")} {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Fatalf("print created directory %s: %v", p, err)
			}
		}
	}
}

func TestActivationFailureKeepsService(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		for _, stage := range []int{0, 1, 2} {
			if platform == "darwin" && stage == 2 {
				continue
			}
			m, path := installation(t, platform)
			calls := 0
			m.Run = func(context.Context, string, ...string) error {
				calls++
				if calls == stage+1 {
					return errors.New("activation denied")
				}
				return nil
			}
			if code, out := call(t, m, "install"); code != 1 || !strings.Contains(out, "written to "+path) || !strings.Contains(out, "activation failed") || !strings.Contains(out, "activation denied") || calls != stage+1 {
				t.Fatalf("%s stage %d: %d %s calls=%d", platform, stage, code, out, calls)
			}
			readService(t, path)
		}
	}
}

func TestBootoutOnlyIgnoresMissingService(t *testing.T) {
	for _, missing := range []bool{true, false} {
		m, path := installation(t, "darwin")
		calls := 0
		m.Run = func(_ context.Context, _ string, args ...string) error {
			calls++
			if args[0] == "bootout" {
				if missing {
					return fmt.Errorf("not loaded: %w", exitStatus(3))
				}
				return fmt.Errorf("permission denied: %w", exitStatus(1))
			}
			return nil
		}
		code, out := call(t, m, "install")
		if missing {
			if code != 0 || calls != 2 {
				t.Fatalf("absent bootout: %d %s calls=%d", code, out, calls)
			}
			calls = 0
			if code, out = call(t, m, "uninstall"); code != 0 || calls != 1 {
				t.Fatalf("absent unload: %d %s calls=%d", code, out, calls)
			}
		} else {
			if code != 1 || calls != 1 {
				t.Fatalf("permission bootout: %d %s calls=%d", code, out, calls)
			}
			calls = 0
			if code, out = call(t, m, "uninstall"); code != 1 || calls != 1 || strings.Contains(out, "removed;") {
				t.Fatalf("unload failure: %d %s calls=%d", code, out, calls)
			}
			readService(t, path)
		}
	}
}

func TestLinuxDisableFailureRetainsFile(t *testing.T) {
	m, path := installation(t, "linux")
	m.Run = func(context.Context, string, ...string) error { return nil }
	if code, out := call(t, m, "install"); code != 0 {
		t.Fatalf("setup: %d %s", code, out)
	}
	m.Run = func(_ context.Context, bin string, args ...string) error {
		if bin != "systemctl" || !reflect.DeepEqual(args, []string{"--user", "disable", fixtureName + ".service"}) {
			t.Fatalf("unexpected disable: %s %q", bin, args)
		}
		return errors.New("disable denied")
	}
	if code, out := call(t, m, "uninstall"); code != 1 || !strings.Contains(out, "disable denied") {
		t.Fatalf("disable failure: %d %s", code, out)
	}
	readService(t, path)
}

func TestInstallRejectsEnvironmentBeforeWriting(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		m, path := installation(t, platform)
		get := m.Getenv
		m.Getenv = func(k string) string {
			if k == "PATH" {
				return "relative::"
			}
			return get(k)
		}
		if code, out := call(t, m, "install"); code != 2 || !strings.Contains(out, "PATH") {
			t.Fatalf("%s invalid environment: %d %s", platform, code, out)
		}
		if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
			t.Fatalf("created directory on invalid input: %v", err)
		}
	}
}

func TestUnsupportedPlatform(t *testing.T) {
	m := manager("freebsd")
	for _, args := range [][]string{{"install", "--print"}, {"install"}, {"uninstall"}} {
		if code, out := call(t, m, args...); code != 1 || !strings.Contains(out, "service: unsupported on freebsd; run herdr-kata up from your login environment") {
			t.Fatalf("unsupported: %d %s", code, out)
		}
	}
	if got := m.Status(fixtureRoot); !strings.Contains(got, ": unknown (") {
		t.Fatalf("unsupported status: %s", got)
	}
}

func TestStatusIsOptionalAndDoesNotProbe(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		m, path := installation(t, platform)
		if got := m.Status(fixtureRoot); got != "service "+fixtureName+": not installed" {
			t.Fatalf("absent status: %s", got)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture service"), 0600); err != nil {
			t.Fatal(err)
		}
		if got := m.Status(fixtureRoot); got != "service "+fixtureName+": installed "+path {
			t.Fatalf("installed status: %s", got)
		}
		m.Home = filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(m.Home, nil, 0600); err != nil {
			t.Fatal(err)
		}
		m.Getenv = func(string) string { return "" }
		if got := m.Status(fixtureRoot); !strings.Contains(got, ": unknown (") {
			t.Fatalf("inaccessible status: %s", got)
		}
	}
}

func TestDifferentRootsCoexist(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		m, path := installation(t, platform)
		m.Run = func(context.Context, string, ...string) error { return nil }
		var out bytes.Buffer
		for _, root := range []string{fixtureRoot, fixtureRoot + "-other"} {
			if code := m.Command(context.Background(), []string{"install"}, "/srv/config.toml", root, "/srv/bin/herdr-kata", &out); code != 0 {
				t.Fatalf("install two roots: %d %s", code, &out)
			}
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil || len(entries) != 2 {
			t.Fatalf("independent installations: %v %v", entries, err)
		}
		if code, out := call(t, m, "uninstall"); code != 0 {
			t.Fatalf("remove first: %d %s", code, out)
		}
		entries, err = os.ReadDir(filepath.Dir(path))
		if err != nil || len(entries) != 1 || entries[0].Name() == filepath.Base(path) {
			t.Fatalf("other installation changed: %v %v", entries, err)
		}
	}
}
