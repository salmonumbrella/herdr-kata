package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/katacli"
)

// Runs the real Herdr TOML parser and linked-plugin installation, never a regex
// or shell-content assertion. Both servers, all panes and the plugin are owned
// temporary artifacts. No provider-backed interactive agent is launched.
func TestRealTask10HerdrPackagingSmoke(t *testing.T) {
	herdr := os.Getenv("HERDR_NATIVE_TEST_BINARY")
	if herdr == "" {
		t.Skip("requires explicit real Herdr binary and isolated native Kata binary")
	}
	c, _ := realNativeProduct(t)
	herdr, err := filepath.Abs(herdr)
	if err != nil {
		t.Fatal(err)
	}
	// Unix-domain socket paths include the config root and session name; keep
	// this owned root short instead of nesting beneath Go's long test name.
	tempRoot := ""
	if runtime.GOOS == "darwin" {
		// macOS's default TMPDIR alone can consume most of sun_path.
		tempRoot = "/private/tmp"
	}
	root, err := os.MkdirTemp(tempRoot, "hk10-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	pluginDir := filepath.Join(root, "plugin")
	if err := os.MkdirAll(filepath.Join(pluginDir, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../herdr-plugin.toml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "herdr-plugin.toml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(pluginDir, "bin", "herdr-kata")
	if os.PathSeparator == '\\' {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, ".")
	if raw, err := build.CombinedOutput(); err != nil {
		t.Fatalf("package build: %v %s", err, raw)
	}
	// The unique config root already isolates the named session.
	session := "smoke"
	// Explicit OS/toolchain allowlist; all credentials, targets, hosted ports,
	// proxies, live Herdr socket/caller context and user config are absent.
	env := []string{}
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "TMPDIR", "TEMP", "SYSTEMROOT", "SystemRoot", "WINDIR", "COMSPEC"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	env = append(env, "HOME="+root, "USERPROFILE="+root, "XDG_CONFIG_HOME="+filepath.Join(root, "config"), "HERDR_CONFIG_PATH="+filepath.Join(root, "config", "herdr", "config.toml"), "HERDR_SESSION="+session, "HERDR_BIN_PATH="+herdr, "HERDR_KATA_HOME="+stateDir(), "GOMAXPROCS=2")
	ctx, cancel := context.WithCancel(context.Background())
	log, err := os.Create(filepath.Join(root, "herdr.log"))
	if err != nil {
		t.Fatal(err)
	}
	server := exec.CommandContext(ctx, herdr, "--session", session, "server")
	server.Env = env
	server.Stdout, server.Stderr = log, log
	if err := server.Start(); err != nil {
		log.Close()
		cancel()
		t.Fatal(err)
	}
	call := func(args ...string) ([]byte, error) {
		callContext, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		cmd := exec.CommandContext(callContext, herdr, append([]string{"--session", session}, args...)...)
		cmd.Env = env
		return cmd.CombinedOutput()
	}
	linked := false
	t.Cleanup(func() {
		// Disabled linking starts no plugin daemon/sentinel. Close only this owned
		// named server and its panes; the native fixture helper owns its daemon.
		if linked {
			if raw, err := call("plugin", "unlink", "salmonumbrella.herdr-kata"); err != nil {
				t.Errorf("owned plugin cleanup: %v %s", err, raw)
			}
		}
		raw, err := call("session", "stop", session, "--json")
		if err != nil {
			t.Errorf("owned session cleanup: %v %s", err, raw)
		}
		cancel()
		server.Wait()
		log.Close()
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := call("status", "server"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			raw, _ := os.ReadFile(log.Name())
			t.Fatalf("isolated Herdr unavailable: %s", raw)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if raw, err := call("plugin", "link", pluginDir, "--disabled"); err != nil {
		t.Fatalf("actual manifest install: %v %s", err, raw)
	}
	linked = true
	listing, err := call("plugin", "list", "--json")
	if err != nil {
		t.Fatalf("actual install list: %v %s", err, listing)
	}
	if !json.Valid(listing) || !strings.Contains(string(listing), "salmonumbrella.herdr-kata") || !strings.Contains(string(listing), "0.1.0") {
		t.Fatalf("wrong installed fork identity: %s", listing)
	}
	t.Logf("installed parsed manifest: %s", listing)
	// The linked binary uses an ordinary native definition and real Herdr
	// workspace lifecycle; the command step makes a concrete local shell effect.
	artifact := filepath.Join(c.Target.Workspace, "smoke-result.txt")
	definition, _ := json.Marshal(map[string]any{"version": 1, "steps": []any{map[string]any{"key": "inspect", "kind": "command", "command": "printf task10 > smoke-result.txt"}}})
	draft, err := katacli.NewDraft("workflow", "", "Packaging smoke", definition, "")
	if err != nil {
		t.Fatal(err)
	}
	def, err := c.Save(t.Context(), draft)
	if err != nil {
		realNativeFailure(t, err)
	}
	cmd := exec.CommandContext(t.Context(), binary, "workflow", "run", def.UID)
	cmd.Env = env
	cmd.Dir = c.Target.Workspace
	if runtime.GOOS != "windows" {
		// Shells report the canonical cwd even when the mapped checkout uses a
		// symlink (including macOS /tmp). Exercise that real execution boundary.
		checkout := filepath.Join(root, "checkout")
		if err := os.Symlink(c.Target.Workspace, checkout); err != nil {
			t.Fatal(err)
		}
		cmd.Args = append(cmd.Args, "--cwd", checkout)
	}
	result, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("real native/Herdr shell workflow: %v %s", err, result)
	}
	data, err := os.ReadFile(artifact)
	if err != nil || string(data) != "task10" {
		t.Fatalf("shell effect absent: %q %v output=%s", data, err, result)
	}
	t.Logf("real native/Herdr shell workflow: %s", result)
	if raw, err := call("plugin", "unlink", "salmonumbrella.herdr-kata"); err != nil {
		t.Fatalf("actual uninstall: %v %s", err, raw)
	}
	linked = false
	listing, err = call("plugin", "list", "--json")
	if err != nil || strings.Contains(string(listing), "salmonumbrella.herdr-kata") {
		t.Fatalf("installed plugin retained after unlink: %s %v", listing, err)
	}
	if _, err := os.Stat(filepath.Join(stateDir(), "native.json")); err != nil {
		t.Fatalf("unlink removed local mapping: %v", err)
	}
}
