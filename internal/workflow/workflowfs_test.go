package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

// Every caller that reads or writes a workflow derives the directory from the state
// directory through Dir, so the two must agree on one location; a second
// spelling anywhere would make `workflow new` write where `workflow list` never looks.
func TestWorkflowsLiveInOnePlaceUnderTheStateDirectory(t *testing.T) {
	state := t.TempDir()
	got := Dir(state)
	if want := filepath.Join(state, "workflows"); got != want {
		t.Errorf("Dir(%q) = %q, want %q", state, got, want)
	}
}

// A saved workflow has to come back through Load unchanged, since that is the whole
// round trip `workflow new` then `workflow run` depends on.
func TestASavedWorkflowIsReadBackAsItWasWritten(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "workflows")
	f := Workflow{
		ID:    "release-check",
		About: "check a release",
		Input: "a version",
		Steps: []store.Step{
			{ID: "assess", Agent: "Look at {{input}}."},
			{ID: "verify", Run: "go test ./..."},
		},
	}
	if err := Save(dir, f); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir, "release-check")
	if err != nil {
		t.Fatal(err)
	}
	if got.About != f.About || got.Input != f.Input {
		t.Errorf("about/input did not survive the round trip: %+v", got)
	}
	if len(got.Steps) != 2 || got.Steps[0].Agent != f.Steps[0].Agent || got.Steps[1].Run != f.Steps[1].Run {
		t.Errorf("steps did not survive the round trip: %+v", got.Steps)
	}
	if got.Path == "" {
		t.Error("a loaded workflow should know the file it came from")
	}
}

// Save creates the directory it needs, so a first `workflow new` on a fresh machine
// works rather than reporting a missing path the user never made.
func TestSaveCreatesTheWorkflowDirectoryOwnerOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "workflows")
	f := Workflow{ID: "triage", Steps: []store.Step{{ID: "one", Run: "true"}}}
	if err := Save(dir, f); err != nil {
		t.Fatal(err)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := di.Mode().Perm(); got != statefs.Dir {
		t.Errorf("workflow directory mode = %o, want %o", got, statefs.Dir)
	}
	fi, err := os.Stat(filepath.Join(dir, "triage"+Ext))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != statefs.File {
		t.Errorf("workflow file mode = %o, want %o", got, statefs.File)
	}
}

// A workflow id is a filename, so an id the loader would refuse must never reach
// the disk: writing one would produce a file that can be listed and never read.
func TestSaveRefusesAnIdThatIsNotOne(t *testing.T) {
	for _, id := range []string{"", "Triage", "two words", "trailing-", "a--b", "../escape"} {
		t.Run(id, func(t *testing.T) {
			dir := t.TempDir()
			if err := Save(dir, Workflow{ID: id, Steps: []store.Step{{ID: "one", Run: "true"}}}); err == nil {
				t.Fatalf("saving a workflow named %q was allowed", id)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("a refused save still wrote %d file(s)", len(entries))
			}
		})
	}
}

// Removing a workflow takes the file with it, so the id is free again and the workflow
// stops appearing in a listing.
func TestRemoveDeletesTheWorkflowFile(t *testing.T) {
	dir := t.TempDir()
	f := Workflow{ID: "triage", Steps: []store.Step{{ID: "one", Run: "true"}}}
	if err := Save(dir, f); err != nil {
		t.Fatal(err)
	}
	if err := Remove(dir, "triage"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "triage"+Ext)); !os.IsNotExist(err) {
		t.Errorf("workflow file still present after Remove: %v", err)
	}
	if _, err := Load(dir, "triage"); err == nil {
		t.Error("a removed workflow can still be loaded")
	}
	// The id is free again.
	if err := Save(dir, f); err != nil {
		t.Errorf("saving after a remove was refused: %v", err)
	}
}

// Removing something that is not there is reported, not swallowed: a silent
// success tells the caller a workflow was deleted when the one they meant, probably
// misspelled, is still on disk and still scheduled.
func TestRemovingAWorkflowThatIsNotThereSaysSo(t *testing.T) {
	dir := t.TempDir()
	err := Remove(dir, "ghost")
	if err == nil {
		t.Fatal("removing a missing workflow reported success")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should name the workflow: %v", err)
	}
}

// Remove validates the id before touching the filesystem, for the same reason
// Save does: an id that is not one cannot name a workflow file.
func TestRemoveRefusesAnIdThatIsNotOne(t *testing.T) {
	if err := Remove(t.TempDir(), "../etc"); err == nil {
		t.Fatal("removing by a bogus id was allowed")
	}
}

// NeedsInput drives whether a run is asked for input, so it must report on what
// the prompts actually reference rather than on what the file declares.
func TestNeedsInputReportsWhatThePromptsReference(t *testing.T) {
	cases := []struct {
		name string
		f    Workflow
		want bool
	}{
		{
			name: "an agent step that uses the input",
			f:    Workflow{ID: "a", Input: "a report", Steps: []store.Step{{ID: "one", Agent: "Assess {{input}}."}}},
			want: true,
		},
		{
			name: "a later step that uses the input",
			f: Workflow{ID: "b", Input: "a report", Steps: []store.Step{
				{ID: "one", Agent: "Start."},
				{ID: "two", Agent: "Now do {{input}}."},
			}},
			want: true,
		},
		{
			name: "declared but never referenced",
			f:    Workflow{ID: "c", Input: "a report", Steps: []store.Step{{ID: "one", Agent: "Assess whatever."}}},
			want: false,
		},
		{
			name: "only the previous result is referenced",
			f: Workflow{ID: "d", Steps: []store.Step{
				{ID: "one", Agent: "Start."},
				{ID: "two", Agent: "{{previous}} — continue."},
			}},
			want: false,
		},
		{
			name: "a run step cannot be seen into",
			f:    Workflow{ID: "e", Input: "a report", Steps: []store.Step{{ID: "one", Run: "echo $HERDR_KATA_INPUT"}}},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedsInput(tc.f); got != tc.want {
				t.Errorf("NeedsInput() = %v, want %v", got, tc.want)
			}
		})
	}
}
