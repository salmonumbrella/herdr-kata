package tui

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/jobform"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

// WorkflowCheck separates definition problems from requirements on a using job.
// It is shared by the snapshot projection and the external editor's CHECK view.
type workflowCheck struct {
	Definition                          *defs.WorkflowDef
	DecodeErr                           error
	Unsupported, JobDependent, Warnings []string
	DeclaredProfiles                    []string
	// StepProfiles maps a prompt step to the profile it runs on instead of the
	// run's; StepModels holds each such profile's model when the lane knows it.
	StepProfiles, StepModels map[string]string
	Order                    []string
	Loop                     *defs.Loop
	Budget                   *workflowBudget
}

type workflowBudget struct{ Total, Prompt, Loop int }

// workflowShape derives execution facts without requiring an available lane.
func workflowShape(raw json.RawMessage) workflowCheck {
	w, err := defs.DecodeWorkflow(raw)
	if err != nil {
		return workflowCheck{DecodeErr: err}
	}
	c := workflowCheck{Definition: &w}
	profiles := map[string]bool{}
	agent := func(raw json.RawMessage) string {
		var opts map[string]json.RawMessage
		if json.Unmarshal(raw, &opts) != nil {
			return ""
		}
		var name string
		if json.Unmarshal(opts["agent"], &name) != nil {
			return ""
		}
		return name
	}
	// Workflow and command step profiles constrain the run's profile; a prompt
	// step's own profile does not.
	if name := agent(w.Options); name != "" {
		profiles[name] = true
	}
	for _, s := range w.Steps {
		if name := agent(s.Options); name != "" && s.Kind != "prompt" {
			profiles[name] = true
		}
	}
	for p := range profiles {
		c.DeclaredProfiles = append(c.DeclaredProfiles, p)
	}
	sort.Strings(c.DeclaredProfiles)
	for _, s := range w.Steps {
		if name := agent(s.Options); name != "" && s.Kind == "prompt" && !(len(c.DeclaredProfiles) == 1 && c.DeclaredProfiles[0] == name) {
			if c.StepProfiles == nil {
				c.StepProfiles = map[string]string{}
			}
			c.StepProfiles[s.Key] = name
		}
	}

	order, topoErr := defs.TopoOrder(w.Steps)
	loop, loopErr := defs.WorkflowLoop(&w)
	if topoErr == nil {
		c.Order = order
	}
	trusted := topoErr == nil && loopErr == nil
	for _, step := range w.Steps {
		if step.Retries < 0 || step.Retries > 100 {
			trusted = false
		}
	}
	if trusted {
		c.Loop = loop
		c.Budget = &workflowBudget{Total: defs.PromptBudget(defs.JobDef{Action: defs.Action{Kind: "execute", WorkflowUID: "probe-workflow"}}, &w)}
		for _, step := range w.Steps {
			if step.Kind == "prompt" {
				c.Budget.Prompt += 1 + step.Retries
			}
		}
		c.Budget.Loop = c.Budget.Total - c.Budget.Prompt
	}
	return c
}

// workflowProblems adds lane-dependent classification using a valid synthetic job.
// It never serializes the source document used by the guarded writer.
func workflowProblems(raw json.RawMessage, cfg config.Config) workflowCheck {
	c := workflowShape(raw)
	if c.DecodeErr != nil {
		return c
	}
	w := c.Definition
	probe := defs.JobDef{
		Version: 1, Kind: "job", Enabled: true,
		Trigger: defs.Trigger{Kind: "manual"},
		Action:  defs.Action{Kind: "execute", WorkflowUID: "probe-workflow"},
		Overlap: "allow", Catchup: "skip",
		Issue: &defs.IssuePolicy{Kind: "per-run", Title: "probe"},
	}
	if len(c.DeclaredProfiles) > 0 {
		probe.Options, _ = json.Marshal(map[string]string{"agent": c.DeclaredProfiles[0]})
	}
	_, support := defs.Classify(probe, w, cfg)
	for key, name := range c.StepProfiles {
		if p, ok := cfg.Profiles[name]; ok {
			if model := profileModel(jobform.ProfileFacts(p)); model != "—" {
				if c.StepModels == nil {
					c.StepModels = map[string]string{}
				}
				c.StepModels[key] = model
			}
		}
	}
	c.Warnings = support.Warnings
	conflict := len(c.DeclaredProfiles) > 1
	if conflict {
		c.Unsupported = append(c.Unsupported, "conflicting profile declarations: "+strings.Join(c.DeclaredProfiles, ", "))
	}
	// A step profile's cap bounds every using job, whatever its profile.
	stepCapExceeded := false
	for _, name := range c.StepProfiles {
		stepCapExceeded = stepCapExceeded || c.Budget != nil && c.Budget.Total > defs.EffectiveCap(cfg, name)
	}
	for _, reason := range support.Reasons {
		if conflict && (reason == "workflow profile differs from run profile" || reason == "step profile differs from run profile") {
			continue
		}
		if len(c.DeclaredProfiles) == 0 && (strings.HasPrefix(reason, "unmapped profile: options.agent ") || strings.HasPrefix(reason, "prompt_budget ") && !stepCapExceeded) {
			c.JobDependent = append(c.JobDependent, reason)
		} else {
			c.Unsupported = append(c.Unsupported, reason)
		}
	}
	return c
}

