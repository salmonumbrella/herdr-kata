package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/usage"
)

func evidence(s runner.StepState, width int) []string {
	var lines []string
	seen := map[string]bool{}
	prose := func(text string, role lipgloss.Style) {
		if strings.TrimSpace(text) == "" || seen[text] {
			return
		}
		seen[text] = true
		for _, l := range wrapText(text, max(1, width-2)) {
			lines = append(lines, "  "+role.Render(l))
		}
	}
	if s.Result != nil {
		prose(s.Result.Summary, detailMetaStyle)
		prose(s.Result.Note, detailMetaStyle)
		for _, e := range s.Result.Evidence {
			prose("· "+e, subtleStyle)
		}
	}
	if p := s.Receipt; p != nil {
		text := fmt.Sprintf("cmd %s #%d: exit %d", clean(s.Key), s.Attempt, p.ExitCode)
		role := detailMetaStyle
		if p.ExitCode != 0 {
			role = failStyle
		}
		text = role.Render(text)
		if p.Interrupted || p.Uncertain || p.GroupAlive {
			text += " " + attentionStyle.Render("(uncertain)")
		}
		lines = append(lines, "  "+ansi.Truncate(text, max(0, width-2), ""))
	}
	if s.Reason != "" {
		text := clean(s.Reason)
		if s.Status != "" {
			text = clean(s.Status) + " (" + text + ")"
		}
		role := attentionStyle
		if s.Status == "failed" {
			role = failStyle
		}
		prose(text, role)
	}
	prose(s.Note, detailMetaStyle)
	if len(lines) == 0 {
		prose("no result recorded", subtleStyle)
	}
	prose(fmt.Sprintf("from %s #%d", clean(s.Key), s.Attempt), subtleStyle)
	return lines
}

func runOutcomeLine(r runner.Run, width int) bodyLine {
	label, role := runLabel(r)
	text := subtleStyle.Render(fitCell("outcome", 14)) + " " + role.Render(label)
	if r.Outcome != "" && r.Outcome != label {
		text += " " + subtleStyle.Render("("+clean(r.Outcome)+")")
	}
	return bodyLine{Text: ansi.Truncate(text, max(0, width), "")}
}

func runHoldLines(r runner.Run, width int, now time.Time) bodyLines {
	if r.Phase != runner.Held && r.Phase != runner.Blocked && r.Phase != runner.HandedOff {
		return nil
	}
	copy := r
	copy.Steps = append([]runner.StepState(nil), r.Steps...)
	var reset *time.Time
	for i, s := range copy.Steps {
		if s.Key == r.ActiveStep && s.Limit != nil {
			limit := *s.Limit
			reset = limit.ResetsAt
			limit.ResetsAt = nil
			copy.Steps[i].Limit = &limit
		}
	}
	text := runner.HoldReason(&copy)
	if reset != nil {
		text += " · resets in " + in(reset.Sub(now))
	}
	if text == "" {
		return nil
	}
	label, _ := runLabel(r)
	var body bodyLines
	for i, line := range wrapText(text, max(1, width-15)) {
		prefix := strings.Repeat(" ", 14)
		if i == 0 {
			prefix = fitCell(label, 14)
		}
		body = append(body, bodyLine{Text: subtleStyle.Render(prefix) + " " + attentionStyle.Render(line)})
	}
	return body
}

