package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

func jobDetailFixture(t testing.TB, definition string) *model {
	t.Helper()
	m := newModel(Options{Root: t.TempDir(), ProjectUID: "p", Project: "example-project", Config: config.Config{Profiles: map[string]config.Profile{"worker": {Kind: "codex", Args: []string{"-m", "example-model", "--sandbox", "read-only"}}}}})
	m.width, m.height = 120, 80
	j := indexJob("p", "job", "Example job")
	j.Definition = []byte(definition)
	m.data.Jobs = []defs.JobEntry{j}
	m.rebuildJobIndex()
	m.rebuild()
	m.pushDetail(viewJob, "p", "job")
	return m
}
func TestJobDetailShapes(t *testing.T) {
	for _, tc := range []struct {
		name, raw    string
		want, absent []string
	}{
		{"prompt", `{"enabled":true,"trigger":{"kind":"cron","cron":"0 8 * * *","timezone":"Europe/Berlin"},"action":{"kind":"execute","prompt":"Review the example"},"options":{"agent":"worker","tags":["daily"],"description":"Example description","opaque":9007199254740993}}`, []string{"Example description", "tags", "daily", "(Europe/Berlin)", "agent", "codex · profile worker", "example-model", "sandbox", "read-only", "1 planned / 12 cap", "opaque (preserved)", "Prompt", "Review the example", "Runs"}, []string{"Kata history", "9007199254740993"}},
		{"notify", `{"enabled":true,"action":{"kind":"notify","recipient":"worker","message":"Read the example"},"issue":{"kind":"existing","uid":"issue"}}`, []string{"recipient", "worker", "existing example-project#ab12", "Message", "Read the example"}, []string{"sandbox", "timeout", "prompt"}},
		{"recurring", `{"enabled":false,"action":{"kind":"notify","recipient":"worker","message":"Optional note"},"issue":{"kind":"per-run","parent_uid":"parent","title":"Child title","body":"Child instructions"}}`, []string{"paused", "native (Kata delivers)", "parent", "Child title", "Child body", "Child instructions", "Message", "Optional note"}, []string{"model", "timeout", "sandbox"}},
		{"workflow", `{"enabled":true,"action":{"kind":"execute","workflow_uid":"flow"},"options":{"input":"example input"}}`, []string{"workflow", "Workflow description", "1 step: check", "example input", "— (command-only workflow)"}, []string{"sandbox", "approvals", "argv"}},
		{"missing", `{"enabled":true,"action":{"kind":"execute","workflow_uid":"absent"}}`, []string{"workflow unavailable"}, []string{"absent", "command-only workflow"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := jobDetailFixture(t, tc.raw)
			m.data.Runs = []runner.Run{{Target: runner.Target{Identity: kata.Identity{ProjectUID: "p"}}, IssueRefs: map[string]string{"issue": "example-project#ab12", "parent": "example-project#cd34"}}}
			m.data.Workflows = []kata.Workflow{{UID: "flow", Name: "Example workflow", Definition: json.RawMessage(`{"version":1,"about":"Workflow description","steps":[{"key":"check","kind":"command","command":"true"}]}`), ProjectUID: "p"}}
			m.rebuildJobIndex()
			text := ansi.Strip(bodyTextForTest(m.buildJobDetail().Body()))
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Fatal(want, text)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(text, absent) {
					t.Fatal(absent, text)
				}
			}
		})
	}
}
func TestJobDetailNotRaw(t *testing.T) {
	m := jobDetailFixture(t, `{"version":1,"enabled":true,"action":{"kind":"execute","prompt":"{\"version\":1}"}}`)
	text := ansi.Strip(bodyTextForTest(m.buildJobDetail().Body()))
	if !strings.Contains(text, `{"version":1}`) || strings.Contains(text, "  \"action\":") {
		t.Fatal(text)
	}
}
func TestJobDetailIgnoresListKeys(t *testing.T) {
	m := jobDetailFixture(t, `{"enabled":true,"action":{"kind":"execute","prompt":"work"}}`)
	for _, key := range []string{"tab", "shift+tab", "1", "2", "3", "4", "5", "/", "[", "]", "f", "F", "P", "n"} {
		send(m, key)
		if m.currentView() != viewJob || m.searching || m.finished || len(m.confirm) > 0 || len(m.favorites) > 0 {
			t.Fatal(key, m.nav, m.notice.Text)
		}
	}
}
func TestJobDetailCappedSelection(t *testing.T) {
	m, s := actionJobs(t)
	m.finished = true
	m.rebuild()
	m.data.Runs = nil
	for i := range 21 {
		m.data.Runs = append(m.data.Runs, runner.Run{UID: fmt.Sprintf("run-%02d", i), Target: runner.Target{Identity: kata.Identity{ProjectUID: "p"}}, Job: kata.Job{UID: "a"}, Phase: runner.Held, Herdr: runner.HerdrRef{PaneID: "pane"}})
	}
	m.rebuildJobIndex()
	m.rebuild()
	m.pushDetail(viewJob, "p", "a")
	m.detailKey = detailItemKey("run", "p", "run-19")
	m.detailItems()
	data := m.data
	data.Runs = append([]runner.Run{{UID: "newest", Target: runner.Target{Identity: kata.Identity{ProjectUID: "p"}}, Job: kata.Job{UID: "a"}}}, data.Runs...)
	m.Update(dataMsg{data: data})
	retained := m.detailKey
	if m.detailOrdinal != 20 || retained != detailItemKey("run", "p", "run-19") {
		t.Fatal(m.detailKey, m.detailOrdinal)
	}
	send(m, "a")
	if len(s.calls) != 1 || s.calls[0] != "focus:run-19" {
		t.Fatal(m.notice.Text, s.calls)
	}
	for _, key := range []string{"k", "j"} {
		m.detailKey = retained
		m.detailOrdinal = 20
		send(m, key)
		if m.detailKey != detailItemKey("run", "p", "run-18") {
			t.Fatal(key, m.detailKey, m.detailOrdinal)
		}
	}
}
func TestJobDetailScheduleZoneOnce(t *testing.T) {
	m := jobDetailFixture(t, `{"enabled":true,"trigger":{"kind":"once","at":"2026-10-20T09:00:37.123+02:00","timezone":"Europe/Berlin"},"action":{"kind":"execute","prompt":"work"}}`)
	text := ansi.Strip(bodyTextForTest(m.buildJobDetail().Body()))
	if strings.Count(text, "Europe/Berlin") != 1 || !strings.Contains(text, "09:00:37.123") {
		t.Fatal(text)
	}
}
func TestJobDetailHitsAndWindow(t *testing.T) {
	m := detailFixture(t, "prompt", 80, 24)
	s := &jobActionSpy{}
	m.opts.Service = s
	m.opts.ProjectUID = "p"
	m.Update(struct{}{})
	selected := m.detailKey
	var y int = -1
	for row, hit := range m.hits.Rows {
		if hit.Key == selected {
			y = row
		}
		if row >= m.height-3 {
			t.Fatal("footer hit", row)
		}
	}
	if y < 0 || m.scroll == 0 {
		t.Fatal("first run did not follow into the viewport", m.hits, m.scroll)
	}
	depth := len(m.nav)
	for range 2 {
		m.Update(tea.MouseClickMsg{X: 2, Y: y, Button: tea.MouseLeft})
	}
	if len(m.nav) != depth || len(s.calls) != 0 {
		t.Fatal("run clicks performed an action", m.nav, s.calls)
	}
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.detailKey != detailItemKey("run", "p", "run-2") {
		t.Fatal(m.detailKey)
	}
	send(m, "ctrl+u")
	if m.follow || m.detailKey != detailItemKey("run", "p", "run-2") {
		t.Fatal(m.follow, m.detailKey)
	}
	send(m, "k")
	if !m.follow || m.detailKey != detailItemKey("run", "p", "run-1") {
		t.Fatal(m.follow, m.detailKey)
	}
}
func TestJobDetailStatusCopy(t *testing.T) {
	m := jobDetailFixture(t, `{"enabled":true,"action":{"kind":"execute","prompt":"work"}}`)
	if m.notice.Text != "no local runs · K opens Kata" {
		t.Fatal(m.notice.Text)
	}
	for _, tc := range []struct{ key, want string }{{"run:r1", "invalid detail selection"}, {detailItemKey("future", "p", "r1"), "invalid detail selection"}, {detailItemKey("step", "p", "r1"), "invalid detail selection"}, {detailItemKey("run", "p", "gone"), "run was removed"}} {
		m.detailKey = tc.key
		send(m, "a")
		if m.notice.Text != tc.want {
			t.Fatal(m.notice.Text)
		}
	}
}
func TestJobDetailHelpAvailableActions(t *testing.T) {
	m := detailFixture(t, "prompt", 200, 50)
	text := ansi.Strip(m.jobDetailHelp().Render(200))
	for _, want := range []string{"h/← back", "j/k move", "l/→ open run", "a attach", "u resume", "R run", "p pause", "e edit", "D delete"} {
		if !strings.Contains(text, want) {
			t.Fatal(want, text)
		}
	}
	if got := ansi.Strip(m.jobDetailHelp().Render(80)); got != "h/← back · j/k move · l/→ open run · a attach · R run · M mouse · q quit" {
		t.Fatal(got)
	}
}

