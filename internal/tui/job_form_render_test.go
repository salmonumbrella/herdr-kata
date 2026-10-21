package tui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

func TestEditorMeasuredWindow(t *testing.T) {
	c := cfg()
	c.Kata.Project = "example-project"
	m := newJobForm(t.Context(), c, &fakeWriter{}, nil, nil, editorContext{})
	m.width, m.height = 120, 40
	text := ansi.Strip(m.View())
	if !strings.HasPrefix(text, "new job · project example-project\n\n▶ Name") || strings.Count(text, "project example-project") != 1 {
		t.Fatal(text)
	}
	if !strings.Contains(text, "▶ Name               —") || !strings.Contains(text, "required; must be unique in the project") || !strings.Contains(text, "j/k move") {
		t.Fatal(text)
	}
	m.Set("prompt", "first\nsecond\nthird")
	if !strings.Contains(m.View(), "(+2 lines)") {
		t.Fatal("textarea summary", m.View())
	}
}
func TestEditorBannerFocus(t *testing.T) {
	m := newJobForm(t.Context(), cfg(), &fakeWriter{}, nil, nil, editorContext{})
	chooseField(t, m, "favorite")
	_ = m.View()
	editorKey(m, "S")
	if !strings.Contains(m.View(), "name: required") || m.scroll != 0 {
		t.Fatal("save error hidden", m.View())
	}
	editorKey(m, "k")
	if !strings.Contains(m.View(), "Enabled") || m.scroll == 0 {
		t.Fatal("movement retained banner focus", m.View())
	}
}
func TestEditorCaretVisible(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {40, 12}, {80, 6}} {
		m := newJobForm(t.Context(), cfg(), &fakeWriter{}, nil, nil, editorContext{})
		m.width, m.height = size[0], size[1]
		m.Set("body", strings.Repeat("界é example ", 100)+"CARET_END")
		chooseField(t, m, "body")
		editorKey(m, "enter")
		text := m.View()
		if !strings.Contains(text, "▏") || !strings.Contains(text, "CARET_END") {
			t.Fatalf("caret hidden at %v: %s", size, text)
		}
		if len(strings.Split(text, "\n")) > size[1] {
			t.Fatal("height")
		}
		for line := range strings.SplitSeq(text, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Fatal("width", line)
			}
		}
		m.Update(tea.WindowSizeMsg{Width: 30, Height: 9})
		if !strings.Contains(m.View(), "▏") {
			t.Fatal("resize hid caret")
		}
	}
}
func TestEditorConflictPresentation(t *testing.T) {
	j := kata.Job{UID: "job", Name: "example-job", DefinitionEventUID: "old-event", Definition: []byte(`{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"work"},"issue":{"kind":"per-run","title":"example"},"overlap":"forbid","catchup":"latest"}`)}
	m := newJobForm(t.Context(), cfg(), &fakeWriter{}, &j, nil, editorContext{})
	chooseField(t, m, "favorite")
	newer := j
	newer.Name = "renamed"
	newer.DefinitionEventUID = "new-event"
	m.Update(savedMsg{newer: &newer, err: errors.New("conflict")})
	text := ansi.Strip(m.View())
	if !strings.Contains(text, "Kata has a newer definition") || !strings.Contains(text, "yours: example-job") || !strings.Contains(text, "Kata: renamed") || strings.Contains(text, `"version"`) {
		t.Fatal(text)
	}
	m.newer = nil
	m.phase = phaseUncertain
	m.attempt = &saveAttempt{job: j}
	m.status = "offline"
	m.bannerFocus = true
	if text := m.View(); !strings.Contains(text, "Readback unavailable") || !strings.Contains(text, "outcome uncertain") || strings.Contains(text, "old-event") || !strings.Contains(text, "example-job") {
		t.Fatal(text)
	}
}

func TestEditorProfileHelpRoles(t *testing.T) {
	c := cfg()
	c.Profiles["default"] = config.Profile{Kind: "codex", Args: []string{"--sandbox", "danger-full-access", "--ask-for-approval", "never"}}
	m := newJobForm(t.Context(), c, &fakeWriter{}, nil, nil, editorContext{})
	m.width, m.height = 120, 40
	chooseField(t, m, "profile")
	assertFrames(t, "job-form", "profile-facts", [][2]int{{120, 40}}, func(int, int) string {
		text := m.View()
		if !strings.Contains(text, failStyle.Render("danger-full-access")) || !strings.Contains(ansi.Strip(text), "1 planned / 12 cap") {
			t.Fatal("profile help roles/budget", text)
		}
		return text
	})
}

