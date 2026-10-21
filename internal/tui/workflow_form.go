package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type workflowWriter interface {
	SaveWorkflow(context.Context, kata.Workflow) (kata.Workflow, error)
	ShowWorkflow(context.Context, string) (kata.Workflow, error)
}
type workflowForm struct {
	ctx                     context.Context
	writer                  workflowWriter
	workflow                kata.Workflow
	cfg                     config.Config
	path, status            string
	pathInfo                os.FileInfo
	width, height, scroll   int
	busy, saved             bool
	check                   workflowCheck
	checkedBody             json.RawMessage
	newer                   kata.Workflow
	newerSummary, newerPath string
	newerInfo               os.FileInfo
	sidecarErr              error
	frozen                  json.RawMessage
}
type workflowMsg struct {
	newer              kata.Workflow
	newerSummary       string
	err                error
	uncertain, blocked bool
	body               json.RawMessage
	check              workflowCheck
	sidecarPath        string
	sidecarInfo        os.FileInfo
	sidecarErr         error
}
type editedMsg struct{ err error }

func newWorkflowForm(ctx context.Context, w workflowWriter, workflow kata.Workflow, cfg config.Config) (*workflowForm, error) {
	f, err := os.CreateTemp("", "herdr-kata-workflow-*.json")
	if err != nil {
		return nil, err
	}
	m := &workflowForm{ctx: ctx, writer: w, workflow: workflow, cfg: cfg, path: f.Name(), width: 80, height: 24, status: "edit the draft in $EDITOR", check: workflowProblems(workflow.Definition, cfg)}
	m.pathInfo, err = f.Stat()
	if err == nil {
		_, err = f.Write(workflow.Definition)
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = m.Close()
		return nil, err
	}
	return m, nil
}

// removeWorkflowFile only removes the regular file this editor created. A
// replaced path, including a symlink, belongs to its new owner.
func removeWorkflowFile(path string, owned os.FileInfo) error {
	if path == "" || owned == nil {
		return nil
	}
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || !os.SameFile(current, owned) {
		return nil
	}
	return os.Remove(path)
}
func (m *workflowForm) Close() error {
	return errors.Join(removeWorkflowFile(m.path, m.pathInfo), removeWorkflowFile(m.newerPath, m.newerInfo))
}

func (m *workflowForm) captureDraft() {
	// A completed editor may have saved by renaming a regular file over the
	// draft. Capture ownership before shutdown can skip editedMsg delivery.
	if info, err := os.Lstat(m.path); err == nil && info.Mode().IsRegular() {
		m.pathInfo = info
	}
}

type workflowEditorCommand struct {
	cmd  *exec.Cmd
	form *workflowForm
}

func (c workflowEditorCommand) Run() error {
	err := c.cmd.Run()
	c.form.captureDraft()
	return err
}
func (c workflowEditorCommand) SetStdin(r io.Reader)  { c.cmd.Stdin = r }
func (c workflowEditorCommand) SetStdout(w io.Writer) { c.cmd.Stdout = w }
func (c workflowEditorCommand) SetStderr(w io.Writer) { c.cmd.Stderr = w }

func (m *workflowForm) edit() tea.Cmd {
	editor := strings.Fields(os.Getenv("EDITOR"))
	if len(editor) == 0 {
		editor = []string{"vi"}
	}
	cmd := exec.CommandContext(m.ctx, editor[0], append(editor[1:], m.path)...)
	return tea.Exec(workflowEditorCommand{cmd: cmd, form: m}, func(err error) tea.Msg { return editedMsg{err} })
}
func (m *workflowForm) Init() tea.Cmd { return m.edit() }

