package tui

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

func TestJobsWidths(t *testing.T) {
	// With no rows, fixed columns are as wide as their headers and job takes
	// the rest; a second project adds no column.
	for _, tc := range []struct {
		width int
		multi bool
		want  []int
	}{
		{120, false, []int{1, 6, 86, 5, 8, 7}},
		{80, false, []int{1, 6, 46, 5, 8, 7}},
		{80, true, []int{1, 6, 46, 5, 8, 7}},
	} {
		m := newModel(Options{Root: t.TempDir()})
		m.width = tc.width
		if tc.multi {
			m.opts.Projects = map[string]*Lane{"first": {}, "second": {}}
		}
		if got := m.jobsWidths(); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("width=%d multi=%v: %v", tc.width, tc.multi, got)
		}
		if got := m.jobsTableWidth(); got != tc.width-2 {
			t.Fatalf("table %d", got)
		}
	}
	// Workflow and next join only while job keeps 30 cells.
	for _, multi := range []bool{false, true} {
		seen := map[int]bool{}
		for width := 100; width <= 220; width++ {
			m := newModel(Options{Root: t.TempDir()})
			m.width = width
			m.favorites[scopedRowKey("p", "job")] = true
			m.data.Jobs = []defs.JobEntry{indexJob("p", "job", "Example job")}
			m.rebuildJobIndex()
			m.rebuild()
			if multi {
				m.opts.Projects = map[string]*Lane{"first": {}, "second": {}}
			}
			cols, widths := m.jobsColumns(), m.jobsWidths()
			extras := len(cols) - 7
			seen[extras] = true
			for i, c := range cols {
				if c.Title == "job" && extras > 0 && widths[i] < 30 {
					t.Fatalf("width %d: job keeps %d cells beside %d extra columns", width, widths[i], extras)
				}
			}
			if m.jobsInspectorWidth() != 0 {
				t.Fatalf("width %d: inspector %d", width, m.jobsInspectorWidth())
			}
		}
		if !seen[0] || !seen[1] || !seen[2] {
			t.Fatalf("multi=%v: extra column counts %v", multi, seen)
		}
	}
}
func TestJobsIndexedFacts(t *testing.T) {
	m := newModel(Options{Root: t.TempDir()})
	m.width = 200
	m.data.Jobs = []defs.JobEntry{indexJob("p", "j", "example-job")}
	m.rebuildJobIndex()
	m.rebuild()
	text := ansi.Strip(m.viewContent())
	if strings.Contains(text, "worker  ") || !strings.Contains(text, "every 1h0m0s") || !strings.Contains(text, "—") {
		t.Fatal(text)
	}
	// Indexed cells must keep the captured definition even if the backing slice changes
	// between snapshots; a new dataMsg is the only refresh boundary.
	m.data.Jobs[0].Name = "unaccepted"
	m.data.Jobs[0].Definition = []byte(`{}`)
	m.frameValid = false
	text = ansi.Strip(m.viewContent())
	if strings.Contains(text, "unaccepted") {
		t.Fatal(text)
	}
}
func TestJobsAllRowsMarquee(t *testing.T) {
	m := newModel(Options{Root: t.TempDir()})
	m.width = 80
	m.height = 24
	m.data.Jobs = []defs.JobEntry{indexJob("p", "a", "short"), indexJob("p", "b", "unselected-wide-界-name"+strings.Repeat(" that keeps going", 4))}
	m.rebuildJobIndex()
	m.rebuild()
	initial := m.viewContent()
	if strings.Contains(ansi.Strip(initial), "…") || !m.rendered.Animates {
		t.Fatal(initial)
	}
	m.frame = 6
	m.frameValid = false
	if m.viewContent() != initial {
		t.Fatal("hold advanced")
	}
	m.frame = 7
	m.frameValid = false
	if m.viewContent() == initial {
		t.Fatal("unselected cells did not scroll")
	}
	// 90 runes, the 7-rune gap and the 6-frame hold make a 103-frame loop.
	m.frame = 102
	m.frameValid = false
	if m.viewContent() == initial {
		t.Fatal("last scrolling frame wrapped early")
	}
	m.frame = 103
	m.frameValid = false
	if m.viewContent() != initial {
		t.Fatal("marquee did not wrap after the last scrolling frame")
	}

}

