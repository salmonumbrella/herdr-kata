package tui

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/jobform"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type fakeWriter struct {
	saved  kata.Job
	parent string
	err    error
	newer  kata.Job
}

func (f *fakeWriter) SaveJob(_ context.Context, j kata.Job, parent string) (kata.Job, error) {
	f.saved = j
	f.parent = parent
	if f.err != nil {
		return kata.Job{}, f.err
	}
	j.DefinitionEventUID = "next"
	return j, nil
}
func (f *fakeWriter) ShowJob(context.Context, string) (kata.Job, error) { return f.newer, nil }
func (f *fakeWriter) ShowIssue(context.Context, string) (kata.IssueView, error) {
	return kata.IssueView{UID: "01K00000000000000000000001", ProjectUID: "project"}, nil
}
func cfg() config.Config {
	return config.Config{Kata: config.Kata{Inbox: "worker/inbox"}, Checkouts: map[string]string{"repo": "/tmp/repo"}, Profiles: map[string]config.Profile{"default": {Kind: "codex"}}}
}

func TestBoardJobEditorNamesChosenProject(t *testing.T) {
	for _, multiple := range []bool{false, true} {
		t.Run(map[bool]string{false: "single project", true: "multiple projects"}[multiple], func(t *testing.T) {
			writer := &fakeWriter{}
			view := cfg()
			view.Kata.Project = "team-project"
			projects := map[string]*Lane{"team-project": {Project: "team-project", Config: view, Service: &laneFormService{fakeActions: &fakeActions{}, fakeWriter: writer}}}
			if multiple {
				projects["example-project"] = &Lane{Project: "example-project"}
			}
			root := t.TempDir()
			for project, uid := range map[string]string{"team-project": "team", "example-project": "example"} {
				if _, ok := projects[project]; !ok {
					continue
				}
				lane := state.Layout{Root: root}.Project(uid)
				if err := state.WriteJSON(state.OSFS{}, lane.StateFile(), kata.Installation{Version: 1, Name: project, ProjectUID: uid}); err != nil {
					t.Fatal(err)
				}
				if err := state.WriteJSON(state.OSFS{}, filepath.Join(lane.Root, "route.json"), map[string]any{"allowed": true}); err != nil {
					t.Fatal(err)
				}
			}
			model := newModel(Options{Root: root, Projects: projects})

			initial := model.Init()()
			if batch, ok := initial.(tea.BatchMsg); ok && len(batch) > 0 {
				model.Update(batch[0]())
			}
			press := func(k tea.KeyPressMsg) {
				_, cmd := model.Update(k)
				for cmd != nil {
					_, cmd = model.Update(cmd())
				}
			}
			press(tea.KeyPressMsg{Code: 'n', Text: "n"})
			if multiple {
				press(tea.KeyPressMsg{Code: tea.KeyDown})
				press(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
			title, _, _ := strings.Cut(ansi.Strip(model.View().Content), "\n")
			want := "new job · project team-project"
			if title != want {
				t.Errorf("editor title = %q, want %q", title, want)
			}
			form, ok := model.editor.(*jobForm)
			if !ok {
				t.Fatal(model.viewContent())
			}
			form.Set("name", "Draft")
			form.Set("prompt", "Do the work")
			form.Set("title", "Run draft")
			model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
			press(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
			if writer.saved.Name != "Draft" || !strings.Contains(model.View().Content, "Draft saved · appears after the worker's next definition refresh · project team-project") {
				t.Fatalf("save did not acknowledge the editor's project: saved=%q\n%s", writer.saved.Name, model.View().Content)
			}
		})
	}
}
func TestEditorRoundTripKinds(t *testing.T) {
	for _, mode := range []string{"prompt", "workflow", "notify", "recurring"} {
		t.Run(mode, func(t *testing.T) {
			d := `{"version":1,"kind":"job","enabled":false,"trigger":{"kind":"cron","cron":"0 * * * *","timezone":"UTC"},"action":{"kind":"execute","prompt":"hello"},"issue":{"kind":"per-run","title":"child","body":"body"},"overlap":"allow","catchup":"latest","future":{"keep":true}}`
			var raw map[string]any
			json.Unmarshal([]byte(d), &raw)
			switch mode {
			case "workflow":
				raw["action"] = map[string]any{"kind": "execute", "workflow_uid": "flow"}
			case "notify":
				raw["action"] = map[string]any{"kind": "notify", "recipient": "worker", "message": "hello"}
				raw["issue"] = map[string]any{"kind": "existing", "uid": "01K00000000000000000000001"}
			case "recurring":
				raw["action"] = map[string]any{"kind": "notify", "recipient": "worker/inbox", "message": "hello"}
				raw["issue"].(map[string]any)["parent_uid"] = "01K00000000000000000000001"
			}
			body, _ := json.Marshal(raw)
			j := kata.Job{UID: "job", Name: "name", DefinitionEventUID: "event", Definition: body}
			f := &fakeWriter{}
			m := newJobForm(t.Context(), cfg(), f, &j, []kata.Workflow{{UID: "flow", Name: "Flow", Definition: json.RawMessage(`{"version":1,"steps":[{"key":"work","kind":"command","command":"true"}]}`)}}, editorContext{})
			m.Set("name", "renamed")
			_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
			if cmd == nil {
				t.Fatal(m.View())
			}
			m.Update(cmd())
			var got map[string]any
			json.Unmarshal(f.saved.Definition, &got)
			if !reflect.DeepEqual(raw, got) || f.saved.DefinitionEventUID != "event" {
				t.Fatalf("lost fields: %s", f.saved.Definition)
			}
			if mode == "recurring" && f.parent != "01K00000000000000000000001" {
				t.Fatal("missing --parent", f.parent)
			}
		})
	}
}
func TestConflictRetainsDraftAndGuard(t *testing.T) {
	f := &fakeWriter{err: &kata.Error{Code: "conflict"}, newer: kata.Job{UID: "job", Name: "newer", DefinitionEventUID: "new-event", Definition: json.RawMessage(`{"enabled":true}`)}}
	j := kata.Job{UID: "job", Name: "original", DefinitionEventUID: "old-event", Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"work"},"issue":{"kind":"per-run","title":"title"},"overlap":"forbid","catchup":"latest"}`)}
	m := newJobForm(t.Context(), cfg(), f, &j, nil, editorContext{})
	m.Set("name", "my edit")
	_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m.Update(cmd())
	if !strings.Contains(m.View(), "newer") || m.Value("name") != "my edit" || m.job.DefinitionEventUID != "old-event" {
		t.Fatal(m.View())
	}
	_, cmd = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd != nil || m.job.DefinitionEventUID != "old-event" {
		t.Fatal("known conflict retried or replaced guard")
	}

}
func TestNewRecurringDisabledAndStableUID(t *testing.T) {
	f := &fakeWriter{err: errors.New("lost reply")}
	m := newJobForm(t.Context(), cfg(), f, nil, nil, editorContext{})
	for key, v := range map[string]string{"mode": "recurring", "name": "Delivery", "parent": "tasks#parent", "title": "Child", "message": "Due", "trigger": "interval", "schedule": "60", "overlap": "allow"} {
		m.Set(key, v)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal(m.View())
	}
	m.Update(cmd())
	first := f.saved
	_, cmd = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m.Update(cmd())
	if first.UID == "" || first.UID != f.saved.UID || first.DefinitionEventUID != "" || f.parent != "01K00000000000000000000001" {
		t.Fatalf("%+v %s", first, f.parent)
	}
	var d map[string]any
	json.Unmarshal(first.Definition, &d)
	if d["enabled"] != false {
		t.Fatal("new native job enabled before binding")
	}
}
func FuzzEditorPreservesUnknownFields(f *testing.F) {
	f.Add("hello", "extra")
	f.Fuzz(func(t *testing.T, prompt, extra string) {
		if len(prompt)+len(extra) > 10000 {
			return
		}
		raw, _ := json.Marshal(map[string]any{"version": 1, "kind": "job", "enabled": true, "trigger": map[string]any{"kind": "manual"}, "action": map[string]any{"kind": "execute", "prompt": prompt}, "custom": extra})
		j := kata.Job{UID: "job", Name: "name", DefinitionEventUID: "e", Definition: raw}
		m := newJobForm(t.Context(), cfg(), &fakeWriter{}, &j, nil, editorContext{})
		m.Set("name", "changed")
		body, err := m.definition()
		if err != nil {
			t.Fatal(err)
		}
		var got, before map[string]any
		json.Unmarshal(raw, &before)
		json.Unmarshal(body, &got)
		if !reflect.DeepEqual(got, before) {
			t.Fatal("unknown fields lost")
		}
	})
}

func TestSwitchExecuteModePreservesOptions(t *testing.T) {
	j := kata.Job{UID: "job", Name: "name", DefinitionEventUID: "event", Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"work"},"issue":{"kind":"per-run","title":"title"},"overlap":"forbid","catchup":"latest","options":{"agent":"default","unknown":"keep"}}`)}
	m := newJobForm(t.Context(), cfg(), &fakeWriter{}, &j, []kata.Workflow{{UID: "flow", Definition: json.RawMessage(`{"version":1,"steps":[{"key":"work","kind":"command","command":"true"}]}`)}}, editorContext{})
	m.Set("mode", "workflow")
	m.Set("workflow", "flow")
	raw, err := m.definition()
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	json.Unmarshal(raw, &d)
	if d["options"].(map[string]any)["unknown"] != "keep" {
		t.Fatal(string(raw))
	}
}
func TestInvalidRecurringFieldsAndNativeConversion(t *testing.T) {
	for _, change := range []map[string]string{{"trigger": "manual"}, {"schedule": "59"}, {"catchup": "skip", "grace": "0"}, {"title": ""}} {
		m := newJobForm(t.Context(), cfg(), &fakeWriter{}, nil, nil, editorContext{})
		for k, v := range map[string]string{"mode": "recurring", "name": "child", "parent": "tasks#p", "title": "child", "message": "due", "trigger": "interval", "schedule": "60"} {
			m.Set(k, v)
		}
		for k, v := range change {
			m.Set(k, v)
		}
		_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		if cmd != nil {
			t.Fatal("invalid native save started", change)
		}
	}
}

