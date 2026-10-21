package tui

import (
	"maps"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

func (m *model) jobDetailKey(k tea.KeyPressMsg) tea.Cmd {
	switch s := k.String(); {
	case matchesKey(s, keys.Quit, keys.Interrupt):
		return tea.Quit
	case matchesKey(s, keys.Back, keys.Left, keys.Cancel, keys.Backspace):
		m.popDetail()
	case matchesKey(s, keys.Down, keys.ScrollDown):
		m.move(1)
	case matchesKey(s, keys.Up, keys.ScrollUp):
		m.move(-1)
	case matchesKey(s, keys.PageDown, keys.HalfDown):
		m.scroll += m.pageSize()
		m.follow = false
	case matchesKey(s, keys.PageUp, keys.HalfUp):
		m.scroll = max(0, m.scroll-m.pageSize())
		m.follow = false
	case matchesKey(s, keys.Open, keys.Right, keys.Accept):
		if r, ok := m.selectedJobRun(); ok {
			return m.pushDetail(viewRun, r.Target.Identity.ProjectUID, r.UID)
		}
	case matchesKey(s, keys.Run, keys.Pause, keys.Delete, keys.Attach, keys.Resume, keys.Issue, keys.Parent):
		return m.jobKeyAction(s)
	case matchesKey(s, keys.Edit):
		return m.editJob()
	case matchesKey(s, keys.Kata):
		if v, ok := m.selectedJobView(); ok {
			opts, err := m.jobActionOptions(v)
			if err == nil {
				return m.openKata(opts)
			}
			m.notify(statusInfo, err.Error())
		}
	case matchesKey(s, keys.Reload):
		return m.refresh()
	case matchesKey(s, keys.Mouse):
		m.mouseOff = !m.mouseOff
		if m.mouseOff {
			m.notify(statusInfo, "mouse released — the terminal's own selection is back (M takes it again)")
			return nil
		}
		m.notify(statusInfo, "mouse on")
		return nil
	}
	return nil
}

// Use only authoritative refs already saved by the worker, scoped to the
// same project. Admission saves run references; rendering never reads Kata.
func (m *model) knownIssueRef(projectUID, uid string) string {
	return m.issueRefIndex().known(projectUID, uid)
}

// IssueRefs holds every saved ref by project and issue UID. The first ref
// wins: jobs' saved references, then runs in order.
type issueRefs struct {
	refs  map[[2]string]string
	names map[string]string // Project names by UID, looked up once.
}

// IssueRefIndex resolves refs for a whole list in one pass over the data.
func (m *model) issueRefIndex() issueRefs {
	idx := issueRefs{refs: map[[2]string]string{}, names: map[string]string{}}
	add := func(projectUID, uid, ref string) {
		key := [2]string{projectUID, uid}
		if _, ok := idx.refs[key]; !ok && uid != "" && ref != "" {
			idx.refs[key] = ref
		}
	}
	for _, job := range m.data.Jobs {
		projectUID := effectiveProjectUID(m.data, job.ProjectUID)
		for uid, ref := range job.IssueRefs {
			add(projectUID, uid, ref)
		}
	}
	for _, r := range m.data.Runs {
		projectUID := r.Target.Identity.ProjectUID
		for uid, ref := range r.IssueRefs {
			add(projectUID, uid, ref)
		}
		project := idx.project(m, projectUID)
		add(projectUID, r.IssueUID, qualifiedIssueRef(project, r.IssueRef))
		if r.Child != nil {
			add(projectUID, r.Child.IssueUID, qualifiedIssueRef(project, r.Child.IssueRef))
		}
	}
	return idx
}

func (idx issueRefs) project(m *model, uid string) string {
	name, ok := idx.names[uid]
	if !ok {
		name = m.projectName(uid)
		idx.names[uid] = name
	}
	return name
}

func (idx issueRefs) known(projectUID, uid string) string {
	if strings.Contains(uid, "#") {
		return uid
	}
	return idx.refs[[2]string{projectUID, uid}]
}

// RunIssueRef is the run's own issue as run detail shows it.
func (m *model) runIssueRef(r runner.Run) string {
	return m.issueRefIndex().run(m, r)
}

func (idx issueRefs) run(m *model, r runner.Run) string {
	if ref := idx.known(r.Target.Identity.ProjectUID, r.IssueUID); ref != "" || r.IssueRef == "" {
		return ref
	}
	return qualifiedIssueRef(idx.project(m, r.Target.Identity.ProjectUID), r.IssueRef)
}

// ShortIssueRef matches Kata's # column: an issue in project drops its
// prefix. Other projects keep their qualified ref.
func shortIssueRef(project, ref string) string {
	if ref == "" {
		return "—"
	}
	i := strings.LastIndex(ref, "#")
	if i < 0 {
		return "#" + ref
	}
	if i == 0 || ref[:i] == project {
		return ref[i:]
	}
	return ref
}

// DefaultProject is the project whose issues a list shows as a bare #abcd:
// the lane's own project, else the first configured project, else the first
// lane by name.
func (m *model) defaultProject() string {
	if m.opts.Project != "" {
		return m.opts.Project
	}
	if entries := m.opts.Config.ProjectEntries(); len(entries) > 0 && entries[0].Name != "" {
		return entries[0].Name
	}
	names := slices.Sorted(maps.Keys(m.opts.Projects))
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

// ListIssueRef fills a list's # cell. Lists have no project column, so an
// issue outside the default project keeps its project#abcd ref; with a single
// project every ref is a bare #abcd.
func (m *model) listIssueRef(project, ref string) string {
	if ref == "" {
		return "—"
	}
	if !m.showProjects() {
		if i := strings.LastIndex(ref, "#"); i >= 0 {
			return ref[i:]
		}
		return "#" + ref
	}
	return shortIssueRef(m.defaultProject(), qualifiedIssueRef(project, ref))
}

// ListName prefixes a row's name with its project, as Kata's all-projects
// list prefixes titles, when the row is outside the default project and has
// no issue ref to say so.
func (m *model) listName(project, name, issue string) string {
	if issue != "—" || !m.showProjects() || project == "" || project == m.defaultProject() {
		return name
	}
	return "[" + project + "] " + name
}

func qualifiedIssueRef(project, ref string) string {
	if strings.Contains(ref, "#") {
		return ref
	}
	if ref != "" && len(ref) < 26 && project != "" {
		return project + "#" + ref
	}
	return ""
}
