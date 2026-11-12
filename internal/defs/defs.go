// Package defs freezes portable definitions into immutable execution plans.
package defs

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
)

var agentName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type Plan struct {
	JobUID, JobEventUID, WorkflowUID, WorkflowEventUID string
	Name, WorkflowName, About                          string
	Steps                                              []Step
	Issue                                              IssuePolicy
	Overlap, Catchup                                   string
	Grace, Timeout                                     time.Duration
	CheckoutPath, Input                                string
	Agent                                              string `json:",omitempty"`
}
type IssuePolicy struct {
	Kind, UID, Title, Body string
	// Parent is options.run_parent: the issue UID each per-run issue is created under.
	Parent                string
	DeadlineOffsetSeconds *int64
}
type Step struct {
	Key, Kind, Text string
	After           []string
	Retries         int
	Argv            []string
	SkipExit        int
}

func text(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func option(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func unsupported(m map[string]any) []string {
	var reasons []string
	for _, key := range []string{"on_fail", "findings"} {
		if _, ok := m[key]; ok {
			reasons = append(reasons, "loops_unsupported")
		}
	}
	if option(m, "on_failure") == "park" {
		reasons = append(reasons, "park_unsupported")
	}
	if mode, ok := m["agent_mode"]; ok && mode != "fresh" {
		reasons = append(reasons, "agent_mode_unsupported")
	}
	if _, ok := m["secret_refs"]; ok {
		reasons = append(reasons, "secrets_unsupported")
	}
	return reasons
}

func Freeze(job kata.CronJob, wfs map[string]kata.CronWorkflow, cfg config.Config, project string, look func(string) (string, error)) (Plan, []string) {
	d := job.Definition
	p := Plan{JobUID: job.UID, JobEventUID: job.DefinitionEventUID, Name: job.Name, Overlap: d.Overlap, Catchup: d.Catchup, Timeout: cfg.Limits.DefaultTimeout, Input: option(d.Options, "input")}
	projectCfg := config.Project{}
	for _, candidate := range cfg.Projects {
		if candidate.Name == project {
			projectCfg = candidate
			break
		}
	}
	reasons := unsupported(d.Options)
	if raw, ok := d.Options["persistent_agent"]; ok {
		name, valid := raw.(string)
		if !valid || !agentName.MatchString(name) {
			reasons = append(reasons, "persistent_agent must be a name of 1–64 letters, digits, dots, underscores or hyphens")
		} else {
			p.Agent = name
		}
		if d.Action.Kind != "execute" || d.Action.Prompt == nil || d.Action.WorkflowUID != nil {
			reasons = append(reasons, "persistent_agent requires a prompt action")
		}
		if d.Overlap != "forbid" {
			reasons = append(reasons, "persistent_agent requires overlap: forbid")
		}
		if d.Catchup == "all" {
			reasons = append(reasons, "persistent_agent does not support catchup: all")
		}
		if d.Issue == nil || d.Issue.Kind != "existing" {
			reasons = append(reasons, "persistent_agent requires an existing issue")
		}
		if _, ok := d.Options["agent"]; ok {
			reasons = append(reasons, "persistent_agent cannot be combined with agent")
		}
		if _, ok := d.Options["model"]; ok {
			reasons = append(reasons, "persistent_agent cannot be combined with model")
		}
	}
	if raw, present := d.Options["executor"]; present {
		if pin, ok := raw.(string); !ok || pin == "" {
			reasons = append(reasons, "options.executor must be a non-empty string")
		}
	}
	if err := sched.Validate(d.Trigger, job.CreatedAt); err != nil {
		reasons = append(reasons, "invalid_trigger")
	} else if d.Trigger.Kind == "once" {
		at, _ := time.Parse(time.RFC3339Nano, text(d.Trigger.At))
		if !at.After(job.CreatedAt) {
			reasons = append(reasons, "once_in_past")
		}
	}
	if adapter, ok := d.Options["adapter"]; ok && adapter != "herdr-kata" {
		reasons = append(reasons, fmt.Sprintf("adapter: %v", adapter))
	}
	if d.Action.Kind == "notify" {
		reasons = append(reasons, "notify_unsupported")
		if d.Issue != nil && d.Issue.Kind == "per-run" {
			reasons = append(reasons, "per-run notify belongs to the hub")
		}
	}
	if len(d.SecretRefs) > 0 {
		reasons = append(reasons, "secrets_unsupported")
	}
	if d.Issue != nil {
		i := d.Issue
		p.Issue = IssuePolicy{Kind: i.Kind, UID: text(i.UID), Title: text(i.Title), Body: text(i.Body)}
		if i.DeadlineOffsetSeconds != nil {
			p.Issue.DeadlineOffsetSeconds = new(*i.DeadlineOffsetSeconds)
		}
		if i.ScheduledOffsetSeconds != nil {
			reasons = append(reasons, "scheduled_offset_unsupported")
		}
	}
	if raw, ok := d.Options["run_parent"]; ok {
		parent, _ := raw.(string)
		switch {
		case !issueUID(parent):
			reasons = append(reasons, "run_parent must be an issue UID")
		case d.Issue == nil || d.Issue.Kind != "per-run":
			reasons = append(reasons, "run_parent requires a per-run issue")
		default:
			p.Issue.Parent = parent
		}
	}
	if d.TimeoutSeconds != nil {
		if *d.TimeoutSeconds > int64(cfg.Limits.MaxTimeout/time.Second) {
			p.Timeout = cfg.Limits.MaxTimeout
		} else {
			p.Timeout = time.Duration(*d.TimeoutSeconds) * time.Second
		}
	}
	if p.Timeout > cfg.Limits.MaxTimeout {
		p.Timeout = cfg.Limits.MaxTimeout
	}
	if d.GraceSeconds != nil {
		p.Grace = time.Duration(*d.GraceSeconds) * time.Second
	}
	checkout := projectCfg.DefaultCheckout
	if d.CheckoutKey != nil {
		checkout = *d.CheckoutKey
	}
	if d.CheckoutKey != nil || checkout != "" {
		p.CheckoutPath = cfg.Checkouts[checkout]
	}
	if !filepath.IsAbs(p.CheckoutPath) {
		reason := "checkout not configured: " + checkout
		if d.CheckoutKey == nil && checkout == "" {
			reason = "checkout not configured: set checkout_key or the project's default_checkout"
		}
		reasons = append(reasons, reason)
	}
	var steps []kata.WorkflowStep
	if d.Action.WorkflowUID != nil {
		wf, ok := wfs[*d.Action.WorkflowUID]
		if !ok || wf.DeletedAt != nil {
			reasons = append(reasons, "workflow not found: "+*d.Action.WorkflowUID)
		} else {
			p.WorkflowUID = wf.UID
			p.WorkflowEventUID = wf.DefinitionEventUID
			p.WorkflowName = wf.Name
			p.About = text(wf.Definition.About)
			reasons = append(reasons, unsupported(wf.Definition.Options)...)
			if _, ok := wf.Definition.Options["persistent_agent"]; ok {
				reasons = append(reasons, "persistent_agent belongs on the job")
			}
			if _, ok := wf.Definition.Options["run_parent"]; ok {
				reasons = append(reasons, "run_parent belongs on the job")
			}
			steps = wf.Definition.Steps
		}
	} else if d.Action.Prompt != nil {
		steps = []kata.WorkflowStep{{Key: "main", Kind: "prompt", Prompt: d.Action.Prompt}}
	}
	if raw, present := d.Options["agent"]; present {
		if _, ok := raw.(string); !ok {
			reasons = append(reasons, "agent must be a string")
		}
	}
	for _, s := range steps {
		reasons = append(reasons, unsupported(s.Options)...)
		if _, ok := s.Options["persistent_agent"]; ok {
			reasons = append(reasons, "persistent_agent belongs on the job")
		}
		if raw, present := s.Options["agent"]; present {
			if _, ok := raw.(string); !ok {
				reasons = append(reasons, "agent must be a string")
			}
		}
		if _, ok := s.Options["run_parent"]; ok {
			reasons = append(reasons, "run_parent belongs on the job")
		}
		frozen := Step{Key: s.Key, Kind: s.Kind, After: slices.Clone(s.After)}
		if s.Retries != nil {
			frozen.Retries = int(*s.Retries)
		}
		switch s.Kind {
		case "prompt":
			frozen.Text = text(s.Prompt)
			if _, target := d.Options["persistent_agent"]; target {
				break
			}
			profile, model, valid := promptOptions(s, d.Options, projectCfg.DefaultAgent)
			if !valid {
				reasons = append(reasons, "model must be a string")
			}
			selected, configured := cfg.Profiles[profile]
			argv := slices.Clone(selected.Argv)
			if model != "" && configured {
				if selected.ModelFlag == "" {
					reasons = append(reasons, "model unsupported by profile: "+profile)
				} else {
					argv = append(argv, selected.ModelFlag, model)
				}
			}
			if len(argv) == 0 {
				reasons = append(reasons, "profile not configured: "+profile)
			} else {
				resolved, err := look(argv[0])
				if err != nil || !filepath.IsAbs(resolved) {
					reasons = append(reasons, "profile executable unavailable: "+profile)
				} else {
					argv[0] = resolved
					frozen.Argv = argv
				}
			}
		case "command":
			frozen.Text = text(s.Command)
			if !projectCfg.Commands {
				reasons = append(reasons, "commands disabled for project")
			}
			if raw, ok := s.Options["skip_exit"]; ok {
				n, err := exitCode(raw)
				if err != nil || n < 1 || n > 255 {
					reasons = append(reasons, "skip_exit must be an exit code 1–255")
				} else {
					frozen.SkipExit = n
				}
			}
		default:
			reasons = append(reasons, "unsupported step kind: "+s.Kind)
		}
		p.Steps = append(p.Steps, frozen)
	}
	// Preserve deterministic reason order while dropping duplicate scope findings.
	unique := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if !slices.Contains(unique, reason) {
			unique = append(unique, reason)
		}
	}
	return p, unique
}

func exitCode(v any) (int, error) {
	switch n := v.(type) {
	case json.Number:
		return strconv.Atoi(n.String())
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		if n == float64(int(n)) {
			return int(n), nil
		}
	}
	return 0, fmt.Errorf("not an integer")
}

// issueUID reports whether s is a full 26-character Crockford ULID.
func issueUID(s string) bool {
	if len(s) != 26 || s[0] > '7' {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", c) {
			return false
		}
	}
	return true
}