func TestJobsMouse(t *testing.T) {
	m, f := fixture(t)
	m.width = 200
	m.Update(struct{}{})
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.cursor != 3 {
		t.Fatal(m.cursor)
	}
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.cursor != 0 {
		t.Fatal(m.cursor)
	}
	m.Update(tea.MouseClickMsg{X: m.jobsTableWidth() + 2, Y: 5, Button: tea.MouseLeft})
	if m.cursor != 0 || m.currentView() != viewList {
		t.Fatal("inspector selected a row")
	}
	m.Update(tea.MouseClickMsg{X: 1, Y: 5, Button: tea.MouseLeft})
	if m.cursor != 1 || m.currentView() != viewList {
		t.Fatal(m.cursor, m.nav)
	}
	_, cmd := m.Update(tea.MouseClickMsg{X: 1, Y: 5, Button: tea.MouseLeft})
	applyTestCommand(m, cmd)
	if m.currentView() != viewJob {
		t.Fatal(m.nav)
	}
	if len(f.calls) != 0 {
		t.Fatal(f.calls)
	}
}
func TestJobsInspectorMarquee(t *testing.T) {
	for _, multi := range []bool{false, true} {
		m := newModel(Options{Root: t.TempDir()})
		m.height = 40
		m.width = 140
		if multi {
			m.opts.Projects = map[string]*Lane{"first": {}, "second": {}}
			m.width = 140
		}
		j := indexJob("p", "j", "short")
		j.Definition = []byte(`{"enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"notify"},"issue":{"kind":"existing"}}`)
		j.Waiting = strings.Repeat("waiting ", 12)
		m.data.Jobs = []defs.JobEntry{j}
		m.rebuildJobIndex()
		m.rebuild()
		for _, frame := range []int{0, 6, 7} {
			m.frame = frame
			m.Update(struct{}{})
			if !m.rendered.Animates {
				t.Fatal("inspector-only overflow did not animate")
			}
		}
		generation := m.marqueeGeneration
		m.Update(tea.WindowSizeMsg{Width: m.width - 1, Height: 40})
		if m.rendered.Animates || m.marqueePending {
			t.Fatal("hidden inspector kept ticking")
		}
		m.Update(marqueeTickMsg{generation: generation})
		if m.frame != 7 {
			t.Fatal("stale tick advanced")
		}
		m.Update(tea.WindowSizeMsg{Width: 200, Height: 4})
		if m.rendered.Animates {
			t.Fatal("clipped inspector animates")
		}
	}
}
func TestJobsSelectionRoles(t *testing.T) {
	m := newModel(Options{Root: t.TempDir()})
	m.width = 200
	m.opts.Projects = map[string]*Lane{"first": {}, "second": {}}
	m.data.Jobs = []defs.JobEntry{indexJob("p", "j", "short")}
	m.favorites[scopedRowKey("p", "j")] = true
	m.rebuildJobIndex()
	m.rebuild()
	cells := m.jobCells(m.indexedJob(m.rows[0]))
	selected := tableRow(cells, m.jobsWidths(), true, 0, 0).Text
	cells[0].Style = failStyle
	if changed := tableRow(cells, m.jobsWidths(), true, 0, 0).Text; changed != selected {
		t.Fatal("selected project retained foreground")
	}
	cells[slices.IndexFunc(m.jobsColumns(), func(c column) bool { return c.Title == " " })].Text = "★"
	star := tableRow(cells, m.jobsWidths(), true, 0, 0).Text
	if !strings.Contains(star, favoriteStyle.Inherit(rowSelectedStyle).Render("★")) {
		t.Fatal("favorite lost bold", star)
	}
}

func TestJobsUnselectedNameIsPlain(t *testing.T) {
	_, err := renderFrames(120, 40, func(_ string) string {
		mode := activeColorMode
		m := jobsFixture(t, "one-project", 120, 40)
		want := " Example report "
		if mode == colorLight {
			want = "\x1b[48;5;254mExample report "
		} else if mode == colorDark {
			want = "\x1b[48;5;236mExample report "
		}
		if text := m.renderJobs()[2].Text; !strings.Contains(text, want) {
			t.Fatal("enabled unselected name must keep the default foreground", mode, text)
		}
		return m.viewContent()
	})
	if err != nil {
		t.Fatal(err)
	}
}

