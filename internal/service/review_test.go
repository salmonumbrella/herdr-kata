package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hegel.dev/go/hegel"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("output closed") }

func TestPrintWriteFailure(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		m, path := installation(t, platform)
		if code := m.Command(context.Background(), []string{"install", "--print"}, "/srv/config", fixtureRoot, "/srv/bin/worker", failingWriter{}); code != 1 {
			t.Fatalf("write failure: %s exit %d", platform, code)
		}
		if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
			t.Fatalf("print created destination: %v", err)
		}
	}
}

func TestInstallIOFailurePreventsActivation(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		m, _ := installation(t, platform)
		// A file at HOME prevents both log and service directory creation.
		m.Home = filepath.Join(t.TempDir(), "home-file")
		if err := os.WriteFile(m.Home, nil, 0600); err != nil {
			t.Fatal(err)
		}
		m.Getenv = func(k string) string {
			if k == "PATH" {
				return "/usr/bin"
			}
			return ""
		}
		if code, out := call(t, m, "install"); code != 1 || strings.Contains(out, "installed:") {
			t.Fatalf("I/O failure: %s %d %s", platform, code, out)
		}
	}
}

func TestUninstallReloadFailureReportsPartialCleanup(t *testing.T) {
	m, path := installation(t, "linux")
	m.Run = func(context.Context, string, ...string) error { return nil }
	if code, out := call(t, m, "install"); code != 0 {
		t.Fatalf("setup: %d %s", code, out)
	}
	calls := 0
	m.Run = func(_ context.Context, bin string, args ...string) error {
		calls++
		if bin != "systemctl" || len(args) < 2 || args[0] != "--user" {
			t.Fatalf("manager: %s %q", bin, args)
		}
		if args[1] == "daemon-reload" {
			return errors.New("reload denied")
		}
		if args[1] != "disable" {
			t.Fatalf("unexpected call: %q", args)
		}
		return nil
	}
	if code, out := call(t, m, "uninstall"); code != 1 || !strings.Contains(out, "reload denied") || strings.Contains(out, "removed;") || calls != 2 {
		t.Fatalf("reload failure: %d %s calls=%d", code, out, calls)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file was not removed before reload: %v", err)
	}
}

func TestPathSelectionOrderAndDuplicates(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			hegel.Test(t, func(ht *hegel.T) {
				text := strings.ReplaceAll(hegel.Draw(ht, hegel.Text()), ":", "_")
				m := manager(platform)
				get := m.Getenv
				m.Getenv = func(k string) string {
					if k == "PATH" {
						return "relative::/srv/bin/" + text + ":.:/usr/bin:/usr/bin:"
					}
					return get(k)
				}
				b, err := m.render("/srv/config", fixtureRoot, "/srv/bin/worker")
				if !representable(text) {
					if err == nil {
						ht.Fatalf("unsafe PATH accepted: %q", text)
					}
					return
				}
				if err != nil {
					ht.Fatal(err)
				}
				var path string
				if platform == "darwin" {
					path = decodePlist(ht, b)["EnvironmentVariables"].(map[string]any)["PATH"].(string)
				} else {
					_, _, env := decodeUnit(ht, b)
					path = env["PATH"]
				}
				if want := "/srv/bin/" + text + ":/usr/bin:/usr/bin"; path != want {
					ht.Fatalf("PATH order/duplicates changed: %q want %q", path, want)
				}
			}, hegel.WithTestCases(1000))
		})
	}
}

func TestNativeInputRejectionPreservesExistingFile(t *testing.T) {
	for _, text := range []string{"'", "\"", "\\", "\ufdd0", "\U0001fffe", "\u0085"} {
		m, path := installation(t, "linux")
		calls := 0
		m.Run = func(context.Context, string, ...string) error { calls++; return nil }
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("existing service"), 0600); err != nil {
			t.Fatal(err)
		}
		get := m.Getenv
		if !strings.ContainsAny(text, "'\"\\") {
			m.Getenv = func(k string) string {
				if k == "XDG_DATA_HOME" {
					return "/srv/data/" + text
				}
				return get(k)
			}
		}
		exe := "/srv/bin/worker"
		if strings.ContainsAny(text, "'\"\\") {
			exe = "/srv/" + text + "/worker"
		}
		var out bytes.Buffer
		if code := m.Command(context.Background(), []string{"install"}, "/srv/config", fixtureRoot, exe, &out); code != 2 {
			t.Fatalf("%q: %d %s", text, code, &out)
		}
		if b := readService(t, path); string(b) != "existing service" {
			t.Fatalf("rejected refresh changed file: %s", b)
		}
		if calls != 0 {
			t.Fatalf("rejected refresh called manager %d times", calls)
		}
	}
}

func TestUninstallRejectsRelativeSelector(t *testing.T) {
	t.Chdir(t.TempDir())
	m := manager("linux")
	calls := 0
	m.Run = func(context.Context, string, ...string) error { calls++; return nil }
	get := m.Getenv
	m.Getenv = func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return ".config"
		}
		return get(k)
	}
	path := filepath.Join(".config", "systemd", "user", fixtureName+".service")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("keep service"), 0600); err != nil {
		t.Fatal(err)
	}
	if code, out := call(t, m, "uninstall"); code != 2 {
		t.Fatalf("invalid selector: %d %s", code, out)
	}
	if b := readService(t, path); string(b) != "keep service" {
		t.Fatalf("invalid uninstall changed file: %s", b)
	}
	if calls != 0 {
		t.Fatalf("invalid uninstall called manager %d times", calls)
	}
}

func TestUninstallRejectsInvalidHome(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			hegel.Test(t, func(ht *hegel.T) {
				m := manager(platform)
				m.Home = "relative/" + hegel.Draw(ht, hegel.Text())
				var out bytes.Buffer
				if code := m.Command(context.Background(), []string{"uninstall"}, "/srv/config", fixtureRoot, "/srv/bin/worker", &out); code != 2 {
					ht.Fatalf("invalid home accepted: %d %s", code, &out)
				}
			}, hegel.WithTestCases(1000))
		})
	}
}
