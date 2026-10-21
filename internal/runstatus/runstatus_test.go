package runstatus

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

// Kata's cron run statuses (docs/reference/cron.md in kenn-io/kata).
var kataRunStatuses = []string{"running", "succeeded", "failed", "cancelled", "unknown"}

func TestRunStatusWordsMatchKata(t *testing.T) {
	for _, word := range kataRunStatuses {
		s, ok := Lookup(word)
		if !ok || !s.Kata {
			t.Errorf("Kata status %q missing from the table or not marked as Kata's", word)
		}
	}
	for _, s := range Table {
		if s.Kata && !strings.Contains(" "+strings.Join(kataRunStatuses, " ")+" ", " "+s.Word+" ") {
			t.Errorf("%q is marked as Kata's word but Kata has no such run status", s.Word)
		}
	}
	// Settled runs keep Kata's outcome word.
	for _, outcome := range []string{"succeeded", "failed", "cancelled", "unknown"} {
		if got := Run(runner.Run{Phase: runner.Settled, Outcome: outcome}).Word; got != outcome {
			t.Errorf("settled %s shows %q", outcome, got)
		}
	}
	if got := Run(runner.Run{Phase: runner.Running}).Word; got != "running" {
		t.Errorf("running shows %q", got)
	}
	// A run handed to a person uses Kata's needs-human attention value.
	if got := Run(runner.Run{Phase: runner.HandedOff, Outcome: "unknown"}).Word; got != "needs-human" {
		t.Errorf("handed-off run shows %q", got)
	}
	for _, retired := range []string{"done", "handoff", "cancel", "pending"} {
		if _, ok := Lookup(retired); ok {
			t.Errorf("retired word %q is still in the table", retired)
		}
	}
	// The CLI keeps Kata's word unless the local journal knows more.
	if got := Remote("running", &runner.Run{Phase: runner.Held}).Word; got != "parked" {
		t.Errorf("remote parked shows %q", got)
	}
	if got := Remote("unknown", &runner.Run{Phase: runner.HandedOff}).Word; got != "needs-human" {
		t.Errorf("remote handed off shows %q", got)
	}
	if got := Remote("succeeded", &runner.Run{Phase: runner.Settled, Outcome: "succeeded"}).Word; got != "succeeded" {
		t.Errorf("remote succeeded shows %q", got)
	}
}

func TestStepStatusFollowsRunPhase(t *testing.T) {
	sent := runner.StepState{Key: "a", Submission: runner.Accepted}
	for _, tc := range []struct {
		phase runner.Phase
		want  string
	}{
		{runner.Running, "running"}, {runner.Admitted, "running"}, {runner.Blocked, "blocked"},
		{runner.Held, "parked"}, {runner.HandedOff, "needs-human"}, {runner.Settled, "unknown"},
	} {
		if got := Step(runner.Run{Phase: tc.phase, ActiveStep: "a"}, sent).Word; got != tc.want {
			t.Errorf("%s: step shows %q, want %q", tc.phase, got, tc.want)
		}
	}
	if got := Step(runner.Run{Phase: runner.Running, ActiveStep: "a"}, runner.StepState{Key: "b"}).Word; got != "not started" {
		t.Errorf("unsent step shows %q", got)
	}
	if got := Step(runner.Run{}, runner.StepState{Status: "needs-human"}).Word; got != "needs-human" {
		t.Errorf("needs-human step shows %q", got)
	}
	if got := Step(runner.Run{}, runner.StepState{Status: "skipped"}).Word; got != "skipped" {
		t.Errorf("skipped step shows %q", got)
	}
}

func TestStatusRolesFollowKata(t *testing.T) {
	for word, want := range map[string]Role{
		"succeeded": Finished, "cancelled": Finished, "skipped": Finished,
		"running": Active, "queued": Active,
		"parked": Attention, "blocked": Attention, "needs-human": Attention, "unknown": Attention,
		"failed": Failure,
	} {
		if got := Of(word).Role; got != want {
			t.Errorf("%s role %s, want %s", word, got, want)
		}
	}
	if s := Of("future"); s.Word != "future" || s.Role != Attention {
		t.Errorf("unlisted word: %+v", s)
	}
}

func TestStatusLegendDocumented(t *testing.T) {
	for _, doc := range []string{"../../docs/reference/tui.md", "../../docs/reference/cli.md"} {
		raw, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "statuses.md") {
			t.Errorf("%s does not link the status legend", doc)
		}
	}
	for _, doc := range []string{"../../docs/reference/statuses.md"} {
		raw, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		start := strings.Index(text, "<!-- status-legend -->")
		end := strings.Index(text, "<!-- /status-legend -->")
		if start < 0 || end < start {
			t.Fatalf("%s: no status legend markers", doc)
		}
		legend := text[start:end]
		for _, s := range Table {
			row := regexp.MustCompile("(?m)^\\| `" + regexp.QuoteMeta(s.Word) + "` \\|[^\\n]*\\| " + string(s.Role) + " \\|")
			if !row.MatchString(legend) {
				t.Errorf("%s: legend has no row for `%s` with color role %s", doc, s.Word, s.Role)
			}
		}
		rows := regexp.MustCompile("(?m)^\\| `([^`]+)` \\|").FindAllStringSubmatch(legend, -1)
		if len(rows) != len(Table) {
			t.Errorf("%s: legend has %d rows, table has %d", doc, len(rows), len(Table))
		}
	}
}
