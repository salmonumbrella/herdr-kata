package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"hegel.dev/go/hegel"
)

func manager(platform string) Manager {
	env := map[string]string{"PATH": ":relative:/usr/bin::/bin:.", "XDG_CONFIG_HOME": "/srv/config", "XDG_STATE_HOME": "/srv/state", "XDG_DATA_HOME": "/srv/data", "XDG_CACHE_HOME": "/srv/cache", "HC_SOCKET": "/srv/private.sock", "KATA_HOME": "/srv/private", "KATA_TOKEN": "secret", "XDG_RUNTIME_DIR": "/srv/runtime", "XDG_SESSION_ID": "private", "SECRET": "secret"}
	return Manager{OS: platform, Home: "/srv/home", UID: "501", Getenv: func(key string) string { return env[key] }, Run: func(context.Context, string, ...string) error { panic("unexpected manager call") }}
}

type xmlNode struct {
	XMLName xml.Name
	Text    string    `xml:",chardata"`
	Nodes   []xmlNode `xml:",any"`
}

func plistValue(n xmlNode) any {
	switch n.XMLName.Local {
	case "dict":
		v := map[string]any{}
		for i := 0; i < len(n.Nodes); i += 2 {
			v[n.Nodes[i].Text] = plistValue(n.Nodes[i+1])
		}
		return v
	case "array":
		v := []string{}
		for _, child := range n.Nodes {
			v = append(v, child.Text)
		}
		return v
	case "true", "false":
		return n.XMLName.Local == "true"
	default:
		return n.Text
	}
}

func decodePlist(t testing.TB, b []byte) map[string]any {
	t.Helper()
	var n xmlNode
	if err := xml.Unmarshal(b, &n); err != nil || len(n.Nodes) != 1 {
		t.Fatalf("invalid plist: %v: %s", err, b)
	}
	return plistValue(n.Nodes[0]).(map[string]any)
}

// Decode systemd's quoted words independently, including percent specifiers.
func unitWords(t testing.TB, input string) []string {
	t.Helper()
	words := []string{}
	for input = strings.TrimSpace(input); input != ""; input = strings.TrimSpace(input) {
		if input[0] != '"' {
			t.Fatalf("unquoted word: %q", input)
		}
		i := 1
		for ; i < len(input); i++ {
			if input[i] == '\\' {
				i++
			} else if input[i] == '"' {
				break
			}
		}
		if i >= len(input) {
			t.Fatalf("unterminated word: %q", input)
		}
		word, err := strconv.Unquote(input[:i+1])
		if err != nil {
			t.Fatal(err)
		}
		words = append(words, strings.ReplaceAll(word, "%%", "%"))
		input = input[i+1:]
	}
	return words
}

func decodeUnit(t testing.TB, b []byte) (map[string][]string, []string, map[string]string) {
	t.Helper()
	fields := map[string][]string{}
	env := map[string]string{}
	var argv []string
	for line := range strings.SplitSeq(string(b), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		fields[key] = append(fields[key], value)
		if key == "ExecStart" {
			if !strings.HasPrefix(value, ":") {
				t.Fatal("launcher must disable dollar substitution")
			}
			argv = unitWords(t, value[1:])
		}
		if key == "Environment" {
			words := unitWords(t, value)
			if len(words) != 1 {
				t.Fatalf("invalid environment: %q", words)
			}
			k, v, ok := strings.Cut(words[0], "=")
			if !ok {
				t.Fatalf("invalid assignment: %q", words[0])
			}
			env[k] = v
		}
	}
	return fields, argv, env
}

