package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
)

func (m *model) workflowDetail() bodyLines {
	d := m.buildWorkflowDetail()
	s := d.Selection()
	m.detailKey, m.detailOrdinal = s.Key, s.Ordinal
	return d.Body()
}

func (m *model) buildWorkflowDetail() *detail {
	d := newDetail(m.width, 14, detailSelection{Key: m.detailKey, Ordinal: m.detailOrdinal}, nearestSurvivor, m.detailItemExists)
	d.Marquee(m.frame, "used-by", "run")
	d.Selectable("step", "used-by", "run")
	v := m.selectedWorkflow()
	if v == nil {
		d.Title("workflow removed", "", subtleStyle)
		return d
	}
	w := m.data.Workflows[v.index]
	c := v.check
	d.Title(w.Name, "", titleStyle)
	if !w.UpdatedAt.IsZero() {
		d.Subtitle("updated " + ago(m.now(), w.UpdatedAt))
	}

	problems := workflowDiagnosis(c) != "" || !v.laneAvailable
	if problems {
		d.Section("PROBLEMS")
		if c.DecodeErr != nil {
			workflowDetailStyled(d, "! does not parse: "+c.DecodeErr.Error(), m.width, failStyle)
		}
		if len(c.Unsupported) > 0 {
			workflowDetailStyled(d, "! unsupported workflow", m.width, failStyle)
			for _, reason := range c.Unsupported {
				workflowDetailStyled(d, "! "+reason, m.width, failStyle)
			}
		}
		if !v.laneAvailable {
			workflowDetailStyled(d, "lane unavailable", m.width, attentionStyle)
		}
		workflowDetailStyled(d, "fix it with e · r reloads", m.width, subtleStyle)
	}
	d.Styled(bodyLine{})
	if m.showProjects() {
		d.KV("project", m.projectName(v.key.projectUID))
	}
	opts, available := m.workflowOptions(v.key.projectUID, w.Project)
	if def := c.Definition; def != nil {
		if def.About != "" {
			for _, line := range wrapText(def.About, max(1, m.width-2)) {
				d.Lines(line)
			}
			d.Lines("")
		}
		input := def.Input
		if input == "" {
			input = "none"
		}
		d.KVWrap("input", input)
		if !available || len(c.Unsupported) > 0 {
			d.KVMissing("failure")
		} else {
			failure := "settle failed"
			if v.parkFailure {
				failure = "park — exhausted failures hold for u resume"
			}
			d.KVWrap("failure", failure)
		}
		if c.Order == nil && len(def.Steps) > 0 {
			d.KVMissing("loop")
		} else if c.Loop == nil {
			d.KV("loop", "none")
		} else {
			d.KVWrap("loop", fmt.Sprintf("%s ↺ %s · max %d · span %s", c.Loop.Checker, c.Loop.Goto, c.Loop.MaxLoops, strings.Join(c.Loop.Span, " → ")))
		}
		if !available {
			d.KVMissing("agent")
		} else {
			agent := "each job's profile"
			if len(c.DeclaredProfiles) == 1 {
				agent = "profile " + c.DeclaredProfiles[0] + " (declared by the workflow)"
			}
			d.KVWrap("agent", agent+" · prompt steps shared unless fresh")
		}
		if c.Budget != nil && available {
			prompts, attempts := "prompts", "attempts"
			if c.Budget.Total == 1 {
				prompts = "prompt"
			}
			if c.Budget.Prompt == 1 {
				attempts = "attempt"
			}
			loops := "attempts"
			if c.Budget.Loop == 1 {
				loops = "attempt"
			}
			text := fmt.Sprintf("up to %d %s per generation (%d step %s + %d extra loop %s)", c.Budget.Total, prompts, c.Budget.Prompt, attempts, c.Budget.Loop, loops)
			if len(c.DeclaredProfiles) == 1 {
				text += fmt.Sprintf(" · cap %d", defs.RunCap(opts.Config, c.DeclaredProfiles[0], def))
			} else {
				text += " · cap per job, see used by"
			}
			d.KVWrap("prompts", text)
		} else {
			d.KVMissing("prompts")
		}
		if available {
			timeout := opts.Config.Limits.DefaultTimeout
			if timeout == 0 {
				timeout = 2 * time.Hour
			}
			d.KVWrap("timeout", "per run, set by each job · default "+dur(timeout))
			commands, role := "allowed", detailMetaStyle
			if !opts.Config.CommandsAllowed() {
				commands = "disabled for project (commands=false)"
				for _, s := range def.Steps {
					if s.Kind == "command" {
						role = failStyle
						break
					}
				}
			}
			d.KVStyled("commands", commands, role)
		} else {
			d.KVMissing("timeout")
			d.KVMissing("commands")
		}
		for _, warning := range c.Warnings {
			workflowDetailStyled(d, "warning: "+warning, m.width, attentionStyle)
		}
		for _, reason := range c.JobDependent {
			workflowDetailStyled(d, "per job: "+reason, m.width, subtleStyle)
		}
		d.Section(fmt.Sprintf("steps %d", len(def.Steps)))
		if c.Order == nil && len(def.Steps) > 0 {
			workflowDetailStyled(d, "execution order unavailable", m.width, attentionStyle)
			for _, s := range def.Steps {
				d.Lines(s.Key + "  " + s.Kind)
				workflowDetailStyled(d, workflowStepText(s), m.width, subtleStyle)
			}
		} else {
			byKey := map[string]defs.Step{}
			for _, s := range def.Steps {
				byKey[s.Key] = s
			}
			items := []detailItem{}
			for i, key := range c.Order {
				s := byKey[key]
				itemKey := detailItemKey("step", v.key.projectUID, key)
				label := v.stepLabels[i]
				text := workflowStepText(s)
				lines := []string{truncate(strings.Split(text, "\n")[0], max(0, m.width-7))}
				if s.Kind == "prompt" {
					rendered := renderMarkdownLines(text, max(1, m.width-7))
					if len(rendered) > 0 {
						lines = rendered[:1]
					}
				}
				if m.expanded[itemKey] {
					wrapKey := workflowWrapKey{v.key, w.DefinitionEventUID, key, m.width}
					if m.workflowWraps == nil {
						m.workflowWraps = map[workflowWrapKey][]string{}
					}
					lines = m.workflowWraps[wrapKey]
					if lines == nil {
						if s.Kind == "prompt" {
							lines = renderMarkdownLines(text, max(1, m.width-7))
						} else {
							lines = wrapText(text, max(1, m.width-7))
						}
						m.workflowWraps[wrapKey] = lines
					}
				}
				after := bodyLines{}
				for _, line := range lines {
					styled := workflowPromptStyle(line)
					if s.Kind == "prompt" {
						styled = line
					}
					after = append(after, bodyLine{Text: "       " + styled})
				}
				items = append(items, detailItem{Key: itemKey, Expandable: true, Expanded: m.expanded[itemKey], Render: func(_ bool, width int) string { return workflowStepStyle(label, width) }, After: after})
			}
			d.List("step", items, -1)
		}
	}
	d.Section(fmt.Sprintf("USED BY %d", len(v.jobs)))
	cols := []column{{Title: "job", Width: 20}, {Title: "state", Width: 12}, {Title: "profile", Width: 12}, {Title: "cap", Width: 7}, {Title: "input", Flex: true, Width: 8}}
	if m.width >= 100 {
		cols = []column{{Title: "job", Width: 20}, {Title: "state", Width: 12}, {Title: "schedule", Width: 16}, {Title: "profile", Width: 12}, {Title: "cap", Width: 7}, {Title: "input", Flex: true, Width: 8}, {Title: "timeout", Width: 7}}
	}
	widths := layout(cols, max(1, m.width-3), 8)
	if len(v.jobs) == 0 {
		d.Lines("no jobs use this workflow")
	} else {
		headers := []cell{}
		for _, col := range cols {
			headers = append(headers, cell{Text: col.Title, Style: headerStyle})
		}
		d.Styled(tableRow(headers, widths, false, 0, -1))
		items := []detailItem{}
		for _, joined := range v.jobs {
			entry := m.data.Jobs[joined.index]
			j := m.jobs.byKey[jobKey{v.key.projectUID, entry.UID}]
			if j == nil {
				continue
			}
			state, role := workflowJobState(j)
			input := "—"
			if joined.input != "" {
				input = fmt.Sprintf("%q", joined.input)
			}
			if !j.entry.Supported {
				input = strings.Join(j.entry.Reasons, "; ")
				if input == "" {
					input = "unsupported"
				}
			}
			cap := "—"
			capRole := detailMetaStyle
			if c.Budget != nil && joined.cap > 0 {
				cap = fmt.Sprintf("%d/%d", c.Budget.Total, joined.cap)
				if c.Budget.Total > joined.cap {
					capRole = failStyle
				}
			}
			cells := []cell{}
			for _, col := range cols {
				cell := cell{Style: detailMetaStyle}
				switch col.Title {
				case "job":
					cell.Text = joined.name
				case "state":
					cell.Text = state
					cell.Style = role
				case "schedule":
					cell.Text = j.schedule
				case "profile":
					cell.Text = joined.profile
				case "cap":
					cell.Text = cap
					cell.Style = capRole
				case "input":
					cell.Text = input
				case "timeout":
					cell.Text = "—"
					if joined.timeout > 0 {
						cell.Text = dur(joined.timeout)
					}
				}
				cells = append(cells, cell)
			}
			items = append(items, detailItem{Key: detailItemKey("used-by", v.key.projectUID, entry.UID), Cells: dimPlaceholders(cells), Widths: widths})
		}
		d.List("used-by", items, -1)
	}
	d.Section(fmt.Sprintf("RUNS %d", len(v.runs)))
	if len(v.runs) == 0 {
		d.Lines("never run")
	} else {
		// The status cell fits the widest status word, needs-human.
		cols := []column{{Width: 11}, {Width: 9}, {Width: 9}, {Width: 18}, {Flex: true, Width: 8}}
		widths := layout(cols, max(1, m.width-3), 8)
		items := []detailItem{}
		for _, i := range v.runs {
			r := m.data.Runs[i]
			rv := runView(r)
			label, role := runLabel(r)
			when := "—"
			if r.StartedAt != nil {
				when = ago(m.now(), *r.StartedAt)
			}
			trigger := rv.Trigger
			if r.Child != nil {
				trigger = "child"
			}
			note, noteRole := runNote(r, m.now())
			if r.Workflow.DefinitionEventUID != w.DefinitionEventUID {
				note += " · older definition"
			}
			items = append(items, detailItem{Key: detailItemKey("run", r.Target.Identity.ProjectUID, r.UID), Cells: []cell{{Text: label, Style: role}, {Text: trigger, Style: detailMetaStyle}, {Text: when, Style: detailMetaStyle}, {Text: rv.Job, Style: detailMetaStyle}, {Text: note, Style: noteRole}}, Widths: widths})
		}
		d.List("run", items, 20, "Runs lists every retained run")
	}
	return d
}