type workflowKey struct{ projectUID, uid string }
type workflowJob struct {
	index                     int
	key, name, profile, input string
	cap                       int
	timeout                   time.Duration
}
type workflowView struct {
	index         int
	key           workflowKey
	check         workflowCheck
	laneAvailable bool
	active        bool
	stepLabels    []string
	parkFailure   bool
	jobs          []workflowJob
	runs          []int
	search        string
}
type workflowIndex map[workflowKey]*workflowView

func (m *model) workflowOptions(uid, name string) (Lane, bool) {
	return m.laneFor(uid, name)
}

func buildWorkflowIndex(d Data, jobs jobIndex, lane func(string, string) (Lane, bool), now time.Time) workflowIndex {
	index := workflowIndex{}
	for i, w := range d.Workflows {
		if w.DeletedAt != nil {
			continue
		}
		key := workflowKey{effectiveProjectUID(d, w.ProjectUID), w.UID}
		opts, available := lane(key.projectUID, w.Project)
		v := &workflowView{index: i, key: key, laneAvailable: available}
		if available {
			v.check = workflowProblems(w.Definition, opts.Config)
		} else {
			v.check = workflowShape(w.Definition)
		}
		v.parkFailure = defs.ParkFailure(v.check.Definition)
		for ordinal := range v.check.Order {
			v.stepLabels = append(v.stepLabels, workflowStepLabel(v.check, ordinal))
		}
		index[key] = v
	}
	for _, j := range jobs.byKey {
		if j.decodeErr != nil || j.def.Action.Kind != "execute" || j.def.Action.WorkflowUID == "" {
			continue
		}
		v := index[workflowKey{j.key.projectUID, j.def.Action.WorkflowUID}]
		if v == nil {
			continue
		}
		item := workflowJob{index: j.index, key: scopedRowKey(j.key.projectUID, j.key.uid), name: j.entry.Name, profile: j.profile, input: j.input, timeout: time.Duration(j.def.TimeoutSeconds) * time.Second}
		if opts, ok := lane(j.key.projectUID, j.entry.Project); ok && v.laneAvailable {
			item.cap = defs.RunCap(opts.Config, j.profile, v.check.Definition)
			item.timeout = jobTimeout(j, opts.Config)
		}
		v.jobs = append(v.jobs, item)
		v.active = v.active || j.def.Enabled
	}
	for i, r := range d.Runs {
		if r.Workflow != nil {
			if v := index[workflowKey{r.Target.Identity.ProjectUID, r.Workflow.UID}]; v != nil {
				v.runs = append(v.runs, i)
			}
		}
	}
	for _, v := range index {
		sort.Slice(v.runs, func(i, j int) bool { return d.Runs[v.runs[i]].UID > d.Runs[v.runs[j]].UID })
		sort.Slice(v.jobs, func(i, j int) bool {
			a, b := v.jobs[i], v.jobs[j]
			if strings.EqualFold(a.name, b.name) {
				return a.key < b.key
			}
			return strings.ToLower(a.name) < strings.ToLower(b.name)
		})
		w := d.Workflows[v.index]
		fields := []string{w.Project, w.UID, w.Name, strings.Join(v.check.Unsupported, " "), strings.Join(v.check.JobDependent, " "), strings.Join(v.check.Warnings, " ")}
		if !v.laneAvailable {
			fields = append(fields, "lane unavailable")
		}
		if v.check.DecodeErr != nil {
			fields = append(fields, "does not parse", v.check.DecodeErr.Error())
		}
		if def := v.check.Definition; def != nil {
			fields = append(fields, def.About, def.Input)
			for _, s := range def.Steps {
				fields = append(fields, s.Key, s.Kind)
			}
		}
		if len(v.runs) > 0 {
			r := d.Runs[v.runs[0]]
			label, _ := runLabel(r)
			detail, _ := runNote(r, now)
			fields = append(fields, label, string(r.Phase), r.Outcome, detail)
		}
		v.search = strings.ToLower(strings.Join(fields, " "))
	}
	return index
}
func (m *model) workflowRows() []row {
	type rankedView struct {
		view *workflowView
		rank int
	}
	views := make([]rankedView, 0, len(m.wfIndex))
	for _, v := range m.wfIndex {
		rank := 2
		if v.check.DecodeErr != nil || len(v.check.Unsupported) > 0 {
			rank = 0
		} else if !v.laneAvailable {
			rank = 1
		}
		views = append(views, rankedView{view: v, rank: rank})
	}
	sort.Slice(views, func(i, j int) bool {
		a, b := views[i], views[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		wa, wb := m.data.Workflows[a.view.index], m.data.Workflows[b.view.index]
		if wa.Project != wb.Project {
			return wa.Project < wb.Project
		}
		if strings.EqualFold(wa.Name, wb.Name) {
			return wa.UID < wb.UID
		}
		return strings.ToLower(wa.Name) < strings.ToLower(wb.Name)
	})
	rows := make([]row, 0, len(views))
	for _, ranked := range views {
		v := ranked.view
		w := m.data.Workflows[v.index]
		rows = append(rows, row{kind: "workflow", projectUID: v.key.projectUID, uid: v.key.uid, label: w.Name, search: v.search, wf: v.index})
	}
	return rows
}

func (m *model) selectedWorkflow() *workflowView {
	r, ok := m.selected()
	if !ok || r.kind != "workflow" {
		return nil
	}
	return m.wfIndex[workflowKey{r.projectUID, r.uid}]
}

func (m *model) workflowListKey(k tea.KeyPressMsg) (tea.Cmd, bool) {
	v := m.selectedWorkflow()
	switch k.String() {
	case "u":
		if v != nil {
			return m.resumeWorkflowRun(v, ""), true
		}
		return nil, true
	case "e":
		if v != nil {
			return m.editWorkflow(v), true
		}
		return nil, true
	case "R":
		m.notify(statusInfo, "R runs a job: l opens this workflow, then R on a used by job")
		return nil, true
	case "n", "D":
		m.notify(statusInfo, "workflows are created and deleted with kata cron workflow …")
		return nil, true
	case "p", "f", "F", "P":
		return nil, true
	}
	return nil, false
}
func (m *model) resumeWorkflowRun(v *workflowView, selectedRunUID string) tea.Cmd {
	if v == nil {
		return nil
	}
	name := m.data.Workflows[v.index].Name
	var target *runner.Run
	for _, i := range v.runs {
		r := m.data.Runs[i]
		if selectedRunUID == "" || r.UID == selectedRunUID {
			target = &r
			break
		}
	}
	if target == nil {
		if selectedRunUID != "" {
			m.notify(statusInfo, "selected run was removed")
		} else {
			m.notify(statusInfo, clean(name)+" has never run")
		}
		return nil
	}
	r := *target
	if r.Phase != runner.Held {
		label, _ := runLabel(r)
		job := runView(r).Job
		m.notify(statusInfo, "Resume requires a parked run · "+label+" ("+job+")")
		if selectedRunUID == "" {
			m.notify(statusInfo, "Resume requires a parked run · latest run of "+clean(name)+" is "+label+" ("+job+")")
		}
		return nil
	}
	if admissionHold(r) {
		m.notify(statusInfo, runNextAction(r))
		return nil
	}
	opts, available := m.workflowOptions(r.Target.Identity.ProjectUID, m.data.Workflows[v.index].Project)
	requester, canResume := opts.requester().(resumeRequester)
	if !available || !canResume {
		m.notify(statusInfo, "this board cannot resume runs")
		return nil
	}
	status := m.acknowledgement("resume requested · "+runView(r).Job, opts)
	return m.resumeAction(status, r, requester)
}
func (m *model) runWorkflowJob(projectUID, jobUID string) tea.Cmd {
	j := m.jobs.byKey[jobKey{projectUID, jobUID}]
	if j == nil {
		m.notify(statusInfo, "selected job was removed")
		return nil
	}
	if j.entry.Owner == defs.Native {
		m.notify(statusInfo, "Kata owns native delivery and has no force-fire")
		return nil
	}
	if !j.entry.Supported || j.entry.Owner != defs.Adapter || !j.def.Enabled {
		m.notify(statusInfo, strings.Join(j.entry.Reasons, "; "))
		if m.notice.Text == "" {
			m.notify(statusInfo, "Job is paused; resume it before running")
		}
		return nil
	}
	opts, available := m.workflowOptions(projectUID, j.entry.Project)
	_, pending := opts.requester().(pendingRequester)
	if !available || opts.requester() == nil || pending {
		m.notify(statusInfo, "this board cannot run jobs")
		return nil
	}
	requester := opts.requester()
	status := m.acknowledgement("run requested · "+clean(j.entry.Name), opts)
	return m.action(status, func() error { return requester.RunNow(jobUID) })
}
func (m *model) editWorkflow(v *workflowView) tea.Cmd {
	if v == nil {
		return nil
	}
	w := m.data.Workflows[v.index]
	opts, available := m.workflowOptions(v.key.projectUID, w.Project)
	if !available || opts.Service == nil {
		m.notify(statusInfo, "workflow editing is unavailable in this lane")
		return nil
	}
	w.Definition = append(json.RawMessage(nil), w.Definition...)
	ctx := m.ctx
	m.openingEditor = true
	m.editorProject = opts.Project
	m.editorName = w.Name
	m.editorCreating = false
	m.generation++
	generation := m.generation
	return func() tea.Msg { return newEditorMsg{generation: generation, editor: workflowFormForLane(ctx, opts, w)} }
}
