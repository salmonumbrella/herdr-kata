package tui

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/jobform"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

const modelColumnWidth = 18

func (m *model) indexedJob(r row) *jobView {
	if r.kind != "job" || m.jobs == nil {
		return nil
	}
	return m.jobs.byKey[jobKey{r.projectUID, r.uid}]
}

type jobKey struct{ projectUID, uid string }
type nextFire struct {
	at    time.Time
	state string
}
type jobView struct {
	key                              jobKey
	entry                            defs.JobEntry
	job                              kata.Job
	def                              defs.JobDef
	decodeErr                        error
	workflow                         *defs.WorkflowDef
	workflowName                     string
	shape, profile, schedule, search string
	issue                            string // The newest run's issue, which i opens.
	facts                            jobform.ProfileInfo
	tags                             []string
	description                      string
	input                            string
	otherOptions                     []string
	runIndexes                       []int
	finished                         bool
	next                             nextFire
	index                            int
}
type decodedJob struct {
	event, raw string
	def        defs.JobDef
	err        error
}
type decodedWorkflow struct {
	event, raw string
	def        defs.WorkflowDef
	err        error
}
type jobIndex struct {
	byKey        map[jobKey]*jobView
	jobMemo      map[jobKey]*decodedJob
	workflowMemo map[jobKey]*decodedWorkflow
}