func TestEditorProfileModelPicker(t *testing.T) {
	c := cfg()
	c.Profiles["careful"] = config.Profile{Kind: "codex", Args: []string{"--model", "example-model", "-c", `model_reasoning_effort="high"`}}
	c.Profiles["quick"] = config.Profile{Kind: "claude", Args: []string{"--model", "other-model", "--effort", "low"}}
	c = c.View(config.Project{Name: "example-project", Profiles: []string{"quick", "careful"}})
	w := &fakeWriter{}
	m := newJobForm(t.Context(), c, w, nil, nil, editorContext{ProjectUID: "project"})
	m.width, m.height = 120, 40
	m.Set("name", "example-job")
	m.Set("prompt", "work")
	chooseField(t, m, "profile")
	for _, tc := range []struct{ key, want string }{
		{"", "careful · example-model high"},
		{"enter", "quick · other-model low"},
		{"right", "careful · example-model high"},
		{"left", "quick · other-model low"},
		{"h", "careful · example-model high"},
		{"l", "quick · other-model low"},
	} {
		if tc.key != "" {
			editorKey(m, tc.key)
		}
		if text := ansi.Strip(m.View()); !strings.Contains(text, "▶ Profile            "+tc.want) {
			t.Fatalf("after %q, want %q:\n%s", tc.key, tc.want, text)
		}
	}
	assertFrames(t, "job-form", "profile-model-picker", [][2]int{{120, 40}, {80, 24}}, func(w, h int) string {
		m.width, m.height = w, h
		return m.View()
	})
	completeSave(t, m)
	var saved struct{ Options map[string]any }
	if err := json.Unmarshal(w.saved.Definition, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Options) != 1 || saved.Options["agent"] != "quick" {
		t.Fatalf("save must retain the profile key as the execution choice: %s", w.saved.Definition)
	}
}

func TestEditorNativeReadOnlyHelp(t *testing.T) {
	m := editorGolden(t, "recurring-native", 120, 40)
	if !strings.Contains(ansi.Strip(m.View()), "fixed; b edits the binding") {
		t.Fatal(m.View())
	}
}
func TestEditorFrozenTextareaSummary(t *testing.T) {
	m := editorGolden(t, "edit-prompt", 120, 40)
	job := m.job
	m.phase = phaseUncertain
	m.attempt = &saveAttempt{job: job}
	if !strings.Contains(ansi.Strip(m.View()), "(+1 lines)") {
		t.Fatal(m.View())
	}
}

func TestEditorIdentifierChromeUsesNames(t *testing.T) {
	const uid = "01HZNQ7VFPK1XGD8R5MABCD4EX"
	j := kata.Job{UID: uid, DefinitionEventUID: uid, Name: "Example job", Definition: []byte(`{"enabled":true}`)}
	m := newJobForm(t.Context(), cfg(), &fakeWriter{}, &j, nil, editorContext{})
	m.phase = phaseUncertain
	m.attempt = &saveAttempt{job: j}
	text := ansi.Strip(m.View())
	if !strings.Contains(text, "Readback unavailable · Example job") || strings.Contains(text, uid) {
		t.Fatal(text)
	}
	w, err := newWorkflowForm(t.Context(), nil, kata.Workflow{UID: uid, DefinitionEventUID: uid, Name: "Example workflow", Definition: []byte(`{"version":1,"steps":[]}`)}, cfg())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	text = ansi.Strip(w.View())
	if !strings.Contains(text, "edit workflow Example workflow") || strings.Contains(text, uid) {
		t.Fatal(text)
	}
}

