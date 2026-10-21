package testenv

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// HomeVar names the temporary home Main gives a test binary. Helper processes
// that a test re-executes inherit it and keep the environment their parent built.
const HomeVar = "HERDR_KATA_TEST_HOME"

// Main runs every test package from a temporary HOME with no inherited Herdr,
// Kata, agent or XDG selectors, so a test resolves the defaults a clean CI host
// would and never reaches the owner's plugin config, state, Kata home or Herdr
// socket. It refuses to run when that isolation does not hold.
func Main(m *testing.M) {
	root, created, err := isolate()
	if err == nil {
		err = checkIsolated()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "testenv:", err)
		os.Exit(2)
	}
	code := m.Run()
	if created {
		_ = os.RemoveAll(root)
	}
	os.Exit(code)
}

// Variables a test must set itself; inherited values point at the owner's
// installation. HERDR_KATA_TEST_* names suite prerequisites and stays.
var isolatedPrefixes = []string{"HERDR_", "KATA_", "XDG_", "AGENTSVIEW_"}
var isolatedNames = []string{"AGENT_VIEWER_DATA_DIR", "CODEX_HOME", "CLAUDE_CONFIG_DIR"}

func isolate() (string, bool, error) {
	if root := os.Getenv(HomeVar); root != "" {
		return root, false, nil
	}
	if err := pinGoEnv(); err != nil {
		return "", false, err
	}
	for _, pair := range os.Environ() {
		name, _, _ := strings.Cut(pair, "=")
		if isolated(name) {
			if err := os.Unsetenv(name); err != nil {
				return "", false, err
			}
		}
	}
	dir, err := os.MkdirTemp("", "herdr-kata-test-home-")
	if err != nil {
		return "", false, err
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", false, err
	}
	for name, value := range map[string]string{"HOME": root, HomeVar: root} {
		if err := os.Setenv(name, value); err != nil {
			return "", false, err
		}
	}
	return root, true, nil
}

func isolated(name string) bool {
	if strings.HasPrefix(name, "HERDR_KATA_TEST_") {
		return false
	}
	for _, prefix := range isolatedPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return slices.Contains(isolatedNames, name)
}

// The Go toolchain derives its cache and module paths from HOME and XDG_*.
// Keep the owner's, so a test that runs go neither rebuilds nor downloads
// everything and the read-only module cache never lands in the test home.
func pinGoEnv() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		gopath = filepath.Join(home, "go")
	}
	defaults := map[string]string{"GOPATH": gopath, "GOMODCACHE": filepath.Join(filepath.SplitList(gopath)[0], "pkg", "mod")}
	if dir, err := os.UserCacheDir(); err == nil {
		defaults["GOCACHE"] = filepath.Join(dir, "go-build")
	}
	if dir, err := os.UserConfigDir(); err == nil {
		defaults["GOENV"] = filepath.Join(dir, "go", "env")
	}
	for name, value := range defaults {
		if os.Getenv(name) != "" {
			continue
		}
		if err := os.Setenv(name, value); err != nil {
			return err
		}
	}
	return nil
}

// Protected lists the owner's Herdr and Kata directories, from the account
// home rather than HOME, which a test changes.
func Protected() []string {
	account, err := user.Current()
	if err != nil || !filepath.IsAbs(account.HomeDir) {
		return nil
	}
	home := account.HomeDir
	return []string{filepath.Join(home, ".config", "herdr"), filepath.Join(home, ".local", "state", "herdr"), filepath.Join(home, ".kata")}
}

// Reaches reports whether path is inside a protected directory or contains one.
func Reaches(path string) bool {
	for _, dir := range Protected() {
		if within(path, dir) || within(dir, path) {
			return true
		}
	}
	return false
}

func checkIsolated() error {
	for _, name := range []string{"HOME", "HERDR_PLUGIN_CONFIG_DIR", "HERDR_PLUGIN_STATE_DIR", "HERDR_SOCKET_PATH", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "KATA_HOME"} {
		if value := os.Getenv(name); value != "" && Reaches(value) {
			return fmt.Errorf("%s %q reaches the owner's Herdr or Kata directories", name, value)
		}
	}
	return nil
}

func within(path, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
