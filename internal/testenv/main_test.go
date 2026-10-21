package testenv

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) { Main(m) }

var helperVars = []string{"HOME", HomeVar, "KATA_SERVER", "HERDR_SOCKET_PATH", "HERDR_PLUGIN_STATE_DIR", "XDG_CONFIG_HOME", "CODEX_HOME", "HERDR_KATA_TEST_KATA_BIN"}

func TestIsolationHelper(t *testing.T) {
	if os.Getenv("HK_TEST_ISOLATION") != "1" {
		return
	}
	got := map[string]*string{}
	for _, name := range helperVars {
		if value, ok := os.LookupEnv(name); ok {
			got[name] = &value
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(got); err != nil {
		t.Fatal(err)
	}
}

func runHelper(t *testing.T, env ...string) (map[string]*string, string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestIsolationHelper$")
	for _, pair := range os.Environ() {
		if !strings.HasPrefix(pair, HomeVar+"=") {
			cmd.Env = append(cmd.Env, pair)
		}
	}
	cmd.Env = append(cmd.Env, append([]string{"HK_TEST_ISOLATION=1"}, env...)...)
	out, err := cmd.Output()
	got := map[string]*string{}
	if line, _, _ := strings.Cut(string(out), "\n"); err == nil {
		if e := json.Unmarshal([]byte(line), &got); e != nil {
			t.Fatalf("helper output %q: %v", out, e)
		}
	}
	var stderr string
	if exit, ok := err.(*exec.ExitError); ok {
		stderr = string(exit.Stderr)
	}
	return got, stderr, err
}

// A fresh test binary drops the owner's selectors and gets its own HOME.
func TestMainIsolatesFreshTestBinary(t *testing.T) {
	owner := t.TempDir()
	got, stderr, err := runHelper(t, "HOME="+owner, "KATA_SERVER=https://hub.example", "HERDR_SOCKET_PATH=/srv/herdr.sock", "XDG_CONFIG_HOME="+owner, "CODEX_HOME="+owner, "HERDR_KATA_TEST_KATA_BIN=/srv/bin/kata")
	if err != nil {
		t.Fatalf("helper failed: %v\n%s", err, stderr)
	}
	for _, name := range []string{"KATA_SERVER", "HERDR_SOCKET_PATH", "XDG_CONFIG_HOME", "CODEX_HOME"} {
		if got[name] != nil {
			t.Errorf("%s inherited as %q", name, *got[name])
		}
	}
	if v := got["HERDR_KATA_TEST_KATA_BIN"]; v == nil || *v != "/srv/bin/kata" {
		t.Errorf("suite prerequisite dropped: %v", v)
	}
	home, root := got["HOME"], got[HomeVar]
	if home == nil || root == nil || *home != *root || *home == owner || !filepath.IsAbs(*home) {
		t.Fatalf("HOME=%v %s=%v, want one new absolute test home", home, HomeVar, root)
	}
	if _, err := os.Stat(*root); !os.IsNotExist(err) {
		t.Fatalf("test home %s left behind: %v", *root, err)
	}
}

// A helper process a test re-executes keeps the environment the test built.
func TestMainKeepsHelperEnvironment(t *testing.T) {
	root, dir := t.TempDir(), t.TempDir()
	got, stderr, err := runHelper(t, HomeVar+"="+root, "HOME="+dir, "HERDR_PLUGIN_STATE_DIR="+dir, "KATA_SERVER=https://hub.example")
	if err != nil {
		t.Fatalf("helper failed: %v\n%s", err, stderr)
	}
	for name, want := range map[string]string{"HOME": dir, "HERDR_PLUGIN_STATE_DIR": dir, "KATA_SERVER": "https://hub.example", HomeVar: root} {
		if v := got[name]; v == nil || *v != want {
			t.Errorf("%s=%v, want %q", name, v, want)
		}
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("helper removed its parent's test home: %v", err)
	}
}

// The guard refuses to run tests that would reach the owner's installation.
func TestMainRefusesOwnerDirectories(t *testing.T) {
	protected := Protected()
	if len(protected) == 0 {
		t.Fatal("no account home to protect")
	}
	for name, value := range map[string]string{
		"HERDR_PLUGIN_CONFIG_DIR": filepath.Join(protected[0], "plugins", "config", "kata"),
		"HERDR_PLUGIN_STATE_DIR":  filepath.Join(protected[1], "plugins", "kata"),
		"KATA_HOME":               protected[2],
		"HOME":                    filepath.Dir(protected[2]),
	} {
		t.Run(name, func(t *testing.T) {
			_, stderr, err := runHelper(t, HomeVar+"="+t.TempDir(), name+"="+value)
			if err == nil || !strings.Contains(stderr, name) || !strings.Contains(stderr, "reaches the owner's") {
				t.Fatalf("%s=%s ran: %v\n%s", name, value, err, stderr)
			}
		})
	}
}

// Every test binary, including tagged suites, must start through Main.
func TestEveryTestPackageUsesMain(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	packages := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" || d.Name() == "docs") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		dir := filepath.Dir(path)
		if _, seen := packages[dir]; !seen {
			packages[dir] = false
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		if callsMain(f) {
			if hasBuildConstraint(f) {
				t.Errorf("%s: TestMain must not carry a build constraint", path)
			}
			packages[dir] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) < 2 {
		t.Fatalf("found %d test packages under %s", len(packages), root)
	}
	for dir, ok := range packages {
		if !ok {
			t.Errorf("%s: test package has no TestMain calling testenv.Main", dir)
		}
	}
}

func callsMain(f *ast.File) bool {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "TestMain" || fn.Recv != nil || fn.Body == nil {
			continue
		}
		found := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				if x, ok := fun.X.(*ast.Ident); ok && x.Name == "testenv" && fun.Sel.Name == "Main" {
					found = true
				}
			case *ast.Ident:
				if f.Name.Name == "testenv" && fun.Name == "Main" {
					found = true
				}
			}
			return !found
		})
		return found
	}
	return false
}

func hasBuildConstraint(f *ast.File) bool {
	for _, group := range f.Comments {
		if group.Pos() > f.Package {
			break
		}
		for _, c := range group.List {
			if strings.HasPrefix(c.Text, "//go:build") || strings.HasPrefix(c.Text, "// +build") {
				return true
			}
		}
	}
	return false
}
