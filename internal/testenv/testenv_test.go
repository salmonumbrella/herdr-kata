package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Selected suites must fail rather than silently skip unusable prerequisites.
func TestRequiredBinary(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	falseBin, err := exec.LookPath("false")
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(root, trueBin)
	if err != nil {
		t.Fatal(err)
	}
	nonExecutable := filepath.Join(root, "not-executable")
	if err := os.WriteFile(nonExecutable, []byte("not a binary"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, path   string
		unset, valid bool
	}{
		{name: "unset", unset: true},
		{name: "empty"},
		{name: "missing", path: filepath.Join(root, "missing")},
		{name: "non-executable", path: nonExecutable},
		{name: "directory", path: root},
		{name: "relative", path: relative},
		{name: "unusable", path: falseBin},
		{name: "usable", path: trueBin, valid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestBinaryHelper$", "-test.v")
			cmd.Dir = root
			for _, pair := range os.Environ() {
				if !strings.HasPrefix(pair, "HK_TEST_BINARY=") && !strings.HasPrefix(pair, "HK_TEST_GUARD=") {
					cmd.Env = append(cmd.Env, pair)
				}
			}
			cmd.Env = append(cmd.Env, "HK_TEST_GUARD=1")
			if !tt.unset {
				cmd.Env = append(cmd.Env, "HK_TEST_BINARY="+tt.path)
			}
			out, err := cmd.CombinedOutput()
			if tt.valid {
				if err != nil || !strings.Contains(string(out), "--- PASS: TestBinaryHelper") {
					t.Fatalf("usable binary rejected: %v\n%s", err, out)
				}
			} else if err == nil || !strings.Contains(string(out), "--- FAIL: TestBinaryHelper") || strings.Contains(string(out), "--- SKIP:") {
				t.Fatalf("invalid prerequisite must fail: %v\n%s", err, out)
			}
		})
	}
}
func TestBinaryHelper(t *testing.T) {
	if os.Getenv("HK_TEST_GUARD") == "1" {
		RequiredBinary(t, "HK_TEST_BINARY")
	}
}
