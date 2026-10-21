package runner

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func resultFixture() *Result {
	return &Result{Version: 1, Run: "run-1", Step: "main", Attempt: 1, Status: "succeeded", Summary: "Updated the parser and verified the focused tests pass.", Evidence: []string{"test:go test ./parser"}, CloseReason: "done"}
}

func TestWriteResultRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "result.json")
	want := resultFixture()
	if err := WriteResult(state.OSFS{}, p, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadResult(state.OSFS{}, p, "run-1", "main", 1)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v", got, err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["run"] != "run-1" || wire["close_reason"] != "done" || wire["version"] != float64(1) {
		t.Fatalf("wire: %s", b)
	}
}

func TestReadResultRejectsTable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		edit    func(*Result)
		raw     string
		special string
	}{
		{name: "missing", special: "missing"},
		{name: "partial", raw: `{"version":1`},
		{name: "null", raw: `null`},
		{name: "trailing", raw: `{} {}`},
		{name: "unknown-field", raw: `{"bogus":true}`},
		{name: "oversized", raw: strings.Repeat(" ", 65537)},
		{name: "symlink", special: "symlink"},
		{name: "directory", special: "directory"},
		{name: "version", edit: func(r *Result) { r.Version = 2 }},
		{name: "wrong-run", edit: func(r *Result) { r.Run = "other" }},
		{name: "wrong-step", edit: func(r *Result) { r.Step = "other" }},
		{name: "stale-attempt", edit: func(r *Result) { r.Attempt = 2 }},
		{name: "zero-attempt", edit: func(r *Result) { r.Attempt = 0 }},
		{name: "status", edit: func(r *Result) { r.Status = "unknown" }},
		{name: "reason", edit: func(r *Result) { r.CloseReason = "wontfix" }},
		{name: "evidence-type", edit: func(r *Result) { r.Evidence = []string{"url:https://example.com"} }},
		{name: "evidence-missing-colon", edit: func(r *Result) { r.Evidence = []string{"test"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "result.json")
			r := resultFixture()
			if tc.edit != nil {
				tc.edit(r)
			}
			b, _ := json.Marshal(r)
			if tc.raw != "" {
				b = []byte(tc.raw)
			}
			switch tc.special {
			case "missing":
			case "directory":
				if err := os.Mkdir(p, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.WriteFile(p+".real", b, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(p+".real", p); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(p, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ReadResult(state.OSFS{}, p, "run-1", "main", 1); err == nil {
				t.Fatal("accepted invalid result")
			} else if tc.special == "missing" && !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
		})
	}
}

func TestWriteResultRejectsBeforeReplace(t *testing.T) {
	p := filepath.Join(t.TempDir(), "result.json")
	if err := WriteResult(state.OSFS{}, p, resultFixture()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p)
	for _, tc := range []struct {
		name string
		edit func(*Result)
	}{
		{"nil", nil},
		{"oversized", func(r *Result) { r.Summary = strings.Repeat("x", 65536) }},
		{"invalid-status", func(r *Result) { r.Status = "done" }},
		{"invalid-utf8", func(r *Result) { r.Summary = "\xff" }},
		{"invalid-identity", func(r *Result) { r.Step = "../other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := resultFixture()
			if tc.edit == nil {
				r = nil
			} else {
				tc.edit(r)
			}
			if err := WriteResult(state.OSFS{}, p, r); err == nil {
				t.Fatal("accepted invalid result")
			}
			after, _ := os.ReadFile(p)
			if string(after) != string(before) {
				t.Fatal("replaced result on failure")
			}
		})
	}
}

func TestResultAllowsUnusableCloseEvidence(t *testing.T) {
	for _, status := range []string{"succeeded", "failed", "needs-human"} {
		r := resultFixture()
		r.Status = status
		r.Evidence = []string{"commit:not-a-sha", "test:"}
		r.CloseReason = ""
		p := filepath.Join(t.TempDir(), "result.json")
		if err := WriteResult(state.OSFS{}, p, r); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadResult(state.OSFS{}, p, r.Run, r.Step, r.Attempt); err != nil {
			t.Fatal(err)
		}
	}
}