func TestJobDetailWarningsPreserveSupportedState(t *testing.T) {
	m := jobDetailFixture(t, `{"enabled":true,"action":{"kind":"execute","prompt":"work"}}`)
	m.data.Jobs[0].Supported = true
	m.data.Jobs[0].Warnings = []string{"ignored options key: example"}
	m.rebuildJobIndex()
	text := ansi.Strip(bodyTextForTest(m.buildJobDetail().Body()))
	if !strings.Contains(text, "Warnings") || !strings.Contains(text, "ignored options key: example") || strings.Contains(text, "unsupported") {
		t.Fatal(text)
	}
	if !strings.Contains(text, "\nWarnings\n  ignored options key: example") {
		t.Fatal("warning body is not indented under its heading", text)
	}
}

func TestJobTagChips(t *testing.T) {
	m := jobDetailFixture(t, `{"enabled":true,"action":{"kind":"execute","prompt":"Work"},"options":{"tags":["weekly","daily","example"]}}`)
	m.width = 200
	if body := workflowText(m.buildJobDetail().Body()); !strings.Contains(body, "[daily] [example] [weekly]") {
		t.Fatal("detail tags:", body)
	}
	v, _ := m.selectedJobView()
	if body := workflowText(m.renderJobInspector(v, 64)); !strings.Contains(body, "[daily] [example] [weekly]") {
		t.Fatal("inspector tags:", body)
	}
}

func TestJobDetailEmptyTags(t *testing.T) {
	for _, raw := range []string{
		`{"enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Work"}}`,
		`{"enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Work"},"options":{"tags":[]}}`,
	} {
		m := jobDetailFixture(t, raw)
		if body := workflowText(m.buildJobDetail().Body()); !strings.Contains(body, "(no labels)") {
			t.Fatal("missing empty detail labels", body)
		}
	}
}
