package tui

// effectiveProjectUID normalizes definitions only. Journals keep recorded identity.
func effectiveProjectUID(data Data, entryProjectUID string) string {
	if entryProjectUID != "" || len(data.Projects) > 0 {
		return entryProjectUID
	}
	if data.Health["version"] == float64(1) {
		if pin, ok := data.Health["project_uid"].(string); ok {
			return pin
		}
	}
	return ""
}

func scopedRowKey(project, uid string) string {
	if project == "" {
		return uid
	}
	return project + "\x00" + uid
}

func (r row) identity() string { return scopedRowKey(r.projectUID, r.uid) }

func (m *model) favoriteKey(projectUID, uid string) bool {
	if value, present := m.favorites[scopedRowKey(projectUID, uid)]; present {
		return value
	}
	return len(m.data.Projects) <= 1 && m.favorites[uid]
}

func (m *model) multipleProjects() bool { return len(m.opts.Projects) > 1 }

// showProjects includes retained identities without making them action lanes.
func (m *model) showProjects() bool {
	if m.multipleProjects() || len(m.data.Projects) > 1 {
		return true
	}
	projects := map[string]bool{}
	for uid := range m.data.Projects {
		projects[uid] = true
	}
	for _, j := range m.data.Jobs {
		if uid := effectiveProjectUID(m.data, j.ProjectUID); uid != "" {
			projects[uid] = true
		}
	}
	for _, r := range m.data.Runs {
		if uid := r.Target.Identity.ProjectUID; uid != "" {
			projects[uid] = true
		}
	}
	for _, w := range m.data.Workflows {
		if uid := effectiveProjectUID(m.data, w.ProjectUID); uid != "" {
			projects[uid] = true
		}
	}
	return len(projects) > 1
}

func (m *model) projectName(uid string) string {
	for _, r := range m.data.Runs {
		if r.Target.Identity.ProjectUID == uid && r.ProjectName != "" {
			return r.ProjectName
		}
	}
	if uid == m.opts.ProjectUID && m.opts.Project != "" {
		return m.opts.Project
	}

	if name := m.data.Projects[uid].Project; name != "" {
		return name
	}
	if len(m.opts.Projects) == 1 {
		if pinned, _ := m.data.Health["project_uid"].(string); uid == "" || pinned == uid {
			for name := range m.opts.Projects {
				return name
			}
		}
	}
	if len(uid) == 26 {
		return "project pending"
	}
	return uid
}

func (m *model) rowProject(r row) string {
	switch r.kind {
	case "job":
		return m.data.Jobs[r.job].Project
	case "run":
		return m.projectName(m.data.Runs[r.run].Target.Identity.ProjectUID)
	case "workflow":
		return m.data.Workflows[r.wf].Project
	default:
		return ""
	}
}

func (m *model) acknowledgement(message string, opts Lane) string {
	name := opts.Project
	if r, ok := m.selected(); ok && name == "" {
		name = m.rowProject(r)
	}
	if name == "" {
		name = m.opts.Config.Kata.Project
	}
	if name != "" {
		return message + " · project " + name
	}
	return message
}

func (m *model) freshProjectOptions(opts Lane) Lane {
	if opts.Project != "" && opts.ProjectUID == "" && m.opts.ForProject != nil {
		uid, matches := "", 0
		for candidate, lane := range m.data.Projects {
			if lane.Project == opts.Project && lane.Route["allowed"] == true {
				uid = candidate
				matches++
			}
		}
		if matches != 1 {
			uid = ""
		}
		// Each open owns its discovery service, including after cancellation.
		opts = m.opts.ForProject(uid, opts.Project)
	}
	return opts
}

// laneFor resolves a snapshot identity to a configured, guarded action lane.
func (m *model) laneFor(uid, name string) (Lane, bool) {
	if len(m.data.Projects) > 0 {
		snapshot, ok := m.data.Projects[uid]
		if !ok {
			return Lane{}, false
		}
		if snapshot.Project != "" {
			if name != "" && name != snapshot.Project {
				return Lane{}, false
			}
			name = snapshot.Project
		}
	}
	if name == "" && len(m.opts.Projects) == 1 && m.data.Health["version"] == float64(1) && m.data.Health["project_uid"] == uid {
		for candidate := range m.opts.Projects {
			name = candidate
		}
	}
	lane := m.opts.Projects[name]
	if lane == nil || lane.ProjectUID != "" && lane.ProjectUID != uid {
		return Lane{}, false
	}
	opts := m.opts.ForProject(uid, name)
	if opts.ProjectUID != uid || opts.Project != name || opts.Service == nil {
		return Lane{}, false
	}
	return opts, true
}
