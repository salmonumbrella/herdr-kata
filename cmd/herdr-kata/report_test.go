package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func reportDir(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "steps", "main", "2", "result.json")
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_KATA_RUN_DIR", dir)
	t.Setenv("HERDR_KATA_RUN", "run-1")
	return dir, p
}

func TestReportCommand(t *testing.T) {
	_, p := reportDir(t)
	var out, stderr bytes.Buffer
	args := []string{"report", "--result", p, "--status", "succeeded", "--summary", "Implemented the requested change and verified tests pass.", "--evidence", "test:go test ./parser", "--evidence", "pr:https://example.com/pr/1", "--close-reason", "done"}
	if code := run(args, &out, &stderr); code != 0 {
		t.Fatalf("exit=%d: %s", code, &stderr)
	}
	r, err := runner.ReadResult(state.OSFS{}, p, "run-1", "main", 2)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "succeeded" || len(r.Evidence) != 2 || r.Evidence[1] != "pr:https://example.com/pr/1" || r.CloseReason != "done" {
		t.Fatalf("result=%+v", r)
	}
}

func TestReportRefusesUnsafePaths(t *testing.T) {
	for _, mode := range []string{"outside", "prefix-sibling", "relative", "traversal", "symlink-parent", "symlink-result", "run-dir-itself", "wrong-layout", "zero-attempt", "noncanonical-attempt", "missing-env", "missing-run"} {
		t.Run(mode, func(t *testing.T) {
			dir, p := reportDir(t)
			outside := filepath.Join(t.TempDir(), "result.json")
			if err := os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "outside":
				p = outside
			case "prefix-sibling":
				p = dir + "-other/result.json"
			case "relative":
				p = "steps/main/2/result.json"
			case "traversal":
				p = dir + "/../result.json"
			case "symlink-parent":
				link := filepath.Join(dir, "steps", "escape")
				if err := os.Symlink(filepath.Dir(outside), link); err != nil {
					t.Fatal(err)
				}
				p = filepath.Join(link, "2", "result.json")
			case "symlink-result":
				if err := os.Symlink(outside, p); err != nil {
					t.Fatal(err)
				}
			case "run-dir-itself":
				p = dir
			case "wrong-layout":
				p = filepath.Join(dir, "result.json")
			case "zero-attempt":
				p = filepath.Join(dir, "steps", "main", "0", "result.json")
			case "noncanonical-attempt":
				p = filepath.Join(dir, "steps", "main", "02", "result.json")
			case "missing-env":
				t.Setenv("HERDR_KATA_RUN_DIR", "")
			case "missing-run":
				t.Setenv("HERDR_KATA_RUN", "")
			}
			var out, stderr bytes.Buffer
			if code := run([]string{"report", "--result", p, "--status", "succeeded", "--summary", "summary"}, &out, &stderr); code == 0 {
				t.Fatal("accepted unsafe report")
			}
			b, err := os.ReadFile(outside)
			if err != nil || string(b) != "untouched" {
				t.Fatalf("outside changed: %q %v", b, err)
			}
		})
	}
}

func TestReportInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--status", "unknown"}, {"--status", "succeeded", "extra"},
		{"--status", "succeeded", "--evidence", "bogus:value"},
		{"--status", "succeeded", "--close-reason", "wontfix"},
		{"--status", "succeeded", "--unexpected"},
	} {
		_, p := reportDir(t)
		var out, stderr bytes.Buffer
		cmd := append([]string{"report", "--result", p, "--summary", "summary"}, args...)
		if code := run(cmd, &out, &stderr); code == 0 {
			t.Fatalf("accepted %v", args)
		}
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("wrote result on invalid flags: %v", err)
		}
	}
}

func TestReportAssertions(t *testing.T) {
	const uid = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	for _, tc := range []struct {
		assert, target string
		want           int
	}{
		{"confirm", "c:01arz3ndektsv4rrffq69g5fav", 0}, {"refute", uid, 0},
		{"confirm", "", 2}, {"", uid, 2}, {"reply", uid, 2}, {"confirm", "c:abcdef", 2}, {"confirm", "ZZARZ3NDEKTSV4RRFFQ69G5FAV", 2},
	} {
		t.Run(tc.assert+tc.target, func(t *testing.T) {
			_, p := reportDir(t)
			var out, stderr bytes.Buffer
			args := []string{"report", "--result", p, "--status", "failed", "--summary", "Evidence about the maker claim."}
			if tc.assert != "" {
				args = append(args, "--assert", tc.assert)
			}
			if tc.target != "" {
				args = append(args, "--target", tc.target)
			}
			if code := run(args, &out, &stderr); code != tc.want {
				t.Fatalf("exit=%d want %d: %s", code, tc.want, &stderr)
			}
			if tc.want == 0 {
				r, e := runner.ReadResult(state.OSFS{}, p, "run-1", "main", 2)
				if e != nil || r.Assert != tc.assert || r.Target != uid {
					t.Fatal(r, e)
				}
			} else if _, e := os.Stat(p); !os.IsNotExist(e) {
				t.Fatal("invalid assertion wrote report", e)
			}
		})
	}
}