func TestFormKeyboardTextBooleanAndChoice(t *testing.T) {
	m := newJobForm(t.Context(), cfg(), &fakeWriter{}, nil, nil, editorContext{})
	editorKey(m, "enter")
	m.Update(press("q"))
	editorKey(m, "ctrl+s")
	if m.Value("name") != "q" {
		t.Fatal("q was not typed")
	}
	selectField := func(key string) {
		t.Helper()
		for i, f := range m.fields() {
			if f.Key == key {
				m.index = i
				return
			}
		}
		t.Fatal("missing field", key)
	}
	selectField("enabled")
	m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if m.Value("enabled") != "false" {
		t.Fatal("boolean did not toggle")
	}
	selectField("mode")
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.Value("mode") != "workflow" {
		t.Fatal("choice did not move")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.fields()[m.index].Key != "tags" {
		t.Fatal("reverse tab")
	}
	editorKey(m, "esc")
	cmd := editorKey(m, "y")
	if cmd == nil {
		t.Fatal("cancel missing")
	}
}

func TestRealSpaceKeyInTextAndTextarea(t *testing.T) {
	m := newJobForm(t.Context(), cfg(), &fakeWriter{}, nil, nil, editorContext{})
	editorKey(m, "enter")
	m.Update(press("hello"))
	m.Update(press("space"))
	m.Update(press("world"))
	editorKey(m, "ctrl+s")
	if m.Value("name") != "hello world" {
		t.Fatal("real spaces dropped", m.Value("name"))
	}
	for i, f := range m.fields() {
		if f.Key == "prompt" {
			m.index = i
			break
		}
	}
	editorKey(m, "enter")
	m.Update(tea.KeyPressMsg{Code: tea.KeyExtended, Text: "do"})
	m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m.Update(tea.KeyPressMsg{Code: tea.KeyExtended, Text: "work"})
	editorKey(m, "ctrl+s")
	if m.Value("prompt") != "do work" {
		t.Fatal(m.Value("prompt"))
	}
}

func TestReferenceResolutionPreservesLargeNumbersAndFreezesCanonicalParent(t *testing.T) {
	f := &fakeWriter{err: &kata.Error{Code: "process_error", Message: "CLI died"}}
	j := kata.Job{UID: "job", Name: "name", DefinitionEventUID: "event", Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"work"},"issue":{"kind":"existing","uid":"old"},"overlap":"forbid","catchup":"latest","options":{"opaque":9007199254740993}}`)}
	m := newJobForm(t.Context(), cfg(), f, &j, nil, editorContext{})
	m.Set("issue", strings.Repeat("x", 21)+"#abc4")
	_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m.Update(cmd())
	if !strings.Contains(string(f.saved.Definition), "9007199254740993") || !strings.Contains(string(f.saved.Definition), "01K00000000000000000000001") {
		t.Fatal(string(f.saved.Definition))
	}
	if m.attempt == nil {
		t.Fatal("ambiguous process failure discarded frozen payload")
	}
}
func TestTextareaKeepsCaretVisible(t *testing.T) {
	m := newJobForm(t.Context(), cfg(), &fakeWriter{}, nil, nil, editorContext{})
	for i, f := range m.fields() {
		if f.Key == "prompt" {
			m.index = i
		}
	}
	text := strings.Repeat("paragraph ", 50) + "CARET_END"
	m.Set("prompt", text)
	editorKey(m, "enter")
	m.caret = len([]rune(text))
	if !strings.Contains(m.View(), "CARET_END") {
		t.Fatal("edited text is invisible")
	}
}

func TestJSONNormalizedUTF8SurvivesEdit(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"action": map[string]any{"kind": "execute", "prompt": string([]byte{0xae})}, "custom": "0"})
	j := kata.Job{UID: "job", DefinitionEventUID: "event", Definition: raw}
	m := newJobForm(t.Context(), cfg(), &fakeWriter{}, &j, nil, editorContext{})
	m.Set("name", "changed")
	out, err := m.definition()
	if err != nil {
		t.Fatal(err)
	}
	var before, after any
	json.Unmarshal(raw, &before)
	json.Unmarshal(out, &after)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("JSON-normalized UTF-8 changed")
	}
}

func TestSwitchAwayFromRecurringDoesNotSendHiddenParent(t *testing.T) {
	f := &fakeWriter{}
	m := newJobForm(t.Context(), cfg(), f, nil, nil, editorContext{})
	for k, v := range map[string]string{"mode": "recurring", "name": "draft", "parent": "tasks#parent", "title": "child", "message": "due", "trigger": "interval", "schedule": "59"} {
		m.Set(k, v)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatal("invalid recurring draft saved")
	}
	m.Set("mode", "prompt")
	m.Set("prompt", "work")
	_, cmd = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal(m.View())
	}
	m.Update(cmd())
	if f.parent != "" {
		t.Fatal("hidden parent leaked into execute job", f.parent)
	}
}

func TestNewRecurringSavesWithoutChangingOverlap(t *testing.T) {
	f := &fakeWriter{}
	m := newJobForm(t.Context(), cfg(), f, nil, nil, editorContext{})
	for k, v := range map[string]string{"mode": "recurring", "name": "Delivery", "parent": "tasks#parent", "title": "Child", "message": "Due", "trigger": "interval", "schedule": "60", "catchup": "latest"} {
		m.Set(k, v)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatalf("save refused: %q", m.status)
	}
	m.Update(cmd())
	var saved map[string]any
	if err := json.Unmarshal(f.saved.Definition, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["overlap"] != "allow" || saved["enabled"] != false {
		t.Fatal(string(f.saved.Definition))
	}
}

func TestRenameOwnerOrAuthorNotifyWithoutMessage(t *testing.T) {
	j := kata.Job{UID: "job", Name: "n", DefinitionEventUID: "e", Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"notify","recipient":"current-owner-or-author"},"issue":{"kind":"existing","uid":"01K00000000000000000000001"},"overlap":"forbid","catchup":"latest"}`)}
	f := &fakeWriter{}
	m := newJobForm(t.Context(), cfg(), f, &j, nil, editorContext{})
	m.Set("name", "renamed")
	_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatalf("rename refused: %q", m.status)
	}
	m.Update(cmd())
	if f.saved.Name != "renamed" {
		t.Fatal("rename was not saved")
	}
}