func TestIssueFieldEditUsesReferenceAndPreservesUnchangedBinding(t *testing.T) {
	const uid = "01K00000000000000000000001"
	for _, known := range []bool{true, false} {
		j := kata.Job{Name: "Example job", Definition: []byte(`{"action":{"kind":"execute","prompt":"work"},"issue":{"kind":"existing","uid":"` + uid + `"}}`)}
		ec := editorContext{IssueReference: func(string) string {
			if known {
				return "example-project#00001"
			}
			return ""
		}}
		m := newJobForm(t.Context(), cfg(), &fakeWriter{}, &j, nil, ec)
		m.width, m.height = 120, 60
		chooseField(t, m, "issue")
		editorKey(m, "enter")
		text := ansi.Strip(m.View())
		if strings.Contains(text, uid) || known && !strings.Contains(text, "example-project#00001") {
			t.Fatal(text)
		}
		editorKey(m, "enter")
		if got := m.Value("issue"); got != uid || m.dirty["issue"] {
			t.Fatalf("unchanged binding edited: %q dirty=%v", got, m.dirty)
		}
	}
}

func TestEditorUncertainInstructionsWrap(t *testing.T) {
	for _, width := range []int{120, 80, 40} {
		m := editorGolden(t, "edit-prompt", width, 24)
		m.Update(savedMsg{job: m.job, uncertain: true, err: errors.New(strings.Repeat("transport unavailable ", 40))})
		text := ansi.Strip(m.View())
		// Instructions precede unbounded transport details, so even a tall
		// diagnostic cannot push the recovery actions out of the viewport.
		words := strings.Join(strings.Fields(text), " ")
		for _, want := range []string{"ctrl+s reads back and retries the same draft", "esc asks to discard this draft"} {
			if !strings.Contains(words, want) {
				t.Fatalf("width %d missing %q:\n%s", width, want, text)
			}
		}
		for line := range strings.SplitSeq(text, "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatalf("width %d overflow: %q", width, line)
			}
		}
	}
}

func TestEditorConflictHelpMatchesActions(t *testing.T) {
	for _, readback := range []bool{false, true} {
		m := editorGolden(t, "edit-prompt", 120, 80)
		m.Set("description", "local draft")
		msg := savedMsg{err: &kata.Error{Code: "conflict"}}
		if readback {
			newer := m.job
			newer.Name = "newer"
			msg.newer = &newer
		}
		m.Update(msg)
		text := ansi.Strip(m.View())
		if strings.Contains(text, "ctrl+s save job") || strings.Contains(text, "esc abandons this draft") || !strings.Contains(text, "esc asks to discard this draft") {
			t.Fatal("misleading conflict help:\n" + text)
		}
		if cmd := editorKey(m, "ctrl+s"); cmd != nil {
			t.Fatal("known conflict retried")
		}
		editorKey(m, "esc")
		if !strings.Contains(ansi.Strip(m.View()), "discard changes? y/n") {
			t.Fatal("discard confirmation missing", m.View())
		}
	}
}

func TestEditorSavingHelpMatchesActions(t *testing.T) {
	m := editorGolden(t, "edit-prompt", 120, 80)
	m.phase = phaseSaving
	text := ansi.Strip(m.View())
	help := text[strings.LastIndex(text, "\n")+1:]
	if !strings.Contains(text, "saving…") || !strings.Contains(help, "ctrl+c quit") {
		t.Fatal("saving state must show the available interruption key:\n" + text)
	}
	for _, unavailable := range []string{"j/k move", "ctrl+s", "esc", "enter edit", "space toggle"} {
		if strings.Contains(help, unavailable) {
			t.Fatalf("saving help advertises unavailable %q:\n%s", unavailable, text)
		}
	}
}

func TestEditorOrdinarySaveErrorWrapsDifferingValues(t *testing.T) {
	const diagnosis = `kata board_route_refused (exit 6): pinned Kata identity, route or executor differs: kata.bin (pinned="/srv/tools/kata-pinned", current="/srv/tools/kata-current")`
	for _, width := range []int{120, 80, 40} {
		m := editorGolden(t, "edit-prompt", width, 24)
		chooseField(t, m, "favorite")
		_ = m.View()
		m.Update(savedMsg{err: errors.New(diagnosis)})
		text := ansi.Strip(m.View())
		words := strings.Join(strings.Fields(text), "")
		if !strings.Contains(words, strings.Join(strings.Fields(diagnosis), "")) {
			t.Fatalf("width %d hid save refusal values:\n%s", width, text)
		}
		for line := range strings.SplitSeq(text, "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatalf("width %d overflow: %q", width, line)
			}
		}
	}
}