func (m *model) renderRunDetail() bodyLines {
	d := newDetail(m.width, 14, detailSelection{}, noSelection, nil)
	f := m.nav[len(m.nav)-1]
	row, ok := m.resolveFrame(f)
	if !ok {
		d.Lines("run removed")
		return d.Body()
	}
	r := m.data.Runs[row.run]
	v := runDetailView(r)
	now := m.now()
	label, role := runLabel(r)
	d.Title(v.View.Job, label, role)
	d.Styled(bodyLine{})
	if m.showProjects() {
		d.KV("project", m.projectName(r.Target.Identity.ProjectUID))
	}
	d.Styled(runOutcomeLine(r, m.width))
	if report, role := reportState(r); report != "" {
		d.KVStyled("reporting", report, role)
	}
	d.Styled(runHoldLines(r, m.width, now)...)
	if failed := runner.FailedStep(&r); failed != nil {
		d.KVWrap("failed step", fmt.Sprintf("%s #%d · %s", failed.Key, failed.Attempt, failed.Note))
	}
	d.KVWrap("next action", runNextAction(r))
	trigger := v.View.Trigger
	if r.Child != nil && r.Child.IssueRef != "" {
		trigger += " · " + qualifiedIssueRef(m.projectName(r.Target.Identity.ProjectUID), r.Child.IssueRef)
	}
	resumes := map[string]bool{}
	for _, uid := range r.ConsumedResumes {
		if uid != "" {
			resumes[uid] = true
		}
	}
	if r.Resume != nil && r.Resume.UID != "" {
		resumes[r.Resume.UID] = true
	}
	if len(resumes) > 0 {
		trigger += fmt.Sprintf(" · resumed ×%d", len(resumes))
	}
	d.KV("trigger", trigger)
	if !r.Manual || r.Child != nil {
		if at, err := time.Parse(time.RFC3339Nano, r.Occurrence); err == nil {
			d.KV("occurrence", stamp(at.In(now.Location())))
			if r.StartedAt != nil && r.StartedAt.Sub(at) >= time.Second {
				d.KV("delay", "started "+in(r.StartedAt.Sub(at))+" after occurrence")
			}
		}
	}
	phase := string(r.Phase)
	if r.Phase == runner.Held {
		phase = "parked"
	}
	d.KV("phase", phase)
	started := "not started"
	if r.StartedAt != nil {
		started = stamp(r.StartedAt.In(now.Location()))
	}
	d.KV("started", started)
	if r.EndedAt != nil {
		d.KV("ended", stamp(r.EndedAt.In(now.Location())))
	}
	if r.StartedAt != nil && !r.StartedAt.IsZero() {
		if r.EndedAt != nil && !r.EndedAt.IsZero() && !r.EndedAt.Before(*r.StartedAt) {
			d.KV("duration", dur(r.EndedAt.Sub(*r.StartedAt)))
		} else if r.EndedAt == nil && r.Phase != runner.Settled && !now.Before(*r.StartedAt) {
			d.KV("duration", "still running · "+in(now.Sub(*r.StartedAt)))
		}
	}
	if !r.ExpiresAt.IsZero() && r.Phase != runner.Settled {
		relative := "in " + in(r.ExpiresAt.Sub(now))
		if r.ExpiresAt.Before(now) {
			relative = ago(now, r.ExpiresAt)
		}
		d.KV("deadline", stamp(r.ExpiresAt.In(now.Location()))+" ("+relative+")")
	}
	if runNeedsAgent(r) {
		agent := r.ProfileName
		if r.Profile.Kind != "" {
			agent += " (" + r.Profile.Kind + ")"
		}
		d.KV("agent", agent)
		ref := runner.AgentReference(&r)
		pane := "no agent pane"
		if ref.PaneID != "" {
			pane = "pane " + ref.PaneID
		}
		if ref.TabID != "" {
			pane += " · tab " + ref.TabID
		}
		if ref.ServerID == (herdr.ServerID{}) {
			pane += " (server unproven)"
		}
		d.KV("herdr tab", pane)
		d.KV("tokens", usage.Line(r.Usage))
		if r.Usage != nil && r.Usage.CostMicroUSD != nil {
			d.KV("cost", fmt.Sprintf("~$%.2f", float64(*r.Usage.CostMicroUSD)/1e6))
		}
		cap := "unlimited"
		if r.PromptCap > 0 {
			cap = fmt.Sprint(r.PromptCap)
		}
		d.KV("prompts", fmt.Sprintf("%d/%s", r.PromptsSent, cap))
		d.KV("model", runModel(r))
	}
	issue := m.runIssueRef(r)

	if issue != "" && r.IssueOwned {
		issue += " · owned"
	}
	d.KV("issue", issue)
	if r.Child == nil {
		parent := defs.RunParent(r.JobDef)
		if r.JobDef.Issue != nil && r.JobDef.Issue.Kind == "per-run" && r.JobDef.Issue.ParentUID != "" {
			parent = r.JobDef.Issue.ParentUID
		}
		d.KV("run parent", m.knownIssueRef(r.Target.Identity.ProjectUID, parent))
	}
	if r.Workflow != nil {
		wf := r.Workflow.Name
		if r.ActiveStep != "" {
			wf += " · step " + r.ActiveStep
		}
		if edge, _ := defs.WorkflowLoop(r.WFDef); edge != nil {
			wf += fmt.Sprintf(" · loop %d/%d", r.Loop.Used, edge.MaxLoops)
		}
		d.KV("workflow", wf)
	}
	if v.PendingWrites > 0 {
		d.KV("pending writes", fmt.Sprint(v.PendingWrites))
	}
	cwd := r.Cwd
	if filepath.Base(cwd) == r.UID && filepath.Base(filepath.Dir(cwd)) == "runs" {
		cwd = "run directory"
	}
	d.KV("cwd", cwd)
	input, _, _ := strings.Cut(r.Input, "\n")
	d.KV("input", input)
	d.Section("RESULT")
	n := 0
	for _, s := range r.Steps {
		if s.Status == "succeeded" || s.Status == "skipped" {
			n++
		}
	}
	total := 1
	if r.WFDef != nil {
		total = len(r.WFDef.Steps)
	}
	if runNeedsAgent(r) {
		d.Lines(fmt.Sprintf("%d/%d steps ok", n, total))
	}
	if v.ResultStep != nil {
		for _, line := range evidence(*v.ResultStep, m.width) {
			d.Styled(bodyLine{Text: line})
		}
	}
	if r.WFDef != nil || len(r.PastAttempts) > 0 {
		d.Section("STEPS")
		for _, line := range renderStepLines(r, m.width, now, 0, false) {
			d.Styled(line)
		}
	}
	d.Section("ARTIFACTS")
	d.Styled(m.artifactLines()...)
	return d.Body()
}
