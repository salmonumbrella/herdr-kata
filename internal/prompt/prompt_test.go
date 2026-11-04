package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"hegel.dev/go/hegel"
)

func runFixture() journal.Run {
	return journal.Run{RunUID: "01J00000000000000000000001", Occurrence: "2026-11-02T09:00:00Z", Plan: defs.Plan{Name: "example-job", WorkflowName: "example-workflow", About: "About {{input}}", Input: "input containing {{previous}}"}}
}
func build(r journal.Run, s defs.Step, previous string) (string, error) {
	return Build(r, s, 1, "abcd", "Example issue", previous, "/srv/run/result.json", "/usr/local/bin/herdr-kata", "/usr/local/bin/kata --project example-project --daemon hub --as worker")
}
func TestPromptPlaceholdersOnce(t *testing.T) {
	r := runFixture()
	got, err := build(r, defs.Step{Key: "main", Text: "Task {{input}} / {{previous}}"}, "prior {{input}}")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Task input containing {{previous}} / prior {{input}}", "About input containing {{previous}}", "example-job", "01J00000000000000000000001", "Example issue", "example-workflow"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
}
func TestUnknownPlaceholderRefused(t *testing.T) {
	if _, err := build(runFixture(), defs.Step{Text: "{{unknown}}"}, ""); err == nil {
		t.Fatal("unknown placeholder accepted")
	}
}
func TestPromptLimit(t *testing.T) {
	if _, err := build(runFixture(), defs.Step{Text: strings.Repeat("a", 256*1024)}, ""); err == nil || !strings.Contains(err.Error(), "invalid_prompt") {
		t.Fatal(err)
	}
}
func TestPromptCarriesKataPrefix(t *testing.T) {
	got, err := build(runFixture(), defs.Step{Key: "main", Text: "Task"}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/usr/local/bin/kata --project example-project --daemon hub --as worker", "--done --message", "--evidence", "if not finished, do not close", "report --result"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q", want)
		}
	}
}
func TestResultRejectsSymlinkStaleOversize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.json")
	valid := Result{RunUID: "01J00000000000000000000001", Step: "main", Attempt: 1, Status: "succeeded", Summary: "Completed"}
	if err := state.WriteJSON(path, valid); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadResult(path, valid.RunUID, "main", 1); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadResult(link, valid.RunUID, "main", 1); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := ReadResult(path, valid.RunUID, "main", 2); err == nil {
		t.Fatal("stale attempt accepted")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 64*1024+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadResult(path, valid.RunUID, "main", 1); err == nil {
		t.Fatal("oversize accepted")
	}
	for _, tc := range []struct{ note, want string }{
		{"two\nlines", "two lines"},
		{strings.Repeat("é", 141), strings.Repeat("é", 134) + " [truncated]"},
		{"bad\rline", "bad line"},
	} {
		valid.Note = tc.note
		if err := state.WriteJSON(path, valid); err != nil {
			t.Fatal(err)
		}
		got, err := ReadResult(path, valid.RunUID, "main", 1)
		if err != nil || got.Note != tc.want || got.Status != "succeeded" || got.Summary != "Completed" {
			t.Fatalf("bad note changed outcome: %+v %v", got, err)
		}
		if err := WriteResult(filepath.Join(dir, "strict.json"), valid); err == nil {
			t.Fatalf("strict writer accepted note %q", tc.note)
		}
	}
}
func TestResultRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	hegel.Test(t, func(ht *hegel.T) {
		summary := hegel.Draw(ht, hegel.Text())
		r := Result{RunUID: "01J00000000000000000000001", Step: "main", Attempt: 1, Status: "succeeded", Summary: summary}
		if err := state.WriteJSON(path, r); err != nil {
			ht.Fatal(err)
		}
		got, err := ReadResult(path, r.RunUID, r.Step, r.Attempt)
		if err != nil || got.Summary != summary {
			ht.Fatalf("result summary changed: %v", err)
		}
	})
}