func TestTriggerCyclePreservesCronSchedule(t *testing.T) {
	j := kata.Job{UID: "job", Name: "n", DefinitionEventUID: "e", Definition: json.RawMessage(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"cron","cron":"0 * * * *","timezone":"UTC"},"action":{"kind":"execute","prompt":"work"},"issue":{"kind":"per-run","title":"t"},"overlap":"forbid","catchup":"latest"}`)}
	f := &fakeWriter{}
	m := newJobForm(t.Context(), cfg(), f, &j, nil, editorContext{})
	m.Set("trigger", "interval")
	m.Set("trigger", "cron")
	m.Set("name", "renamed")
	_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatalf("save refused: %q", m.status)
	}
	m.Update(cmd())
	var saved map[string]any
	if err := json.Unmarshal(f.saved.Definition, &saved); err != nil {
		t.Fatal(err)
	}
	trigger := saved["trigger"].(map[string]any)
	if trigger["cron"] != "0 * * * *" || trigger["interval_seconds"] != nil {
		t.Fatal(string(f.saved.Definition))
	}
}

func TestScheduleLabelMatchesTrigger(t *testing.T) {
	for trigger, want := range map[string]string{"cron": "Cron", "interval": "Interval", "once": "Run at"} {
		t.Run(trigger, func(t *testing.T) {
			m := newJobForm(t.Context(), cfg(), &fakeWriter{}, nil, nil, editorContext{})
			m.Set("trigger", trigger)
			for _, f := range m.fields() {
				if f.Key == "schedule" {
					if f.Label != want {
						t.Fatalf("got %q, want %q", f.Label, want)
					}
					return
				}
			}
			t.Fatal("schedule field missing")
		})
	}
}

func TestIssueTitleLabelMatchesMode(t *testing.T) {
	for mode, want := range map[string]string{"prompt": "Issue title", "workflow": "Issue title", "recurring": "Child title"} {
		t.Run(mode, func(t *testing.T) {
			m := newJobForm(t.Context(), cfg(), &fakeWriter{}, nil, nil, editorContext{})
			m.Set("mode", mode)
			for _, f := range m.fields() {
				if f.Key == "title" {
					if f.Label != want {
						t.Fatalf("got %q, want %q", f.Label, want)
					}
					return
				}
			}
			t.Fatal("title field missing")
		})
	}
}

func TestNewProjectDraftUsesAllowedResources(t *testing.T) {
	c := cfg()
	c.Profiles["team"] = config.Profile{Kind: "codex"}
	c.Checkouts["team-work"] = "/srv/projects/example"
	view := c.View(config.Project{Name: "team-project", Inbox: "worker/team", Profiles: []string{"team"}, Checkouts: []string{"team-work"}})
	m := newJobForm(t.Context(), view, &fakeWriter{}, nil, nil, editorContext{})
	if m.Value("profile") != "team" {
		t.Fatalf("new draft selected forbidden default profile: %s", m.View())
	}
	for _, field := range m.fields() {
		switch field.Key {
		case "profile":
			if strings.Join(field.Choices, ",") != "team" {
				t.Fatal(field.Choices)
			}
		case "checkout":
			if strings.Join(field.Choices, ",") != ",team-work" {
				t.Fatal(field.Choices)
			}
		}
	}
	body, err := m.definition()
	if err != nil {
		t.Fatal(err)
	}
	var prompt struct{ Options struct{ Agent string } }
	if err := json.Unmarshal(body, &prompt); err != nil || prompt.Options.Agent != "team" {
		t.Fatal("draft did not persist allowed profile", string(body), err)
	}
	m.Set("mode", "recurring")
	body, err = m.definition()
	if err != nil {
		t.Fatal(err)
	}
	var d struct{ Action struct{ Recipient string } }
	if err := json.Unmarshal(body, &d); err != nil || d.Action.Recipient != "worker/team" {
		t.Fatal(string(body), err)
	}
	empty := c.View(config.Project{Name: "example-project", Profiles: []string{}, Checkouts: []string{}})
	if m := newJobForm(t.Context(), empty, &fakeWriter{}, nil, nil, editorContext{}); m.Value("profile") != "" {
		t.Fatal("empty allowlist selected a profile", m.Value("profile"))
	}
}

func editorKey(m *jobForm, key string) tea.Cmd {
	_, cmd := m.Update(press(key))
	return cmd
}

func chooseField(t *testing.T, m *jobForm, key string) {
	t.Helper()
	for i, f := range m.fields() {
		if f.Key == key {
			m.index = i
			return
		}
	}
	t.Fatal("missing field", key)
}
func TestEditorNavigateAndType(t *testing.T) {
	m := newJobForm(t.Context(), cfg(), &fakeWriter{}, nil, nil, editorContext{})
	editorKey(m, "j")
	if m.Value("name") != "" || m.fields()[m.index].Key != "description" {
		t.Fatal("navigation typed")
	}
	editorKey(m, "k")
	editorKey(m, "enter")
	editorKey(m, "jSk")
	if !strings.Contains(m.View(), "jSk") || m.Value("name") != "" {
		t.Fatal("active text not separate/visible", m.View())
	}
	if editorKey(m, "ctrl+s") != nil || m.Value("name") != "jSk" {
		t.Fatal("field commit saved the job")
	}
	chooseField(t, m, "mode")
	editorKey(m, "right")
	if m.Value("mode") != "workflow" {
		t.Fatal("choice forward")
	}
	editorKey(m, "left")
	if m.Value("mode") != "prompt" {
		t.Fatal("choice backward")
	}
	chooseField(t, m, "enabled")
	editorKey(m, " ")
	if m.Value("enabled") != "false" {
		t.Fatal("bool")
	}
	chooseField(t, m, "prompt")
	editorKey(m, "enter")
	editorKey(m, "work")
	editorKey(m, "enter")
	editorKey(m, "more")
	editorKey(m, "ctrl+s")
	if m.Value("prompt") != "work\nmore" {
		t.Fatal(m.Value("prompt"))
	}
	if editorKey(m, "S") == nil {
		t.Fatal("navigation S did not save", m.View())
	}
}
func TestEditorDirtyExit(t *testing.T) {
	clean := newJobForm(t.Context(), cfg(), &fakeWriter{}, nil, nil, editorContext{})
	if cmd := editorKey(clean, "esc"); cmd == nil {
		t.Fatal("clean exit prompts")
	}
	m := newJobForm(t.Context(), cfg(), &fakeWriter{}, nil, nil, editorContext{})
	editorKey(m, "enter")
	editorKey(m, "example")
	if editorKey(m, "esc") != nil || !strings.Contains(m.View(), "discard changes? y/n") {
		t.Fatal("dirty exit skipped prompt", m.View())
	}
	editorKey(m, "n")
	editorKey(m, " job")
	editorKey(m, "ctrl+s")
	if m.Value("name") != "example job" {
		t.Fatal("discard n lost active input")
	}
	editorKey(m, "esc")
	if cmd := editorKey(m, "y"); cmd == nil {
		t.Fatal("discard did not exit")
	}
	restored := newJobForm(t.Context(), cfg(), &fakeWriter{}, nil, nil, editorContext{})
	restored.Set("description", "changed")
	restored.Set("description", "")
	if editorKey(restored, "q") == nil {
		t.Fatal("restored draft remained dirty")
	}
	m.phase = phaseSaving
	if editorKey(m, "esc") != nil {
		t.Fatal("saving escaped")
	}
}
func TestEditorFieldErrorKeepsBuffer(t *testing.T) {
	m := newJobForm(t.Context(), cfg(), &fakeWriter{}, nil, nil, editorContext{})
	m.Set("trigger", "interval")
	chooseField(t, m, "schedule")
	editorKey(m, "enter")
	editorKey(m, "1.5s")
	editorKey(m, "ctrl+s")
	if m.Value("schedule") == "1.5s" || m.active != "schedule" || m.buffer != "1.5s" || m.caret != 4 || !strings.Contains(m.fieldErrors["schedule"], "whole seconds only") {
		t.Fatalf("failed input lost: active=%s buffer=%q caret=%d errors=%v", m.active, m.buffer, m.caret, m.fieldErrors)
	}
}

func TestEditorReadOnlyAndMouse(t *testing.T) {
	m := newJobForm(t.Context(), cfg(), &fakeWriter{}, nil, nil, editorContext{})
	m.Set("mode", "recurring")
	chooseField(t, m, "overlap")
	editorKey(m, "enter")
	if m.status != "Overlap is read-only: recurring jobs require allow" {
		t.Fatal(m.status)
	}
	index := m.index
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.index != index || m.active != "" {
		t.Fatal("mouse changed editor")
	}
}

func TestEditorHydrationRoundTripAndDiffMode(t *testing.T) {
	for _, tc := range []struct{ action, issue, mode string }{
		{`{"kind":"execute","prompt":"work","workflow_uid":""}`, `{"kind":"per-run","title":"Example"}`, "prompt"},
		{`{"kind":"execute","workflow_uid":"flow"}`, `{"kind":"per-run","title":"Example"}`, "workflow"},
		{`{"kind":"notify","recipient":"worker","message":"due"}`, `{"kind":"existing","uid":"issue"}`, "notify"},
		{`{"kind":"notify","recipient":"worker/inbox","message":"due"}`, `{"kind":"per-run","parent_uid":"parent","title":"Example"}`, "recurring"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			j := kata.Job{UID: "job", Name: "Example", DefinitionEventUID: "event", Definition: []byte(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"interval","interval_seconds":3600},"action":` + tc.action + `,"issue":` + tc.issue + `,"options":{"opaque":9007199254740993},"overlap":"forbid","catchup":"latest"}`)}
			m := newJobForm(t.Context(), cfg(), &fakeWriter{}, &j, nil, editorContext{})
			if m.Value("mode") != tc.mode || m.Value("schedule") != "1h0m0s" || len(m.dirty) != 0 || m.dirtyDraft() {
				t.Fatal("unchanged draft changed", m.values, m.dirty, m.View())
			}
			body, err := m.definition()
			if err != nil || !kata.SameDefinition(j, kata.Job{UID: j.UID, Name: j.Name, Definition: body}) {
				t.Fatal("round trip changed definition", string(body), err)
			}
			a, err := defs.DecodeJob(j.Definition)
			if err != nil {
				t.Fatal(err)
			}
			b := a
			b.Action = defs.Action{Kind: "execute", Prompt: "other"}
			if tc.mode == "prompt" {
				b.Action = defs.Action{Kind: "execute", WorkflowUID: "flow"}
			}
			found := false
			for _, change := range jobform.Diff(a, b, j.Name, j.Name) {
				if change.Field == "action" {
					found = true
					if change.Yours != tc.mode {
						t.Fatal("diff disagrees with form mode", change)
					}
				}
			}
			if !found {
				t.Fatal("mode change missing from diff")
			}
		})
	}
}