func workflowDetailStyled(d *detail, text string, width int, role lipgloss.Style) {
	for _, line := range wrapText(text, max(1, width-2)) {
		d.Styled(bodyLine{Text: "  " + role.Render(line)})
	}
}

func workflowStepText(s defs.Step) string {
	if s.Kind == "command" {
		return s.Command
	}
	return s.Prompt
}

func workflowPromptStyle(text string) string {
	text = clean(text)
	out := ""
	for text != "" {
		start := strings.Index(text, "{{")
		if start < 0 {
			return out + subtleStyle.Render(text)
		}
		out += subtleStyle.Render(text[:start])
		text = text[start:]
		end := strings.Index(text, "}}")
		if end < 0 {
			return out + subtleStyle.Render(text)
		}
		token := text[:end+2]
		role := subtleStyle
		if token == "{{input}}" || token == "{{previous}}" {
			role = headerStyle
		}
		out += role.Render(token)
		text = text[end+2:]
	}
	return out
}

func workflowStepStyle(label string, width int) string {
	label = truncate(label, width)
	number, rest, found := strings.Cut(label, ". ")
	if !found {
		return label
	}
	key, kind, found := strings.Cut(rest, "  ")
	if !found {
		return subtleStyle.Render(number+". ") + rest
	}
	return subtleStyle.Render(number+". ") + key + "  " + chipStyle.Render(kind)
}

func workflowJobState(j *jobView) (string, lipgloss.Style) {
	switch {
	case j.finished:
		return "finished", subtleStyle
	case !j.def.Enabled:
		return "paused", subtleStyle
	case j.entry.Owner == defs.Native:
		return "native", subtleStyle
	case !j.entry.Supported:
		return "unsupported", attentionStyle
	default:
		return "enabled", detailMetaStyle
	}
}