func rendered(t testing.TB, m Manager, cfg, root, exe string) []byte {
	t.Helper()
	b, err := m.render(cfg, root, exe)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A wrong argv, broadened environment or lost escaping changes the decoded launcher.
func TestRender(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			m := manager(platform)
			cfg, root, exe := `/srv/config &< "\%$/config.toml`, `/srv/state &< "\%$`, `/srv/projects/example/$build/herdr-kata`
			b := rendered(t, m, cfg, root, exe)
			wantArgv := []string{exe, "--config", cfg, "--state", root, "up"}
			wantEnv := map[string]string{"PATH": "/usr/bin:/bin", "XDG_CONFIG_HOME": "/srv/config", "XDG_STATE_HOME": "/srv/state", "XDG_DATA_HOME": "/srv/data", "XDG_CACHE_HOME": "/srv/cache"}
			if platform == "darwin" {
				dict := decodePlist(t, b)
				if !reflect.DeepEqual(dict["ProgramArguments"], wantArgv) || !reflect.DeepEqual(dict["EnvironmentVariables"], stringAnyMap(wantEnv)) {
					t.Fatalf("argv/env: %#v", dict)
				}
				for _, key := range []string{"RunAtLoad", "AbandonProcessGroup"} {
					if dict[key] != true {
						t.Fatalf("%s: %#v", key, dict[key])
					}
				}
				if !reflect.DeepEqual(dict["KeepAlive"], map[string]any{"SuccessfulExit": false}) || dict["ThrottleInterval"] != "30" || dict["ProcessType"] != "Background" {
					t.Fatalf("launcher retry policy: %#v", dict)
				}
				if !strings.HasPrefix(dict["Label"].(string), "herdr-kata-") || dict["StandardOutPath"] != "/srv/home/Library/Logs/"+dict["Label"].(string)+".log" || dict["StandardOutPath"] != dict["StandardErrorPath"] {
					t.Fatalf("identity/logs: %#v", dict)
				}
			} else {
				fields, argv, env := decodeUnit(t, b)
				if !reflect.DeepEqual(argv, wantArgv) || !reflect.DeepEqual(env, wantEnv) {
					t.Fatalf("argv=%q env=%q", argv, env)
				}
				for key, want := range map[string]string{"Type": "oneshot", "RemainAfterExit": "yes", "Restart": "on-failure", "RestartSec": "30", "KillMode": "process", "WantedBy": "default.target"} {
					if !reflect.DeepEqual(fields[key], []string{want}) {
						t.Fatalf("%s: %q", key, fields[key])
					}
				}
				if len(fields["ExecStop"]) != 0 || !bytes.Contains(b, []byte(`ExecStart=:"/srv/projects/example/$build/herdr-kata"`)) {
					t.Fatalf("launcher lifecycle/executable: %s", b)
				}
			}
		})
	}
}

func stringAnyMap(input map[string]string) map[string]any {
	m := map[string]any{}
	for k, v := range input {
		m[k] = v
	}
	return m
}

func TestRenderRejectsUnsafeInput(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		for _, key := range []string{"PATH", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
			for _, value := range []string{"relative", "/srv/\nvalue", "/srv/\rvalue", "/srv/\x00value", "/srv/\x01value", "/srv/\xffvalue"} {
				m := manager(platform)
				get := m.Getenv
				m.Getenv = func(k string) string {
					if k == key {
						return value
					}
					return get(k)
				}
				if _, err := m.render("/srv/config", "/srv/state", "/srv/bin/worker"); err == nil {
					t.Fatalf("%s accepted %s=%q", platform, key, value)
				}
			}
		}
		for _, path := range []string{"", "relative", "/srv/\npath", "/srv/\xffpath", "/srv/\uffffpath"} {
			for i := range 4 {
				m := manager(platform)
				args := []string{"/srv/config", "/srv/state", "/srv/bin/worker"}
				if i == 3 {
					m.Home = path
				} else {
					args[i] = path
				}
				if _, err := m.render(args[0], args[1], args[2]); err == nil {
					t.Fatalf("%s accepted path %q at %d", platform, path, i)
				}
			}
		}
	}
}

// XML's own round-trip defines representability independently of our validator.
func representable(s string) bool {
	b, err := xml.Marshal(s)
	var roundtrip string
	return err == nil && xml.Unmarshal(b, &roundtrip) == nil && roundtrip == s && strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || r >= 0xfdd0 && r <= 0xfdef || r&0xffff >= 0xfffe }) < 0
}