func (m *model) jobConfig(entry defs.JobEntry) config.Config {
	name := entry.Project
	if name == "" {
		name = m.projectName(effectiveProjectUID(m.data, entry.ProjectUID))
	}
	if opts := m.opts.Projects[name]; opts != nil {
		return opts.Config
	}
	return config.Config{}
}
func jobShape(d defs.JobDef) string {
	if d.Action.Kind == "notify" {
		if d.Issue != nil && d.Issue.Kind == "per-run" {
			return "recurring"
		}
		return "notify"
	}
	if d.Action.WorkflowUID != "" {
		return "workflow"
	}
	return "prompt"
}
func jobSchedule(t defs.Trigger) string {
	switch t.Kind {
	case "cron":
		return t.Cron
	case "interval":
		return "every " + (time.Duration(t.IntervalSeconds) * time.Second).String()
	case "once":
		at, err := time.Parse(time.RFC3339Nano, t.At)
		zone := time.UTC
		if t.Timezone != "" {
			var e error
			zone, e = time.LoadLocation(t.Timezone)
			if e != nil {
				return "once " + t.At
			}
		}
		if err != nil {
			return "once " + t.At
		}
		at = at.In(zone)
		format := "2006-01-02 15:04"
		if at.Second() != 0 || at.Nanosecond() != 0 {
			format += ":05.999999999"
		}
		return "once " + at.Format(format)
	case "manual":
		return "manual"
	}
	return t.Kind
}
func (m *model) rebuildJobIndex() {
	old := m.jobs
	index := &jobIndex{byKey: map[jobKey]*jobView{}, jobMemo: map[jobKey]*decodedJob{}, workflowMemo: map[jobKey]*decodedWorkflow{}}
	workflows := map[jobKey]kata.Workflow{}
	for _, w := range m.data.Workflows {
		if w.DeletedAt != nil {
			continue
		}
		key := jobKey{effectiveProjectUID(m.data, w.ProjectUID), w.UID}
		workflows[key] = w
		var cached *decodedWorkflow
		if old != nil {
			cached = old.workflowMemo[key]
		}
		if cached == nil || cached.event != w.DefinitionEventUID || cached.raw != string(w.Definition) {
			d, err := defs.DecodeWorkflow(w.Definition)
			cached = &decodedWorkflow{w.DefinitionEventUID, string(w.Definition), d, err}
		}
		index.workflowMemo[key] = cached
	}
	refs := m.issueRefIndex()
	groups := map[jobKey][]int{}
	for i, r := range m.data.Runs {
		key := jobKey{r.Target.Identity.ProjectUID, r.Job.UID}
		groups[key] = append(groups[key], i)
	}
	for i, entry := range m.data.Jobs {
		entry = m.pendingJobEntry(entry)
		if entry.DeletedAt != nil {
			continue
		}
		key := jobKey{effectiveProjectUID(m.data, entry.ProjectUID), entry.UID}
		var cached *decodedJob
		if old != nil {
			cached = old.jobMemo[key]
		}
		if cached == nil || cached.event != entry.DefinitionEventUID || cached.raw != string(entry.Definition) {
			d, err := defs.DecodeJob(entry.Definition)
			cached = &decodedJob{entry.DefinitionEventUID, string(entry.Definition), d, err}
		}
		index.jobMemo[key] = cached
		v := &jobView{key: key, entry: entry, def: cached.def, decodeErr: cached.err, runIndexes: groups[key], index: i}
		v.job = kata.Job{ProjectUID: key.projectUID, Project: entry.Project, UID: entry.UID, Name: entry.Name, DefinitionEventUID: entry.DefinitionEventUID, Definition: entry.Definition, CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt, DeletedAt: entry.DeletedAt}
		v.shape = jobShape(v.def)
		v.schedule = jobSchedule(v.def.Trigger)
		if v.shape == "workflow" {
			wk := jobKey{key.projectUID, v.def.Action.WorkflowUID}
			if w, ok := workflows[wk]; ok {
				v.workflowName = w.Name
				if memo := index.workflowMemo[wk]; memo.err == nil {
					v.workflow = &memo.def
				}
			}
		}
		if v.shape != "recurring" {
			v.tags, v.description, _ = defs.JobMetadata(v.def.Options)
		}
		v.profile = defs.OptionString(v.def.Options, "agent")
		v.input = defs.OptionString(v.def.Options, "input")
		if v.shape != "recurring" {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(v.def.Options, &fields)
			for key := range fields {
				switch key {
				case "agent", "input", "run_parent", "tags", "description":
				default:
					v.otherOptions = append(v.otherOptions, key)
				}
			}
			sort.Strings(v.otherOptions)
		}
		if v.profile == "" {
			v.profile = "default"
		}
		cfg := m.jobConfig(entry)
		if v.shape == "prompt" || v.shape == "workflow" && v.workflow != nil && defs.PromptBudget(v.def, v.workflow) > 0 {
			if p, ok := cfg.Profiles[v.profile]; ok {
				v.facts = jobform.ProfileFacts(p)
			}
		}
		v.finished = m.finishedJob(v)
		v.next = m.jobNext(v)
		last := ""
		if len(v.runIndexes) > 0 {
			r := m.data.Runs[v.runIndexes[0]]
			last = string(r.Phase) + " " + r.Outcome
			v.issue = refs.run(m, r)
		}
		project := entry.Project
		if project == "" {
			project = m.projectName(key.projectUID)
		}
		v.search = strings.ToLower(strings.Join([]string{project, entry.UID, entry.Name, v.description, strings.Join(v.tags, " "), v.schedule, v.profile, v.facts.Model, v.workflowName, strings.Join(entry.Reasons, " "), last, v.issue}, " "))
		index.byKey[key] = v
	}
	m.jobs = index
}
func (m *model) finishedJob(v *jobView) bool {
	if v.def.Trigger.Kind != "once" {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, v.def.Trigger.At)
	if err != nil {
		return false
	}
	for _, i := range v.runIndexes {
		r := m.data.Runs[i]
		if r.Manual || r.Phase != runner.Settled || r.Outcome != "succeeded" || r.Job.DefinitionEventUID != v.entry.DefinitionEventUID {
			continue
		}
		occurrence, err := time.Parse(time.RFC3339Nano, r.Occurrence)
		if err == nil && occurrence.Equal(at) {
			return true
		}
	}
	return false
}
func (m *model) jobNext(v *jobView) nextFire {
	if v.shape == "recurring" {
		return nextFire{state: "Kata-managed; next delivery unavailable locally"}
	}
	unavailable := nextFire{state: "next unavailable locally"}
	if v.decodeErr != nil {
		return unavailable
	}
	schedule, err := sched.Compile(v.def.Trigger, v.entry.CreatedAt)
	if err != nil {
		return unavailable
	}
	if v.def.Trigger.Kind == "manual" || v.finished {
		return nextFire{}
	}
	if !v.def.Enabled {
		return nextFire{state: "paused"}
	}
	lane := m.data
	if len(m.data.Projects) > 0 {
		var ok bool
		lane, ok = m.data.Projects[v.key.projectUID]
		if !ok {
			return unavailable
		}
	}
	if lane.Health["version"] != float64(1) || lane.Health["project_uid"] != v.key.projectUID || v.key.projectUID == "" {
		return unavailable
	}
	created, _ := lane.Health["state_created_at"].(string)
	first, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return unavailable
	}
	if v.entry.CreatedAt.After(first) {
		first = v.entry.CreatedAt
	}
	frontier := first
	if cp, ok := lane.Checkpoints[v.key.uid]; ok {
		if cp.Version != 1 || cp.JobUID != v.key.uid || cp.HandledThrough.IsZero() {
			return unavailable
		}
		frontier = cp.HandledThrough
	}
	return nextFire{at: schedule.Next(frontier)}
}
func (m *model) visibleJobs(includeFinished bool) []*jobView {
	if m.jobs == nil {
		return nil
	}
	rows := make([]*jobView, 0, len(m.jobs.byKey))
	for _, v := range m.jobs.byKey {
		if (includeFinished || !v.finished) && strings.Contains(v.search, strings.ToLower(m.query)) {
			rows = append(rows, v)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		af, bf := m.favoriteKey(a.key.projectUID, a.key.uid), m.favoriteKey(b.key.projectUID, b.key.uid)
		if af != bf {
			return af
		}
		if !a.entry.CreatedAt.Equal(b.entry.CreatedAt) {
			return a.entry.CreatedAt.Before(b.entry.CreatedAt)
		}
		if a.key.uid != b.key.uid {
			return a.key.uid < b.key.uid
		}
		return a.key.projectUID < b.key.projectUID
	})
	return rows
}

// jobTimeout is the shared per-run timeout projection for job and workflow views.
func jobTimeout(v *jobView, cfg config.Config) time.Duration {
	if v.def.TimeoutSeconds != 0 {
		return time.Duration(v.def.TimeoutSeconds) * time.Second
	}
	if cfg.Limits.DefaultTimeout != 0 {
		return cfg.Limits.DefaultTimeout
	}
	return 2 * time.Hour
}