func workflowSidecar(path string, previous os.FileInfo, body []byte) (os.FileInfo, error) {
	if previous != nil {
		if err := removeWorkflowFile(path, previous); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil {
		_, err = f.Write(body)
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = removeWorkflowFile(path, info)
		return nil, err
	}
	return info, nil
}
func newerWorkflowSummary(w kata.Workflow) string {
	summary := w.Name
	if def, err := defs.DecodeWorkflow(w.Definition); err == nil {
		keys := make([]string, 0, min(6, len(def.Steps)))
		for _, s := range def.Steps[:min(6, len(def.Steps))] {
			keys = append(keys, s.Key)
		}
		summary += fmt.Sprintf(" · %d steps: %s", len(def.Steps), strings.Join(keys, ", "))
		if len(def.Steps) > 6 {
			summary += " …"
		}
	}
	return summary
}
func readWorkflowDraft(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	body, readErr := io.ReadAll(io.LimitReader(f, 256*1024+1))
	closeErr := f.Close()
	return body, errors.Join(readErr, closeErr)
}

func (m *workflowForm) save(force bool) tea.Cmd {
	if m.busy {
		return nil
	}
	m.busy = true
	// Freeze every input before handing the command to Bubble Tea. In particular,
	// a retry cannot read a changed draft or substitute a newer event guard.
	ctx, writer, w, cfg, path := m.ctx, m.writer, m.workflow, m.cfg, m.path
	frozen := append(json.RawMessage(nil), m.frozen...)
	priorInfo := m.newerInfo
	return func() tea.Msg {
		body := frozen
		if body == nil {
			var readErr error
			body, readErr = readWorkflowDraft(path)
			if readErr != nil {
				return workflowMsg{err: readErr, blocked: true}
			}
		}
		result := workflowMsg{body: body}
		if len(body) > 256*1024 || !json.Valid(body) {
			result.err = fmt.Errorf("definition: invalid JSON or exceeds 256 KiB")
			result.blocked = true
			return result
		}
		result.check = workflowProblems(body, cfg)
		if !force && (result.check.DecodeErr != nil || len(result.check.Unsupported) > 0) {
			result.err = fmt.Errorf("workflow CHECK needs attention; ctrl+s saves with the original guard")
			result.blocked = true
			return result
		}
		w.Definition = body
		_, result.err = writer.SaveWorkflow(ctx, w)
		if result.err == nil {
			return result
		}
		newer, readErr := writer.ShowWorkflow(ctx, w.UID)
		if readErr == nil && kata.SameDefinition(kata.Job(w), kata.Job(newer)) {
			result.err = nil
			return result
		}
		result.uncertain = uncertain(result.err) || frozen != nil
		if readErr == nil {
			result.newer = newer
			result.newerSummary = newerWorkflowSummary(newer)
			result.sidecarInfo, result.sidecarErr = workflowSidecar(path+".newer.json", priorInfo, newer.Definition)
			if result.sidecarErr == nil {
				result.sidecarPath = path + ".newer.json"
			}
		}
		return result
	}
}
func (m *workflowForm) finish(message string) tea.Cmd {
	if err := m.Close(); err != nil {
		m.status = "cleanup: " + err.Error()
		return nil
	}
	return func() tea.Msg { return editorClosedMsg{Message: message} }
}
func (m *workflowForm) Update(msg tea.Msg) (form, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
	case editedMsg:
		m.captureDraft()
		if v.err != nil {
			m.status = v.err.Error()
		} else {
			body, err := readWorkflowDraft(m.path)
			if err != nil {
				m.status = err.Error()
				return m, nil
			}
			if bytes.Equal(body, m.workflow.Definition) {
				return m, m.finish("workflow unchanged; nothing saved")
			}
			return m, m.save(false)
		}
	case workflowMsg:
		m.busy = false
		if v.body != nil {
			m.check, m.checkedBody = v.check, v.body
		}
		if v.err == nil {
			m.saved = true
			m.status = "workflow saved · shown once the worker refreshes definitions"
			return m, m.finish(m.status)
		}
		m.status = v.err.Error()
		if !v.blocked {
			m.newer, m.newerSummary = v.newer, v.newerSummary
			m.sidecarErr = v.sidecarErr
			if v.sidecarPath != "" {
				m.newerPath, m.newerInfo = v.sidecarPath, v.sidecarInfo
			}
			if v.uncertain {
				m.frozen = append(json.RawMessage(nil), v.body...)
			}
		}
		if m.frozen != nil {
			m.status += " · uncertain outcome; ctrl+s retries the identical draft"
		}
	case tea.KeyPressMsg:
		if m.busy {
			return m, nil
		}
		switch {
		case matchesKey(v.String(), keys.Cancel, keys.Quit):
			return m, m.finish("workflow edit cancelled")
		case matchesKey(v.String(), keys.Edit):
			if m.frozen == nil {
				return m, m.edit()
			}
		case matchesKey(v.String(), keys.Save):
			return m, m.save(true)
		case matchesKey(v.String(), keys.PageDown, keys.HalfDown):
			m.scroll += max(1, m.height/2)
		case matchesKey(v.String(), keys.PageUp, keys.HalfUp):
			m.scroll = max(0, m.scroll-max(1, m.height/2))
		case matchesKey(v.String(), keys.Down, keys.ScrollDown):
			m.scroll++
		case matchesKey(v.String(), keys.Up, keys.ScrollUp):
			m.scroll = max(0, m.scroll-1)
		}
	}
	return m, nil
}
func workflowEditorText(d *detail, text string, width int, role lipgloss.Style) {
	for _, line := range wrapText(text, max(1, width-2)) {
		d.Styled(bodyLine{Text: "  " + role.Render(line)})
	}
}
func (m *workflowForm) View() string {
	d := newDetail(m.width, 14, detailSelection{}, noSelection, nil)
	d.Title("edit workflow "+m.workflow.Name, "", titleStyle)
	d.Subtitle(m.path)
	d.Section("STATUS")
	role := failStyle
	if m.saved {
		role = passStyle
	} else if m.frozen != nil || m.busy {
		role = attentionStyle
	} else if m.status == "edit the draft in $EDITOR" {
		role = subtleStyle
	}
	workflowEditorText(d, m.status, m.width, role)
	d.Section("CHECK")
	if m.check.DecodeErr != nil {
		workflowEditorText(d, "! does not parse: "+m.check.DecodeErr.Error(), m.width, failStyle)
	} else if len(m.checkedBody) > 256*1024 || m.checkedBody != nil && !json.Valid(m.checkedBody) {
		workflowEditorText(d, "! invalid JSON or exceeds 256 KiB", m.width, failStyle)
	} else {
		if len(m.check.Unsupported) == 0 {
			workflowEditorText(d, "local workflow checks passed", m.width, passStyle)
		}
		for _, reason := range m.check.Unsupported {
			workflowEditorText(d, "! unsupported workflow: "+reason, m.width, failStyle)
		}
		for _, warning := range m.check.Warnings {
			workflowEditorText(d, warning, m.width, attentionStyle)
		}
		for _, reason := range m.check.JobDependent {
			workflowEditorText(d, "per job: "+reason, m.width, subtleStyle)
		}
	}
	if m.newerSummary != "" {
		d.Section("NEWER IN KATA")
		workflowEditorText(d, m.newerSummary, m.width, detailMetaStyle)
		if m.sidecarErr != nil {
			workflowEditorText(d, "newer copy unavailable: "+m.sidecarErr.Error(), m.width, failStyle)
		} else if m.newerPath != "" {
			workflowEditorText(d, m.newerPath, m.width, subtleStyle)
		}
	}
	items := []helpItem{hintGroup("e", "edit again", 80, keys.Edit), hintGroup("ctrl+s", "save", 100, keys.Save, keys.SaveAlt), keys.Cancel.hint(1000)}
	help := footerHelp{Full: items, Short: items}
	p := pane{Body: d.Body(), Bottom: bodyLines{{Text: help.Render(m.width)}}, Width: m.width, Scroll: &m.scroll}
	return p.Render(m.height).Text
}

func workflowFormForLane(ctx context.Context, lane Lane, w kata.Workflow) form {
	m, err := newWorkflowForm(ctx, lane.Service, w, lane.Config)
	if err != nil {
		return errorForm{err}
	}
	return m
}

type errorForm struct{ err error }

func (m errorForm) Init() tea.Cmd {
	return func() tea.Msg { return editorClosedMsg{Message: m.err.Error()} }
}
func (m errorForm) Update(tea.Msg) (form, tea.Cmd) { return m, nil }
func (m errorForm) View() string                   { return m.err.Error() }
