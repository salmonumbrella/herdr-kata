package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type editorContext struct {
	IssueReference  func(string) string
	ProjectUID      string
	NameTaken       func(name, uid string) bool
	DefaultCheckout string
	Favorite        bool
}

func routeRefusal(lane Data) string {
	if lane.Route["access_lost"] == true {
		return "access_lost"
	}
	if lane.Route["signing_blocked"] == true {
		return "signing_blocked"
	}
	if text, ok := lane.Route["error"].(string); ok && text != "" {
		return text
	}
	return "route refused"
}
func (m *model) editorContext(opts Lane, j *kata.Job) (editorContext, Lane, error) {
	name := opts.Project
	if name == "" {
		name = opts.Config.Kata.Project
	}
	ec := editorContext{}
	unavailable := func(reason string) (editorContext, Lane, error) {
		return ec, Lane{}, fmt.Errorf("project %s unavailable: %s", name, reason)
	}
	if len(m.data.Projects) > 0 {
		matches := 0
		refusal := "project unpinned"
		for uid, lane := range m.data.Projects {
			if lane.Project != name {
				continue
			}
			if j == nil && lane.Route["allowed"] != true {
				refusal = routeRefusal(lane)
				continue
			}
			if lane.Health["version"] != float64(1) || lane.Health["project_uid"] != uid {
				continue
			}
			if j != nil && j.ProjectUID != "" && j.ProjectUID != uid {
				continue
			}
			ec.ProjectUID = uid
			matches++
		}
		if matches != 1 {
			return unavailable(refusal)
		}
	} else {
		if j == nil && m.data.Route["allowed"] == false {
			return unavailable(routeRefusal(m.data))
		}
		ec.ProjectUID = effectiveProjectUID(m.data, "")
		if ec.ProjectUID == "" {
			return unavailable("project unpinned")
		}
	}
	if j != nil && j.ProjectUID != "" && j.ProjectUID != ec.ProjectUID {
		return unavailable("project identity changed")
	}
	if m.opts.ForProject != nil {
		opts = m.opts.ForProject(ec.ProjectUID, name)
	} else {
		opts.ProjectUID = ec.ProjectUID
	}
	if opts.ProjectUID != ec.ProjectUID {
		return unavailable("project identity changed")
	}
	if opts.Service == nil {
		return unavailable("job editor unavailable")
	}
	opts.Project = name
	names := map[string][]string{}
	if m.jobs != nil {
		for _, v := range m.jobs.byKey {
			if v.key.projectUID == ec.ProjectUID {
				key := strings.TrimSpace(v.entry.Name)
				names[key] = append(names[key], v.key.uid)
			}
		}
	}
	ec.NameTaken = func(name, uid string) bool {
		for _, candidate := range names[strings.TrimSpace(name)] {
			if candidate != uid {
				return true
			}
		}
		return false
	}
	if selected, ok := m.selected(); ok && selected.kind == "job" {
		if selected.projectUID == ec.ProjectUID && m.jobs != nil {
			if v := m.jobs.byKey[jobKey{ec.ProjectUID, selected.uid}]; v != nil {
				if _, allowed := opts.Config.Checkouts[v.def.CheckoutKey]; allowed {
					ec.DefaultCheckout = v.def.CheckoutKey
				}
			}
		}
	}
	if j != nil {
		ec.Favorite = m.favoriteKey(ec.ProjectUID, j.UID)
	}
	projectUID := ec.ProjectUID
	ec.IssueReference = func(uid string) string { return m.knownIssueRef(projectUID, uid) }
	return ec, opts, nil
}
func (m *model) renderProjectPicker() bodyLines {
	if m.projectPurpose == "to open Kata" {
		body := block("Project " + clean(m.projectPurpose))
		for i, name := range m.projectChoices {
			prefix := "  "
			if i == m.projectCursor {
				prefix = "▶ "
			}
			body = append(body, bodyLine{Text: prefix + clean(name)})
		}
		return body
	}
	body := bodyLines{{Text: "  " + detailSectionHeaderStyle.Render("new job in project")}, {Text: ""}}
	for i, name := range m.projectChoices {
		count := 0
		for _, j := range m.data.Jobs {
			if j.DeletedAt == nil && (j.Project == name || m.projectName(effectiveProjectUID(m.data, j.ProjectUID)) == name) {
				count++
			}
		}
		state := "project unpinned"
		for uid, lane := range m.data.Projects {
			if lane.Project != name {
				continue
			}
			if lane.Route["allowed"] == true && lane.Health["version"] == float64(1) && lane.Health["project_uid"] == uid {
				state = ""
				break
			}
			if lane.Route["allowed"] != true {
				state = routeRefusal(lane)
			}
		}
		prefix := "    "
		label := subtleStyle.Render(fitCell(name, 20))
		if i == m.projectCursor {
			prefix = "  " + cursorStyle.Render("▶ ")
			label = rowSelectedStyle.Render(fitCell(name, 20))
		}
		route := subtleStyle.Render("route ok")
		if state != "" {
			route = failStyle.Render("route refused: " + clean(state))
		}
		body = append(body, bodyLine{Text: prefix + label + subtleStyle.Render(fmt.Sprintf(" %d jobs · ", count)) + route})
	}
	return append(body, bodyLine{Text: ""}, bodyLine{Text: "  " + footerHelp{Full: []helpItem{hintGroup("j/k", "move", 0, keys.Down, keys.Up, keys.ScrollUp, keys.ScrollDown), keys.Accept.hint(0), keys.Cancel.hint(0)}}.Render(m.width-2)})
}

func (m *model) editJob() tea.Cmd {
	v, ok := m.selectedJobView()
	if !ok {
		m.notify(statusInfo, "job was removed")
		return nil
	}
	opts := m.selectedOptions()
	if opts.Service == nil {
		m.notify(statusInfo, "job editor unavailable")
		return nil
	}
	j := v.job
	ec, lane, err := m.editorContext(opts, &j)
	if err != nil {
		m.notify(statusInfo, err.Error())
		return nil
	}
	if lane.Service == nil {
		m.notify(statusInfo, "job editor unavailable")
		return nil
	}
	m.editorProject = lane.Project
	m.editorCreating = false
	m.editorName = j.Name
	m.editor = newJobForm(m.ctx, lane.Config, lane.Service, &j, m.workflowsFor(lane), ec)
	_, sizeCmd := m.editor.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	return tea.Batch(m.editor.Init(), sizeCmd)
}

// pickerState is owned by this view.
type pickerState struct {
	projectChoices []string
	projectCursor  int
	projectPurpose string
	// projectWrapTo carries a Tab cycle's wrap target through the choice.
	projectWrapTo *int
}