// promptOptions shares precedence and raw model validity with diagnostics.
func promptOptions(s kata.WorkflowStep, job map[string]any, agent string) (string, string, bool) {
	if agent == "" {
		agent = "default"
	}
	model, valid := "", true
	for _, opts := range []map[string]any{job, s.Options} {
		if v := option(opts, "agent"); v != "" {
			agent = v
		}
		if raw, present := opts["model"]; present {
			v, ok := raw.(string)
			valid = valid && ok
			if v != "" {
				model = v
			}
		}
	}
	return agent, model, valid
}

// DefaultModelSteps reports prompt steps without an explicit model selection.
func DefaultModelSteps(job kata.CronJob, wfs map[string]kata.CronWorkflow, cfg config.Config, project string) []string {
	if _, ok := job.Definition.Options["persistent_agent"]; ok {
		return nil
	}
	agent := ""
	for _, p := range cfg.Projects {
		if p.Name == project {
			agent = p.DefaultAgent
			break
		}
	}
	var steps []kata.WorkflowStep
	if uid := job.Definition.Action.WorkflowUID; uid != nil {
		wf, ok := wfs[*uid]
		if !ok || wf.DeletedAt != nil {
			return nil
		}
		steps = wf.Definition.Steps
	} else if job.Definition.Action.Prompt != nil {
		steps = []kata.WorkflowStep{{Key: "main", Kind: "prompt"}}
	}
	var warnings []string
	for _, s := range steps {
		profile, model, valid := promptOptions(s, job.Definition.Options, agent)
		p := cfg.Profiles[profile]
		if s.Kind == "prompt" && valid && model == "" && p.ModelFlag != "" && !fixedModel(p) {
			warnings = append(warnings, fmt.Sprintf("warning: job %s step %s: profile %s has no explicit model in job options or profile argv; set options.model", job.UID, s.Key, profile))
		}
	}
	return warnings
}
func fixedModel(p config.Profile) bool {
	for i, arg := range p.Argv {
		if arg == p.ModelFlag && i+1 < len(p.Argv) && p.Argv[i+1] != "" || strings.HasPrefix(arg, p.ModelFlag+"=") && arg != p.ModelFlag+"=" {
			return true
		}
	}
	return false
}