func TestLauncherArgumentRoundTrip(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			hegel.Test(t, func(ht *hegel.T) {
				text := hegel.Draw(ht, hegel.Text())
				m := manager(platform)
				cfg, root, exe := "/srv/config/"+text, "/srv/state/"+text, "/srv/bin/worker"
				b, err := m.render(cfg, root, exe)
				if !representable(text) {
					if err == nil {
						ht.Fatalf("accepted unrepresentable argv %q", text)
					}
					return
				}
				if err != nil {
					ht.Fatal(err)
				}
				var argv []string
				if platform == "darwin" {
					argv = decodePlist(ht, b)["ProgramArguments"].([]string)
				} else {
					_, argv, _ = decodeUnit(ht, b)
				}
				if !reflect.DeepEqual(argv, []string{exe, "--config", cfg, "--state", root, "up"}) {
					ht.Fatalf("argv roundtrip: %q", argv)
				}
			}, hegel.WithTestCases(1000))
		})
	}
}

func TestLauncherEnvironmentRoundTrip(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			hegel.Test(t, func(ht *hegel.T) {
				text := hegel.Draw(ht, hegel.Text())
				m := manager(platform)
				get := m.Getenv
				m.Getenv = func(k string) string {
					if k == "XDG_CACHE_HOME" {
						return "/srv/cache/" + text
					}
					return get(k)
				}
				b, err := m.render("/srv/config", "/srv/state", "/srv/bin/worker")
				if !representable(text) {
					if err == nil {
						ht.Fatalf("accepted unrepresentable environment %q", text)
					}
					return
				}
				if err != nil {
					ht.Fatal(err)
				}
				var value any
				if platform == "darwin" {
					value = decodePlist(ht, b)["EnvironmentVariables"].(map[string]any)["XDG_CACHE_HOME"]
				} else {
					_, _, env := decodeUnit(ht, b)
					value = env["XDG_CACHE_HOME"]
				}
				if value != "/srv/cache/"+text {
					ht.Fatalf("environment roundtrip: %q", value)
				}
			}, hegel.WithTestCases(1000))
		})
	}
}

func TestPlatformValidators(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			validator, args, extension := "plutil", []string{"-lint", "--"}, ".plist"
			if platform == "linux" {
				validator, args, extension = "systemd-analyze", []string{"verify", "--user"}, ".service"
			} else if runtime.GOOS != "darwin" {
				t.Skip("plutil is only required on darwin")
			}
			bin, err := exec.LookPath(validator)
			if err != nil {
				t.Skipf("%s unavailable", validator)
			}
			path := filepath.Join(t.TempDir(), "herdr-kata-fixture"+extension)
			b := rendered(t, manager(platform), "/srv/config", "/srv/state", "/usr/bin/true")
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bin, append(args, path)...)
			home := t.TempDir()
			cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home}
			for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
				cmd.Env = append(cmd.Env, key+"="+home)
			}
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatal(fmt.Errorf("validator: %w: %s", err, output))
			}
		})
	}
}

func TestExecutableRoundTrip(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			hegel.Test(t, func(ht *hegel.T) {
				text := hegel.Draw(ht, hegel.Text())
				exe := "/srv/bin/" + text + "/worker"
				b, err := manager(platform).render("/srv/config", "/srv/state", exe)
				if !representable(text) || platform == "linux" && strings.ContainsAny(text, "'\"\\") {
					if err == nil {
						ht.Fatalf("accepted unrepresentable executable %q", exe)
					}
					return
				}
				if err != nil {
					ht.Fatal(err)
				}
				var argv []string
				if platform == "darwin" {
					argv = decodePlist(ht, b)["ProgramArguments"].([]string)
				} else {
					_, argv, _ = decodeUnit(ht, b)
				}
				if argv[0] != exe {
					ht.Fatalf("executable changed: %q", argv)
				}
			}, hegel.WithTestCases(1000))
		})
	}
}
