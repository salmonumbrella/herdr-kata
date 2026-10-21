package tui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type workflowFake struct {
	guard string
	err   error
}

func (f *workflowFake) SaveWorkflow(_ context.Context, w kata.Workflow) (kata.Workflow, error) {
	f.guard = w.DefinitionEventUID
	return w, f.err
}
func (f *workflowFake) ShowWorkflow(context.Context, string) (kata.Workflow, error) {
	return kata.Workflow{Name: "newer", Definition: json.RawMessage(`{"version":1}`)}, nil
}
func TestWorkflowConflictRetainsFileAndGuard(t *testing.T) {
	f := &workflowFake{err: &kata.Error{Code: "conflict"}}
	w := kata.Workflow{UID: "flow", DefinitionEventUID: "event", Name: "Flow", Definition: json.RawMessage(`{"version":1,"steps":[]}`)}
	m, err := newWorkflowForm(t.Context(), f, w, config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	msg := m.save(true)()
	m.Update(msg)
	if _, err := os.Stat(m.path); err != nil || f.guard != "event" || m.newer.Name != "newer" {
		t.Fatal(err, f.guard, m.newer)
	}
}

const checkedWorkflow = `{"version":1,"steps":[{"key":"main","kind":"prompt","prompt":"Work"}]}`

type checkedWorkflowWriter struct {
	sent             []kata.Workflow
	saveErr, readErr error
	newer            kata.Workflow
	reads            []string
}

func (f *checkedWorkflowWriter) SaveWorkflow(_ context.Context, w kata.Workflow) (kata.Workflow, error) {
	w.Definition = append(json.RawMessage(nil), w.Definition...)
	f.sent = append(f.sent, w)
	return w, f.saveErr
}
func (f *checkedWorkflowWriter) ShowWorkflow(_ context.Context, uid string) (kata.Workflow, error) {
	f.reads = append(f.reads, uid)
	return f.newer, f.readErr
}
func checkedEditor(t *testing.T, f *checkedWorkflowWriter, raw string) *workflowForm {
	t.Helper()
	m, err := newWorkflowForm(t.Context(), f, kata.Workflow{UID: "flow", Name: "Example", DefinitionEventUID: "original", Definition: json.RawMessage(raw)}, config.Config{Profiles: map[string]config.Profile{"default": {}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}
func workflowEditorKey(m *workflowForm, key string) tea.Cmd {
	_, cmd := m.Update(press(key))
	return cmd
}
func applyEditorCommand(t *testing.T, m *workflowForm, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	if cmd == nil {
		t.Fatal("missing editor command")
	}
	_, next := m.Update(cmd())
	return next
}
func TestWorkflowEditorChecks(t *testing.T) {
	for _, tt := range []struct {
		name, raw, diagnosis string
		auto, forced         bool
	}{
		{"supported", checkedWorkflow, "local workflow checks passed", true, true},
		{"warning", strings.Replace(checkedWorkflow, `"version":1`, `"version":1,"options":{"future":true}`, 1), "ignored options key: future", true, true},
		{"unsupported", strings.Replace(checkedWorkflow, `"version":1`, `"version":1,"options":{"on_failure":"retry"}`, 1), "options.on_failure supports only park workflows", false, true},
		{"null-input", `{"input":null}`, "does not parse:", false, true},
		{"array", `[]`, "does not parse:", false, true},
		{"typed-steps", `{"steps":"x"}`, "does not parse:", false, true},
		{"invalid", `{`, "invalid JSON", false, false},
		{"oversize", `{"padding":"` + strings.Repeat("x", 256*1024) + `"}`, "256 KiB", false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, force := range []bool{false, true} {
				f := &checkedWorkflowWriter{}
				m := checkedEditor(t, f, tt.raw)
				applyEditorCommand(t, m, m.save(force))
				want := 0
				if force && tt.forced || !force && tt.auto {
					want = 1
				}
				if len(f.sent) != want || len(f.reads) != 0 {
					t.Fatalf("force=%v sent=%v reads=%v", force, f.sent, f.reads)
				}
				if want == 1 && (string(f.sent[0].Definition) != tt.raw || f.sent[0].DefinitionEventUID != "original") {
					t.Fatal("draft or guard changed", f.sent)
				}
				if !strings.Contains(ansi.Strip(m.View()), tt.diagnosis) {
					t.Fatalf("missing %q: %s", tt.diagnosis, m.View())
				}
			}
		})
	}
	t.Run("job-dependent auto-saves", func(t *testing.T) {
		f := &checkedWorkflowWriter{}
		m := checkedEditor(t, f, checkedWorkflow)
		m.cfg.Profiles = nil
		applyEditorCommand(t, m, m.save(false))
		if len(f.sent) != 1 || !strings.Contains(ansi.Strip(m.View()), "unmapped profile") {
			t.Fatal(f.sent, m.View())
		}
	})
}
func TestWorkflowEditorGuard(t *testing.T) {
	f := &checkedWorkflowWriter{saveErr: &kata.Error{Code: "conflict"}, newer: kata.Workflow{Name: "Newer", DefinitionEventUID: "new-event", Definition: json.RawMessage(checkedWorkflow)}}
	raw := " \n" + strings.Replace(checkedWorkflow, `"version":1`, `"version":1,"unknown":{"preserve":true}`, 1) + "\n"
	m := checkedEditor(t, f, raw)
	cmd := m.save(true)
	if len(f.sent) != 0 {
		t.Fatal("save ran before command")
	}
	msg := cmd()
	if len(f.sent) != 1 || f.sent[0].DefinitionEventUID != "original" || string(f.sent[0].Definition) != raw {
		t.Fatal(f.sent)
	}
	if _, err := os.Stat(m.path + ".newer.json"); err != nil {
		t.Fatal("sidecar not created in command", err)
	}
	_, _ = m.Update(msg)
	if m.newerPath != m.path+".newer.json" || strings.Contains(m.View(), checkedWorkflow) {
		t.Fatal(m.newerPath, m.View())
	}
	info, err := os.Stat(m.path + ".newer.json")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	f.newer.DefinitionEventUID = "newest-event"
	f.newer.Definition = json.RawMessage(`{"version":1,"steps":[]}`)
	applyEditorCommand(t, m, m.save(true))
	if len(f.sent) != 2 || f.sent[1].DefinitionEventUID != "original" {
		t.Fatal(f.sent)
	}
	b, err := os.ReadFile(m.path + ".newer.json")
	if err != nil || string(b) != string(f.newer.Definition) {
		t.Fatal(string(b), err)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	if err = m.Close(); err != nil {
		t.Fatal("repeated Close", err)
	}
	for _, p := range []string{m.path, m.path + ".newer.json"} {
		if _, err = os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("owned file remains", p, err)
		}
	}
}
func TestWorkflowEditorUncertain(t *testing.T) {
	f := &checkedWorkflowWriter{saveErr: context.DeadlineExceeded, readErr: errors.New("unavailable")}
	m := checkedEditor(t, f, checkedWorkflow)
	cmd := m.save(true)
	if again := workflowEditorKey(m, "ctrl+s"); again != nil {
		t.Fatal("busy save duplicated")
	}
	applyEditorCommand(t, m, cmd)
	if err := os.WriteFile(m.path, []byte(`{"different":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if cmd := workflowEditorKey(m, "e"); cmd != nil {
		t.Fatal("uncertain editor reopened")
	}
	applyEditorCommand(t, m, workflowEditorKey(m, "ctrl+s"))
	if len(f.sent) != 2 || string(f.sent[1].Definition) != checkedWorkflow || f.sent[1].DefinitionEventUID != "original" || !strings.Contains(ansi.Strip(m.View()), "uncertain outcome") {
		t.Fatal(f.sent, m.View())
	}
	// Even a later definitive refusal cannot make an uncertain write editable.
	f.saveErr = &kata.Error{Code: "conflict"}
	applyEditorCommand(t, m, workflowEditorKey(m, "ctrl+s"))
	if cmd := workflowEditorKey(m, "e"); cmd != nil {
		t.Fatal("failed retry cleared frozen body")
	}
	f.saveErr = nil
	next := applyEditorCommand(t, m, workflowEditorKey(m, "ctrl+s"))
	closed, ok := next().(editorClosedMsg)
	if !ok || closed.Message != "workflow saved · shown once the worker refreshes definitions" {
		t.Fatal(closed)
	}
}
func TestWorkflowEditorCleanup(t *testing.T) {
	for _, kind := range []string{"foreign-file", "foreign-link", "replaced-file", "replaced-draft"} {
		t.Run(kind, func(t *testing.T) {
			f := &checkedWorkflowWriter{saveErr: &kata.Error{Code: "conflict"}, newer: kata.Workflow{Name: "Newer", DefinitionEventUID: "new", Definition: json.RawMessage(`{"version":1}`)}}
			m := checkedEditor(t, f, checkedWorkflow)
			p := m.path + ".newer.json"
			sentinel := filepath.Join(t.TempDir(), "sentinel")
			if err := os.WriteFile(sentinel, []byte("foreign"), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "replaced-file" || kind == "replaced-draft" {
				applyEditorCommand(t, m, m.save(true))
			}
			if kind == "replaced-draft" {
				p = m.path
			}
			if kind == "replaced-file" || kind == "replaced-draft" {
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "foreign-link" {
				if err := os.Symlink(sentinel, p); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Link(sentinel, p); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "replaced-draft" {
				msg := m.save(true)().(workflowMsg)
				if msg.sidecarPath != "" || msg.sidecarInfo != nil || msg.sidecarErr == nil {
					t.Fatal("foreign path advertised as an owned sidecar", msg)
				}
				m.Update(msg)
			}
			cancel := workflowEditorKey(m, "esc")
			if cancel == nil {
				t.Fatal("foreign replacement prevented cancellation", m.View())
			}
			if closed, ok := cancel().(editorClosedMsg); !ok || closed.Message != "workflow edit cancelled" {
				t.Fatal("cancel did not return to the board", closed)
			}
			b, err := os.ReadFile(p)
			if err != nil || string(b) != "foreign" {
				t.Fatal("foreign file modified", string(b), err)
			}
			b, err = os.ReadFile(sentinel)
			if err != nil || string(b) != "foreign" {
				t.Fatal("symlink/hardlink target modified", string(b), err)
			}
			_ = os.Remove(p)
		})
	}
}

func TestWorkflowEditorAtomicDraftReplacement(t *testing.T) {
	for _, editorErr := range []error{nil, errors.New("editor exited")} {
		f := &checkedWorkflowWriter{}
		m := checkedEditor(t, f, checkedWorkflow)
		replacement := filepath.Join(t.TempDir(), "edited.json")
		raw := strings.Replace(checkedWorkflow, "Work", "Edited atomically", 1)
		if err := os.WriteFile(replacement, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, m.path); err != nil {
			t.Fatal(err)
		}
		_, save := m.Update(editedMsg{err: editorErr})
		var close tea.Cmd
		if editorErr == nil {
			close = applyEditorCommand(t, m, save)
			if len(f.sent) != 1 || string(f.sent[0].Definition) != raw || f.sent[0].DefinitionEventUID != "original" {
				t.Fatal(f.sent)
			}
		} else {
			close = workflowEditorKey(m, "esc")
		}
		if close == nil {
			t.Fatal("editor did not close")
		}
		if _, err := os.Stat(m.path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("atomic editor replacement was not cleaned up", err)
		}
	}
}

func TestWorkflowEditorGolden(t *testing.T) {
	for _, state := range []string{"idle", "saved", "unsupported", "warning", "invalid", "conflict", "uncertain"} {
		t.Run(state, func(t *testing.T) {
			for _, size := range []tea.WindowSizeMsg{{Width: 120, Height: 40}, {Width: 80, Height: 24}} {
				f := &checkedWorkflowWriter{}
				raw := checkedWorkflow
				switch state {
				case "unsupported":
					raw = strings.Replace(raw, `"version":1`, `"version":1,"options":{"on_failure":"retry"}`, 1)
				case "warning":
					raw = strings.Replace(raw, `"version":1`, `"version":1,"options":{"future":true}`, 1)
				case "invalid":
					raw = "{"
				case "conflict":
					f.saveErr = &kata.Error{Code: "conflict", Message: "definition changed"}
					f.newer = kata.Workflow{Name: "Updated example", DefinitionEventUID: "new-event", Definition: json.RawMessage(checkedWorkflow)}
				case "uncertain":
					f.saveErr = context.DeadlineExceeded
					f.readErr = errors.New("unavailable")
				}
				m := checkedEditor(t, f, raw)
				m.Update(size)
				if state != "idle" {
					applyEditorCommand(t, m, m.save(false))
				}
				assertFrames(t, "workflow-form", state, [][2]int{{size.Width, size.Height}}, func(int, int) string {
					path, newer := m.path, m.newerPath
					defer func() { m.path, m.newerPath = path, newer }()
					m.path = "/tmp/workflow-draft.json"
					if newer != "" {
						m.newerPath = m.path + ".newer.json"
					}
					return m.View()
				})
			}
		})
	}
}
func TestWorkflowEditorReadback(t *testing.T) {
	f := &checkedWorkflowWriter{saveErr: context.DeadlineExceeded, newer: kata.Workflow{UID: "flow", Name: "Example", Definition: json.RawMessage(checkedWorkflow)}}
	m := checkedEditor(t, f, checkedWorkflow)
	next := applyEditorCommand(t, m, m.save(true))
	if next == nil {
		t.Fatal("equal readback did not finish")
	}
	if len(f.sent) != 1 || len(f.reads) != 1 || f.reads[0] != "flow" {
		t.Fatal(f.sent, f.reads)
	}
}
func TestWorkflowEditorFrame(t *testing.T) {
	f := &checkedWorkflowWriter{saveErr: errors.New("failed\x1b[31m\n" + strings.Repeat("diagnosis ", 100))}
	m := checkedEditor(t, f, strings.Replace(checkedWorkflow, "Work", `Work\u001b[31m`, 1))
	applyEditorCommand(t, m, m.save(true))
	for _, size := range []tea.WindowSizeMsg{{Width: 120, Height: 40}, {Width: 80, Height: 24}, {Width: 24, Height: 8}} {
		m.Update(size)
		v := ansi.Strip(m.View())
		lines := strings.Split(v, "\n")
		if len(lines) != size.Height {
			t.Fatal("height", len(lines), size)
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size.Width {
				t.Fatal("width", line)
			}
		}
		if size.Width >= 80 && lines[len(lines)-1] != "e edit again · ctrl+s save · esc cancel" {
			t.Fatal("help", lines[len(lines)-1])
		}
	}
}

func TestWorkflowEditorPageKeysScrollCheck(t *testing.T) {
	for _, keys := range []struct {
		name     string
		down, up tea.KeyPressMsg
	}{{"page keys", press("pgdown"), press("pgup")}, {"control keys", press("ctrl+d"), press("ctrl+u")}} {
		t.Run(keys.name, func(t *testing.T) {
			raw := strings.Replace(checkedWorkflow, `"prompt":"Work"`, `"prompt":"Work","after":["`+strings.Repeat("example-missing-step-", 120)+`"]`, 1)
			m := checkedEditor(t, &checkedWorkflowWriter{}, raw)
			m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			applyEditorCommand(t, m, m.save(false))
			before := ansi.Strip(m.View())
			if !strings.Contains(before, "Check") || !strings.Contains(before, "↓") {
				t.Fatal("CHECK fixture does not overflow", before)
			}
			m.Update(keys.down)
			after := ansi.Strip(m.View())
			if after == before || !strings.Contains(after, "↑") {
				t.Fatal("CHECK did not scroll down", after)
			}
			if !strings.HasSuffix(after, "e edit again · ctrl+s save · esc cancel") {
				t.Fatal("help moved while scrolling", after)
			}
			m.Update(keys.up)
			if got := ansi.Strip(m.View()); got != before {
				t.Fatal("CHECK did not scroll back", got)
			}
		})
	}
}

func TestWorkflowEditorUnchangedRoundTrip(t *testing.T) {
	for _, raw := range []string{checkedWorkflow, `{"version":1,"steps":[]}`} {
		t.Run(raw, func(t *testing.T) {
			f := &checkedWorkflowWriter{}
			m := checkedEditor(t, f, raw)
			_, cmd := m.Update(editedMsg{})
			if cmd == nil {
				t.Fatal("unchanged editor exit did not acknowledge completion")
			}
			msg := cmd()
			// Save commands return workflowMsg first; process it to expose
			// the write count and the acknowledgement a user would see.
			if _, ok := msg.(workflowMsg); ok {
				_, cmd = m.Update(msg)
				if cmd != nil {
					msg = cmd()
				}
			}
			closed, ok := msg.(editorClosedMsg)
			if len(f.sent) != 0 || len(f.reads) != 0 || !ok || closed.Message != "workflow unchanged; nothing saved" {
				t.Fatalf("writes=%d reads=%d completion=%#v", len(f.sent), len(f.reads), msg)
			}
			if _, err := os.Stat(m.path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unchanged draft not cleaned up", err)
			}
		})
	}
}
