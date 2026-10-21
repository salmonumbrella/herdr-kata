package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/agentlimit"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"github.com/salmonumbrella/herdr-kata/internal/usage"
)

func u1Golden(name string, width, height int) *model {
	m := goldenFixture("chrome", width, height)
	m.tab = 1
	m.data.Route = map[string]any{"allowed": true}
	m.artifactScanner = func(state.FS, string, string) artifactListing {
		return artifactListing{Paths: []string{"/state/runs/run-a/run.json", "/state/runs/run-a/steps/verify/1/stderr.log", "/state/log/herdr-kata.log"}}
	}
	r := m.data.Runs[0]
	r.Job.Name = "Example build"
	r.Phase = runner.Held
	r.Reason = "step_failed"
	r.Outcome = "failed"
	r.ActiveStep = "verify"
	r.PromptCap = 12
	r.PromptsSent = 3
	r.WFDef = &defs.WorkflowDef{Steps: []defs.Step{{Key: "plan", Kind: "prompt"}, {Key: "verify", Kind: "command"}, {Key: "publish", Kind: "prompt"}}}
	r.Steps = []runner.StepState{{Key: "plan", Kind: "prompt", Attempt: 1, Status: "succeeded", Result: &runner.ResultRef{Summary: "An unrelated earlier success"}}, {Key: "verify", Kind: "command", Attempt: 2, Status: "failed", Reason: "command verify exited 1", Receipt: &runner.Receipt{ExitCode: 1, StartedAt: runNow.Add(-time.Minute), EndedAt: runNow}, Note: "Inspect the command output before continuing."}, {Key: "publish", Kind: "prompt", Attempt: 1}}
	r.PastAttempts = []runner.StepState{{Key: "verify", Kind: "command", Attempt: 1, Reason: "no_activity"}}
	m.data.Runs = []runner.Run{r}
	m.expanded[r.UID] = true
	switch name {
	case "labels", "labels-p2":
		m.data.Runs = nil
		for i, c := range runLabelFixture() {
			r := c.Run
			r.UID = fmt.Sprintf("run-%02d", i)
			r.Job.Name = "Example " + c.Name
			m.data.Runs = append(m.data.Runs, r)
		}
	case "pages-1", "pages-2":
		for i := 1; i < 348; i++ {
			copy := r
			copy.UID = fmt.Sprintf("run-%03d", i)
			copy.Job.Name = fmt.Sprintf("Example job %03d", i)
			m.data.Runs = append(m.data.Runs, copy)
		}
	case "empty":
		m.query = "no matching text"
	case "multi":
		m.opts.Projects = map[string]*Lane{"example-project": {}, "other-project": {}}
		m.data.Projects = map[string]Data{"p": {Project: "example-project"}}
		m.data.Runs[0].Target.Identity.ProjectUID = "p"
	case "route":
		m.data.Route = map[string]any{"allowed": false, "error": "route refused"}
		m.notice.Text = "local snapshot retained"
	case "overflow-0", "overflow-6", "overflow-7":
		m.data.Runs[0].Job.Name = strings.Repeat("Long example name ", 8)
		m.data.Runs[0].WFDef = nil
		m.data.Runs[0].Phase = runner.Settled
		m.data.Runs[0].Reason = ""
		m.data.Runs[0].Steps = []runner.StepState{{Note: strings.Repeat("A long Unicode 診断 message ", 12)}}
		copy := m.data.Runs[0]
		copy.UID = "run-b"
		m.data.Runs = append(m.data.Runs, copy)
	case "tall":
		for i := range 60 {
			m.data.Runs[0].Steps = append(m.data.Runs[0].Steps, runner.StepState{Key: fmt.Sprintf("step-%02d", i), Kind: "command"})
		}
	case "no-result":
		r.Reason = "no_result"
		r.ActiveStep = "main"
		r.WFDef = nil
		r.PastAttempts = nil
		r.Steps = []runner.StepState{{Key: "main", Kind: "prompt", Attempt: 1, Submission: runner.Accepted}}
		m.data.Runs[0] = r
	case "attempts":
		r.Phase = runner.Running
		r.Reason = ""
		r.Steps[1].Status = ""
		r.Steps[1].Attempt = 3
		r.Steps[1].Receipt = nil
		r.Steps[1].Submission = runner.Sending
		r.PastAttempts = []runner.StepState{{Key: "verify", Kind: "command", Attempt: 1, Limit: &agentlimit.Limit{Kind: "agent_quota"}}, {Key: "verify", Kind: "command", Attempt: 2, Reason: "no_activity"}}
		m.data.Runs[0] = r
	case "usage":
		r.Phase, r.Outcome = runner.Settled, "succeeded"
		r.Reason = ""
		r.EndedAt = &runNow
		r.WFDef = nil
		r.PastAttempts = nil
		r.Steps = []runner.StepState{{Key: "main", Kind: "prompt", Attempt: 1, Status: "succeeded"}}
		cost := int64(1234567)
		r.Usage = &usage.Usage{InputTokens: 1200, OutputTokens: 45, CacheReadTokens: 6, CacheCreationTokens: 7, CostMicroUSD: &cost, Found: true}
		m.data.Runs[0] = r
	case "prompt":
		r.Phase = runner.Settled
		r.Outcome = "succeeded"
		r.WFDef = nil
		r.PastAttempts = nil
		r.Steps = []runner.StepState{{Key: "main", Kind: "prompt", Attempt: 1, Status: "succeeded", Result: &runner.ResultRef{Summary: "The example is ready.", Evidence: []string{"Verified the example output"}}}}
		r.EndedAt = &runNow
		r.Herdr.PaneID = "pane-1"
		m.data.Runs[0] = r
	case "held-end":
		r.Steps[1].Note = strings.Repeat("Read the local command evidence before continuing. ", 35)
		m.data.Runs[0] = r
	case "quota":
		reset := runNow.Add(3 * time.Hour)
		r.Reason = "agent_quota"
		r.Steps[1].Status = ""
		r.Steps[1].Limit = &agentlimit.Limit{Kind: "agent_quota", Code: "codex:usage_limit_exceeded", ResetsAt: &reset}
		m.data.Runs[0] = r
	case "admission":
		r.Phase = runner.Held
		r.Reason = "admission_rejected"
		r.UID = "01K00000000000000000000000"
		r.Steps = nil
		r.WFDef = nil
		r.PastAttempts = nil
		m.data.Runs[0] = r
	case "deadline":
		r.Phase = runner.Running
		r.Outcome = ""
		r.Reason = ""
		r.ExpiresAt = runNow.Add(50 * time.Minute)
		r.Steps[1].Status = ""
		r.Steps[1].Receipt = nil
		r.Steps[1].Submission = runner.Sending
		m.data.Runs[0] = r
	case "child":
		r.Child = &runner.ChildRun{}
		r.Child.IssueRef = "example-child"
		m.data.Runs[0] = r
	case "manual":
		m.data.Runs[0].Manual = true
	case "delayed":
		r.Occurrence = runNow.Add(-12 * time.Minute).Format(time.RFC3339)
		m.data.Runs[0] = r
	case "handoff":
		r.Phase = runner.HandedOff
		r.ActiveStep = "plan"
		r.Steps[0].Status = "needs-human"
		r.Steps[0].Result = &runner.ResultRef{Summary: "A decision is needed.", Evidence: []string{"The example requires approval"}}
		m.data.Runs[0] = r
	case "failed-prompt":
		r.ActiveStep = "plan"
		r.Steps[0].Status = "failed"
		r.Steps[0].Result = &runner.ResultRef{Summary: "The prompt could not finish."}
		m.data.Runs[0] = r
	case "command-only":
		r.Steps = r.Steps[1:2]
		r.WFDef = &defs.WorkflowDef{Steps: []defs.Step{{Key: "verify", Kind: "command"}}}
		m.data.Runs[0] = r
	case "uncertain":
		r.Steps[1].Status = "unknown"
		r.Steps[1].Receipt = &runner.Receipt{ExitCode: 0, Uncertain: true}
		r.Reason = "command_outcome_unknown"
		m.data.Runs[0] = r
	case "inspect":
		m.data.Runs[0].Inspect = "Resolve the local diagnosis."
	case "signing":
		m.data.Runs[0].Ops = []runner.Op{{SigningRefused: true}}
	case "partial":
		m.artifactScanner = func(state.FS, string, string) artifactListing {
			return artifactListing{Paths: []string{"/state/runs/run-a/run.json"}, Err: errors.New("artifact metadata unavailable")}
		}
	case "removed-dir":
		m.artifactScanner = func(state.FS, string, string) artifactListing { return artifactListing{Missing: true} }
	case "capped":
		m.artifactScanner = func(state.FS, string, string) artifactListing {
			return artifactListing{Paths: []string{"/state/runs/run-a/run.json"}, More: 7}
		}
	}
	m.rebuild()
	if name == "labels-p2" || name == "pages-2" {
		m.cursor = m.pageRows()
	}
	switch name {
	case "overflow-6":
		m.frame = 6
	case "overflow-7":
		m.frame = 7
	case "tall":
		m.scroll = 21
		m.follow = false
	}
	m.frameValid = false
	return m
}
func TestU1RunsGolden(t *testing.T) {
	for _, name := range []string{"labels", "labels-p2", "held", "attempts", "quota", "inspect", "signing", "multi", "pages-1", "pages-2", "empty", "route", "overflow-0", "overflow-6", "overflow-7", "tall"} {
		t.Run(name, func(t *testing.T) {
			assertFrames(t, "runs", name, [][2]int{{120, 40}, {80, 24}, {200, 50}}, func(w, h int) string {
				m := u1Golden(name, w, h)
				text := m.viewContent()
				return text
			})
		})
	}
}
func TestU1RunDetailGolden(t *testing.T) {
	for _, name := range []string{"usage", "prompt", "no-result", "held", "held-end", "quota", "admission", "deadline", "child", "manual", "delayed", "handoff", "failed-prompt", "command-only", "uncertain", "inspect", "signing", "multi", "loading", "partial", "removed-dir", "capped"} {
		t.Run(name, func(t *testing.T) {
			assertFrames(t, "run-detail", name, [][2]int{{120, 40}, {80, 24}, {200, 50}}, func(w, h int) string {
				m := u1Golden(name, w, h)
				r := m.data.Runs[0]
				_ = m.pushDetail(viewRun, r.Target.Identity.ProjectUID, r.UID)
				m.artifactVisit = m.activeRunKey()
				m.artifactSignature = artifactSet(r)
				if name != "loading" {
					m.acceptArtifacts(m.requestArtifacts()().(artifactsMsg))
				}
				if name == "held-end" || name == "loading" || name == "partial" || name == "removed-dir" || name == "capped" {
					m.scroll = 100000
					m.follow = false
				}
				m.frameValid = false
				text := m.viewContent()
				if strings.Contains(ansi.Strip(text), "\n▶ ") || strings.Contains(text, "│ Runs │") {
					t.Fatal("detail has list chrome")
				}
				return text
			})
		})
	}
}
func TestU1FramesContract(t *testing.T) {
	for _, c := range runLabelFixture() {
		m := u1Golden("held", 120, 40)
		r := c.Run
		r.UID = "run"
		r.Job.Name = "Example"
		m.data.Runs = []runner.Run{r}
		m.expanded = map[string]bool{}
		m.rebuild()
		cells := m.runCells(m.rows[0])
		status := cells[slices.IndexFunc(m.runsColumns(), func(c column) bool { return c.Title == "status" })]
		if status.Text != c.Label {
			t.Fatal(c.Name, cells)
		}
		_ = m.pushDetail(viewRun, "", "run")
		text := ansi.Strip(bodyTextForTest(m.renderRunDetail()))
		if !strings.Contains(text, c.Label) || c.Report != "" && !strings.Contains(text, c.Report) {
			t.Fatal(c.Name, text)
		}
		roles := map[string]string{"DetailMeta": detailMetaStyle.Render(c.Label), "Subtle": subtleStyle.Render(c.Label), "Attention": attentionStyle.Render(c.Label), "Pass": passStyle.Render(c.Label), "Fail": failStyle.Render(c.Label), "Neutral": neutralStyle.Render(c.Label), "Open": openStyle.Render(c.Label), "Closed": closedStyle.Render(c.Label), "Error": errorStyle.Render(c.Label)}
		if status.Style.Render(c.Label) != roles[c.LabelRole] {
			t.Fatal("wrong literal role", c.Name)
		}
	}
	m := u1Golden("overflow-7", 80, 24)
	m.data.Runs[0].Job.Name = "Short"
	m.data.Runs[0].Steps = nil
	m.rebuild()
	m.Update(struct{}{})
	if !m.rendered.Animates || !m.marqueePending {
		t.Fatal("unselected overflow lost timer")
	}
	m.Update(press("j"))
	if m.frame != 7 {
		t.Fatal("selection reset frame")
	}
	m.Update(marqueeTickMsg{generation: m.marqueeGeneration})
	if m.frame != 8 {
		t.Fatal(m.frame)
	}
	for _, frame := range []int{0, 6, 7} {
		m := u1Golden("overflow-0", 80, 24)
		m.frame = frame
		text := m.viewContent()
		if strings.Contains(ansi.Strip(text), "…") {
			t.Fatal("table overflow ellipsis", text)
		}
	}
}
func TestU1FrameRoles(t *testing.T) {
	_, err := renderFrames(120, 40, func(_ string) string {
		mode := activeColorMode
		w, h := 120, 40
		m := u1Golden("labels", w, h)
		text := m.viewContent()
		cells := m.runCells(m.rows[4])
		status := cells[slices.IndexFunc(m.runsColumns(), func(c column) bool { return c.Title == "status" })]
		if mode == colorLight || mode == colorDark {
			green := "28"
			if mode == colorDark {
				green = "46"
			}
			if status.Style.GetForeground() != lipgloss.Color(green) || status.Style.GetBold() {
				t.Fatal("running must be Kata's open green", mode)
			}
		}
		for line := range strings.SplitSeq(text, "\n") {
			if strings.HasPrefix(ansi.Strip(line), "▶ ") {
				prefix := cursorStyle.Inherit(rowSelectedStyle).Render("▶") + rowSelectedStyle.Render(" ")
				tail := strings.TrimPrefix(line, prefix)
				if strings.Contains(tail, "38;") {
					t.Fatal("selected cells kept semantic foreground", line)
				}
			}
		}
		if mode == colorNone && (strings.Contains(text, "38;") || strings.Contains(text, "48;")) {
			t.Fatal("colors in none", mode)
		}
		return text
	})
	if err != nil {
		t.Fatal(err)
	}
}