func indexJob(project, uid, name string) defs.JobEntry {
	return defs.JobEntry{ProjectUID: project, Project: "example-project", UID: uid, Name: name, DefinitionEventUID: "event", CreatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Supported: true, Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"interval","interval_seconds":3600},"action":{"kind":"execute","prompt":"work"},"issue":{"kind":"per-run","title":"example"},"overlap":"forbid","catchup":"latest","options":{"agent":"worker","tags":["daily"],"description":"example description"}}`)}
}
func TestEffectiveProjectUID(t *testing.T) {
	for _, tc := range []struct {
		name        string
		data        Data
		entry, want string
	}{
		{"pinned", Data{Health: map[string]any{"version": float64(1), "project_uid": "p"}}, "", "p"},
		{"unpinned", Data{}, "", ""},
		{"bad version", Data{Health: map[string]any{"version": float64(2), "project_uid": "p"}}, "", ""},
		{"project dirs", Data{Projects: map[string]Data{"p": {}}, Health: map[string]any{"version": float64(1), "project_uid": "p"}}, "", ""},
		{"explicit", Data{}, "q", "q"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveProjectUID(tc.data, tc.entry); got != tc.want {
				t.Fatalf("%q", got)
			}
		})
	}
}
func TestJobIdentityAgreement(t *testing.T) {
	for _, tc := range []struct {
		name, entry, want string
		data              Data
	}{
		{"pinned", "", "p", Data{Health: map[string]any{"version": float64(1), "project_uid": "p"}}},
		{"unpinned", "", "", Data{}},
		{"project dirs", "", "", Data{Projects: map[string]Data{"p": {}}}},
		{"explicit", "p", "p", Data{Projects: map[string]Data{"p": {}, "q": {}}, Jobs: []defs.JobEntry{indexJob("q", "same", "other")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel(Options{Root: t.TempDir()})
			m.data = tc.data
			m.data.Jobs = append(m.data.Jobs, indexJob(tc.entry, "same", "example-job"))
			m.rebuildJobIndex()
			if _, ok := m.jobs.byKey[jobKey{tc.want, "same"}]; !ok {
				t.Fatal("index identity")
			}
			r, ok := m.resolveFrame(navFrame{view: viewJob, projectUID: tc.want, uid: "same"})
			if !ok || r.label != "example-job" {
				t.Fatalf("frame %+v %v", r, ok)
			}
			m.data.Runs = []runner.Run{{UID: "run", Target: runner.Target{Identity: kata.Identity{ProjectUID: tc.want}}, Job: kata.Job{UID: "same"}}}
			m.pushDetail(viewRun, tc.want, "run")
			m.openJobOfRun()
			f := m.nav[len(m.nav)-1]
			if f.view != viewJob || f.projectUID != tc.want || f.uid != "same" {
				t.Fatalf("run link: %+v", f)
			}

		})
	}
}
func TestJobIndexJoins(t *testing.T) {
	m := newModel(Options{Root: t.TempDir()})
	m.data.Health = map[string]any{"version": float64(1), "project_uid": "p"}
	j := indexJob("", "same", "example-job")
	m.data.Jobs = []defs.JobEntry{j}
	m.data.Runs = []runner.Run{{UID: "new", Job: kata.Job{UID: "same"}, Phase: runner.Running}, {UID: "old", Target: runner.Target{Identity: kata.Identity{ProjectUID: "p"}}, Job: kata.Job{UID: "same"}, Phase: runner.Held}}
	m.rebuildJobIndex()
	v := m.jobs.byKey[jobKey{"p", "same"}]
	if !reflect.DeepEqual(v.runIndexes, []int{1}) {
		t.Fatal(v.runIndexes)
	}
	j.Definition = json.RawMessage(`{"action":{"kind":"execute","workflow_uid":"flow"}}`)
	m.data.Jobs[0] = j
	m.data.Workflows = []kata.Workflow{{UID: "flow", Definition: json.RawMessage(`{"version":1,"steps":[{"key":"a","kind":"command","command":"true"}]}`), ProjectUID: "q"}}
	m.rebuildJobIndex()
	if m.jobs.byKey[jobKey{"p", "same"}].workflow != nil {
		t.Fatal("cross-project workflow")
	}
}
func TestJobSearchAndSort(t *testing.T) {
	m := newModel(Options{Root: t.TempDir(), Project: "example-project", Config: config.Config{Profiles: map[string]config.Profile{"worker": {Kind: "codex", Args: []string{"--model", "example-model"}}}}})
	a, b := indexJob("p", "a", "zebra"), indexJob("p", "b", "alpha")
	b.CreatedAt = b.CreatedAt.Add(time.Hour)
	m.data.Jobs = []defs.JobEntry{b, a}
	m.rebuildJobIndex()
	m.rebuild()
	if len(m.rows) != 2 || m.rows[0].uid != "a" {
		t.Fatal(m.rows)
	}
	for _, query := range []string{"daily", "example-model", "worker", "example-project", "example description"} {
		m.query = query
		m.rebuild()
		if len(m.rows) != 2 {
			t.Fatalf("query %s: %v", query, m.rows)
		}
	}
	m.query = "true"
	m.rebuild()
	if len(m.rows) != 0 {
		t.Fatal("raw JSON searched")
	}
	m.query = ""
	m.favorites[scopedRowKey("p", "b")] = true
	m.rebuild()
	if m.rows[0].uid != "b" {
		t.Fatal(m.rows)
	}
}
func TestJobNext(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	m := newModel(Options{Root: t.TempDir(), Now: func() time.Time { return at.Add(4 * time.Hour) }})
	m.data.Health = map[string]any{"version": float64(1), "project_uid": "p", "state_created_at": at.Add(2 * time.Hour).Format(time.RFC3339)}
	m.data.Jobs = []defs.JobEntry{indexJob("", "job", "example-job")}
	m.rebuildJobIndex()
	if got := m.jobs.byKey[jobKey{"p", "job"}].next.at; !got.Equal(at.Add(3 * time.Hour)) {
		t.Fatal(got)
	}
	m.data.Checkpoints = map[string]sched.Checkpoint{"job": {Version: 1, JobUID: "job", HandledThrough: at.Add(5 * time.Hour)}}
	m.rebuildJobIndex()
	if got := m.jobs.byKey[jobKey{"p", "job"}].next.at; !got.Equal(at.Add(6 * time.Hour)) {
		t.Fatal(got)
	}
	m.data.Jobs[0].Definition = json.RawMessage(`{"enabled":true,"trigger":{"kind":"interval","interval_seconds":60},"action":{"kind":"notify"},"issue":{"kind":"per-run"}}`)
	m.rebuildJobIndex()
	if v := m.jobs.byKey[jobKey{"p", "job"}]; !v.next.at.IsZero() || v.next.state != "Kata-managed; next delivery unavailable locally" {
		t.Fatal(v.next)
	}
	m.data.Jobs[0].Owner = defs.Ignored
	m.data.Jobs[0].Definition = json.RawMessage(`{"enabled":false,"trigger":{"kind":"interval","interval_seconds":60},"action":{"kind":"notify"},"issue":{"kind":"per-run"}}`)
	m.rebuildJobIndex()
	if v := m.jobs.byKey[jobKey{"p", "job"}]; v.shape != "recurring" || !v.next.at.IsZero() {
		t.Fatal(v)
	}
}
func TestJobIndexMemo(t *testing.T) {
	m := newModel(Options{Root: t.TempDir()})
	m.data.Jobs = []defs.JobEntry{indexJob("p", "job", "example-job")}
	m.rebuildJobIndex()
	k := jobKey{"p", "job"}
	original := m.jobs.jobMemo[k]
	m.rebuildJobIndex()
	if m.jobs.jobMemo[k] != original {
		t.Fatal("unchanged job decoded again")
	}
	m.data.Jobs[0].DefinitionEventUID = "new-event"
	m.data.Jobs[0].Supported = false
	m.rebuildJobIndex()
	if m.jobs.jobMemo[k] == original || m.jobs.byKey[k].entry.Supported {
		t.Fatal("event/support stale")
	}
	m.data.Jobs[0].DefinitionEventUID = ""
	m.rebuildJobIndex()
	original = m.jobs.jobMemo[k]
	m.data.Jobs[0].Definition = json.RawMessage(`{"enabled":false}`)
	m.rebuildJobIndex()
	if m.jobs.jobMemo[k] == original || m.jobs.byKey[k].def.Enabled {
		t.Fatal("missing event fallback stale")
	}
	m.data.Jobs = nil
	m.rebuildJobIndex()
	if len(m.jobs.jobMemo) != 0 {
		t.Fatal("removed memo retained")
	}
}

func TestJobNextInvalidDefinition(t *testing.T) {
	for _, raw := range []string{`{"enabled":true,"trigger":{"kind":"manual","interval_seconds":60}}`, `{"enabled":true,"trigger":{"kind":"manual"},"timeout_seconds":"bad"}`} {
		m := newModel(Options{Root: t.TempDir()})
		j := indexJob("p", "job", "example-job")
		j.Definition = []byte(raw)
		m.data.Jobs = []defs.JobEntry{j}
		m.rebuildJobIndex()
		if got := m.jobs.byKey[jobKey{"p", "job"}].next.state; got != "next unavailable locally" {
			t.Fatalf("invalid definition next=%q", got)
		}
	}
}
func TestJobIndexLaneProfileFacts(t *testing.T) {
	profile := func(model string) config.Config {
		return config.Config{Profiles: map[string]config.Profile{"worker": {Kind: "codex", Args: []string{"--model", model}}}}
	}
	m := newModel(Options{Root: t.TempDir(), Projects: map[string]*Lane{"example-project": {Config: profile("model-a")}, "team-project": {Config: profile("model-b")}}, Config: profile("root-model")})
	a, b, c := indexJob("p", "job", "first"), indexJob("q", "job", "second"), indexJob("r", "job", "third")
	b.Project = "team-project"
	c.Project = "removed-project"
	m.data.Jobs = []defs.JobEntry{a, b, c}
	m.rebuildJobIndex()
	for _, tc := range []struct{ project, model string }{{"p", "model-a"}, {"q", "model-b"}, {"r", ""}} {
		if got := m.jobs.byKey[jobKey{tc.project, "job"}].facts.Model; got != tc.model {
			t.Fatalf("lane %s model %q want %q", tc.project, got, tc.model)
		}
	}
}

func TestFinishedOnceRequiresCurrentScheduledOccurrence(t *testing.T) {
	for _, tc := range []struct {
		name, outcome, occurrence, event string
		phase                            runner.Phase
		manual, want                     bool
	}{
		{"scheduled success", "succeeded", "2099-01-01T00:00:00Z", "event", runner.Settled, false, true},
		{"failed", "failed", "2099-01-01T00:00:00Z", "event", runner.Settled, false, false},
		{"held", "succeeded", "2099-01-01T00:00:00Z", "event", runner.Held, false, false},
		{"manual", "succeeded", "2099-01-01T00:00:00Z", "event", runner.Settled, true, false},
		{"old event", "succeeded", "2099-01-01T00:00:00Z", "old", runner.Settled, false, false},
		{"old occurrence", "succeeded", "2020-01-01T00:00:00Z", "event", runner.Settled, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := fixture(t)
			m.data.Jobs[0].DefinitionEventUID = "event"
			m.data.Jobs[0].Definition = json.RawMessage(`{"trigger":{"kind":"once","at":"2099-01-01T00:00:00Z"}}`)
			m.data.Runs[0].Phase = tc.phase
			m.data.Runs[0].Outcome = tc.outcome
			m.data.Runs[0].Occurrence = tc.occurrence
			m.data.Runs[0].Manual = tc.manual
			m.data.Runs[0].Job.DefinitionEventUID = tc.event
			m.rebuildJobIndex()
			if m.isFinished(0) != tc.want {
				t.Fatalf("finished=%v", m.isFinished(0))
			}
		})
	}
}

// favoriteGutter reports whether Jobs reserves the favorite column, which
// sits between status and job.
func favoriteGutter(m *model) bool {
	return slices.ContainsFunc(m.jobsColumns(), func(c column) bool { return c.Title == " " })
}

func TestJobsOmitUnusedFavoriteGutter(t *testing.T) {
	m, _ := fixture(t)
	text := ansi.Strip(bodyTextForTest(m.renderJobs()))
	if favoriteGutter(m) || !strings.Contains(text, "running Job 00") {
		t.Fatal("unused favorite slot wastes row space:\n" + text)
	}
	send(m, "f")
	text = ansi.Strip(bodyTextForTest(m.renderJobs()))
	if !strings.Contains(text, "running ★ Job 00") {
		t.Fatal("favorite is missing from selected row:\n" + text)
	}
	send(m, "f")
	text = ansi.Strip(bodyTextForTest(m.renderJobs()))
	if favoriteGutter(m) || !strings.Contains(text, "running Job 00") || strings.Contains(text, "★") {
		t.Fatal("last favorite did not release its gutter:\n" + text)
	}
}

func TestJobsFavoriteGutterFollowsVisibleRows(t *testing.T) {
	for _, mode := range []string{"deleted", "filtered"} {
		t.Run(mode, func(t *testing.T) {
			m, _ := fixture(t)
			send(m, "f")
			if mode == "deleted" {
				m.data.Jobs = m.data.Jobs[1:]
				m.Update(dataMsg{data: m.data})
			} else {
				m.Update(press("/"))
				m.Update(tea.PasteMsg{Content: "Job 01"})
				m.Update(press("enter"))
			}
			text := ansi.Strip(bodyTextForTest(m.renderJobs()))
			if favoriteGutter(m) || !strings.Contains(text, "never  Job 01") {
				t.Fatal("invisible favorite reserves empty gutter:\n" + text)
			}
			if !m.favorites["j00"] {
				t.Fatal("hiding the column discarded the saved preference")
			}
		})
	}
}

// The inspector must show the exact option keys that execution consumes.
func TestJobsInspectorUsesExactOptionKeys(t *testing.T) {
	for _, tc := range []struct {
		name, options, profile, input string
	}{
		{"variants only", `{"Agent":"other","Input":"ignored-input"}`, "default · codex", ""},
		{"canonical then variants", `{"agent":"default","AGENT":"other","input":"canonical-input","Input":"ignored-input"}`, "default · codex", "canonical-input"},
		{"canonical", `{"agent":"other","input":"canonical-input"}`, "other · claude", "canonical-input"},
		{"duplicate canonical", `{"agent":"default","agent":"other","input":"ignored-input","input":"canonical-input"}`, "other · claude", "canonical-input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{Profiles: map[string]config.Profile{"default": {Kind: "codex"}, "other": {Kind: "claude"}}}
			m := newModel(Options{Root: t.TempDir(), Project: "example-project", Config: cfg})
			j := indexJob("p", "job", "Example job")
			d, err := defs.DecodeJob(j.Definition)
			if err != nil {
				t.Fatal(err)
			}
			d.Action = defs.Action{Kind: "execute", WorkflowUID: "wf"}
			d.Options = json.RawMessage(tc.options)
			j.Definition, err = json.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			w := defs.WorkflowDef{Version: 1, Steps: []defs.Step{{Key: "main", Kind: "prompt", Prompt: "work"}}}
			_, support := defs.Classify(d, &w, cfg)
			if !support.Supported {
				t.Fatalf("unsupported fixture: %+v", support)
			}
			raw, err := json.Marshal(w)
			if err != nil {
				t.Fatal(err)
			}
			m.data.Jobs = []defs.JobEntry{j}
			m.data.Workflows = []kata.Workflow{{ProjectUID: "p", UID: "wf", Name: "Example workflow", DefinitionEventUID: "wf-event", Definition: raw}}
			m.rebuildJobIndex()
			text := bodyText(m.renderJobInspector(m.jobs.byKey[jobKey{"p", "job"}], 100))
			if !strings.Contains(text, tc.profile) {
				t.Errorf("profile %q missing: %s", tc.profile, text)
			}
			if tc.input != "" && !strings.Contains(text, tc.input) {
				t.Errorf("input %q missing: %s", tc.input, text)
			}
			if strings.Contains(text, "ignored-input") {
				t.Errorf("ignored input displayed: %s", text)
			}
		})
	}
}
